package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy/ntfytest"
	"github.com/samclement/swhurl-platform/v2/notify"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// The notify service's scenario, as the Python service's tests state it: orders-staging belongs to team payments,
// whose channels are payments-phone and payments-desk.
const (
	notifyAddress = "https://ntfy.example.test/private-topic"
	notifyRandom  = "0123456789abcdef0123456789abcdef"
	webhookSecret = "webhook-secret"
)

var notifyTokens = map[string]string{
	"checker-token": notify.Checker, "app-token": "system:serviceaccount:orders-staging:orders",
	"other-app-token": "system:serviceaccount:billing-prod:billing", "operator-token": "kubernetes-admin",
	"console-token": "system:serviceaccount:console:console", "stranger-token": "system:serviceaccount:orders-staging:default",
}

var notifyBody = map[string]string{"app": "orders", "env": "staging", "key": "event:1@notify", "title": "orders/staging deployed",
	"message": "Flux UpgradeSucceeded", "priority": "normal", "click": "https://console.example.test/apps/orders/staging"}

var notifyAlert = map[string]string{"secret": webhookSecret, "state": "ALERT", "title": "🚨 swhurl:orders:error-logs",
	"link": "https://clickstack.example.test/dashboards/d1?from=1",
	"body": "Group: \"arrayElement(ResourceAttributes, 'k8s.namespace.name'):orders-staging\"\n16 meets or exceeds 5\n" +
		"Time Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)\n"}

// notifyScenario is the fake cluster a test starts from: the tokens, the namespace, the routes naming teams,
// the outbox holding state, the channels' Secrets for addresses and, unless alertSecret is empty, the webhook's.
type notifyScenario struct {
	teams       map[string][]string
	state       string
	addresses   map[string]string
	alertSecret string
}

func defaultScenario() notifyScenario {
	return notifyScenario{teams: map[string][]string{"payments": {"payments-phone", "payments-desk"}},
		addresses:   map[string]string{"payments-phone": notifyAddress, "payments-desk": notifyAddress + "-desk"},
		alertSecret: webhookSecret}
}

func (s notifyScenario) cluster(t *testing.T) *kubetest.Fake {
	t.Helper()
	fake := kubetest.New(kubetest.Answers{})
	fake.Tokens = map[string]kube.TokenReview{
		"other-audience-token": {Authenticated: true, Audiences: []string{"orders"}, Username: notify.Checker}}
	for token, who := range notifyTokens {
		fake.Tokens[token] = kube.TokenReview{Authenticated: true, Audiences: []string{notify.Audience}, Username: who}
	}
	namespace, err := kube.RefOf("", "Namespace", "", "orders-staging")
	if err != nil {
		t.Fatal(err)
	}
	fake.Objects = map[observe.Ref][]byte{namespace: []byte(
		`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"orders-staging","labels":{"platform.swhurl.com/team":"payments"}}}`)}
	routes, err := json.Marshal(map[string]any{"channels": map[string]any{}, "teams": s.teams})
	if err != nil {
		t.Fatal(err)
	}
	fake.ConfigMapData = map[string][]kube.ConfigMap{notify.Namespace: {
		{Name: notify.RoutesName, Data: map[string]string{notify.RoutesKey: string(routes)}},
		{Name: notify.OutboxName, Data: map[string]string{notify.OutboxKey: s.state}}}}
	fake.SecretData = map[string][]kube.Secret{notify.Namespace: {}}
	for channel, address := range s.addresses {
		fake.SecretData[notify.Namespace] = append(fake.SecretData[notify.Namespace], kube.Secret{Name: "channel-" + channel,
			Labels: map[string]string{notify.TransportLabel: "ntfy"}, Data: map[string][]byte{notify.AddressKey: []byte(address)}})
	}
	if s.alertSecret != "" {
		fake.SecretData[notify.Namespace] = append(fake.SecretData[notify.Namespace], kube.Secret{Name: notify.AlertSecret,
			Data: map[string][]byte{notify.AlertSecretKey: []byte(s.alertSecret)}})
	}
	return fake
}

// notifyHarness is the service over HTTP on the fake cluster and a fake ntfy, with a clock the test moves.
type notifyHarness struct {
	fake      *kubetest.Fake
	published *ntfytest.Fake
	service   *notifyService
	server    *httptest.Server
	log       *lockedBuffer
	now       time.Time
	connects  int
}

