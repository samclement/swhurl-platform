package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The embedded assets' hash is in every page's asset URLs, so a changed stylesheet or script is a new URL and no
// browser keeps the old one; this pins it, so a change to the assets is seen here.
func TestAssetHash(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	if server.assets != "93e3b193821e" {
		t.Fatalf("assets = %s", server.assets)
	}
}

// Overview renders the same bytes as the Python console. testdata/overview.html is that console's answer to the
// fixture in tests/test_console.py (a runner with five units, two app instances and a failing check), rendered by
// tests/render_console_goldens.py; the data below is what that fixture holds.
func TestOverviewIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	data := overviewData{
		shell: shell{Title: "Overview", Path: "/", Identity: "sam@swhurl.com", ReadAt: "09:43", RefreshURL: "/",
			Assets: "93e3b193821e", AutoRefresh: true, HasGithub: false},
		Checks: pages.ChecksOf([]pages.Check{{Section: "Flux Kustomizations", Entries: []pages.Entry{
			{Level: "ok", Message: "homelab-a"}, {Level: "ok", Message: "homelab-b"}, {Level: "ok", Message: "homelab-c"}}},
			{Section: "Applications", Entries: []pages.Entry{{Level: "ok", Message: "web/staging"}, {Level: "ok", Message: "my-api/staging"}}},
			{Section: "Secrets", Entries: []pages.Entry{{Level: "ok", Message: "age key"}, {Level: "ok", Message: "github token"}}}}),
		AppCount: 2,
		Units:    []pages.Unit{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}},
		Problems: []pages.Problem{
			{Level: "failing", Text: "my-api/staging is not Ready: <script>x</script>", Link: "/apps/my-api/staging"},
			{Level: "suspended", Text: "my-api/staging is suspended: Git changes are not applied to it until it is resumed", Link: "/apps/my-api/staging"}},
		PRsError: "No GitHub token is configured, so open pull requests are not listed.",
	}
	var rendered bytes.Buffer
	set := server.templates["overview.html"]
	if err := set.ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/overview.html")
	if err != nil {
		t.Fatal(err)
	}
	if rendered.String() != string(want) {
		os.WriteFile("/tmp/overview-go.html", rendered.Bytes(), 0o644)
		t.Fatalf("the page differs from the Python console's; wrote /tmp/overview-go.html")
	}
}

