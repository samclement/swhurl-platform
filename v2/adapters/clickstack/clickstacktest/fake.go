// Package clickstacktest is an in-memory HyperDX for tests: its listings, writes and failures, and the Contract
// both it and the real adapter keep.
package clickstacktest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
	"github.com/samclement/swhurl-platform/v2/dashboards"
)

// Inventory is what HyperDX holds and lists.
type Inventory struct {
	Sources    []dashboards.Source
	Dashboards []dashboards.Dashboard
	Webhooks   []dashboards.Webhook
	Alerts     []dashboards.Alert
}

// Fake holds an Inventory for the admin whose MongoDB URI and email it names. Key is the admin's access key ("" when
// it has none). Failing answers an operation ("PUT /webhooks/w0", "GET /alerts") with that HTTP status; Totals
// states a listing's total ("/webhooks": 3), so a total above the items is a truncated listing. Calls records every
// operation in order, Bodies each write's body, Opened every Open.
type Fake struct {
	Key, URI, Email string
	Held            Inventory
	Failing         map[string]int
	Totals          map[string]int
	Calls           []string
	Bodies          []any // the body of each write, in order: a Webhook, Dashboard or Alert
	Opened          int
	created         int
}

var (
	_ clickstack.Opener = (*Fake)(nil)
	_ clickstack.API    = (*Fake)(nil)
)

// Open answers the key for the URI and email the fake holds, and refuses others as an unreadable answer.
func (f *Fake) Open(_ context.Context, uri, email string) (clickstack.API, error) {
	f.Opened++
	if uri == "" || email == "" {
		return nil, &clickstack.Error{Operation: "open", Reason: "credentials_required"}
	}
	if uri != f.URI || email != f.Email {
		return nil, &clickstack.Error{Operation: "admin key", Reason: "malformed"}
	}
	if f.Key == "" {
		return nil, clickstack.ErrNoAccessKey
	}
	return f, nil
}

// Writes is every operation other than a listing, in order.
func (f *Fake) Writes() []string {
	return slices.DeleteFunc(slices.Clone(f.Calls), func(call string) bool { return strings.HasPrefix(call, "GET ") })
}

func (f *Fake) request(operation string, body any) error {
	f.Calls = append(f.Calls, operation)
	if body != nil {
		f.Bodies = append(f.Bodies, body)
	}
	if status, ok := f.Failing[operation]; ok {
		return &clickstack.Error{Operation: operation, Reason: "HTTP " + strconv.Itoa(status)}
	}
	return nil
}

func (f *Fake) listing(path string, count int) error {
	if err := f.request("GET "+path, nil); err != nil {
		return err
	}
	if total, ok := f.Totals[path]; ok && total > count {
		return &clickstack.Error{Operation: "GET " + path, Reason: "truncated"}
	}
	return nil
}

// newID is a fresh ObjectId-shaped id.
func (f *Fake) newID() string {
	f.created++
	return fmt.Sprintf("%024x", 0xc0ffee000+f.created)
}

func (f *Fake) Sources(context.Context) ([]dashboards.Source, error) {
	if err := f.listing("/sources", len(f.Held.Sources)); err != nil {
		return nil, err
	}
	return slices.Clone(f.Held.Sources), nil
}

func (f *Fake) Webhooks(context.Context) ([]dashboards.Webhook, error) {
	if err := f.listing("/webhooks", len(f.Held.Webhooks)); err != nil {
		return nil, err
	}
	return slices.Clone(f.Held.Webhooks), nil
}

func (f *Fake) CreateWebhook(_ context.Context, webhook dashboards.Webhook) (string, error) {
	webhook.ID = ""
	if err := f.request("POST /webhooks", webhook); err != nil {
		return "", err
	}
	webhook.ID = f.newID()
	f.Held.Webhooks = append(f.Held.Webhooks, webhook)
	return webhook.ID, nil
}

