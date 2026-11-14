package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack/clickstacktest"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/dashboards"
	"github.com/samclement/swhurl-platform/v2/observe"
)

const (
	dashKey    = "admin-access-key-0123"
	dashURI    = "mongodb://hyperdx:private-password@clickstack-mongodb-svc/hyperdx"
	dashEmail  = "admin@example.test"
	dashTopic  = "swhurl-test-private-topic-012345"
	dashSecret = "private-alert-secret"
	idWeb      = "aaaaaaaaaaaaaaaaaaaaaaaa"
	idJob      = "bbbbbbbbbbbbbbbbbbbbbbbb"
)

var (
	helmReleases  = observe.Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease"}
	appNamespaces = observe.Ref{APIVersion: "v1", Kind: "Namespace"}
	privateValues = []string{dashKey, dashURI, dashTopic, dashSecret}
	webApp        = dashboards.App{Name: "web", Kind: dashboards.Web, Environments: []string{"staging", "prod"}}
	jobApp        = dashboards.App{Name: "job", Kind: dashboards.Worker, Environments: []string{"staging"}}
)

// dashWorld is a cluster and a HyperDX: the four Secrets, the console's record, the app releases and namespaces
// discovery lists, and HyperDX holding the sources and both reserved webhooks as they should be.
type dashWorld struct {
	kube    *kubetest.Fake
	hyperdx *clickstacktest.Fake
}

func newDashWorld(t *testing.T) dashWorld {
	t.Helper()
	k := kubetest.New(kubetest.Answers{})
	k.SecretData = map[string][]kube.Secret{
		"observability": {{Name: "clickstack-runtime-inputs", Data: map[string][]byte{"CLICKSTACK_ADMIN_EMAIL": []byte(dashEmail)}},
			{Name: "clickstack-mongodb-hyperdx-hyperdx", Data: map[string][]byte{"connectionString.standard": []byte(dashURI)}}},
		"console": {{Name: "notification-ntfy", Data: map[string][]byte{"NTFY_PLATFORM_URL": []byte("https://ntfy.sh/" + dashTopic)}}},
		"notify":  {{Name: "notify-alerts", Data: map[string][]byte{"secret": []byte(dashSecret)}}},
	}
	k.ConfigMapData = map[string][]kube.ConfigMap{"console": {{Name: "app-dashboards"}}}
	k.Collections = map[observe.Ref][][]byte{helmReleases: {}, appNamespaces: {}}
	h := &clickstacktest.Fake{Key: dashKey, URI: dashURI, Email: dashEmail, Held: clickstacktest.Inventory{
		Sources: []dashboards.Source{{ID: "t1", Kind: "trace", Table: "otel_traces"}, {ID: "l1", Kind: "log", Table: "otel_logs"},
			{ID: "m1", Kind: "metric"}},
		Webhooks: ownedWebhooks(t), Dashboards: platformBoards()}}
	return dashWorld{kube: k, hyperdx: h}
}

func ownedWebhooks(t *testing.T) []dashboards.Webhook {
	t.Helper()
	hooks, err := dashboards.Destinations("https://ntfy.sh/"+dashTopic, []byte(dashSecret))
	if err != nil {
		t.Fatal(err)
	}
	hooks[0].ID, hooks[1].ID = "w0", "w1"
	return hooks
}

// sync runs one pass over the world's records (apps), or the cluster's releases when apps is nil.
func (w dashWorld) sync(t *testing.T, apps []dashboards.App, dry bool) (string, error) {
	t.Helper()
	options := DashboardOptions{DryRun: dry}
	if apps != nil {
		options.FromRecords, options.Records = true, dashRecords(t, apps)
	}
	report, err := SyncDashboards(context.Background(), DashboardPorts{Reader: w.kube, Data: w.kube, ClickStack: w.hyperdx}, options)
	text := []string{}
	for _, line := range report.Lines {
		text = append(text, line.Text)
	}
	out := strings.Join(text, "\n")
	if err != nil {
		out += "\nERROR " + err.Error()
	}
	for _, private := range privateValues {
		if strings.Contains(out, private) {
			t.Fatalf("the report repeats a credential:\n%s", out)
		}
	}
	return out, err
}