// unitJSON is one Flux Kustomization as kubectl's JSON lists it, with the fields the units list shows.
func unitJSON(t *testing.T, name string, depends []string, ready, message string, suspend bool) []byte {
	t.Helper()
	deps := make([]map[string]string, 0, len(depends))
	for _, dependency := range depends {
		deps = append(deps, map[string]string{"name": dependency})
	}
	spec := map[string]any{"dependsOn": deps}
	if suspend {
		spec["suspend"] = true
	}
	document := map[string]any{
		"metadata": map[string]any{"name": name},
		"spec":     spec,
		"status": map[string]any{"lastAppliedRevision": "main@sha1:abc1234def",
			"conditions": []map[string]string{{"type": "Ready", "status": ready, "message": message}}},
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// The platform page renders the same bytes as the Python console's for the same units and checks: the fixture is
// tests/test_console.py's UNITS and fakecluster.HEALTHY_CHECKS, with the golden rendered by
// tests/render_console_goldens.py.
func TestPlatformIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	units := [][]byte{
		unitJSON(t, "cluster-sources", nil, "True", "Applied", false),
		unitJSON(t, "infra-base", nil, "True", "Applied", false),
		unitJSON(t, "platform-oauth2-proxy", []string{"infra-base"}, "True", "Applied", false),
		unitJSON(t, "app-web-prod", []string{"infra-base", "platform-oauth2-proxy"}, "True", "Applied", false),
		unitJSON(t, "app-my-api-staging", []string{"infra-base"}, "False", "<script>x</script>", true),
	}
	listed, err := pages.Units(units)
	if err != nil {
		t.Fatal(err)
	}
	checks := pages.ChecksOf([]pages.Check{
		{Section: "Flux Kustomizations", Entries: []pages.Entry{{Level: "ok", Message: "homelab-a"}, {Level: "ok", Message: "homelab-b"}}},
		{Section: "Ingress", Entries: []pages.Entry{{Level: "ok", Message: "Traefik redirects HTTP to HTTPS"}}},
		{Section: "Alerts", Entries: []pages.Entry{{Level: "ok", Message: "alert failures sends to ntfy-platform"}}},
		{Section: "Notify service", Entries: []pages.Entry{{Level: "ok", Message: "the notify service is running b63d2aff9dfd4cf9df0307e382038dcbd15e29ed, as the console"}}},
		{Section: "App dashboards and alerts", Entries: []pages.Entry{{Level: "ok", Message: "dashboard and alert sync succeeded in the last 5 minutes"}}},
		{Section: "Notifications", Entries: []pages.Entry{{Level: "ok", Message: "notification checker succeeded in the last 5 minutes"}}},
	})
	revision := pages.CommonRevision(listed)
	data := platformData{
		shell:  shell{Title: "Platform", Path: "/platform", Identity: "sam@swhurl.com", ReadAt: "09:43", RefreshURL: "/platform", Assets: "93e3b193821e", AutoRefresh: true},
		Checks: checks, Units: listed, Problems: pages.Problems(checks, listed), Updating: pages.Updating(listed),
		Groups: pages.CheckGroups(checks), Revision: revision,
	}
	for _, unit := range listed {
		if !unit.Healthy() {
			data.Unhealthy = append(data.Unhealthy, unit)
		}
		if unit.Revision != revision {
			data.Elsewhere = append(data.Elsewhere, unit)
		}
	}
	for _, layer := range append(pages.Layers, struct{ Prefix, Title, Text string }{Title: pages.OtherLayer}) {
		view := layerView{Title: layer.Title, Text: layer.Text}
		for _, unit := range listed {
			if unit.Layer() == layer.Title {
				view.Members = append(view.Members, unitRowView{UnitRow: pages.UnitRowOf(unit, revision), RefreshURL: "/platform",
					Refused: pages.RefusedUnits[unit.Name]})
			}
		}
		if len(view.Members) > 0 {
			data.Layers = append(data.Layers, view)
		}
	}
	var rendered bytes.Buffer
	if err := server.templates["platform.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/platform.html")
	if err != nil {
		t.Fatal(err)
	}
	if rendered.String() != string(want) {
		os.WriteFile("/tmp/platform-go.html", rendered.Bytes(), 0o644)
		t.Fatalf("the page differs from the Python console's; wrote /tmp/platform-go.html")
	}
}

// A request without oauth2-proxy's identity is refused before any page reads; /healthz needs nothing, and a path
// with a trailing slash goes to its canonical form.
func TestConsoleIdentity(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{})
	if err != nil {
		t.Fatal(err)
	}
	reached, wantIdentity := false, ""
	handler := server.identityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if got := identityOf(r); got != wantIdentity {
			t.Errorf("identity = %q, want %q", got, wantIdentity)
		}
	}))
	for name, want := range map[string]int{"/": http.StatusUnauthorized, "/apps": http.StatusUnauthorized} {
		request := httptest.NewRequest(http.MethodGet, name, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want || reached {
			t.Errorf("%s without identity: %d, reached=%v", name, response.Code, reached)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Errorf("/healthz: %d", response.Code)
	}
	reached = false
	request := httptest.NewRequest(http.MethodGet, "/apps/", nil)
	request.Header.Set(identityHeader, "sam@swhurl.com")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != "/apps" || reached {
		t.Errorf("trailing slash: %d %q", response.Code, response.Header().Get("Location"))
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(identityHeader, "sam@swhurl.com")
	wantIdentity = "sam@swhurl.com"
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !reached {
		t.Error("a signed-in request did not reach its page")
	}
}

// appsRows is the fixture's two apps: web/prod healthy, my-api/staging suspended and failing. test_console.py's
// declared() gives both the release's image and, for prod, its digest; my-api's release is absent, so its database
// path is unknown.
func appsRows(t *testing.T) []pages.AppRow {
	t.Helper()
	digest := sha256.Sum256([]byte("1.0"))
	inventory := func(image, digest string) *pages.Inventory {
		return &pages.Inventory{Team: "demo", Kind: "worker", Exposure: "private",
			Artifact: pages.Artifact{Image: image, Digest: digest}}
	}
	teams := map[string]string{"web": "demo", "my-api": "demo"}
	return []pages.AppRow{
		{Instance: pages.Instance{App: "web", Env: "prod"}, State: pages.State{Key: "healthy", Detail: "Applied"},
			Inventory: inventory("docker.io/x/web", "sha256:"+hex.EncodeToString(digest[:])), Teams: teams,
			Report: &pages.Report{Live: pages.Live{Release: pages.Evidence{Status: "present"}}}},
		{Instance: pages.Instance{App: "my-api", Env: "staging"},
			State:     pages.State{Key: "suspended", Detail: "<script>x</script>"},
			Suspended: true, Inventory: inventory("docker.io/x/web", ""), Teams: teams,
			Report: &pages.Report{Live: pages.Live{Release: pages.Evidence{Status: "absent"}}}},
	}
}

// The Deployments list and its map render the same bytes as the Python console's, for the fixture's apps.
func TestAppsIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	rows := appsRows(t)
	site := pages.Site{Domain: "homelab.swhurl.com", Owner: "samclement"}
	for _, one := range []struct{ golden, template, view, refresh string }{
		{"apps.html", "apps.html", "list", "/apps"},
		{"apps-map.html", "apps.html", "map", "/apps?view=map"},
	} {
		data := appsData{shell: shell{Title: "Deployments", Path: "/apps", Identity: "sam@swhurl.com", ReadAt: "09:43",
			RefreshURL: one.refresh, Assets: "93e3b193821e", AutoRefresh: true},
			AppsPage: pages.AppsPageOf(pages.AppsInput{Rows: rows, View: one.view, Env: "staging", RefreshURL: one.refresh,
				Stacks: map[string]string{"web": "typescript", "my-api": "typescript"}, Site: site})}
		var rendered bytes.Buffer
		if err := server.templates[one.template].ExecuteTemplate(&rendered, "base", data); err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile("testdata/" + one.golden)
		if err != nil {
			t.Fatal(err)
		}
		if rendered.String() != string(want) {
			written := "/tmp/" + strings.TrimSuffix(one.golden, ".html") + "-go.html"
			os.WriteFile(written, rendered.Bytes(), 0o644)
			t.Fatalf("%s differs from the Python console's; wrote %s", one.golden, written)
		}
	}
}

// The Egress page renders the same bytes as the Python console's: neither fixture instance is granted a host, so it
// shows the two sections and the "none" of each column.
func TestEgressIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	data := egressData{shell: shell{Title: "Egress", Path: "/egress", Identity: "sam@swhurl.com", ReadAt: "09:43",
		RefreshURL: "/egress", Assets: "93e3b193821e"},
		EgressPage: pages.EgressPageOf(appsRows(t), pages.Site{Domain: "homelab.swhurl.com", Owner: "samclement"})}
	var rendered bytes.Buffer
	if err := server.templates["egress.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/egress.html")
	if err != nil {
		t.Fatal(err)
	}
	if rendered.String() != string(want) {
		os.WriteFile("/tmp/egress-go.html", rendered.Bytes(), 0o644)
		t.Fatal("the page differs from the Python console's; wrote /tmp/egress-go.html")
	}
}

