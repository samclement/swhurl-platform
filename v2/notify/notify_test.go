package notify_test

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/notify"
)

// The retired Python service's tests (9766d4c5:tests/test_notify_service.py) supplied these cases: each keeps its
// input and expected status, reason or outbox entry. HTTP, Kubernetes and delivery-loop cases live in the
// orchestration and command tests.

const (
	now       = 1_800_000_000.0
	randomKey = "0123456789abcdef0123456789abcdef"
	address   = "https://ntfy.sh/private-topic"
)

var (
	team = []string{"payments-phone", "payments-desk"}
	// body is the checker's message about orders/staging.
	body = map[string]any{"app": "orders", "env": "staging", "key": "event:1@notify", "title": "orders/staging deployed",
		"message": "Flux UpgradeSucceeded", "priority": "normal", "click": "https://console.example.test/apps/orders/staging"}
	orders = sender("system:serviceaccount:orders-staging:orders")
)

// absent removes a field in with.
type absent struct{}

// with is base with some fields changed or, given absent, removed.
func with(base map[string]any, changes map[string]any) map[string]any {
	out := maps.Clone(base)
	for field, value := range changes {
		if _, remove := value.(absent); remove {
			delete(out, field)
		} else {
			out[field] = value
		}
	}
	return out
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sender(who string) notify.Sender {
	found, err := notify.SenderOf(who)
	if err != nil {
		panic(err)
	}
	return found
}

// refused fails unless err is the refusal with this status and reason.
func refused(t *testing.T, err error, status int, reason string) {
	t.Helper()
	var refusal *notify.Refusal
	if !errors.As(err, &refusal) || refusal.Status != status || refusal.Reason != reason {
		t.Fatalf("error = %#v, want %d %q", err, status, reason)
	}
}

// message decodes a message that must be accepted.
func message(t *testing.T, doc map[string]any, from notify.Sender) notify.Request {
	t.Helper()
	request, err := notify.DecodeMessage(encode(t, doc), from, randomKey)
	if err != nil {
		t.Fatalf("%v: %v", doc, err)
	}
	return request
}

const tokenRequired = "a service-account token for the audience notify is required"

// test_only_a_token_kubernetes_vouches_for_this_audience_is_a_caller: no token, another scheme, a token Kubernetes
// does not vouch for and one it vouches for another audience only are all 401.
func TestOnlyATokenKubernetesVouchesForThisAudienceIsACaller(t *testing.T) {
	for _, header := range []string{"", "Basic checker-token", "Bearer", "Bearer ", "Bearer  \t", "Bearerchecker-token"} {
		_, err := notify.Bearer(header)
		refused(t, err, 401, tokenRequired)
	}
	for header, want := range map[string]string{"Bearer checker-token": "checker-token",
		"bearer checker-token": "checker-token", "BEARER  checker-token \t": "checker-token"} {
		if token, err := notify.Bearer(header); err != nil || token != want {
			t.Fatalf("Bearer(%q) = %q, %v", header, token, err)
		}
	}
	_, err := notify.Identity(false, nil, "")
	refused(t, err, 401, tokenRequired)
	_, err = notify.Identity(true, []string{"orders"}, notify.Checker)
	refused(t, err, 401, tokenRequired)
	if who, err := notify.Identity(true, []string{"orders", "notify"}, notify.Checker); err != nil || who != notify.Checker {
		t.Fatalf("Identity = %q, %v", who, err)
	}
}

// test_a_person_or_a_service_account_that_is_not_an_apps_may_not_send.
func TestAPersonOrAServiceAccountThatIsNotAnAppsMayNotSend(t *testing.T) {
	for _, who := range []string{"kubernetes-admin", "system:serviceaccount:console:console",
		"system:serviceaccount:orders-staging:default", "", "system:serviceaccount:orders-dev:orders",
		"system:serviceaccount:orders-staging:billing", "system:serviceaccount:orders-staging:orders\n"} {
		_, err := notify.SenderOf(who)
		refused(t, err, 403, "this caller may not send notifications")
	}
	if checker := sender(notify.Checker); checker.IsApp() {
		t.Fatalf("the checker is the platform sender: %+v", checker)
	}
	for who, want := range map[string]notify.Instance{
		"system:serviceaccount:orders-staging:orders": {App: "orders", Env: "staging"},
		"system:serviceaccount:billing-prod:billing":  {App: "billing", Env: "prod"},
		"system:serviceaccount:a-b-prod:a-b":          {App: "a-b", Env: "prod"},
	} {
		if found := sender(who); !found.IsApp() || found.Instance != want {
			t.Fatalf("SenderOf(%q) = %+v", who, found)
		}
	}
}

// test_an_app_sends_as_itself_with_its_name_on_the_title_and_needs_no_key.
func TestAnAppSendsAsItselfWithItsNameOnTheTitleAndNeedsNoKey(t *testing.T) {
	request := message(t, map[string]any{"title": " Order backlog ", "message": "412 waiting", "priority": "high"}, orders)
	want := notify.Request{Instance: notify.Instance{App: "orders", Env: "staging"}, Key: "app:orders:staging:" + randomKey,
		Notice: notify.Notice{Title: "orders/staging: Order backlog", Message: "412 waiting", High: true}}
	if request != want {
		t.Fatalf("request = %+v, want %+v", request, want)
	}
	var state notify.State
	if queued, err := state.Accept(request, team, now); err != nil || strings.Join(queued, ",") != "payments-phone,payments-desk" {
		t.Fatalf("Accept = %v, %v", queued, err)
	}
	entry := state.Pending[0]
	if entry.Title != "orders/staging: Order backlog" || entry.Message != "412 waiting" || entry.Priority != 4 ||
		entry.App != "orders" || entry.Env != "staging" || entry.Key != "app:orders:staging:"+randomKey+"@payments-phone" {
		t.Fatalf("entry = %+v", entry)
	}
	again := message(t, map[string]any{"title": "again", "key": "backlog-1"}, orders)
	if again.Key != "app:orders:staging:backlog-1" {
		t.Fatalf("its own key is kept under its name: %q", again.Key)
	}
	if queued, _ := state.Accept(again, team, now); len(queued) != 2 {
		t.Fatalf("first = %v", queued)
	}
	if queued, err := state.Accept(again, team, now); err != nil || len(queued) != 0 {
		t.Fatalf("its own key makes a retry harmless: %v, %v", queued, err)
	}
	message(t, map[string]any{"title": "x", "app": "orders", "env": "staging"}, orders)
}

// test_an_app_cannot_send_about_another_app_or_environment.
func TestAnAppCannotSendAboutAnotherAppOrEnvironment(t *testing.T) {
	for _, doc := range []map[string]any{{"title": "x", "app": "billing"}, {"title": "x", "env": "prod"},
		with(body, map[string]any{"app": "billing"})} {
		_, err := notify.DecodeMessage(encode(t, doc), orders, randomKey)
		refused(t, err, 403, "an app sends about itself only")
	}
	_, err := notify.DecodeMessage(encode(t, map[string]any{"message": "no title"}), orders, randomKey)
	refused(t, err, 400, "a title of at most 250 characters is required; a message is at most 3500")
	_, err = notify.DecodeMessage(encode(t, map[string]any{"title": "x", "app": 7}), orders, randomKey)
	refused(t, err, 400, "app, env, title, message, click and key are strings")
}

// test_an_app_is_limited_to_a_number_of_messages_an_hour_and_the_checker_is_not: the limit counts per instance;
// the checker, which is not an app, is never given to it.
func TestAnAppIsLimitedToANumberOfMessagesAnHour(t *testing.T) {
	var limit notify.RateLimit
	staging, billing := orders.Instance, notify.Instance{App: "billing", Env: "prod"}
	for range notify.RateCount {
		if err := limit.Take(staging, now); err != nil {
			t.Fatal(err)
		}
	}
	refused(t, limit.Take(staging, now), 429, "orders/staging has sent 30 messages in the last hour; try again later")
	refused(t, limit.Take(staging, now+notify.RateSeconds-1), 429,
		"orders/staging has sent 30 messages in the last hour; try again later")
	if err := limit.Take(billing, now); err != nil {
		t.Fatalf("each app has its own count: %v", err)
	}
	for range notify.RateCount {
		if err := limit.Take(staging, now+notify.RateSeconds); err != nil {
			t.Fatalf("a message an hour old no longer counts: %v", err)
		}
	}
	refused(t, limit.Take(staging, now+notify.RateSeconds), 429,
		"orders/staging has sent 30 messages in the last hour; try again later")
	if sender(notify.Checker).IsApp() {
		t.Fatal("the checker is not an app")
	}
}

// test_a_malformed_message_is_refused_and_says_why, with each reason in full.
func TestAMalformedMessageIsRefusedAndSaysWhy(t *testing.T) {
	const (
		name     = "app must be an app name and env staging or prod"
		lengths  = "a title of at most 250 characters is required; a message is at most 3500"
		priority = "priority is one of normal, high"
		click    = "click must be an https URL of at most 500 characters"
		key      = "key is required (at most 300 characters): the same key is delivered once"
	)
	cases := []struct {
		doc    any
		reason string
	}{
		{with(body, map[string]any{"app": "../x"}), name}, {with(body, map[string]any{"env": "dev"}), name},
		{with(body, map[string]any{"app": "orders\n"}), name}, {with(body, map[string]any{"app": absent{}}), name},
		{with(body, map[string]any{"title": " "}), lengths}, {with(body, map[string]any{"title": strings.Repeat("x", 251)}), lengths},
		{with(body, map[string]any{"title": strings.Repeat("é", 251)}), lengths},
		{with(body, map[string]any{"message": strings.Repeat("x", 3501)}), lengths},
		{with(body, map[string]any{"priority": "urgent"}), priority}, {with(body, map[string]any{"priority": nil}), priority},
		{with(body, map[string]any{"priority": 4}), priority},
		{with(body, map[string]any{"click": "javascript:alert(1)"}), click},
		{with(body, map[string]any{"click": "https://" + strings.Repeat("x", 493)}), click},
		{with(body, map[string]any{"key": ""}), key}, {with(body, map[string]any{"key": strings.Repeat("k", 301)}), key},
		{with(body, map[string]any{"channel": "payments-phone", "about": 1}), "unknown field about, channel"},
		{with(body, map[string]any{"title": 7}), "app, env, title, message, click and key are strings"},
		{with(body, map[string]any{"key": nil}), "app, env, title, message, click and key are strings"},
		{[]any{body}, "the body must be one JSON object"}, {nil, "the body must be one JSON object"},
		// The order of checks: the first that fails is the reason.
		{with(body, map[string]any{"app": "../x", "title": " "}), name},
		{with(body, map[string]any{"title": " ", "priority": "urgent"}), lengths},
		{with(body, map[string]any{"channel": "x", "title": 7}), "unknown field channel"},
	}
	for _, c := range cases {
		_, err := notify.DecodeMessage(encode(t, c.doc), notify.Sender{}, randomKey)
		refused(t, err, 400, c.reason)
	}
	for _, raw := range []string{"{", "", "\xff", `"text"`} {
		_, err := notify.DecodeMessage([]byte(raw), notify.Sender{}, randomKey)
		refused(t, err, 400, "the body must be one JSON object")
	}
	message(t, with(body, map[string]any{"message": strings.Repeat("x", 3000), "title": strings.Repeat("y", 250),
		"key": strings.Repeat("k", 300)}), notify.Sender{})
	message(t, with(body, map[string]any{"title": strings.Repeat("é", 250), "message": strings.Repeat("é", 3500),
		"click": "https://" + strings.Repeat("x", 492), "priority": absent{}}), notify.Sender{})
	refused(t, notify.CheckBody(make([]byte, notify.MaxBody+1)), 413, "a request is at most 16384 bytes")
	if err := notify.CheckBody(make([]byte, notify.MaxBody)); err != nil {
		t.Fatal(err)
	}
}

// A message's title is trimmed; its message, link and priority are kept as given, and a checker's message without
// a key is refused rather than given one.
func TestAMessageIsTheNoticeItStates(t *testing.T) {
	request := message(t, with(body, map[string]any{"title": "\t orders/staging deployed \n", "priority": "high"}), notify.Sender{})
	want := notify.Request{Instance: notify.Instance{App: "orders", Env: "staging"}, Key: "event:1@notify",
		Notice: notify.Notice{Title: "orders/staging deployed", Message: "Flux UpgradeSucceeded", High: true,
			Link: "https://console.example.test/apps/orders/staging"}}
	if request != want {
		t.Fatalf("request = %+v, want %+v", request, want)
	}
	_, err := notify.DecodeMessage(encode(t, with(body, map[string]any{"key": absent{}})), notify.Sender{}, randomKey)
	refused(t, err, 400, "key is required (at most 300 characters): the same key is delivered once")
	long := message(t, map[string]any{"title": strings.Repeat("x", 234)}, orders)
	if long.Notice.Title != "orders/staging: "+strings.Repeat("x", 234) {
		t.Fatalf("title = %q", long.Notice.Title)
	}
	_, err = notify.DecodeMessage(encode(t, map[string]any{"title": strings.Repeat("x", 235)}), orders, randomKey)
	refused(t, err, 400, "a title of at most 250 characters is required; a message is at most 3500")
}

// test_forwarding_names_the_app_and_proves_who_sends_with_the_token_file (tests/test_notifications.py): the body the
// checker sends is accepted as it is.
func TestTheCheckersRequestIsAcceptedAsItSendsIt(t *testing.T) {
	sent := map[string]any{"app": "orders", "env": "prod", "key": "event:1@notify", "priority": "high",
		"title": "orders/prod unhealthy", "message": "m", "click": "https://console.example.test/apps/orders/prod"}
	request := message(t, sent, sender(notify.Checker))
	want := notify.Request{Instance: notify.Instance{App: "orders", Env: "prod"}, Key: "event:1@notify",
		Notice: notify.Notice{Title: "orders/prod unhealthy", Message: "m", High: true, Link: "https://console.example.test/apps/orders/prod"}}
	if request != want {
		t.Fatalf("request = %+v, want %+v", request, want)
	}
	plain := message(t, with(sent, map[string]any{"click": "", "message": "", "priority": "normal"}), notify.Sender{})
	if plain.Notice.High || plain.Notice.Link != "" || plain.Notice.Message != "" {
		t.Fatalf("notice = %+v", plain.Notice)
	}
}

// An app's namespace is <app>-<env>: its own name may contain hyphens, its environment is staging or prod.
func TestAnInstanceIsNamedByItsNamespace(t *testing.T) {
	for namespace, want := range map[string]notify.Instance{"orders-staging": {App: "orders", Env: "staging"},
		"a-b-prod": {App: "a-b", Env: "prod"}, strings.Repeat("a", 40) + "-prod": {App: strings.Repeat("a", 40), Env: "prod"}} {
		if found, ok := notify.OfNamespace(namespace); !ok || found != want || found.Namespace() != namespace {
			t.Fatalf("OfNamespace(%q) = %+v, %v", namespace, found, ok)
		}
	}
	for _, namespace := range []string{"orders", "orders-dev", "-staging", "Orders-staging", "orders--staging",
		"1orders-staging", strings.Repeat("a", 41) + "-prod", "orders-staging\n"} {
		if found, ok := notify.OfNamespace(namespace); ok {
			t.Fatalf("OfNamespace(%q) = %+v", namespace, found)
		}
	}
}