// dashRecords is the v2 records of apps: each environment's release record, of the app's kind.
func dashRecords(t *testing.T, apps []dashboards.App) []RecordedInstance {
	t.Helper()
	raw, err := os.ReadFile("../testdata/r42.json")
	if err != nil {
		t.Fatal(err)
	}
	records := []RecordedInstance{}
	for _, app := range apps {
		var release map[string]any
		_ = json.Unmarshal(raw, &release)
		release["application"] = app.Name
		release["source"].(map[string]any)["repository"] = "samclement/" + app.Name
		release["artifact"].(map[string]any)["image"] = "ghcr.io/samclement/" + app.Name
		if app.Kind == dashboards.Worker {
			release["runtime"] = map[string]any{"kind": "worker", "port": 0, "healthPath": "", "uid": 65532}
			delete(release, "apiPath")
			delete(release, "documents")
		}
		encoded, _ := json.Marshal(release)
		for _, environment := range app.Environments {
			records = append(records, RecordedInstance{Application: app.Name, Environment: environment, Release: encoded})
		}
	}
	return records
}

// boardOf is an app's dashboard as HyperDX lists it, with its id and an id on every tile.
func boardOf(app dashboards.App, id string) dashboards.Dashboard {
	board := dashboards.Definition(app, "t1", "l1")
	board.ID = id
	for i := range board.Tiles {
		board.Tiles[i].ID = id + "-" + board.Tiles[i].Name
	}
	return board
}

func defaultAlert(app, preset, tile, dashboard string) dashboards.Alert {
	return dashboards.Alert{ID: "a-" + app + "-" + preset, Name: "swhurl:" + app + ":" + preset, Tags: []string{dashboards.Tag, app},
		Source: "tile", DashboardID: dashboard, TileID: dashboard + "-" + tile, Threshold: 5, ThresholdType: "above",
		Interval: "5m", Channel: dashboards.Channel{Type: "webhook", WebhookID: "w1"}}
}

func expectWrites(t *testing.T, h *clickstacktest.Fake, out string, want ...string) {
	t.Helper()
	if got := h.Writes(); !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Fatalf("writes %q, want %q\n%s", got, want, out)
	}
}

func expectIn(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, text := range want {
		if !strings.Contains(out, text) {
			t.Fatalf("%q missing from:\n%s", text, out)
		}
	}
}

// Python case 1: an app gets one alert per preset on its dashboard's tiles, through the notify webhook.
func TestAnAppGetsItsDefaultAlertsThroughTheNotifyWebhook(t *testing.T) {
	w := newDashWorld(t)
	w.hold(boardOf(webApp, idWeb))
	out, err := w.sync(t, []dashboards.App{webApp}, false)
	if err != nil {
		t.Fatal(out)
	}
	expectWrites(t, w.hyperdx, out, "POST /alerts", "POST /alerts")
	for _, body := range w.hyperdx.Bodies {
		if alert := body.(dashboards.Alert); alert.Channel.WebhookID != "w1" || alert.DashboardID != idWeb {
			t.Fatalf("%+v", alert)
		}
	}
	expectIn(t, out, "created swhurl:web:error-logs", "created swhurl:web:errors")
}

// A dashboard created or rebuilt in this pass gets its alerts in the same pass, naming the tile ids the server gave
// it then, not those of the listing taken before the writes.
func TestANewOrRebuiltDashboardGetsAlertsOnItsNewTiles(t *testing.T) {
	w := newDashWorld(t)
	stale := boardOf(jobApp, idJob)
	stale.Tiles = stale.Tiles[:1]
	w.hold(stale)
	w.hyperdx.Held.Alerts = []dashboards.Alert{defaultAlert("job", "errors", "Errors", idJob)}
	out, err := w.sync(t, []dashboards.App{webApp, jobApp}, false)
	if err != nil {
		t.Fatal(out)
	}
	expectWrites(t, w.hyperdx, out, "PUT /dashboards/"+idJob, "POST /dashboards", "POST /alerts", "PUT /alerts/a-job-errors",
		"POST /alerts", "POST /alerts")
	tiles := map[string]bool{}
	for _, board := range w.hyperdx.Held.Dashboards {
		for _, tile := range board.Tiles {
			tiles[board.ID+"/"+tile.ID] = true
		}
	}
	for _, alert := range w.hyperdx.Held.Alerts {
		if !tiles[alert.DashboardID+"/"+alert.TileID] {
			t.Fatalf("alert %s names a tile that is gone: %+v", alert.Name, alert)
		}
	}
}