// The unit page renders the same bytes as the Python console's: infra-base, Ready, with nothing depending on it and
// nothing waiting for it.
func TestUnitIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := pages.Units([][]byte{
		unitJSON(t, "cluster-sources", nil, "True", "Applied", false),
		unitJSON(t, "infra-base", nil, "True", "Applied", false),
		unitJSON(t, "platform-oauth2-proxy", []string{"infra-base"}, "True", "Applied", false),
		unitJSON(t, "app-web-prod", []string{"infra-base", "platform-oauth2-proxy"}, "True", "Applied", false),
		unitJSON(t, "app-my-api-staging", []string{"infra-base"}, "False", "<script>x</script>", true),
	})
	if err != nil {
		t.Fatal(err)
	}
	var unit pages.Unit
	for _, one := range listed {
		if one.Name == "infra-base" {
			unit = one
		}
	}
	data := unitData{shell: shell{Title: unit.Name, Path: "/units/" + unit.Name, Identity: "sam@swhurl.com",
		ReadAt: "09:43", RefreshURL: "/units/" + unit.Name, Assets: "93e3b193821e"},
		Unit: unit, Refused: pages.RefusedUnits[unit.Name], Detail: unit.Ready.Detail, App: unitApp(unit.Name)}
	if data.Detail == "" {
		data.Detail = "applied " + unit.Revision
	}
	var rendered bytes.Buffer
	if err := server.templates["unit.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/unit.html")
	if err != nil {
		t.Fatal(err)
	}
	if rendered.String() != string(want) {
		os.WriteFile("/tmp/unit-go.html", rendered.Bytes(), 0o644)
		t.Fatal("the page differs from the Python console's; wrote /tmp/unit-go.html")
	}
}