// UpdateWebhook replaces the webhook's fields in place, keeping its id, as HyperDX's PUT does.
func (f *Fake) UpdateWebhook(_ context.Context, id string, webhook dashboards.Webhook) error {
	webhook.ID = ""
	if err := f.request("PUT /webhooks/"+id, webhook); err != nil {
		return err
	}
	i := slices.IndexFunc(f.Held.Webhooks, func(w dashboards.Webhook) bool { return w.ID == id })
	if i < 0 {
		return &clickstack.Error{Operation: "PUT /webhooks/" + id, Reason: "HTTP 404"}
	}
	webhook.ID = id
	f.Held.Webhooks[i] = webhook
	return nil
}

func (f *Fake) Dashboards(context.Context) ([]dashboards.Dashboard, error) {
	if err := f.listing("/dashboards", len(f.Held.Dashboards)); err != nil {
		return nil, err
	}
	out := make([]dashboards.Dashboard, 0, len(f.Held.Dashboards))
	for _, board := range f.Held.Dashboards {
		board.Tags, board.Tiles = slices.Clone(board.Tags), slices.Clone(board.Tiles)
		out = append(out, board)
	}
	return out, nil
}

// withTileIDs gives each tile a fresh id, as HyperDX does on every create and update, so an alert planned from
// tiles listed before a write names ids that no longer exist.
func (f *Fake) withTileIDs(board dashboards.Dashboard) dashboards.Dashboard {
	board.Tiles = slices.Clone(board.Tiles)
	for i := range board.Tiles {
		board.Tiles[i].ID = f.newID()
	}
	return board
}

func (f *Fake) CreateDashboard(_ context.Context, board dashboards.Dashboard) (string, error) {
	board.ID = ""
	if err := f.request("POST /dashboards", board); err != nil {
		return "", err
	}
	board.ID = f.newID()
	f.Held.Dashboards = append(f.Held.Dashboards, f.withTileIDs(board))
	return board.ID, nil
}

func (f *Fake) UpdateDashboard(_ context.Context, id string, board dashboards.Dashboard) error {
	board.ID = ""
	if err := f.request("PUT /dashboards/"+id, board); err != nil {
		return err
	}
	i := slices.IndexFunc(f.Held.Dashboards, func(d dashboards.Dashboard) bool { return d.ID == id })
	if i < 0 {
		return &clickstack.Error{Operation: "PUT /dashboards/" + id, Reason: "HTTP 404"}
	}
	board.ID = id
	f.Held.Dashboards[i] = f.withTileIDs(board)
	return nil
}

func (f *Fake) DeleteDashboard(_ context.Context, id string) error {
	if err := f.request("DELETE /dashboards/"+id, nil); err != nil {
		return err
	}
	f.Held.Dashboards = slices.DeleteFunc(f.Held.Dashboards, func(d dashboards.Dashboard) bool { return d.ID == id })
	return nil
}

func (f *Fake) Alerts(context.Context) ([]dashboards.Alert, error) {
	if err := f.listing("/alerts", len(f.Held.Alerts)); err != nil {
		return nil, err
	}
	out := make([]dashboards.Alert, 0, len(f.Held.Alerts))
	for _, alert := range f.Held.Alerts {
		alert.Tags = slices.Clone(alert.Tags)
		out = append(out, alert)
	}
	return out, nil
}

func (f *Fake) CreateAlert(_ context.Context, alert dashboards.Alert) error {
	alert.ID = ""
	if err := f.request("POST /alerts", alert); err != nil {
		return err
	}
	alert.ID = f.newID()
	f.Held.Alerts = append(f.Held.Alerts, alert)
	return nil
}

func (f *Fake) UpdateAlert(_ context.Context, id string, alert dashboards.Alert) error {
	alert.ID = ""
	if err := f.request("PUT /alerts/"+id, alert); err != nil {
		return err
	}
	i := slices.IndexFunc(f.Held.Alerts, func(a dashboards.Alert) bool { return a.ID == id })
	if i < 0 {
		return &clickstack.Error{Operation: "PUT /alerts/" + id, Reason: "HTTP 404"}
	}
	alert.ID = id
	f.Held.Alerts[i] = alert
	return nil
}

