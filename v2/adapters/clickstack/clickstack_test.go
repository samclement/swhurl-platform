package clickstack_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
	"github.com/samclement/swhurl-platform/v2/adapters/clickstack/clickstacktest"
	"github.com/samclement/swhurl-platform/v2/dashboards"
)

const (
	key   = "admin-access-key-0123"
	uri   = "mongodb://hyperdx:private-password@clickstack-mongodb-svc:27017/hyperdx"
	email = "admin@example.test"
)

var fetchLine = regexp.MustCompile(`^const r = await fetch\(("[^"]*"), (\{.*\})\);$`)

// server answers the adapter's programs as ClickStack's containers would, from a fake's inventory: the mongosh
// script with the admin's key, and each fetch as HyperDX answers it, adding fields the server keeps for itself.
type server struct {
	t    *testing.T
	fake *clickstacktest.Fake
}

func (s server) mongo(_ context.Context, program []byte) ([]byte, error) {
	text := string(program)
	if !strings.Contains(text, "db = connect("+quote(s.fake.URI)+");") || !strings.Contains(text, "const EMAIL = "+quote(s.fake.Email)+";") ||
		strings.Contains(text, "updateOne") {
		return []byte("MongoServerError: not this account\n"), nil
	}
	answer, _ := json.Marshal(map[string]string{"key": s.fake.Key})
	return append([]byte("Warning: some notice\nRESULT "), answer...), nil
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (s server) app(_ context.Context, program []byte) ([]byte, error) {
	lines := strings.Split(string(program), "\n")
	found := fetchLine.FindStringSubmatch(lines[0])
	if found == nil {
		s.t.Fatalf("not a fetch: %q", lines[0])
	}
	var address string
	var request struct {
		Method, Redirect string
		Headers          map[string]string
		Body             *string
	}
	if json.Unmarshal([]byte(found[1]), &address) != nil || json.Unmarshal([]byte(found[2]), &request) != nil {
		s.t.Fatalf("an undecodable request: %s", lines[0])
	}
	path, ok := strings.CutPrefix(address, "http://127.0.0.1:8000/api/v2")
	if !ok || request.Redirect != "manual" || request.Headers["content-type"] != "application/json" {
		s.t.Fatalf("request %s %+v", address, request)
	}
	status, body := 401, any(map[string]string{"error": "unauthorized"})
	if request.Headers["authorization"] == "Bearer "+s.fake.Key {
		status, body = s.answer(request.Method, path, request.Body)
	}
	encoded, _ := json.Marshal(map[string]any{"status": status, "body": body})
	return append([]byte("RESULT "), encoded...), nil
}

// answer serves one request from the fake. A refusal answers its status with a body that repeats the
// credentials, which the adapter must never repeat.
func (s server) answer(method, path string, body *string) (int, any) {
	ctx := context.Background()
	collection, id, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	data, err := s.serve(ctx, method, collection, id, body)
	var refused *clickstack.Error
	switch {
	case errors.As(err, &refused) && refused.Reason == "truncated":
		return 200, map[string]any{"data": data, "meta": map[string]int{"total": s.fake.Totals[path]}}
	case errors.As(err, &refused) && strings.HasPrefix(refused.Reason, "HTTP "):
		status, _ := strconv.Atoi(strings.TrimPrefix(refused.Reason, "HTTP "))
		return status, map[string]any{"error": []string{key, uri, email}}
	case err != nil:
		s.t.Fatal(err)
	}
	if method == "DELETE" || method == "PUT" {
		return 200, map[string]any{}
	}
	return 200, map[string]any{"data": data}
}

func (s server) serve(ctx context.Context, method, collection, id string, body *string) (any, error) {
	decoded := func(into any) {
		if body == nil || json.Unmarshal([]byte(*body), into) != nil {
			s.t.Fatalf("%s %s without a body", method, collection)
		}
	}
	switch method + " " + collection {
	case "GET sources":
		return s.sources(), s.listed("sources")
	case "GET webhooks":
		return s.fake.Held.Webhooks, s.listed("webhooks")
	case "GET dashboards":
		return withServerDefaults(s.fake.Held.Dashboards), s.listed("dashboards")
	case "GET alerts":
		return s.fake.Held.Alerts, s.listed("alerts")
	case "POST webhooks":
		var hook dashboards.Webhook
		decoded(&hook)
		created, err := s.fake.CreateWebhook(ctx, hook)
		return map[string]string{"id": created, "name": hook.Name}, err
	case "PUT webhooks":
		var hook dashboards.Webhook
		decoded(&hook)
		return nil, s.fake.UpdateWebhook(ctx, id, hook)
	case "POST dashboards":
		var board dashboards.Dashboard
		decoded(&board)
		created, err := s.fake.CreateDashboard(ctx, board)
		return map[string]string{"id": created}, err
	case "PUT dashboards":
		var board dashboards.Dashboard
		decoded(&board)
		return nil, s.fake.UpdateDashboard(ctx, id, board)
	case "DELETE dashboards":
		return nil, s.fake.DeleteDashboard(ctx, id)
	}
	return s.serveAlerts(ctx, method, id, decoded)
}

func (s server) serveAlerts(ctx context.Context, method, id string, decoded func(any)) (any, error) {
	var alert dashboards.Alert
	switch method {
	case "POST":
		decoded(&alert)
		return map[string]string{}, s.fake.CreateAlert(ctx, alert)
	case "PUT":
		decoded(&alert)
		return nil, s.fake.UpdateAlert(ctx, id, alert)
	}
	return nil, s.fake.DeleteAlert(ctx, id)
}

// listed records the listing on the fake, where a stated total above its items is truncation.
func (s server) listed(collection string) error {
	ctx := context.Background()
	var err error
	switch collection {
	case "sources":
		_, err = s.fake.Sources(ctx)
	case "webhooks":
		_, err = s.fake.Webhooks(ctx)
	case "dashboards":
		_, err = s.fake.Dashboards(ctx)
	default:
		_, err = s.fake.Alerts(ctx)
	}
	return err
}

func (s server) sources() []map[string]any {
	out := []map[string]any{}
	for _, source := range s.fake.Held.Sources {
		out = append(out, map[string]any{"id": source.ID, "kind": source.Kind, "name": source.Kind,
			"from": map[string]string{"databaseName": "default", "tableName": source.Table}})
	}
	return out
}

// withServerDefaults is the dashboards as HyperDX lists them: with fields it adds that the platform never writes.
func withServerDefaults(boards []dashboards.Dashboard) []map[string]any {
	out := []map[string]any{}
	for _, board := range boards {
		raw, _ := json.Marshal(board)
		var listed map[string]any
		_ = json.Unmarshal(raw, &listed)
		listed["filters"] = []any{}
		for _, tile := range listed["tiles"].([]any) {
			tile.(map[string]any)["config"].(map[string]any)["fillNulls"] = true
		}
		out = append(out, listed)
	}
	return out
}

func scenario() clickstacktest.Scenario {
	board := dashboards.Definition(dashboards.App{Name: "web", Kind: dashboards.Web, Environments: []string{"staging"}}, "t1", "l1")
	board.ID = "d1"
	for i := range board.Tiles {
		board.Tiles[i].ID = "d1-" + board.Tiles[i].Name
	}
	return clickstacktest.Scenario{Key: key, URI: uri, Email: email, Held: clickstacktest.Inventory{
		Sources:    []dashboards.Source{{ID: "t1", Kind: "trace", Table: "otel_traces"}, {ID: "l1", Kind: "log", Table: "otel_logs"}},
		Webhooks:   []dashboards.Webhook{{ID: "w0", Name: "ntfy: platform", Service: "generic", URL: "https://ntfy.example.test/", Body: "{}"}},
		Dashboards: []dashboards.Dashboard{board, {ID: "d2", Name: "Someone's board", Tags: []string{"team"}, Tiles: []dashboards.Tile{}}},
		Alerts: []dashboards.Alert{{ID: "a1", Name: "swhurl:web:errors", Tags: []string{dashboards.Tag, "web"}, Source: "tile",
			DashboardID: "d1", TileID: "d1-Errors", Threshold: 5, ThresholdType: "above", Interval: "5m",
			Channel: dashboards.Channel{Type: "webhook", WebhookID: "w1"}}},
	}}
}

func TestContract(t *testing.T) {
	clickstacktest.Contract(t, scenario(), func(t *testing.T, s *clickstacktest.Scenario) clickstack.Opener {
		held := *s
		backend := server{t: t, fake: &held}
		return clickstack.New(backend.mongo, backend.app)
	})
}

// recorded answers each listing with its raw fixture, as HyperDX's answer body.
func recorded(t *testing.T) clickstack.API {
	t.Helper()
	mongo := func(context.Context, []byte) ([]byte, error) { return []byte(`RESULT {"key":"` + key + `"}`), nil }
	app := func(_ context.Context, program []byte) ([]byte, error) {
		found := fetchLine.FindStringSubmatch(strings.SplitN(string(program), "\n", 2)[0])
		var address string
		_ = json.Unmarshal([]byte(found[1]), &address)
		raw, err := os.ReadFile("testdata/" + strings.TrimPrefix(address, "http://127.0.0.1:8000/api/v2/") + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var compact bytes.Buffer
		_ = json.Compact(&compact, raw)
		return []byte(`RESULT {"status":200,"body":` + compact.String() + "}\n"), nil
	}
	api, err := clickstack.New(mongo, app).Open(context.Background(), uri, email)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

// The recorded listings decode to the platform's fields: the server's own are dropped, and an object of a shape
// the platform never writes keeps its id and name, so it is rewritten if it is the platform's and left otherwise.
func TestRecordedListingsDecode(t *testing.T) {
	ctx := context.Background()
	api := recorded(t)
	sources, err := api.Sources(ctx)
	if picked, perr := dashboards.PickSources(sources); err != nil || perr != nil ||
		picked != (dashboards.Sources{Traces: "6650a1f0c0ffee0000000001", Logs: "6650a1f0c0ffee0000000002"}) {
		t.Fatalf("%+v, %v, %v", sources, err, perr)
	}
	hooks, err := api.Webhooks(ctx)
	if err != nil || len(hooks) != 2 || hooks[0] != (dashboards.Webhook{ID: "6650a1f0c0ffee0000000010", Name: "ntfy: platform",
		Service: "generic", URL: "https://ntfy.example.test/", Description: "ClickStack alerts to the platform ntfy topic",
		Body: `{"topic":"private-topic"}`}) || hooks[1].Name != "my webhook" {
		t.Fatalf("%+v, %v", hooks, err)
	}
	boards, err := api.Dashboards(ctx)
	if err != nil || len(boards) != 2 || len(boards[0].Tiles) != 2 || boards[0].Tiles[0].Config.Select[0].ValueExpression != "" ||
		boards[0].Tiles[1].Config.Columns != "Timestamp, Body" || boards[0].Tiles[0].ID != "t-req" {
		t.Fatalf("%+v, %v", boards, err)
	}
	if odd := boards[1]; odd.ID != "6650a1f0c0ffee0000000021" || odd.Name != "Someone's board" || odd.Tiles != nil || len(odd.Tags) != 1 {
		t.Fatalf("an undecodable board: %+v", odd)
	}
	alerts, err := api.Alerts(ctx)
	if err != nil || len(alerts) != 2 || alerts[0].Threshold != 5 || alerts[0].Channel.WebhookID != "6650a1f0c0ffee0000000012" ||
		!reflect.DeepEqual(alerts[1], dashboards.Alert{ID: "6650a1f0c0ffee0000000031", Name: "my own alert"}) {
		t.Fatalf("%+v, %v", alerts, err)
	}
}

// The programs are the Python job's: the same fetch with the key in a header inside the program on stdin, and the
// body as JSON text (the same content; Go writes no spaces between fields). The MongoDB script only reads.
func TestProgramsAreStated(t *testing.T) {
	var programs []string
	mongo := func(_ context.Context, program []byte) ([]byte, error) {
		programs = append(programs, string(program))
		return []byte(`RESULT {"key":"` + key + `"}`), nil
	}
	app := func(_ context.Context, program []byte) ([]byte, error) {
		programs = append(programs, string(program))
		return []byte(`RESULT {"status":200,"body":{"data":{"id":"w9"}}}`), nil
	}
	api, err := clickstack.New(mongo, app).Open(context.Background(), uri, email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateWebhook(context.Background(), dashboards.Webhook{ID: "ignored", Name: "n", Service: "generic", URL: "u", Body: `{"a":"<b>"}`}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`db = connect("` + uri + `");` + "\n" + `const EMAIL = "` + email + `"; const u = db.users.findOne({email: EMAIL}, {accessKey: 1}); print("RESULT " + JSON.stringify({key: (u && u.accessKey) || ""}));` + "\n",
		`const r = await fetch("http://127.0.0.1:8000/api/v2/webhooks", {"method":"POST","headers":{"authorization":"Bearer ` + key +
			`","content-type":"application/json"},"redirect":"manual","body":"{\"name\":\"n\",\"service\":\"generic\",\"url\":\"u\",\"description\":\"\",\"body\":\"{\\\"a\\\":\\\"\\u003cb\\u003e\\\"}\"}"});` + "\n" +
			"let body = null; try { body = await r.json(); } catch {}\n" +
			`console.log("RESULT " + JSON.stringify({status: r.status, body}));` + "\n",
	}
	if len(programs) != 2 || programs[0] != want[0] || programs[1] != want[1] {
		t.Fatalf("programs:\n%s\nwant:\n%s", strings.Join(programs, "\n---\n"), strings.Join(want, "\n---\n"))
	}
}

// Every failure names the operation and a fixed reason, never a credential or an answer's bytes.
func TestFailuresNeverRepeatCredentialsOrAnswers(t *testing.T) {
	ctx := context.Background()
	private := []string{key, uri, email, "private-answer"}
	answers := map[string]func(context.Context, []byte) ([]byte, error){
		"exec":      func(context.Context, []byte) ([]byte, error) { return nil, errors.New("exec in pod-0 failed") },
		"no result": func(context.Context, []byte) ([]byte, error) { return []byte("private-answer"), nil },
		"status": func(context.Context, []byte) ([]byte, error) {
			return []byte(`RESULT {"status":302,"body":{"error":"private-answer"}}`), nil
		},
		"not a listing": func(context.Context, []byte) ([]byte, error) {
			return []byte(`RESULT {"status":200,"body":{"data":"private-answer"}}`), nil
		},
		"created without id": func(context.Context, []byte) ([]byte, error) { return []byte(`RESULT {"status":200,"body":{}}`), nil },
	}
	for name, app := range answers {
		mongo := func(context.Context, []byte) ([]byte, error) { return []byte(`RESULT {"key":"` + key + `"}`), nil }
		api, _ := clickstack.New(mongo, app).Open(ctx, uri, email)
		_, listErr := api.Webhooks(ctx)
		_, createErr := api.CreateWebhook(ctx, dashboards.Webhook{Name: "n"})
		for _, err := range []error{listErr, createErr} {
			if name == "not a listing" && err == createErr || name == "created without id" && err == listErr {
				continue
			}
			if err == nil || containsAny(err.Error(), private) {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	for name, mongo := range map[string]clickstack.Runner{
		"exec":      answers["exec"],
		"no result": answers["no result"],
		"no key":    func(context.Context, []byte) ([]byte, error) { return []byte(`RESULT {}`), nil },
	} {
		if _, err := clickstack.New(mongo, nil).Open(ctx, uri, email); err == nil || containsAny(err.Error(), private) {
			t.Errorf("open %s: %v", name, err)
		}
	}
	if _, err := clickstack.New(nil, nil).Open(ctx, "", email); err == nil {
		t.Error("open without a URI")
	}
	_, err := clickstack.New(answers["exec"], nil).Open(ctx, uri, email)
	if err == nil || !strings.Contains(err.Error(), "exec in pod-0 failed") {
		t.Errorf("the runner's reason is lost: %v", err)
	}
}

func containsAny(text string, values []string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

// An id is one path segment: one that could reshape the path is never sent as given.
func TestAnIDCannotReshapeThePath(t *testing.T) {
	var paths []string
	app := func(_ context.Context, program []byte) ([]byte, error) {
		found := fetchLine.FindStringSubmatch(strings.SplitN(string(program), "\n", 2)[0])
		paths = append(paths, found[1])
		return []byte(`RESULT {"status":404,"body":null}`), nil
	}
	mongo := func(context.Context, []byte) ([]byte, error) { return []byte(`RESULT {"key":"k"}`), nil }
	api, _ := clickstack.New(mongo, app).Open(context.Background(), uri, email)
	for _, id := range []string{"../webhooks", "a/b", "a?b", "", "a%2f"} {
		_ = api.DeleteDashboard(context.Background(), id)
	}
	for _, path := range paths {
		if path != `"http://127.0.0.1:8000/api/v2/dashboards/invalid-id"` {
			t.Errorf("%s", path)
		}
	}
}