// Python case 3: matching alerts are left; others are updated in place or deleted, in that order.
func TestAlertsAreUpdatedInPlaceOrDeleted(t *testing.T) {
	w := newDashWorld(t)
	w.hold(boardOf(webApp, idWeb))
	untagged := defaultAlert("web", "error-logs", "Error logs", idWeb)
	untagged.Tags = []string{dashboards.Tag}
	w.hyperdx.Held.Alerts = []dashboards.Alert{untagged, defaultAlert("web", "errors", "Errors", "old"),
		defaultAlert("gone", "errors", "Errors", "d9"), {ID: "mine", Name: "my own alert"}}
	out, err := w.sync(t, []dashboards.App{webApp}, false)
	if err != nil {
		t.Fatal(out)
	}
	expectWrites(t, w.hyperdx, out, "PUT /alerts/a-web-error-logs", "PUT /alerts/a-web-errors", "DELETE /alerts/a-gone-errors")
	expectIn(t, out, "updated swhurl:web:errors", "deleted swhurl:gone:errors: its app or tile is gone")
	w.hyperdx.Calls = nil
	out, _ = w.sync(t, []dashboards.App{webApp}, false)
	expectWrites(t, w.hyperdx, out)
	expectIn(t, out, "1 app(s) have their default alerts")
}

// Python cases 4 and 32: a missing notify webhook is created first and the alerts name its new id; one
// authentication and one webhook listing serve the whole pass, and only reads come before the webhooks are written.
func TestMissingWebhooksAreCreatedBeforeAlertsWithOneAuthentication(t *testing.T) {
	w := newDashWorld(t)
	w.hyperdx.Held.Webhooks = nil
	w.hold(boardOf(webApp, idWeb))
	out, err := w.sync(t, []dashboards.App{webApp}, false)
	if err != nil {
		t.Fatal(out)
	}
	expectWrites(t, w.hyperdx, out, "POST /webhooks", "POST /webhooks", "POST /alerts", "POST /alerts")
	notify := w.hyperdx.Held.Webhooks[1]
	if notify.Name != dashboards.NotifyWebhook || w.hyperdx.Opened != 1 || strings.Count(strings.Join(w.hyperdx.Calls, ","), "GET /webhooks") != 1 {
		t.Fatalf("%+v, opened %d, %q", notify, w.hyperdx.Opened, w.hyperdx.Calls)
	}
	for _, body := range w.hyperdx.Bodies[2:] {
		if body.(dashboards.Alert).Channel.WebhookID != notify.ID {
			t.Fatalf("an alert names %+v", body)
		}
	}
	if w.hyperdx.Calls[0] != "GET /webhooks" {
		t.Fatalf("the first request %q", w.hyperdx.Calls[0])
	}
}

// Python cases 5, 14 and 33: a dry run reads and plans everything and writes nothing, the record included.
func TestADryRunWritesNothing(t *testing.T) {
	w := newDashWorld(t)
	w.hyperdx.Held.Webhooks = []dashboards.Webhook{w.hyperdx.Held.Webhooks[0]}
	w.hyperdx.Held.Webhooks[0].Body = "stale"
	w.hold(boardOf(webApp, idWeb), dashboards.Dashboard{ID: "d3", Name: "App: gone", Tags: []string{dashboards.Tag}})
	w.hyperdx.Held.Alerts = []dashboards.Alert{defaultAlert("gone", "errors", "Errors", "d3")}
	out, err := w.sync(t, []dashboards.App{webApp, jobApp}, true)
	if err != nil {
		t.Fatal(out)
	}
	expectWrites(t, w.hyperdx, out)
	if slices.ContainsFunc(w.kube.Calls, func(call string) bool { return strings.HasPrefix(call, "set ") }) {
		t.Fatalf("a dry run wrote the record: %q", w.kube.Calls)
	}
	expectIn(t, out, "would update ntfy: platform", "would create swhurl notify", "would create App: job",
		"would delete App: gone (its app has no records in Git)", "would create swhurl:web:errors", "would delete swhurl:gone:errors")
}