func newNotifyHarness(t *testing.T, scenario notifyScenario) *notifyHarness {
	t.Helper()
	h := &notifyHarness{fake: scenario.cluster(t), published: ntfytest.New(), log: &lockedBuffer{},
		now: time.Unix(1_800_000_000, 0)}
	connect := func() (orchestration.NotifyCluster, error) {
		h.connects++
		return orchestration.NotifyCluster{Reviewer: h.fake, Data: h.fake, Reader: h.fake}, nil
	}
	h.service = newNotifyService(connect, h.published, h.log, func() time.Time { return h.now },
		func() string { return notifyRandom })
	h.server = httptest.NewServer(h.service.handler())
	t.Cleanup(h.server.Close)
	return h
}

// lockedBuffer is a log the delivery loop and the test can both use.
type lockedBuffer struct {
	lock sync.Mutex
	data bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.data.Write(p)
}

func (b *lockedBuffer) String() string {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.data.String()
}

// post sends one request; body is sent as it is when it is bytes, and as JSON otherwise.
func (h *notifyHarness) post(t *testing.T, path, token string, body any) (int, string) {
	t.Helper()
	raw, ok := body.([]byte)
	if !ok {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(http.MethodPost, h.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	answered, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("answered %s", response.Header.Get("Content-Type"))
	}
	return response.StatusCode, string(answered)
}

func (h *notifyHarness) message(t *testing.T, token string, body any) (int, string) {
	t.Helper()
	return h.post(t, "/v1/notify", token, body)
}

// outbox is the outbox as the ConfigMap holds it.
func (h *notifyHarness) outbox(t *testing.T) notify.State {
	t.Helper()
	state, err := notify.DecodeState(h.fake.ConfigMapData[notify.Namespace][1].Data[notify.OutboxKey])
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// count is how many of the fake's calls were the operation.
func (h *notifyHarness) count(operation string) int {
	found := 0
	for _, call := range h.fake.Calls {
		if call == operation {
			found++
		}
	}
	return found
}

func (h *notifyHarness) woken() bool {
	select {
	case <-h.service.wake:
		return true
	default:
		return false
	}
}

func refusal(reason string) string { return `{"error":"` + reason + `"}` }

func with(body map[string]string, changes ...string) map[string]string {
	changed := map[string]string{}
	for key, value := range body {
		changed[key] = value
	}
	for i := 0; i+1 < len(changes); i += 2 {
		changed[changes[i]] = changes[i+1]
	}
	return changed
}

var tokenRequired = refusal("a service-account token for the audience notify is required")

func TestOnlyATokenKubernetesVouchesForThisAudienceIsACaller(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	for _, token := range []string{"", "forged-token", "other-audience-token"} {
		if status, body := h.message(t, token, notifyBody); status != 401 || body != tokenRequired {
			t.Fatalf("%q: %d %s", token, status, body)
		}
	}
	request, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/notify", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Basic checker-token")
	if response, err := h.server.Client().Do(request); err != nil || response.StatusCode != 401 {
		t.Fatalf("another scheme: %v, %v", response, err)
	}
	if h.count("review") != 2 || h.connects != 2 {
		t.Fatalf("no token, no question to Kubernetes and no connection: %v, %d connections", h.fake.Calls, h.connects)
	}
	if h.count("set notify-outbox") != 0 || strings.Contains(h.log.String(), "-token") {
		t.Fatalf("nothing saved, no token logged: %v\n%s", h.fake.Calls, h.log)
	}
}

func TestAPersonOrAServiceAccountThatIsNotAnAppsMayNotSendAndIsLoggedByName(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	for _, token := range []string{"operator-token", "console-token", "stranger-token"} {
		status, body := h.message(t, token, notifyBody)
		if status != 403 || body != refusal("this caller may not send notifications") {
			t.Fatalf("%s: %d %s", token, status, body)
		}
		if want := "Refused a message from " + notifyTokens[token] + ": 403"; !strings.Contains(h.log.String(), want) {
			t.Fatalf("not logged by name: %s", h.log)
		}
	}
	if h.count("set notify-outbox") != 0 {
		t.Fatal("nothing is saved")
	}
}

func TestAnAppSendsAsItselfWithItsNameOnTheTitleAndNeedsNoKey(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	status, body := h.message(t, "app-token", map[string]string{"title": "payment failed", "priority": "high"})
	if status != 202 || body != `{"channels":["payments-phone","payments-desk"]}` {
		t.Fatalf("%d %s", status, body)
	}
	pending := h.outbox(t).Pending
	if len(pending) != 2 {
		t.Fatalf("saved %+v", pending)
	}
	entry := pending[0]
	if entry.Key != "app:orders:staging:"+notifyRandom+"@payments-phone" || entry.Title != "orders/staging: payment failed" ||
		entry.Priority != 4 || entry.App != "orders" || entry.Env != "staging" {
		t.Fatalf("saved %+v", entry)
	}
	if !strings.Contains(h.log.String(), `about orders/staging from orders for 2 channel(s)`) {
		t.Fatalf("log: %s", h.log)
	}
	if key := randomKey(); !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(key) || key == randomKey() {
		t.Fatalf("a random key is 32 lowercase hex characters, new each time: %s", key)
	}
}

func TestAnAppCannotSendAboutAnotherAppOrEnvironment(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	for _, body := range []map[string]string{{"app": "billing", "title": "x"}, {"env": "prod", "title": "x"}} {
		if status, answer := h.message(t, "app-token", body); status != 403 || answer != refusal("an app sends about itself only") {
			t.Fatalf("%v: %d %s", body, status, answer)
		}
	}
	if h.count("set notify-outbox") != 0 {
		t.Fatal("nothing is saved")
	}
}

func TestAnAppIsLimitedToANumberOfMessagesAnHourAndTheCheckerIsNot(t *testing.T) {
	h := newNotifyHarness(t, notifyScenario{teams: map[string][]string{}})
	title := map[string]string{"title": "x"}
	for range notify.RateCount {
		if status, body := h.message(t, "app-token", title); status != 202 {
			t.Fatalf("%d %s", status, body)
		}
	}
	status, body := h.message(t, "app-token", title)
	if status != 429 || body != refusal("orders/staging has sent 30 messages in the last hour; try again later") {
		t.Fatalf("%d %s", status, body)
	}
	if status, _ := h.message(t, "other-app-token", title); status != 202 {
		t.Fatal("each app has its own count")
	}
	for range notify.RateCount + 1 {
		if status, _ := h.message(t, "checker-token", notifyBody); status != 202 {
			t.Fatal("the checker is not an app")
		}
	}
	h.now = h.now.Add((notify.RateSeconds + 1) * time.Second)
	if status, _ := h.message(t, "app-token", title); status != 202 {
		t.Fatal("an hour later the app may send again")
	}
}

func TestATokenThatCannotBeCheckedIsNotTrusted(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	h.fake.Failing = map[string]error{"review": &kube.Error{Code: kube.Failed, Subject: "tokenreview", Reason: "forbidden"}}
	if status, body := h.message(t, "checker-token", notifyBody); status != 503 || body != refusal(notify.CannotReview) {
		t.Fatalf("%d %s", status, body)
	}
	if !strings.Contains(h.log.String(), `"level":"ERROR","logger":"notify","message":"Refused a message: 503 `+
		notify.CannotReview+` (failed: tokenreview: forbidden)"`) {
		t.Fatalf("the failure is logged: %s", h.log)
	}
	h.service.connect = func() (orchestration.NotifyCluster, error) {
		return orchestration.NotifyCluster{}, errors.New("invalid: in-cluster: token is not mounted")
	}
	if status, body := h.message(t, "checker-token", notifyBody); status != 503 || body != refusal(notify.CannotReview) {
		t.Fatalf("no connection: %d %s", status, body)
	}
}

func TestAFiringAlertBecomesAHighPriorityMessageForTheAppsTeam(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	status, body := h.post(t, "/v1/alert", "", notifyAlert)
	if status != 202 || body != `{"channels":["payments-phone","payments-desk"]}` {
		t.Fatalf("%d %s", status, body)
	}
	pending := h.outbox(t).Pending
	if len(pending) != 2 {
		t.Fatalf("saved %+v", pending)
	}
	entry := pending[0]
	if entry.Priority != 4 || !strings.HasPrefix(entry.Title, "orders/staging alert: ") || entry.Click != notifyAlert["link"] {
		t.Fatalf("saved %+v", entry)
	}
	if !strings.Contains(h.log.String(), "from ClickStack for 2 channel(s)") || !h.woken() {
		t.Fatalf("log: %s", h.log)
	}
	saved := h.fake.ConfigMapData[notify.Namespace][1].Data[notify.OutboxKey]
	if strings.Contains(h.log.String()+saved, webhookSecret) {
		t.Fatal("the secret is neither logged nor saved")
	}
	if status, body := h.post(t, "/v1/alert", "", notifyAlert); status != 202 || body != `{"channels":[]}` {
		t.Fatalf("the same alert is queued once: %d %s", status, body)
	}
}

func TestTheAlertSecretIsRequiredAndComparedWithTheClusters(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	secretRequired := refusal("the alert webhook secret is required")
	for _, secret := range []string{"guess", ""} {
		if status, body := h.post(t, "/v1/alert", "checker-token", with(notifyAlert, "secret", secret)); status != 401 ||
			body != secretRequired {
			t.Fatalf("%q: %d %s", secret, status, body)
		}
	}
	if h.count("secrets notify-alerts") != 1 || h.count("review") != 0 {
		t.Fatalf("an empty secret reads nothing, and a token is no way in: %v", h.fake.Calls)
	}
	unset := newNotifyHarness(t, notifyScenario{teams: defaultScenario().teams})
	if status, body := unset.post(t, "/v1/alert", "", notifyAlert); status != 503 || body != refusal(notify.NoAlertSecret) {
		t.Fatalf("no Secret: %d %s", status, body)
	}
	if h.count("set notify-outbox")+unset.count("set notify-outbox") != 0 {
		t.Fatal("nothing is saved")
	}
}

func TestOnlyAnAppsOwnAlertForItsOwnNamespaceIsAccepted(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	body := with(notifyAlert, "body", strings.Replace(notifyAlert["body"], "orders-staging", "billing-staging", 1))
	status, answer := h.post(t, "/v1/alert", "", body)
	if status != 400 || answer != refusal("not an alert of an app: its name is swhurl:<app>:<preset> and its group the app's namespace") {
		t.Fatalf("%d %s", status, answer)
	}
	if status, answer := h.post(t, "/v1/alert", "", []byte(`[]`)); status != 400 ||
		answer != refusal("the body must be one JSON object of strings") {
		t.Fatalf("%d %s", status, answer)
	}
	if h.count("set notify-outbox") != 0 {
		t.Fatal("nothing is saved")
	}
}

func TestAMessageBecomesOneSavedEntryPerChannelOfTheAppsTeamBeforeAnythingIsSent(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	status, body := h.message(t, "checker-token", notifyBody)
	if status != 202 || body != `{"channels":["payments-phone","payments-desk"]}` {
		t.Fatalf("%d %s", status, body)
	}
	if h.published.Attempts != 0 || h.count("set notify-outbox") != 1 {
		t.Fatalf("accepting only saves: %d sends, %v", h.published.Attempts, h.fake.Calls)
	}
	var got [][]string
	for _, entry := range h.outbox(t).Pending {
		got = append(got, []string{entry.Key, entry.Channel, entry.Title, entry.App, entry.Env})
	}
	want := [][]string{{"event:1@notify@payments-phone", "payments-phone", "orders/staging deployed", "orders", "staging"},
		{"event:1@notify@payments-desk", "payments-desk", "orders/staging deployed", "orders", "staging"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("saved %v", got)
	}
	if !strings.Contains(h.log.String(),
		`Accepted \"orders/staging deployed\" about orders/staging from notification-check for 2 channel(s)`) {
		t.Fatalf("log: %s", h.log)
	}
	if !h.woken() {
		t.Fatal("delivery starts at once")
	}
	if status, body := h.message(t, "checker-token", notifyBody); status != 202 || body != `{"channels":[]}` || h.woken() {
		t.Fatalf("the same key is queued once: %d %s", status, body)
	}
	if h.count("set notify-outbox") != 1 {
		t.Fatal("nothing queued, nothing saved")
	}
	response, err := h.server.Client().Get(h.server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if health, _ := io.ReadAll(response.Body); string(health) != "ok\n" || response.StatusCode != 200 {
		t.Fatalf("healthz: %d %q", response.StatusCode, health)
	}
}

func TestAnAppWithoutATeamChannelOrNamespaceIsAcceptedForNoChannel(t *testing.T) {
	for _, tt := range []struct {
		name  string
		body  map[string]string
		teams map[string][]string
	}{
		{"no channel", notifyBody, map[string][]string{}},
		{"no namespace", with(notifyBody, "app", "ghost"), defaultScenario().teams},
		{"another environment", with(notifyBody, "env", "prod"), defaultScenario().teams},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newNotifyHarness(t, notifyScenario{teams: tt.teams})
			if status, body := h.message(t, "checker-token", tt.body); status != 202 || body != `{"channels":[]}` {
				t.Fatalf("%d %s", status, body)
			}
			if h.count("set notify-outbox") != 0 || h.woken() {
				t.Fatal("nothing queued, nothing saved")
			}
		})
	}
}

func TestAMalformedMessageIsRefusedAndSaysWhy(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	for _, tt := range []struct {
		body   any
		reason string
	}{
		{with(notifyBody, "env", "dev"), "app must be an app name and env staging or prod"},
		{with(notifyBody, "priority", "urgent"), "priority is one of normal, high"},
		{with(notifyBody, "channel", "payments-phone"), "unknown field channel"},
		{[]byte(`{`), "the body must be one JSON object"},
	} {
		if status, body := h.message(t, "checker-token", tt.body); status != 400 || body != refusal(tt.reason) {
			t.Fatalf("%v: %d %s", tt.body, status, body)
		}
	}
	largest := with(notifyBody, "message", strings.Repeat("x", 3000), "title", strings.Repeat("y", 250),
		"key", strings.Repeat("k", 300))
	if status, body := h.message(t, "checker-token", largest); status != 202 {
		t.Fatalf("the largest message: %d %s", status, body)
	}
	reviews := h.count("review")
	status, body := h.message(t, "", bytes.Repeat([]byte("x"), notify.MaxBody+1))
	if status != 413 || body != refusal("a request is at most 16384 bytes") || h.count("review") != reviews {
		t.Fatalf("too large, before any token is asked about: %d %s", status, body)
	}
	if status, _ := h.post(t, "/v1/alert", "", bytes.Repeat([]byte("x"), notify.MaxBody+1)); status != 413 {
		t.Fatalf("an alert too large: %d", status)
	}
	if h.count("set notify-outbox") != 1 {
		t.Fatal("only the accepted message is saved")
	}
}

func TestWhatCannotBeReadOrSavedIsAnswered503AndNothingIsKeptInMemoryOnly(t *testing.T) {
	failure := &kube.Error{Code: kube.Failed, Subject: "configmaps", Reason: "unavailable"}
	for _, tt := range []struct {
		failing, reason string
	}{
		{"configmaps " + notify.RoutesName, notify.CannotRoute},
		{"configmaps " + notify.OutboxName, notify.CannotSave},
		{"set " + notify.OutboxName, notify.CannotSave},
	} {
		t.Run(tt.failing, func(t *testing.T) {
			h := newNotifyHarness(t, defaultScenario())
			h.fake.Failing = map[string]error{tt.failing: failure}
			if status, body := h.message(t, "checker-token", notifyBody); status != 503 || body != refusal(tt.reason) {
				t.Fatalf("%d %s", status, body)
			}
			if len(h.outbox(t).Pending) != 0 {
				t.Fatal("nothing is saved")
			}
			h.fake.Failing = nil
			if status, body := h.message(t, "checker-token", notifyBody); status != 202 || body == `{"channels":[]}` {
				t.Fatalf("what failed is not remembered: %d %s", status, body)
			}
		})
	}
	h := newNotifyHarness(t, defaultScenario())
	h.fake.Objects = nil
	namespace, _ := kube.RefOf("", "Namespace", "", "orders-staging")
	h.fake.Answers.Failed = namespace
	if status, body := h.message(t, "checker-token", notifyBody); status != 503 || body != refusal(notify.CannotRoute) {
		t.Fatalf("the namespace: %d %s", status, body)
	}
	full := notify.State{Seen: map[string]float64{}}
	for i := range notify.MaxPending - 1 {
		full.Pending = append(full.Pending, notify.Entry{Key: string(rune('a'+i%26)) + strings.Repeat("k", i/26)})
	}
	encoded, err := full.Encode()
	if err != nil {
		t.Fatal(err)
	}
	scenario := defaultScenario()
	scenario.state = encoded
	h = newNotifyHarness(t, scenario)
	if status, body := h.message(t, "checker-token", notifyBody); status != 503 || body != refusal("the outbox is full; try again later") {
		t.Fatalf("full: %d %s", status, body)
	}
}

func TestEntriesGoToTheirChannelsAddressAndAreRemembered(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	h.message(t, "checker-token", with(notifyBody, "priority", "high"))
	h.service.pass(context.Background())
	if len(h.published.Sent) != 2 || h.published.Sent[0].Address != notifyAddress || h.published.Sent[1].Address != notifyAddress+"-desk" {
		t.Fatalf("sent %+v", h.published.Sent)
	}
	var sent struct {
		Title    string `json:"title"`
		Priority int    `json:"priority"`
		Click    string `json:"click"`
	}
	if err := json.Unmarshal(h.published.Sent[0].Document, &sent); err != nil || sent.Title != "orders/staging deployed" ||
		sent.Priority != 4 || sent.Click != notifyBody["click"] {
		t.Fatalf("published %s", h.published.Sent[0].Document)
	}
	state := h.outbox(t)
	until := float64(h.now.Unix()) + notify.History
	if len(state.Pending) != 0 || !reflect.DeepEqual(state.Seen,
		map[string]float64{"event:1@notify@payments-phone": until, "event:1@notify@payments-desk": until}) {
		t.Fatalf("saved %+v", state)
	}
	if !strings.Contains(h.log.String(), "Notification delivered: orders/staging deployed (channel payments-phone)") ||
		strings.Contains(h.log.String(), "private-topic") {
		t.Fatalf("log: %s", h.log)
	}
	if _, body := h.message(t, "checker-token", notifyBody); body != `{"channels":[]}` {
		t.Fatal("a repeated request is not sent twice")
	}
	calls := len(h.fake.Calls)
	h.service.pass(context.Background())
	if len(h.fake.Calls) != calls {
		t.Fatalf("an empty outbox reads nothing: %v", h.fake.Calls[calls:])
	}
}

func TestAChannelThatFailsOrHasNoSecretIsRetriedWithoutHoldingUpTheOthers(t *testing.T) {
	scenario := defaultScenario()
	scenario.addresses = map[string]string{"payments-desk": notifyAddress + "-desk"}
	h := newNotifyHarness(t, scenario)
	h.message(t, "checker-token", notifyBody)
	h.service.pass(context.Background())
	if len(h.published.Sent) != 1 || h.published.Sent[0].Address != notifyAddress+"-desk" {
		t.Fatalf("sent %+v", h.published.Sent)
	}
	if pending := h.outbox(t).Pending; len(pending) != 1 || pending[0].Channel != "payments-phone" {
		t.Fatalf("pending %+v", pending)
	}
	if !strings.Contains(h.log.String(), `"level":"ERROR","logger":"notify","message":"channel payments-phone: `+
		`no ntfy channel Secret with that name in the cluster (will retry)"`) {
		t.Fatalf("log: %s", h.log)
	}
	h.fake.SecretData[notify.Namespace] = defaultScenario().cluster(t).SecretData[notify.Namespace]
	h.published.Refusing[notifyAddress] = true
	h.now = h.now.Add(notify.ChannelRetry * time.Second)
	h.service.pass(context.Background())
	if !strings.Contains(h.log.String(), "channel payments-phone: failed: publish: status_403 (gave up)") ||
		strings.Contains(h.log.String(), "private-topic") {
		t.Fatalf("given up after the retry window, without the address: %s", h.log)
	}
	if state := h.outbox(t); len(state.Pending) != 0 || state.Seen["event:1@notify@payments-phone"] == 0 {
		t.Fatalf("a copy given up is remembered: %+v", state)
	}
}

func TestARestartedServiceSendsWhatTheLastOneAccepted(t *testing.T) {
	before := newNotifyHarness(t, defaultScenario())
	before.message(t, "checker-token", notifyBody)
	scenario := defaultScenario()
	scenario.state = before.fake.ConfigMapData[notify.Namespace][1].Data[notify.OutboxKey]
	after := newNotifyHarness(t, scenario)
	after.service.pass(context.Background())
	if len(after.published.Sent) != 2 || len(after.outbox(t).Pending) != 0 {
		t.Fatalf("sent %d, pending %+v", len(after.published.Sent), after.outbox(t).Pending)
	}
	scenario.state = `{"version":2}`
	refused := newNotifyHarness(t, scenario)
	refused.service.pass(context.Background())
	if !strings.Contains(refused.log.String(), "Delivery pass failed: the outbox is invalid; refusing to reset delivery history") ||
		refused.count("set notify-outbox") != 0 {
		t.Fatalf("another version is refused, never reset: %s", refused.log)
	}
	if status, body := refused.message(t, "checker-token", notifyBody); status != 503 || body != refusal(notify.CannotSave) {
		t.Fatalf("and nothing is accepted into it: %d %s", status, body)
	}
}

func TestTheDeliveryLoopRunsWhenWokenAndEveryInterval(t *testing.T) {
	for _, tt := range []struct {
		name     string
		interval time.Duration
		wake     bool
	}{{"woken", time.Hour, true}, {"interval", 10 * time.Millisecond, false}} {
		t.Run(tt.name, func(t *testing.T) {
			h := newNotifyHarness(t, defaultScenario())
			h.message(t, "checker-token", notifyBody)
			h.woken() // the accept's signal: this test decides
			ctx, cancel := context.WithCancel(context.Background())
			stopped := make(chan struct{})
			go func() { h.service.loop(ctx, tt.interval); close(stopped) }()
			if tt.wake {
				h.service.wakeUp()
			}
			deadline := time.Now().Add(5 * time.Second)
			for strings.Count(h.log.String(), "Notification delivered") < 2 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			<-stopped
			if got := strings.Count(h.log.String(), "Notification delivered"); got != 2 {
				t.Fatalf("delivered %d: %s", got, h.log)
			}
		})
	}
}

func TestEachRequestAndPassUsesAFreshConnection(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	h.message(t, "checker-token", notifyBody)
	h.message(t, "checker-token", notifyBody)
	h.service.pass(context.Background())
	if h.connects != 3 {
		t.Fatalf("connections: %d", h.connects)
	}
}

func TestTheServiceAccountIsReadAgainForEachConnection(t *testing.T) {
	var lock sync.Mutex
	var presented []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		presented = append(presented, r.Header.Get("Authorization"))
		lock.Unlock()
		_, _ = io.WriteString(w, `{"status":{"authenticated":true,"audiences":["notify"],"user":{"username":"x"}}}`)
	}))
	defer server.Close()
	mount, _ := serviceAccountMount(t, server)
	c := cli{readFile: readInput, inCluster: mount}
	for _, token := range []string{"first-token", "rotated-token"} {
		if err := os.WriteFile(filepath.Join(mount.dir, "token"), []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cluster, err := c.notifyCluster()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cluster.Reviewer.Review(context.Background(), "caller", notify.Audience); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(presented, []string{"Bearer first-token", "Bearer rotated-token"}) {
		t.Fatalf("presented %v", presented)
	}
	if err := os.Remove(filepath.Join(mount.dir, "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.notifyCluster(); err == nil || !strings.HasSuffix(err.Error(), "token is not mounted") {
		t.Fatalf("a missing token: %v", err)
	}
	var out, diagnostic bytes.Buffer
	c.out, c.diagnostic = &out, &diagnostic
	if status := c.notifyServe(request{listen: "127.0.0.1:0"}); status != 2 || !strings.Contains(diagnostic.String(), "token is not mounted") {
		t.Fatalf("outside a pod it does not start: %d %s", status, diagnostic.String())
	}
}

func TestNotifyServeRefusesAnAddressItCannotListenOn(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	mount, _ := serviceAccountMount(t, server)
	var out, diagnostic bytes.Buffer
	c := cli{readFile: readInput, inCluster: mount, out: &out, diagnostic: &diagnostic}
	if status := c.notifyServe(request{listen: "256.0.0.1:1"}); status != 1 || !strings.HasPrefix(diagnostic.String(), "io: listen: ") {
		t.Fatalf("%d %s", status, diagnostic.String())
	}
}

func TestNotifyServeTakesAnOptionalListenAddress(t *testing.T) {
	for _, tt := range []struct {
		args   []string
		listen string
		ok     bool
	}{
		{[]string{"notify-serve"}, ":8080", true},
		{[]string{"notify-serve", "--listen", "127.0.0.1:9000"}, "127.0.0.1:9000", true},
		{[]string{"notify-serve", "--listen", ""}, "", false},
		{[]string{"notify-serve", "--listen", "a", "--listen", "b"}, "", false},
		{[]string{"notify-serve", "extra"}, "", false},
	} {
		r, outcome := parse(tt.args)
		if (outcome == parsed) != tt.ok || tt.ok && r.listen != tt.listen {
			t.Fatalf("%v: %v %q", tt.args, outcome, r.listen)
		}
	}
	if !strings.Contains(usage, "  swhurl-v2 notify-serve [--listen ADDR]\n") {
		t.Fatal("usage")
	}
}