// The Teams page renders the same bytes as the Python console's. The fixture has no GitHub token, so the registry
// cannot be read: the page then treats the teams the apps name as registered, and says so.
func TestTeamsIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	site := pages.Site{Domain: "homelab.swhurl.com", Owner: "samclement"}
	data := teamsData{shell: shell{Title: "Teams", Path: "/teams", Identity: "sam@swhurl.com", ReadAt: "09:43",
		RefreshURL: "/teams", Assets: "93e3b193821e"},
		TeamsPage: pages.TeamsPageOf(nil, false, appsRows(t), site,
			map[string]string{"web": "typescript", "my-api": "typescript"}, map[string]string{})}
	data.RegistryError = "No GitHub token is configured, so the team registry on main cannot be read."
	data.Github = false
	var rendered bytes.Buffer
	if err := server.templates["teams.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/teams.html")
	if err != nil {
		t.Fatal(err)
	}
	if rendered.String() != string(want) {
		os.WriteFile("/tmp/teams-go.html", rendered.Bytes(), 0o644)
		t.Fatal("the page differs from the Python console's; wrote /tmp/teams-go.html")
	}
}

// The catalogue's documents, as test_console.py's ConnectionsTests states them: orders documents its API's one route
// and the payload of the event it publishes.
const (
	catalogueOpenAPI  = `{"openapi": "3.1.0", "info": {"title": "orders", "description": "Create and read orders"}, "paths": {"/orders/{id}": {"get": {"summary": "Read one order"}}}}`
	catalogueAsyncAPI = `asyncapi: 3.0.0
info: {title: orders, version: 0.1.0}
channels:
  created:
    address: created
    description: An order was <b>accepted</b>
    messages:
      created:
        payload: {type: object, required: [id], properties: {id: {type: string}}}
        examples: [{payload: {id: o-1}}]
operations:
  sendCreated: {action: send, channel: {$ref: '#/channels/created'}}
`
)

// catalogueRows is the fixture's four instances: orders in both environments, reports that calls it, and a worker
// that receives its event.
func catalogueRows() []pages.AppRow {
	teams := map[string]string{"orders": "payments", "reports": "<b>insights", "job": "payments"}
	return []pages.AppRow{
		{Instance: pages.Instance{App: "orders", Env: "staging"}, Teams: teams, Inventory: &pages.Inventory{
			Team: "payments", Kind: "web", Port: 9000, Hosts: []string{"staging-orders.homelab.swhurl.com"},
			Address: "http://orders.orders-staging.svc.cluster.local:9000",
			API:     "https://staging-orders.homelab.swhurl.com/openapi.json", Offers: "abc1234",
			Callers: []string{"reports"}, Publishes: []string{"created", "paid"},
			Connections: []pages.Connection{{Role: "publishes", Event: "created"}, {Role: "publishes", Event: "paid"},
				{Role: "reaches", Host: "api.stripe.com:443"},
				{Role: "called-by", App: "reports", Outcome: "connected"},
				{Role: "subscriber", App: "job", Event: "created", Outcome: "connected"}}}},
		{Instance: pages.Instance{App: "orders", Env: "prod"}, Teams: teams, Inventory: &pages.Inventory{
			Team: "payments", Kind: "web", Port: 9000,
			Address:     "http://orders.orders-prod.svc.cluster.local:9000",
			API:         "http://orders.orders-prod.svc.cluster.local:9000/openapi.json",
			Publishes:   []string{"created", "paid"},
			Connections: []pages.Connection{{Role: "publishes", Event: "created"}, {Role: "publishes", Event: "paid"}}}},
		{Instance: pages.Instance{App: "reports", Env: "staging"}, Teams: teams, Inventory: &pages.Inventory{
			Team: "<b>insights", Kind: "web", Port: 8080,
			Address: "http://reports.reports-staging.svc.cluster.local:8080", Calls: []string{"orders"},
			Connections: []pages.Connection{{Role: "calls", App: "orders", Outcome: "connected"}}}},
		{Instance: pages.Instance{App: "job", Env: "staging"}, Teams: teams, Inventory: &pages.Inventory{
			Team: "payments", Kind: "worker", Subscriptions: []string{"orders.created"},
			Connections: []pages.Connection{{Role: "subscribes", App: "orders", Event: "created", Outcome: "connected"}}}},
	}
}