// Python cases 6 and 8: the cluster's apps take their alert settings from their namespaces.
func TestTheClusterJobReadsAlertSettingsFromNamespaces(t *testing.T) {
	w := newDashWorld(t)
	w.kube.Collections[helmReleases] = [][]byte{release(t, "web", "staging", true)}
	w.kube.Collections[appNamespaces] = [][]byte{namespaceOf("web-staging", "error-logs=20,errors=none")}
	staging := dashboards.App{Name: "web", Kind: dashboards.Web, Environments: []string{"staging"}}
	w.hold(boardOf(staging, idWeb))
	w.hyperdx.Held.Alerts = []dashboards.Alert{defaultAlert("web", "error-logs", "Error logs", idWeb), defaultAlert("web", "errors", "Errors", idWeb)}
	out, err := w.sync(t, nil, false)
	if err != nil {
		t.Fatal(out)
	}
	expectWrites(t, w.hyperdx, out, "PUT /alerts/a-web-error-logs", "DELETE /alerts/a-web-errors")
	if w.hyperdx.Bodies[0].(dashboards.Alert).Threshold != 20 {
		t.Fatalf("%+v", w.hyperdx.Bodies[0])
	}
	expectIn(t, out, "deleted swhurl:web:errors: its app or tile is gone, or the app switched it off")
}

func release(t *testing.T, app, environment string, web bool) []byte {
	t.Helper()
	values := map[string]any{}
	if web {
		values["service"] = map[string]any{}
	}
	raw, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": app, "namespace": app + "-" + environment,
		"labels": map[string]string{"kustomize.toolkit.fluxcd.io/name": "app-" + app + "-" + environment,
			"kustomize.toolkit.fluxcd.io/namespace": "flux-system"}}, "spec": map[string]any{"values": values}})
	return raw
}

func namespaceOf(name, alerts string) []byte {
	raw, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": name,
		"annotations": map[string]string{dashboards.AlertsAnnotation: alerts}}})
	return raw
}

// Python case 10: by app, dashboards are kept, updated, created and deleted; a person's untagged one is never
// touched.
func TestDashboardsAreCreatedKeptUpdatedAndDeletedByApp(t *testing.T) {
	w := newDashWorld(t)
	changed := boardOf(jobApp, idJob)
	changed.Tiles = nil
	w.hold(boardOf(webApp, idWeb), changed, dashboards.Dashboard{ID: "d3", Name: "App: gone", Tags: []string{dashboards.Tag}},
		dashboards.Dashboard{ID: "d4", Name: "App: gone"})
	fresh := dashboards.App{Name: "new", Kind: dashboards.Web, Environments: []string{"staging"}}
	out, err := w.sync(t, []dashboards.App{webApp, jobApp, fresh}, false)
	if err != nil {
		t.Fatal(out)
	}
	writes := slices.DeleteFunc(w.hyperdx.Writes(), func(call string) bool { return strings.Contains(call, "/alerts") })
	if !slices.Equal(writes, []string{"PUT /dashboards/" + idJob, "POST /dashboards", "DELETE /dashboards/d3"}) {
		t.Fatalf("%q\n%s", writes, out)
	}
	expectIn(t, out, "App: web is up to date", "deleted App: gone (its app has no records in Git)")
}

// Python cases 11 and 13: each app's dashboard id is recorded for the console, written only when it changed, and a
// record that cannot be written stops the pass before the alerts.
func TestTheRecordIsWrittenOnlyWhenItChangedAndAFailedWriteStopsThePass(t *testing.T) {
	w := newDashWorld(t)
	w.hold(boardOf(webApp, idWeb), boardOf(jobApp, idJob))
	out, err := w.sync(t, []dashboards.App{webApp, jobApp}, false)
	want := `{"job": "` + idJob + `", "web": "` + idWeb + `"}`
	if got := w.kube.ConfigMapData["console"][0].Data["dashboards.json"]; err != nil || got != want {
		t.Fatalf("%s, %v\n%s", got, err, out)
	}
	expectIn(t, out, "recorded 2 dashboard addresses for the console")
	w.kube.Calls = nil
	out, _ = w.sync(t, []dashboards.App{webApp, jobApp}, false)
	if slices.Contains(w.kube.Calls, "set app-dashboards") || strings.Contains(out, "recorded") {
		t.Fatalf("an unchanged record was written: %q", w.kube.Calls)
	}
	w.kube.Failing = map[string]error{"set app-dashboards": &kube.Error{Code: kube.Failed, Subject: "configmap", Reason: "forbidden"}}
	w.hyperdx.Calls = nil
	out, err = w.sync(t, []dashboards.App{webApp}, false)
	if err == nil || !strings.Contains(err.Error(), "could not record dashboard addresses in console/app-dashboards") ||
		slices.ContainsFunc(w.hyperdx.Calls, func(call string) bool { return strings.Contains(call, "/alerts") }) {
		t.Fatalf("%v, %q\n%s", err, w.hyperdx.Calls, out)
	}
}