func (f *Fake) DeleteAlert(_ context.Context, id string) error {
	if err := f.request("DELETE /alerts/"+id, nil); err != nil {
		return err
	}
	f.Held.Alerts = slices.DeleteFunc(f.Held.Alerts, func(a dashboards.Alert) bool { return a.ID == id })
	return nil
}

// Scenario is the inventory a contract run starts from: what open is given to build its Opener over.
type Scenario = Fake

// Contract is the behaviour every clickstack.Opener and its API must have: the admin's key opens it once; listings
// are the inventory; a create answers an id the listing then holds; an update keeps the id; a delete removes;
// an answer outside 2xx is an error naming its status and no body; a listing stating more items than it holds is
// truncated, not partial; and an admin without a key is ErrNoAccessKey.
func Contract(t *testing.T, scenario Scenario, open func(*testing.T, *Scenario) clickstack.Opener) {
	t.Helper()
	ctx := context.Background()
	api := opened(t, scenario, open)
	listed(t, ctx, api, scenario)
	webhooksChange(t, ctx, api)
	dashboardsChange(t, ctx, api)
	alertsChange(t, ctx, api)
	failures(t, scenario, open)
}

func opened(t *testing.T, scenario Scenario, open func(*testing.T, *Scenario) clickstack.Opener) clickstack.API {
	t.Helper()
	keyless := scenario
	keyless.Key = ""
	if _, err := open(t, &keyless).Open(context.Background(), scenario.URI, scenario.Email); !errors.Is(err, clickstack.ErrNoAccessKey) {
		t.Fatalf("an admin without a key: %v", err)
	}
	api, err := open(t, &scenario).Open(context.Background(), scenario.URI, scenario.Email)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return api
}

func listed(t *testing.T, ctx context.Context, api clickstack.API, scenario Scenario) {
	t.Helper()
	sources, err := api.Sources(ctx)
	if err != nil || !slices.Equal(sources, scenario.Held.Sources) {
		t.Fatalf("sources: %+v, %v", sources, err)
	}
	webhooks, err := api.Webhooks(ctx)
	if err != nil || !slices.Equal(webhooks, scenario.Held.Webhooks) {
		t.Fatalf("webhooks: %+v, %v", webhooks, err)
	}
	boards, err := api.Dashboards(ctx)
	if err != nil || len(boards) != len(scenario.Held.Dashboards) || !sameBoards(boards, scenario.Held.Dashboards) {
		t.Fatalf("dashboards: %+v, %v", boards, err)
	}
	alerts, err := api.Alerts(ctx)
	if err != nil || !slices.EqualFunc(alerts, scenario.Held.Alerts, sameAlert) {
		t.Fatalf("alerts: %+v, %v", alerts, err)
	}
}

func sameBoards(a, b []dashboards.Dashboard) bool {
	return slices.EqualFunc(a, b, func(x, y dashboards.Dashboard) bool {
		return x.ID == y.ID && dashboards.Same(x, y) && slices.EqualFunc(x.Tiles, y.Tiles, func(p, q dashboards.Tile) bool { return p.ID == q.ID })
	})
}

func sameAlert(a, b dashboards.Alert) bool {
	return a.ID == b.ID && a.Name == b.Name && slices.Equal(a.Tags, b.Tags) && a.TileID == b.TileID &&
		a.DashboardID == b.DashboardID && a.Threshold == b.Threshold && a.Channel == b.Channel
}

func webhooksChange(t *testing.T, ctx context.Context, api clickstack.API) {
	t.Helper()
	hook := dashboards.Webhook{Name: "contract", Service: "generic", URL: "https://example.test/", Body: "{}"}
	id, err := api.CreateWebhook(ctx, hook)
	if err != nil || id == "" {
		t.Fatalf("create webhook: %q, %v", id, err)
	}
	hook.Body = `{"changed":true}`
	if err := api.UpdateWebhook(ctx, id, hook); err != nil {
		t.Fatalf("update webhook: %v", err)
	}
	hooks, _ := api.Webhooks(ctx)
	i := slices.IndexFunc(hooks, func(w dashboards.Webhook) bool { return w.Name == "contract" })
	if i < 0 || hooks[i].ID != id || hooks[i].Body != hook.Body {
		t.Fatalf("the webhook after update: %+v", hooks)
	}
}

