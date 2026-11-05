package notify_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/notify"
)

const secret = "webhook-secret"

// alert is ClickStack's webhook body for an error-logs alert on orders/staging.
var alert = map[string]any{"secret": secret, "state": "ALERT", "title": "🚨 swhurl:orders:error-logs",
	"link": "https://clickstack.example.test/dashboards/d1?from=1",
	"body": "Group: \"arrayElement(ResourceAttributes, 'k8s.namespace.name'):orders-staging\"\n16 meets or exceeds 5\n" +
		"Time Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)\n"}

const notAnAppsAlert = "not an alert of an app: its name is swhurl:<app>:<preset> and its group the app's namespace"

// fromClickStack is the request an alert body becomes once its secret matches.
func fromClickStack(t *testing.T, doc map[string]any) (notify.Request, error) {
	t.Helper()
	decoded, err := notify.DecodeAlert(encode(t, doc))
	if err != nil {
		return notify.Request{}, err
	}
	if err := decoded.Signed(); err != nil {
		return notify.Request{}, err
	}
	return decoded.Request(secret)
}

// test_a_firing_alert_becomes_a_high_priority_message_for_the_apps_team.
func TestAFiringAlertBecomesAHighPriorityMessageForTheAppsTeam(t *testing.T) {
	request, err := fromClickStack(t, alert)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(alert["body"].(string)))
	want := notify.Request{Instance: notify.Instance{App: "orders", Env: "staging"},
		Key: "alert:orders:staging:error-logs:ALERT:" + hex.EncodeToString(digest[:])[:16],
		Notice: notify.Notice{Title: "orders/staging alert: error logs", High: true, Link: alert["link"].(string),
			Message: "16 meets or exceeds 5\nTime Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)"}}
	if request != want {
		t.Fatalf("request = %+v, want %+v", request, want)
	}
	var state notify.State
	if queued, err := state.Accept(request, team, now); err != nil || len(queued) != 2 {
		t.Fatalf("Accept = %v, %v", queued, err)
	}
	entry := state.Pending[0]
	if entry.Title != want.Notice.Title || entry.Priority != 4 || entry.Click != want.Notice.Link || entry.App != "orders" ||
		entry.Env != "staging" || strings.Join(entry.Tags, ",") != "warning" {
		t.Fatalf("entry = %+v", entry)
	}
	again, _ := fromClickStack(t, alert)
	if queued, err := state.Accept(again, team, now); err != nil || len(queued) != 0 {
		t.Fatalf("the same alert twice is sent once: %v, %v", queued, err)
	}
	if encoded, _ := state.Encode(); strings.Contains(encoded, secret) {
		t.Fatalf("the secret reached the outbox: %s", encoded)
	}
}

// test_recovery_is_a_normal_priority_message_and_an_unknown_preset_keeps_its_name; the other default preset has
// its label.
func TestRecoveryIsANormalPriorityMessageAndAnUnknownPresetKeepsItsName(t *testing.T) {
	request, err := fromClickStack(t, with(alert, map[string]any{"state": "OK", "title": "✅ swhurl:orders:slow-pages"}))
	if err != nil {
		t.Fatal(err)
	}
	var state notify.State
	if _, err := state.Accept(request, team, now); err != nil {
		t.Fatal(err)
	}
	entry := state.Pending[0]
	if entry.Title != "orders/staging recovered: slow-pages" || entry.Priority != 3 || strings.Join(entry.Tags, ",") != "white_check_mark" {
		t.Fatalf("entry = %+v", entry)
	}
	if !strings.HasPrefix(request.Key, "alert:orders:staging:slow-pages:OK:") {
		t.Fatalf("key = %q", request.Key)
	}
	errors, _ := fromClickStack(t, with(alert, map[string]any{"title": "swhurl:orders:errors"}))
	if errors.Notice.Title != "orders/staging alert: failed requests or runs" {
		t.Fatalf("title = %q", errors.Notice.Title)
	}
}