// Python case 15: an admin without an access key stops before any API request.
func TestNoAccessKeyStopsBeforeTheAPI(t *testing.T) {
	w := newDashWorld(t)
	w.hyperdx.Key = ""
	out, err := w.sync(t, nil, false)
	if err == nil || len(w.hyperdx.Calls) != 0 || !strings.Contains(out, "make clickstack-bootstrap") {
		t.Fatalf("%v, %q", err, w.hyperdx.Calls)
	}
}

// Python cases 20 and 23: a new app's first environment creates its dashboard; its next environment, or one
// removed, updates the same dashboard and never deletes it.
func TestEnvironmentsJoiningOrLeavingUpdateTheSameDashboard(t *testing.T) {
	w := newDashWorld(t)
	w.kube.Collections[helmReleases] = [][]byte{release(t, "new-app", "staging", true)}
	out, err := w.sync(t, nil, false)
	if err != nil || !slices.Equal(slices.DeleteFunc(w.hyperdx.Writes(), isAlert), []string{"POST /dashboards"}) {
		t.Fatalf("%v, %q\n%s", err, w.hyperdx.Writes(), out)
	}
	for _, environments := range [][]string{{"prod", "staging"}, {"staging"}} {
		w.hyperdx.Calls = nil
		w.kube.Collections[helmReleases] = nil
		for _, environment := range environments {
			w.kube.Collections[helmReleases] = append(w.kube.Collections[helmReleases], release(t, "new-app", environment, true))
		}
		out, err = w.sync(t, nil, false)
		writes := slices.DeleteFunc(w.hyperdx.Writes(), isAlert)
		if err != nil || len(writes) != 1 || !strings.HasPrefix(writes[0], "PUT /dashboards/") {
			t.Fatalf("%v, %q\n%s", err, writes, out)
		}
	}
	board := w.hyperdx.Held.Dashboards[0]
	if strings.Contains(board.Tiles[0].Config.Select[0].Where, "'new-app-prod'") {
		t.Fatalf("%+v", board)
	}
}

func isAlert(call string) bool { return strings.Contains(call, "/alerts") }

// Python cases 22, 24 and 25: the scheduled pass deletes only a tagged dashboard named exactly for an app that has
// no release left; a dry run only says so.
func TestTheClusterPassDeletesOnlyDashboardsNamedForAGoneApp(t *testing.T) {
	kept := dashboards.App{Name: "kept", Kind: dashboards.Web, Environments: []string{"staging"}}
	gone := dashboards.App{Name: "gone", Kind: dashboards.Web, Environments: []string{"staging", "prod"}}
	for _, dry := range []bool{false, true} {
		w := newDashWorld(t)
		w.kube.Collections[helmReleases] = [][]byte{release(t, "kept", "staging", true)}
		w.hold(boardOf(kept, idWeb), boardOf(gone, "d2"),
			dashboards.Dashboard{ID: "c1", Name: "App: kept (copy)", Tags: []string{dashboards.Tag}},
			dashboards.Dashboard{ID: "h1", Name: "App: other"})
		out, err := w.sync(t, nil, dry)
		if err != nil {
			t.Fatal(out)
		}
		writes := slices.DeleteFunc(w.hyperdx.Writes(), isAlert)
		if want := []string{"DELETE /dashboards/d2"}; dry && len(writes) != 0 || !dry && !slices.Equal(writes, want) {
			t.Fatalf("dry %t: %q\n%s", dry, writes, out)
		}
		verb := map[bool]string{false: "deleted", true: "would delete"}[dry]
		expectIn(t, out, verb+" App: gone (its app has no HelmRelease on the cluster)",
			"left App: kept (copy): tagged swhurl-app but not named for an app")
	}
}

// Python cases 5, 26 and 31: no app release on the cluster (one being restored) deletes no dashboard, alert or
// record entry, but still repairs the webhooks.
func TestEmptyDiscoveryRepairsWebhooksAndPrunesNothing(t *testing.T) {
	w := newDashWorld(t)
	w.hyperdx.Held.Webhooks[1].Body = "stale"
	w.hold(dashboards.Dashboard{ID: "old", Name: "App: gone", Tags: []string{dashboards.Tag}})
	w.hyperdx.Held.Alerts = []dashboards.Alert{defaultAlert("gone", "errors", "Errors", "old")}
	out, err := w.sync(t, nil, false)
	if err != nil {
		t.Fatal(out)
	}
	expectWrites(t, w.hyperdx, out, "PUT /webhooks/w1")
	if slices.Contains(w.kube.Calls, "set app-dashboards") {
		t.Fatal("the record was written")
	}
	expectIn(t, out, "updated swhurl notify", "not deleting dashboards (1 left)")
}