func dashboardsChange(t *testing.T, ctx context.Context, api clickstack.API) {
	t.Helper()
	app := dashboards.App{Name: "contract", Kind: dashboards.Web, Environments: []string{"staging"}}
	board := dashboards.Definition(app, "t1", "l1")
	id, err := api.CreateDashboard(ctx, board)
	if err != nil || id == "" {
		t.Fatalf("create dashboard: %q, %v", id, err)
	}
	app.Environments = append(app.Environments, "prod")
	board = dashboards.Definition(app, "t1", "l1")
	if err := api.UpdateDashboard(ctx, id, board); err != nil {
		t.Fatalf("update dashboard: %v", err)
	}
	boards, _ := api.Dashboards(ctx)
	i := slices.IndexFunc(boards, func(d dashboards.Dashboard) bool { return d.ID == id })
	if i < 0 || !dashboards.Same(boards[i], board) || boards[i].Tiles[0].ID == "" {
		t.Fatalf("the dashboard after update: %+v", boards)
	}
	if err := api.DeleteDashboard(ctx, id); err != nil {
		t.Fatalf("delete dashboard: %v", err)
	}
	if boards, _ = api.Dashboards(ctx); slices.ContainsFunc(boards, func(d dashboards.Dashboard) bool { return d.ID == id }) {
		t.Fatal("a deleted dashboard is listed")
	}
}

func alertsChange(t *testing.T, ctx context.Context, api clickstack.API) {
	t.Helper()
	alert := dashboards.Alert{Name: "swhurl:contract:errors", Tags: []string{dashboards.Tag, "contract"}, Source: "tile",
		DashboardID: "d1", TileID: "d1-Errors", Threshold: 5, ThresholdType: "above", Interval: "5m",
		Channel: dashboards.Channel{Type: "webhook", WebhookID: "w1"}}
	if err := api.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("create alert: %v", err)
	}
	alerts, _ := api.Alerts(ctx)
	i := slices.IndexFunc(alerts, func(a dashboards.Alert) bool { return a.Name == alert.Name })
	if i < 0 || alerts[i].ID == "" {
		t.Fatalf("the alert created: %+v", alerts)
	}
	id := alerts[i].ID
	alert.Threshold = 20
	if err := api.UpdateAlert(ctx, id, alert); err != nil {
		t.Fatalf("update alert: %v", err)
	}
	if alerts, _ = api.Alerts(ctx); !slices.ContainsFunc(alerts, func(a dashboards.Alert) bool { return a.ID == id && a.Threshold == 20 }) {
		t.Fatalf("the alert after update: %+v", alerts)
	}
	if err := api.DeleteAlert(ctx, id); err != nil {
		t.Fatalf("delete alert: %v", err)
	}
	if alerts, _ = api.Alerts(ctx); slices.ContainsFunc(alerts, func(a dashboards.Alert) bool { return a.ID == id }) {
		t.Fatal("a deleted alert is listed")
	}
}

func failures(t *testing.T, scenario Scenario, open func(*testing.T, *Scenario) clickstack.Opener) {
	t.Helper()
	ctx := context.Background()
	failing := scenario
	failing.Failing = map[string]int{"GET /webhooks": 503, "DELETE /alerts/" + scenario.Held.Alerts[0].ID: 500}
	api, err := open(t, &failing).Open(ctx, scenario.URI, scenario.Email)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.Webhooks(ctx)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || strings.Contains(err.Error(), scenario.Key) {
		t.Fatalf("a refused listing: %v", err)
	}
	if err := api.DeleteAlert(ctx, scenario.Held.Alerts[0].ID); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("a refused delete: %v", err)
	}
	truncated := scenario
	truncated.Totals = map[string]int{"/alerts": len(scenario.Held.Alerts) + 1}
	api, _ = open(t, &truncated).Open(ctx, scenario.URI, scenario.Email)
	if alerts, err := api.Alerts(ctx); err == nil || alerts != nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("a truncated listing: %+v, %v", alerts, err)
	}
}