// The Catalogue page renders the same bytes as the Python console's in each of its states: nothing described, the
// fixture's own documents read, and a filter that leaves nothing. testdata/catalogue*.html are the Python console's
// answers, rendered by tests/render_console_goldens.py.
func TestCatalogueIsByteIdentical(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	site := pages.Site{Domain: "homelab.swhurl.com", Owner: "samclement"}
	tree := map[string][]byte{
		"v2-apps/instances/orders/staging/offers/openapi.json":  []byte(catalogueOpenAPI),
		"v2-apps/instances/orders/staging/offers/asyncapi.yaml": []byte(catalogueAsyncAPI),
	}
	rows := catalogueRows()
	for _, one := range []struct {
		golden, refresh string
		input           pages.CatalogueInput
	}{
		{"catalogue.html", "/catalogue", pages.CatalogueInput{Rows: appsRows(t), Site: site}},
		{"catalogue-described.html", "/catalogue", pages.CatalogueInput{Rows: rows, Site: site,
			Offers: orchestration.ConsoleOffers(tree, rows, "staging")}},
		{"catalogue-empty.html", "/catalogue?env=prod&kind=event&team=nobody", pages.CatalogueInput{Rows: rows,
			Env: "prod", Kind: "event", Team: "nobody", Site: site, Offers: orchestration.ConsoleOffers(tree, rows, "prod")}},
	} {
		data := catalogueData{shell: shell{Title: "Catalogue", Path: "/catalogue", Identity: "sam@swhurl.com", ReadAt: "09:43",
			RefreshURL: one.refresh, Assets: "93e3b193821e"}, CataloguePage: pages.CataloguePageOf(one.input)}
		var rendered bytes.Buffer
		if err := server.templates["catalogue.html"].ExecuteTemplate(&rendered, "base", data); err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile("testdata/" + one.golden)
		if err != nil {
			t.Fatal(err)
		}
		if rendered.String() != string(want) {
			written := "/tmp/" + strings.TrimSuffix(one.golden, ".html") + "-go.html"
			os.WriteFile(written, rendered.Bytes(), 0o644)
			t.Fatalf("%s differs from the Python console's; wrote %s", one.golden, written)
		}
	}
}

// A job row on a unit page and on a team's card renders: the mark its state maps to, the job's link and its words.
// The byte-identical fixtures have no jobs, so only this catches a call site handing the "job" template the state
// string where it reads a field from a job — which failed on the deployed console the first time a unit had one.
func TestJobRowsRender(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := pages.Units([][]byte{unitJSON(t, "infra-base", nil, "True", "Applied", false)})
	if err != nil {
		t.Fatal(err)
	}
	unit := listed[0]
	unitPage := unitData{shell: shell{Title: unit.Name, Path: "/units/" + unit.Name, Identity: "sam@swhurl.com",
		ReadAt: "09:43", RefreshURL: "/units/" + unit.Name, Assets: "93e3b193821e"},
		Unit: unit, Refused: pages.RefusedUnits[unit.Name], Detail: unit.Ready.Detail, App: unitApp(unit.Name)}
	unitPage.JobViews = []jobView{
		{ID: 61, Action: "reconcile", Identity: "sam@swhurl.com", State: "interrupted", StartedClock: "21:29"},
		{ID: 62, Action: "reconcile", Identity: "sam@swhurl.com", State: "running", StartedClock: "21:31"},
	}
	var rendered bytes.Buffer
	if err := server.templates["unit.html"].ExecuteTemplate(&rendered, "base", unitPage); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), `<span class="mark failing" aria-hidden="true"></span> <a href="/jobs/61">reconcile</a>`) ||
		!strings.Contains(rendered.String(), `<span class="mark updating" aria-hidden="true"></span> <a href="/jobs/62">reconcile</a>`) {
		t.Fatal("a unit's job rows do not carry their state's mark and their job's link")
	}

	teams := teamsData{shell: shell{Title: "Teams", Path: "/teams", Identity: "sam@swhurl.com", ReadAt: "09:43",
		RefreshURL: "/teams", Assets: "93e3b193821e"},
		TeamsPage: pages.TeamsPage{Teams: []pages.TeamView{{Name: "web",
			Job: &pages.TeamJob{ID: 63, Action: "team edit", State: "succeeded"}}}}}
	rendered.Reset()
	if err := server.templates["teams.html"].ExecuteTemplate(&rendered, "base", teams); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), `<span class="mark healthy" aria-hidden="true"></span> Last change: <a href="/jobs/63">team edit succeeded</a>`) {
		t.Fatal("a team's last change does not carry its state's mark and its job's link")
	}
}