// Python case 27: a discovery that fails stops the pass before any request to ClickStack.
func TestDiscoveryFailureStopsBeforeTheAPI(t *testing.T) {
	w := newDashWorld(t)
	delete(w.kube.Collections, helmReleases)
	if out, err := w.sync(t, nil, false); err == nil || w.hyperdx.Opened != 0 || len(w.hyperdx.Calls) != 0 {
		t.Fatalf("%v\n%s", err, out)
	}
	w.kube.Collections[helmReleases] = [][]byte{[]byte("{")}
	if out, err := w.sync(t, nil, false); err == nil || w.hyperdx.Opened != 0 {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Deliberately changed: records that are missing or invalid stop the pass; nothing is deleted.
func TestBadRecordsStopThePass(t *testing.T) {
	w := newDashWorld(t)
	w.hold(boardOf(webApp, idWeb))
	bad := dashRecords(t, []dashboards.App{webApp})
	bad[1].Release = []byte("{")
	for _, records := range [][]RecordedInstance{{}, bad} {
		_, err := SyncDashboards(context.Background(), DashboardPorts{Reader: w.kube, Data: w.kube, ClickStack: w.hyperdx},
			DashboardOptions{FromRecords: true, Records: records})
		if err == nil || w.hyperdx.Opened != 0 {
			t.Fatalf("%d records: %v", len(records), err)
		}
	}
}

// Python case 28: the reserved webhooks are created, then repaired in place keeping their ids, then left; another
// webhook is never touched.
func TestWebhooksAreCreatedRepairedAndKeptWithStableIDs(t *testing.T) {
	w := newDashWorld(t)
	manual := dashboards.Webhook{ID: "manual", Name: "my webhook", Service: "generic", Body: "unchanged"}
	w.hyperdx.Held.Webhooks = []dashboards.Webhook{manual}
	out, err := w.sync(t, nil, false)
	if err != nil {
		t.Fatal(out)
	}
	expectWrites(t, w.hyperdx, out, "POST /webhooks", "POST /webhooks")
	ids := []string{w.hyperdx.Held.Webhooks[1].ID, w.hyperdx.Held.Webhooks[2].ID}
	for i := 1; i < 3; i++ {
		w.hyperdx.Held.Webhooks[i].Body, w.hyperdx.Held.Webhooks[i].URL = "UI edit", "https://wrong.example/"
	}
	w.hyperdx.Calls = nil
	out, _ = w.sync(t, nil, false)
	expectWrites(t, w.hyperdx, out, "PUT /webhooks/"+ids[0], "PUT /webhooks/"+ids[1])
	w.hyperdx.Calls = nil
	out, _ = w.sync(t, nil, false)
	expectWrites(t, w.hyperdx, out)
	if !strings.Contains(out, "ntfy: platform is up to date") || !strings.Contains(out, "swhurl notify is up to date") ||
		w.hyperdx.Held.Webhooks[0] != manual {
		t.Fatalf("%+v\n%s", w.hyperdx.Held.Webhooks, out)
	}
}

// Python cases 29 and 30: a conflict on either reserved name, or a truncated listing, stops the pass before any
// webhook is written.
func TestAConflictOrATruncatedListingWritesNoWebhook(t *testing.T) {
	conflicted := newDashWorld(t)
	conflicted.hyperdx.Held.Webhooks[0].Body = "stale"
	conflicted.hyperdx.Held.Webhooks[1].Service = "slack"
	truncated := newDashWorld(t)
	truncated.hyperdx.Held.Webhooks = nil
	truncated.hyperdx.Totals = map[string]int{"/webhooks": 1}
	for _, w := range []dashWorld{conflicted, truncated} {
		out, err := w.sync(t, nil, false)
		if err == nil || !slices.Equal(w.hyperdx.Calls, []string{"GET /webhooks"}) {
			t.Fatalf("%v, %q\n%s", err, w.hyperdx.Calls, out)
		}
	}
}

// Python cases 34 and 35: a credential that is missing, empty, unreadable or invalid stops the pass before any
// webhook is written, and no error repeats a credential or what the API server answered.
func TestBadCredentialsStopBeforeAnyWebhookWrite(t *testing.T) {
	for _, secret := range []struct{ namespace, name, key string }{
		{"console", "notification-ntfy", "NTFY_PLATFORM_URL"}, {"notify", "notify-alerts", "secret"},
		{"observability", "clickstack-runtime-inputs", "CLICKSTACK_ADMIN_EMAIL"},
		{"observability", "clickstack-mongodb-hyperdx-hyperdx", "connectionString.standard"},
	} {
		for _, failure := range []string{"missing", "empty", "unreadable", "invalid"} {
			w := newDashWorld(t)
			breakSecret(w.kube, secret.namespace, secret.name, secret.key, failure)
			out, err := w.sync(t, nil, false)
			if err == nil || len(w.hyperdx.Writes()) != 0 || strings.Contains(out, "private-response") ||
				strings.Contains(out, "bad/topic") {
				t.Fatalf("%s %s: %v, %q\n%s", secret.name, failure, err, w.hyperdx.Writes(), out)
			}
		}
	}
}

func breakSecret(k *kubetest.Fake, namespace, name, key, failure string) {
	held := k.SecretData[namespace]
	i := slices.IndexFunc(held, func(s kube.Secret) bool { return s.Name == name })
	switch failure {
	case "missing":
		k.SecretData[namespace] = slices.Delete(held, i, i+1)
	case "empty":
		held[i].Data[key] = nil
	case "unreadable":
		k.Failing = map[string]error{"get secret " + name: &kube.Error{Code: kube.Failed, Subject: "secret", Reason: "forbidden"}}
	default:
		held[i].Data[key] = []byte("https://ntfy.sh/bad/topic") // valid only as an email or URI, never as a topic
		if key == "secret" {
			held[i].Data[key] = []byte{0xff}
		}
	}
}

// Python case 36: an answer outside 2xx fails the pass naming its status, never the answer or a credential, and
// nothing later in the pass is reconciled.
func TestAPIErrorsFailWithoutLaterReconciliation(t *testing.T) {
	for _, operation := range []string{"GET /webhooks", "PUT /webhooks/w0", "POST /webhooks"} {
		w := newDashWorld(t)
		if operation == "POST /webhooks" {
			w.hyperdx.Held.Webhooks = nil
		} else {
			w.hyperdx.Held.Webhooks[0].Body, w.hyperdx.Held.Webhooks[1].Body = "stale", "stale"
		}
		w.hyperdx.Failing = map[string]int{operation: 503}
		out, err := w.sync(t, nil, false)
		if err == nil || !strings.Contains(out, "HTTP 503") || w.hyperdx.Calls[len(w.hyperdx.Calls)-1] != operation ||
			strings.Contains(out, "updated ntfy: platform") || strings.Contains(out, "created ntfy: platform") {
			t.Fatalf("%s: %v, %q\n%s", operation, err, w.hyperdx.Calls, out)
		}
	}
}

// The runners reach ClickStack through the API server's exec: mongosh beside MongoDB, and Node in the app's ready
// pod, found once from its Deployment; a failed or nonzero exec is an error carrying neither program nor output.
func TestClickStackRunnersExecInTheRightContainers(t *testing.T) {
	k := kubetest.New(kubetest.Answers{})
	deployment := observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "observability", Name: "clickstack-app"}
	k.Objects = map[observe.Ref][]byte{deployment: []byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"clickstack-app","namespace":"observability"},` +
		`"spec":{"selector":{"matchLabels":{"app":"clickstack"}}}}`)}
	k.Collections = map[observe.Ref][][]byte{{APIVersion: "v1", Kind: "Pod", Namespace: "observability"}: {
		[]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"clickstack-app-7d9f","namespace":"observability"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],` +
			`"containerStatuses":[{"name":"app","ready":true}]}}`)}}
	executor := kubetest.NewExecutor(kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte("RESULT {}")}},
		kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte("RESULT {}")}},
		kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte("private-output"), ExitCode: 1}},
		kubetest.ExecAnswer{Err: errors.New("private-transport")})
	mongo, app := ClickStackRunners(k, executor)
	ctx := context.Background()
	if out, err := mongo(ctx, []byte("db = connect(private)")); err != nil || string(out) != "RESULT {}" {
		t.Fatalf("%s, %v", out, err)
	}
	if _, err := app(ctx, []byte("fetch")); err != nil {
		t.Fatal(err)
	}
	for _, run := range []func() error{
		func() error { _, err := app(ctx, []byte("fetch")); return err },
		func() error { _, err := mongo(ctx, []byte("db")); return err },
	} {
		if err := run(); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("%v", err)
		}
	}
	got := executor.Requests
	if got[0].Pod != "clickstack-mongodb-0" || got[0].Container != "mongod" || got[1].Pod != "clickstack-app-7d9f" ||
		got[1].Container != "app" || got[2].Pod != "clickstack-app-7d9f" || len(k.Reads) != 1 {
		t.Fatalf("%+v, reads %d", got, len(k.Reads))
	}
}

