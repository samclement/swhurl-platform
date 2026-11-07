package dashboards

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// withIDs is a dashboard as HyperDX lists it: its id, and an id on every tile.
func withIDs(board Dashboard, id string) Dashboard {
	board.ID = id
	board.Tiles = slices.Clone(board.Tiles)
	for i := range board.Tiles {
		board.Tiles[i].ID = id + "-" + board.Tiles[i].Name
	}
	return board
}

func liveAlert(app, preset, tile, dashboard string) Alert {
	return Alert{ID: "a-" + app + "-" + preset, Name: "swhurl:" + app + ":" + preset, Tags: []string{Tag, app}, Source: "tile",
		DashboardID: dashboard, TileID: dashboard + "-" + tile, Threshold: 5, ThresholdType: "above", Interval: "5m",
		Channel: Channel{Type: "webhook", WebhookID: "w1"}}
}

func summary(steps []AlertStep) []string {
	out := []string{}
	for _, step := range steps {
		out = append(out, string(step.Action)+" "+step.Alert.Name)
	}
	return out
}

// An app gets one alert per preset on its dashboard's tiles, tagged with its name, through the notify webhook.
func TestAnAppGetsOneAlertPerPresetOnItsDashboardsTiles(t *testing.T) {
	steps, err := PlanAlerts([]App{web}, []Dashboard{withIDs(Definition(web, "t1", "l1"), "d1")}, nil, "w1", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []AlertStep{
		{Action: Create, Alert: liveAlert("web", "error-logs", "Error logs", "d1")},
		{Action: Create, Alert: liveAlert("web", "errors", "Errors", "d1")},
	}
	for i := range want {
		want[i].Alert.ID = ""
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("%+v", steps)
	}
}

// Matching alerts are kept; one made before alerts carried their app's tag, or on a rebuilt dashboard's old tile,
// is updated in place; an alert of an app that is gone is deleted; another's own alert is never touched.
func TestAlertsThatMatchAreLeftAndTheRestAreUpdatedInPlaceOrDeleted(t *testing.T) {
	untagged := liveAlert("web", "error-logs", "Error logs", "d1")
	untagged.Tags = []string{Tag}
	rebuilt := liveAlert("web", "errors", "Errors", "d1")
	rebuilt.TileID = "old-tile"
	live := []Alert{untagged, rebuilt, liveAlert("gone", "errors", "Errors", "d9"), {ID: "mine", Name: "my own alert"}}
	boards := []Dashboard{withIDs(Definition(web, "t1", "l1"), "d1")}
	steps, err := PlanAlerts([]App{web}, boards, live, "w1", true)
	if err != nil || !slices.Equal(summary(steps), []string{"update swhurl:web:error-logs", "update swhurl:web:errors", "delete swhurl:gone:errors"}) {
		t.Fatalf("%q, %v", summary(steps), err)
	}
	if steps[0].ID != "a-web-error-logs" || !slices.Equal(steps[0].Alert.Tags, []string{Tag, "web"}) {
		t.Fatalf("%+v", steps[0])
	}
	steps, _ = PlanAlerts([]App{web}, boards, []Alert{liveAlert("web", "error-logs", "Error logs", "d1"), liveAlert("web", "errors", "Errors", "d1")}, "w1", true)
	if !slices.Equal(summary(steps), []string{"keep swhurl:web:error-logs", "keep swhurl:web:errors"}) {
		t.Fatalf("%q", summary(steps))
	}
	steps, _ = PlanAlerts(nil, nil, live, "w1", false)
	if len(steps) != 0 {
		t.Fatalf("without prune nothing is deleted: %q", summary(steps))
	}
}

// An app sets its own thresholds or switches its alerts off; a bad setting on the cluster falls back to the
// defaults.
func TestAnAppSetsItsOwnThresholdsOrSwitchesAlertsOff(t *testing.T) {
	boards := []Dashboard{withIDs(Definition(web, "t1", "l1"), "d1")}
	live := []Alert{liveAlert("web", "error-logs", "Error logs", "d1"), liveAlert("web", "errors", "Errors", "d1")}
	tuned := web
	tuned.Alerts = "error-logs=20,errors=none"
	steps, _ := PlanAlerts([]App{tuned}, boards, live, "w1", true)
	if !slices.Equal(summary(steps), []string{"update swhurl:web:error-logs", "delete swhurl:web:errors"}) || steps[0].Alert.Threshold != 20 {
		t.Fatalf("%+v", steps)
	}
	tuned.Alerts = "none"
	steps, _ = PlanAlerts([]App{tuned}, boards, live[:1], "w1", true)
	if !slices.Equal(summary(steps), []string{"delete swhurl:web:error-logs"}) {
		t.Fatalf("%q", summary(steps))
	}
	tuned.Alerts = "nonsense=1"
	steps, _ = PlanAlerts([]App{tuned}, boards, nil, "w1", true)
	if !slices.Equal(summary(steps), []string{"create swhurl:web:error-logs", "create swhurl:web:errors"}) {
		t.Fatalf("%q", summary(steps))
	}
}

// Deliberately changed from Python, which deleted all but the first: two alerts of one name stop the plan.
func TestDuplicateOrIncompleteAlertsStopThePlan(t *testing.T) {
	missing := liveAlert("web", "errors", "Errors", "d1")
	missing.ID = ""
	for _, live := range [][]Alert{{liveAlert("web", "errors", "Errors", "d1"), liveAlert("web", "errors", "Errors", "d1")}, {missing}} {
		if _, err := PlanAlerts([]App{web}, nil, live, "w1", true); err == nil || !strings.Contains(err.Error(), "no change made") {
			t.Fatalf("%v", err)
		}
	}
	tileless := Dashboard{ID: "d1", Name: "App: web", Tags: []string{Tag}}
	if _, err := PlanAlerts([]App{web}, []Dashboard{tileless}, []Alert{liveAlert("web", "errors", "Errors", "d1")}, "w1", true); err == nil {
		t.Fatal("a dashboard listed without tiles would delete its app's alerts")
	}
	if _, err := PlanAlerts(nil, nil, []Alert{{Name: "mine"}, {Name: "mine"}}, "w1", true); err != nil {
		t.Fatalf("another's alerts are never considered: %v", err)
	}
}

func TestSettingsAreParsedStrictly(t *testing.T) {
	for spec, want := range map[string]map[string]int{
		"":                              {"error-logs": 5, "errors": 5},
		" error-logs=20 , errors=none ": {"error-logs": 20, "errors": 0},
		"none":                          {"error-logs": 0, "errors": 0},
	} {
		if got, err := Thresholds(spec); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, %v", spec, got, err)
		}
	}
	for bad, message := range map[string]string{
		"latency=5": `"latency" is not an alert: choose from error-logs, errors`,
		"errors=0":  `errors="0": give a number from 1 to 1000000, or none`,
		"errors":    `errors="": give a number`, "errors=-1": "give a number", "errors=1.5": "give a number",
		"errors=+5": "give a number", "errors=1000001": "give a number",
	} {
		if _, err := Thresholds(bad); err == nil || !strings.Contains(err.Error(), message) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// The notify service reads the app and preset from an alert's name, which HyperDX prefixes with an emoji.
func TestAnAlertsNameSaysWhichAppAndPresetItIs(t *testing.T) {
	if name := AlertName("orders-api", "error-logs"); name != "swhurl:orders-api:error-logs" {
		t.Fatal(name)
	}
}

// filled is a webhook body as HyperDX sends it: each variable JSON-escaped, the condition on the state decided.
func filled(template string, values map[string]string) string {
	priority := "3"
	if values["state"] == "ALERT" {
		priority = "4"
	}
	body := strings.Replace(template, `{{#if (eq state "ALERT")}}4{{else}}3{{/if}}`, priority, 1)
	for name, value := range values {
		escaped, _ := json.Marshal(value)
		body = strings.ReplaceAll(body, "{{"+name+"}}", string(escaped[1:len(escaped)-1]))
	}
	return body
}

var fired = map[string]string{"title": "🚨 swhurl:orders:error-logs", "link": "https://clickstack.example.test/dashboards/d1?from=1",
	"body": "Group: \"arrayElement(ResourceAttributes, 'k8s.namespace.name'):orders-staging\"\n16 meets or exceeds 5\n" +
		"Time Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)\n"}

// Both webhooks are the Python job's, byte for byte, so cutover rewrites neither; the notify webhook, filled in,
// is the body the notify service reads (the fixture its Go test consumes).
func TestTheWebhooksAreThePythonJobsAndTheNotifyServiceReadsThem(t *testing.T) {
	hooks, err := Destinations("https://ntfy.sh/swhurl-test-private-topic-012345", []byte("private-alert-secret"))
	var python []Webhook
	fixture(t, "webhooks.json", &python)
	if err != nil || !reflect.DeepEqual(hooks, python) {
		t.Fatalf("%+v, %v", hooks, err)
	}
	if strings.Contains(hooks[0].Body, "{{/if}}}") {
		t.Fatal("Handlebars reads {{/if}}} as a triple-brace close")
	}
	raw, err := os.ReadFile("../notify/testdata/clickstack-alerts.json")
	if err != nil {
		t.Fatal(err)
	}
	var bodies []map[string]any
	_ = json.Unmarshal(raw, &bodies)
	for _, want := range bodies {
		values := map[string]string{"state": want["state"].(string)}
		for key, value := range fired {
			values[key] = value
		}
		var sent map[string]any
		if err := json.Unmarshal([]byte(filled(hooks[1].Body, values)), &sent); err != nil || !reflect.DeepEqual(sent, want) {
			t.Errorf("%s: %v %v", values["state"], sent, err)
		}
	}
	var platform map[string]any
	if err := json.Unmarshal([]byte(filled(hooks[0].Body, map[string]string{"state": "OK", "title": "t", "body": "b", "link": "l"})), &platform); err != nil ||
		platform["priority"] != 3.0 || platform["topic"] != "swhurl-test-private-topic-012345" || platform["title"] != "ClickStack: t" {
		t.Fatalf("%v, %v", platform, err)
	}
}

// An address that is not an HTTPS topic URL, or no secret, makes no webhook; the error never repeats the address.
func TestInvalidCredentialsMakeNoWebhook(t *testing.T) {
	for _, address := range []string{"https://ntfy.sh/bad/topic", "http://ntfy.sh/topic", "https://ntfy.sh/", "https://u@ntfy.sh/t",
		"https://ntfy.sh/t?x=1", "https://ntfy.sh/t#f", ""} {
		if _, err := Destinations(address, []byte("s")); err == nil || address != "" && strings.Contains(err.Error(), address) {
			t.Errorf("%q: %v", address, err)
		}
	}
	for _, secret := range [][]byte{nil, {}, {0xff}} {
		if _, err := Destinations("https://ntfy.sh/t", secret); err == nil {
			t.Errorf("secret %q accepted", secret)
		}
	}
	hooks, err := Destinations("https://ntfy.sh//t//", []byte("é\"\n😀"))
	if err != nil || hooks[0].URL != "https://ntfy.sh/" || !strings.Contains(hooks[1].Body, `"secret":"\u00e9\"\n\ud83d\ude00"`) {
		t.Fatalf("%+v, %v", hooks, err)
	}
}

func owned(t *testing.T) []Webhook {
	t.Helper()
	hooks, err := Destinations("https://ntfy.sh/topic", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	hooks[0].ID, hooks[1].ID = "w0", "w1"
	return hooks
}

func TestWebhooksAreCreatedRepairedInPlaceOrKept(t *testing.T) {
	wanted := owned(t)
	for i := range wanted {
		wanted[i].ID = ""
	}
	manual := Webhook{ID: "manual", Name: "my webhook", Service: "generic", Body: "unchanged"}
	steps, err := PlanWebhooks([]Webhook{manual}, wanted)
	if err != nil || steps[0].Action != Create || steps[1].Action != Create {
		t.Fatalf("%+v, %v", steps, err)
	}
	live := owned(t)
	live[0].Body, live[1].URL = "UI edit", "https://wrong.example/"
	steps, _ = PlanWebhooks(append(live, manual), wanted)
	if steps[0].Action != Update || steps[0].ID != "w0" || steps[1].Action != Update || steps[1].ID != "w1" || steps[0].Webhook.ID != "" {
		t.Fatalf("%+v", steps)
	}
	steps, _ = PlanWebhooks(owned(t), wanted)
	if steps[0].Action != Keep || steps[1].Action != Keep {
		t.Fatalf("%+v", steps)
	}
}

// Both reserved names are checked before either is planned: a conflict on one holds a repair of the other.
func TestBothReservedNamesArePreflightedBeforeAnyWebhookWrite(t *testing.T) {
	wanted := owned(t)
	for index := range 2 {
		for _, conflict := range []string{"duplicate", "mixed", "wrong-service", "missing-id"} {
			hooks := owned(t)
			hooks[0].Body = "stale"
			switch conflict {
			case "duplicate", "mixed":
				extra := hooks[index]
				extra.ID = "duplicate"
				if conflict == "mixed" {
					extra.Service = "slack"
				}
				hooks = append(hooks, extra)
			case "wrong-service":
				hooks[index].Service = "slack"
			default:
				hooks[index].ID = ""
			}
			if steps, err := PlanWebhooks(hooks, wanted); err == nil || steps != nil || !strings.Contains(err.Error(), "no change made") {
				t.Errorf("%d %s: %+v, %v", index, conflict, steps, err)
			}
		}
	}
}
