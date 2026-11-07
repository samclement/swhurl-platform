package dashboards

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var (
	web    = App{Name: "web", Kind: Web, Environments: []string{"staging", "prod"}}
	worker = App{Name: "job", Kind: Worker, Environments: []string{"staging"}}
	both   = Sources{Traces: "t1", Logs: "l1"}
)

func fixture(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

// generic is a document as untyped JSON, so two encodings compare by content rather than by field order.
func generic(t *testing.T, document any) any {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The definitions are kept exactly: each request body is the document the Python job sent (testdata from
// dashboards.dashboard), field for field.
func TestDefinitionsAreWhatThePythonJobSent(t *testing.T) {
	var python map[string]json.RawMessage
	fixture(t, "definitions.json", &python)
	for name, app := range map[string]App{"web": web, "worker": worker} {
		var want any
		if err := json.Unmarshal(python[name], &want); err != nil {
			t.Fatal(err)
		}
		if got := generic(t, Definition(app, "t1", "l1")); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", name, got, want)
		}
	}
}

// Python's DefinitionTests: every tile filters on the app's namespaces, lines split by environment, rows stack
// without overlapping, and a worker counts its root spans as runs.
func TestTilesFilterOnTheAppsNamespacesAndSplitByEnvironment(t *testing.T) {
	board, runs := Definition(web, "t1", "l1"), Definition(worker, "t1", "l1")
	if board.Tiles[0].Name != "Requests per minute" || runs.Tiles[0].Name != "Runs per minute" {
		t.Fatalf("first tiles %q, %q", board.Tiles[0].Name, runs.Tiles[0].Name)
	}
	ys := []int{}
	for _, tile := range board.Tiles {
		where := tile.Config.Where
		if tile.Config.Select != nil {
			where = tile.Config.Select[0].Where
		}
		if !strings.Contains(where, "IN ('web-staging', 'web-prod')") || tile.X+tile.W > 24 {
			t.Errorf("tile %q: %q at %d+%d", tile.Name, where, tile.X, tile.W)
		}
		if tile.Config.DisplayType == "line" && tile.H != chartHeight {
			t.Errorf("tile %q is %d rows", tile.Name, tile.H)
		}
		ys = append(ys, tile.Y)
	}
	if !slices.Equal(ys, []int{0, 0, 0, 9, 9, 18}) {
		t.Errorf("rows %v", ys)
	}
	if !strings.Contains(runs.Tiles[0].Config.Select[0].Where, "ParentSpanId = ''") {
		t.Errorf("worker runs %q", runs.Tiles[0].Config.Select[0].Where)
	}
}

// The server's tile ids and the defaults it adds are not a change; a changed group-by is.
func TestServerDefaultsDoNotCountAsAChange(t *testing.T) {
	wanted := Definition(web, "t1", "l1")
	raw, _ := json.Marshal(wanted)
	var live map[string]any
	_ = json.Unmarshal(raw, &live)
	for _, tile := range live["tiles"].([]any) {
		tile := tile.(map[string]any)
		config := tile["config"].(map[string]any)
		tile["id"], config["fillNulls"] = "x", true
		if selects, ok := config["select"].([]any); ok {
			if _, set := selects[0].(map[string]any)["valueExpression"]; !set {
				selects[0].(map[string]any)["valueExpression"] = "" // the server's default for a count
			}
		}
	}
	decoded := decode(t, live)
	if !Same(decoded, wanted) {
		t.Fatal("server defaults counted as a change")
	}
	decoded.Tiles[0].Config.GroupBy = "ServiceName"
	if Same(decoded, wanted) {
		t.Fatal("a changed group-by is not a change")
	}
}

func decode(t *testing.T, document any) Dashboard {
	t.Helper()
	raw, _ := json.Marshal(document)
	var out Dashboard
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A tile config of a shape the platform never writes decodes as empty, so the dashboard is rewritten, not refused.
func TestAnUnexpectedTileShapeIsADifferenceNotAFailure(t *testing.T) {
	board := decode(t, map[string]any{"id": "d1", "name": "App: web", "tags": []string{Tag},
		"tiles": []any{map[string]any{"name": "Errors", "config": map[string]any{"select": map[string]any{"odd": 1}, "sourceId": 7}}}})
	if board.Tiles[0].Config.Select != nil || board.Tiles[0].Config.SourceID != "" || Same(board, Definition(web, "t1", "l1")) {
		t.Fatalf("%+v", board.Tiles[0].Config)
	}
}

func TestPlanCreatesKeepsUpdatesAndDeletesByApp(t *testing.T) {
	kept := Definition(web, "t1", "l1")
	kept.ID = "d1"
	changed := Definition(worker, "t1", "l1")
	changed.ID, changed.Tiles = "d2", nil
	live := []Dashboard{kept, changed,
		{ID: "d3", Name: "App: gone", Tags: []string{Tag}},
		{ID: "d4", Name: "App: gone", Tags: nil},                  // a person's own: untagged
		{ID: "d5", Name: "App: kept (copy)", Tags: []string{Tag}}} // tagged, not named for an app
	fresh := App{Name: "new", Kind: Web, Environments: []string{"staging"}}
	steps, err := PlanDashboards([]App{web, worker, fresh}, live, both, true)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, step := range steps {
		got = append(got, string(step.Action)+" "+step.Dashboard.Name+" "+step.ID)
	}
	want := []string{"keep App: web d1", "update App: job d2", "create App: new ", "delete App: gone d3", "leave App: kept (copy) d5"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	steps, _ = PlanDashboards(nil, live, both, false)
	if len(steps) != 0 {
		t.Fatalf("without prune nothing is deleted: %+v", steps)
	}
}

// Deliberately changed from Python: two tagged dashboards of one name, or one without an id, stop the plan.
func TestDuplicateOrIncompleteDashboardsStopThePlan(t *testing.T) {
	for _, live := range [][]Dashboard{
		{{ID: "a", Name: "App: web", Tags: []string{Tag}}, {ID: "b", Name: "App: web", Tags: []string{Tag}}},
		{{Name: "App: web", Tags: []string{Tag}}},
	} {
		if _, err := PlanDashboards([]App{web}, live, both, true); err == nil || !strings.Contains(err.Error(), "no change made") {
			t.Fatalf("%+v: %v", live, err)
		}
	}
	untagged := []Dashboard{{ID: "a", Name: "App: web"}, {Name: "App: web"}}
	if _, err := PlanDashboards([]App{web}, untagged, both, true); err != nil {
		t.Fatalf("untagged dashboards are a person's own: %v", err)
	}
}

func TestSourcesAreTheTraceAndLogTables(t *testing.T) {
	sources := []Source{{"m1", "metric", ""}, {"t1", "trace", "otel_traces"}, {"t2", "trace", "otel_traces"}, {"l1", "log", "otel_logs"}}
	if picked, err := PickSources(sources); err != nil || picked != both {
		t.Fatalf("%+v, %v", picked, err)
	}
	if _, err := PickSources(sources[:3]); err == nil {
		t.Fatal("no log source accepted")
	}
}

// The record holds only app names and ObjectIds, in the Python job's exact bytes, so an unchanged record is never
// rewritten at cutover.
func TestRecordIsThePythonBytesOfValidEntries(t *testing.T) {
	ids := map[string]string{"web": strings.Repeat("a", 24), "job": strings.Repeat("b", 24), "new": "",
		"Bad Name": strings.Repeat("c", 24), "x": "../../evil"}
	if got := Record(ids); got != `{"job": "bbbbbbbbbbbbbbbbbbbbbbbb", "web": "aaaaaaaaaaaaaaaaaaaaaaaa"}` {
		t.Fatalf("%s", got)
	}
	if got := Record(nil); got != "{}" {
		t.Fatalf("%s", got)
	}
}