// The platform's own services get a dashboard each from the fixed catalogue, kept on the next pass, and a tagged
// dashboard of a service the catalogue no longer has is deleted with its reason. They are never recorded for the
// console and never carry an app's alerts.
func TestPlatformDashboardsAreCreatedKeptAndPrunedWithoutARecord(t *testing.T) {
	w := newDashWorld(t)
	stale := dashboards.Dashboard{ID: "s1", Name: "Platform: gone", Tags: []string{dashboards.PlatformTag}}
	w.hyperdx.Held.Dashboards = []dashboards.Dashboard{stale}
	out, err := w.sync(t, []dashboards.App{webApp}, false)
	if err != nil {
		t.Fatal(out)
	}
	creates := 0
	for _, call := range w.hyperdx.Writes() {
		if call == "POST /dashboards" {
			creates++
		}
	}
	if want := len(dashboards.PlatformServices()) + 1; creates != want { // the catalogue, and App: web
		t.Fatalf("%d dashboards created, want %d\n%s", creates, want, out)
	}
	expectIn(t, out, "created Platform: console", "deleted Platform: gone (its service is not in the platform catalogue)")
	if record := w.kube.ConfigMapData["console"][0].Data["dashboards.json"]; strings.Contains(record, "Platform") ||
		!strings.Contains(record, `"web"`) {
		t.Fatalf("the record holds %s", record)
	}
	for _, alert := range w.hyperdx.Held.Alerts {
		if strings.Contains(alert.Name, "platform") || strings.HasPrefix(alert.Name, "swhurl:console") {
			t.Errorf("an alert names a platform service: %q", alert.Name)
		}
	}

	w.hyperdx.Calls = nil
	out, err = w.sync(t, []dashboards.App{webApp}, false)
	if err != nil {
		t.Fatal(out)
	}
	for _, call := range w.hyperdx.Writes() {
		if strings.Contains(call, "/dashboards") {
			t.Fatalf("a second pass wrote %q\n%s", call, out)
		}
	}
	expectIn(t, out, "Platform: console is up to date", "Platform: nats is up to date")
}