// test_the_secret_is_required_and_compared_with_the_clusters: one that is absent or empty is refused before the
// cluster's is read, and one that differs once it is.
func TestTheSecretIsRequiredAndComparedWithTheClusters(t *testing.T) {
	const required = "the alert webhook secret is required"
	for _, doc := range []map[string]any{with(alert, map[string]any{"secret": ""}), with(alert, map[string]any{"secret": absent{}})} {
		decoded, err := notify.DecodeAlert(encode(t, doc))
		if err != nil {
			t.Fatal(err)
		}
		refused(t, decoded.Signed(), 401, required)
		_, err = decoded.Request(secret)
		refused(t, err, 401, required)
	}
	for _, guess := range []string{"guess", secret + "x", strings.ToUpper(secret), secret[:5]} {
		_, err := fromClickStack(t, with(alert, map[string]any{"secret": guess}))
		refused(t, err, 401, required)
	}
	decoded, _ := notify.DecodeAlert(encode(t, alert))
	_, err := decoded.Request("")
	refused(t, err, 401, required)
}

// test_only_an_apps_own_alert_for_its_own_namespace_is_accepted.
func TestOnlyAnAppsOwnAlertForItsOwnNamespaceIsAccepted(t *testing.T) {
	body := alert["body"].(string)
	for _, doc := range []map[string]any{with(alert, map[string]any{"title": "🚨 CPU high"}),
		with(alert, map[string]any{"body": "16 meets or exceeds 5"}),
		with(alert, map[string]any{"body": strings.ReplaceAll(body, "orders-staging", "billing-staging")}),
		with(alert, map[string]any{"body": strings.ReplaceAll(body, "orders-staging", "orders-dev")}),
		with(alert, map[string]any{"title": absent{}})} {
		_, err := fromClickStack(t, doc)
		refused(t, err, 400, notAnAppsAlert)
	}
	for _, raw := range []string{string(encode(t, with(alert, map[string]any{"title": 7}))),
		string(encode(t, with(alert, map[string]any{"secret": nil}))), "[]", "{", "null", `"x"`} {
		_, err := notify.DecodeAlert([]byte(raw))
		refused(t, err, 400, "the body must be one JSON object of strings")
	}
}

// What else the Python service did with an alert: any other field is ignored, a link that is not a bounded https
// URL is dropped, the detail is cut to a message's length, and the "Group:" line and blank lines are left out.
func TestAnAlertKeepsOnlyWhatANotificationShows(t *testing.T) {
	body := alert["body"].(string)
	request, err := fromClickStack(t, with(alert, map[string]any{"extra": 7, "link": "http://clickstack.example.test",
		"body": body + "\n\r\n  \nGroup: again\nlast\u2028line"}))
	if err != nil {
		t.Fatal(err)
	}
	if request.Notice.Link != "" || request.Notice.Message !=
		"16 meets or exceeds 5\nTime Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)\nlast\nline" {
		t.Fatalf("notice = %+v", request.Notice)
	}
	long, _ := fromClickStack(t, with(alert, map[string]any{"link": "https://" + strings.Repeat("x", 493)}))
	kept, _ := fromClickStack(t, with(alert, map[string]any{"link": "https://" + strings.Repeat("x", 492)}))
	if long.Notice.Link != "" || kept.Notice.Link == "" {
		t.Fatalf("links: %q, %q", long.Notice.Link, kept.Notice.Link)
	}
	huge, _ := fromClickStack(t, with(alert, map[string]any{"body": body + strings.Repeat("é", notify.MaxMessage)}))
	if got := []rune(huge.Notice.Message); len(got) != notify.MaxMessage || !strings.HasPrefix(huge.Notice.Message, "16 meets") {
		t.Fatalf("message of %d characters", len(got))
	}
	if huge.Notice.High != true || request.Key == huge.Key {
		t.Fatalf("a different body is a different key: %q", huge.Key)
	}
}