// A dry run plans the platform dashboards and writes none of them.
func TestAPlatformDryRunWritesNothing(t *testing.T) {
	w := newDashWorld(t)
	w.hyperdx.Held.Dashboards = nil // the catalogue's dashboards are missing, so a dry run would create them
	_, err := w.sync(t, []dashboards.App{webApp}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range w.hyperdx.Writes() {
		if strings.Contains(call, "/dashboards") {
			t.Fatalf("a dry run wrote %q", call)
		}
	}
	out, _ := w.sync(t, []dashboards.App{webApp}, true)
	expectIn(t, out, "would create Platform: traefik")
}

// hold sets what HyperDX lists: the platform services' dashboards as they should be, then the test's own. A test
// that wants the platform pass to create or delete something assigns Held.Dashboards itself.
func (w dashWorld) hold(boards ...dashboards.Dashboard) {
	w.hyperdx.Held.Dashboards = append(platformBoards(), boards...)
}

// platformBoards is every catalogue dashboard as HyperDX lists it: correct, with ids on it and its tiles.
func platformBoards() []dashboards.Dashboard {
	boards := []dashboards.Dashboard{}
	for _, service := range dashboards.PlatformServices() {
		board := dashboards.PlatformDefinition(service, "t1", "l1")
		board.ID = "p-" + service.Name
		for i := range board.Tiles {
			board.Tiles[i].ID = board.ID + "-" + board.Tiles[i].Name
		}
		boards = append(boards, board)
	}
	return boards
}
