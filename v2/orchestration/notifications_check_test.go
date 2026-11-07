package orchestration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/adapters/notifysvc"
	"github.com/samclement/swhurl-platform/v2/adapters/notifysvc/notifysvctest"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy/ntfytest"
	"github.com/samclement/swhurl-platform/v2/incident"
	"github.com/samclement/swhurl-platform/v2/notify"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

const checkerNow = 1791028800.0
const checkerBase = "https://console.example.test"

type checkerStore struct {
	*kubetest.Fake
	saved  []string
	failAt int
}

func (s *checkerStore) SetConfigMapKey(ctx context.Context, namespace, name, key, value, manager string) error {
	if namespace != incident.Namespace || name != incident.StateName || key != incident.StateKey || manager != incident.FieldManager {
		return errors.New("wrong state destination")
	}
	if s.failAt > 0 && len(s.saved)+1 == s.failAt {
		return errors.New("checkpoint failed")
	}
	s.saved = append(s.saved, value)
	return s.Fake.SetConfigMapKey(ctx, namespace, name, key, value, manager)
}
func checkerRig(t *testing.T) (orchestration.CheckerPorts, *checkerStore, *ntfytest.Fake, *notifysvctest.Fake, orchestration.CheckOptions) {
	t.Helper()
	raw, err := os.ReadFile("../incident/testdata/snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot incident.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	f := kubetest.New(kubetest.Answers{})
	f.ConfigMapData = map[string][]kube.ConfigMap{incident.Namespace: {{Name: incident.StateName, Data: map[string]string{"unrelated": "keep"}}}}
	f.Collections = map[observe.Ref][][]byte{}
	for _, group := range []struct {
		ref   observe.Ref
		items []incident.Object
	}{
		{observe.Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization", Namespace: "flux-system"}, snapshot.Units},
		{observe.Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease"}, snapshot.Releases},
		{observe.Ref{APIVersion: "apps/v1", Kind: "Deployment"}, snapshot.Workloads},
		{observe.Ref{APIVersion: "apps/v1", Kind: "StatefulSet"}, nil},
		{observe.Ref{APIVersion: "v1", Kind: "Pod"}, snapshot.Pods},
		{observe.Ref{APIVersion: "v1", Kind: "Event"}, nil},
	} {
		for _, item := range group.items {
			if _, ok := item["metadata"].(map[string]any)["name"]; !ok {
				item["metadata"].(map[string]any)["name"] = "app-example-staging"
			}
			if group.ref.Namespace != "" {
				item["metadata"].(map[string]any)["namespace"] = group.ref.Namespace
			}
			raw, _ := json.Marshal(item)
			f.Collections[group.ref] = append(f.Collections[group.ref], raw)
		}
		if _, ok := f.Collections[group.ref]; !ok {
			f.Collections[group.ref] = [][]byte{}
		}
	}
	store := &checkerStore{Fake: f}
	platform, copies := ntfytest.New(), &notifysvctest.Fake{}
	options := orchestration.CheckOptions{Now: checkerNow, Base: checkerBase, PlatformAddress: "https://ntfy.example.test/synthetic-topic", NotifyAddress: "http://notify.example.test", Token: "synthetic-token"}
	return orchestration.CheckerPorts{Cluster: f, Data: store, Platform: platform, Copies: copies}, store, platform, copies, options
}
func seedChecker(t *testing.T, store *checkerStore, options orchestration.CheckOptions, entries ...map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"version": 1, "started": options.Now - 1, "last_success": 0, "instances": map[string]any{}, "seen": map[string]any{}, "monitor": map[string]any{}, "pending": entries})
	// A nil variadic slice must encode an empty array, not null.
	if len(entries) == 0 {
		raw = []byte(strings.Replace(string(raw), `"pending":null`, `"pending":[]`, 1))
	}
	store.ConfigMapData[incident.Namespace][0].Data[incident.StateKey] = string(raw)
}
func pendingChecker(key string, copy bool) map[string]any {
	e := map[string]any{"key": key, "title": key, "message": "message", "priority": 3, "queued": checkerNow, "tags": []string{"white_check_mark"}, "click": checkerBase + "/apps/example/staging"}
	if copy {
		e["about"] = []string{"example", "staging"}
	}
	return e
}
func storedChecker(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}
func checkerPending(doc map[string]any) []any { return doc["pending"].([]any) }
func TestCheckerSavedBeforeSendingAndAfterEachAcceptance(t *testing.T) {
	ports, store, platform, copies, options := checkerRig(t)
	seedChecker(t, store, options, pendingChecker("platform", false), pendingChecker("copy", true))
	ports.Platform = checkedCheckerPublisher{Publisher: platform, before: func() {
		if len(store.saved) != 1 {
			t.Fatal("network send was not preceded by durable outbox")
		}
		doc := storedChecker(t, store.saved[0])
		if len(checkerPending(doc)) != 2 || len(doc["seen"].(map[string]any)) != 0 {
			t.Fatal("outbox acknowledged before acceptance")
		}
	}}
	report, err := orchestration.CheckNotifications(context.Background(), ports, options)
	if err != nil || len(report.Offered) != 2 || len(platform.Sent) != 1 || len(copies.Sent) != 1 {
		t.Fatalf("pass: %+v %v", report, err)
	}
	if len(store.saved) != 4 {
		t.Fatalf("checkpoints: %d", len(store.saved))
	}
	for i, want := range []int{2, 1, 0, 0} {
		if got := len(checkerPending(storedChecker(t, store.saved[i]))); got != want {
			t.Fatalf("checkpoint %d pending %d want %d", i, got, want)
		}
	}
	if store.ConfigMapData[incident.Namespace][0].Data["unrelated"] != "keep" {
		t.Fatal("unrelated state key lost")
	}
	if copies.Sent[0].Token != "synthetic-token" || copies.Sent[0].Request.App != "example" || copies.Sent[0].Request.Env != "staging" {
		t.Fatal("copy wire identity lost")
	}
}
func TestCheckerPlatformFailureAndRestart(t *testing.T) {
	ports, store, platform, _, options := checkerRig(t)
	seedChecker(t, store, options, pendingChecker("platform", false))
	platform.Fail = errors.New("synthetic transport failure")
	if _, err := orchestration.CheckNotifications(context.Background(), ports, options); err == nil {
		t.Fatal("platform failure succeeded")
	}
	failed := storedChecker(t, store.saved[len(store.saved)-1])
	if len(checkerPending(failed)) != 1 || len(failed["seen"].(map[string]any)) != 0 {
		t.Fatal("failed send acknowledged")
	}
	platform.Fail = nil
	report, err := orchestration.CheckNotifications(context.Background(), ports, options)
	if err != nil || len(report.State.Pending()) != 0 || len(platform.Sent) != 1 {
		t.Fatalf("restart: %+v %v", report, err)
	}
}
func TestCheckerCheckpointFailureStopsNetwork(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		ports, store, platform, _, options := checkerRig(t)
		seedChecker(t, store, options, pendingChecker("one", false), pendingChecker("two", false))
		store.failAt = failAt
		_, err := orchestration.CheckNotifications(context.Background(), ports, options)
		if err == nil || platform.Attempts != failAt-1 {
			t.Fatalf("checkpoint %d: sends %d, %v", failAt, platform.Attempts, err)
		}
		store.failAt = 0
		report, err := orchestration.CheckNotifications(context.Background(), ports, options)
		if err != nil || len(report.State.Pending()) != 0 || platform.Attempts != failAt+1 {
			t.Fatalf("checkpoint restart must replay only uncheckpointed work: %d %+v %v", platform.Attempts, report, err)
		}
	}
}
func TestCheckerRetriesSurviveEveryCheckpointAndLaterFailure(t *testing.T) {
	ports, store, platform, copies, options := checkerRig(t)
	seedChecker(t, store, options, pendingChecker("copy-one", true), pendingChecker("platform-one", false), pendingChecker("copy-two", true), pendingChecker("platform-two", false))
	copies.Fail = &notifysvc.Error{Reason: "refused"}
	store.failAt = 4 // initial save, retry copy, platform accepted, then next checkpoint fails
	_, err := orchestration.CheckNotifications(context.Background(), ports, options)
	if err == nil || len(copies.Sent) != 1 || len(platform.Sent) != 1 {
		t.Fatalf("partial pass: %d %d %v", len(copies.Sent), len(platform.Sent), err)
	}
	for i, raw := range store.saved {
		keys := map[string]bool{}
		for _, value := range checkerPending(storedChecker(t, raw)) {
			keys[value.(map[string]any)["key"].(string)] = true
		}
		if !keys["copy-one"] || !keys["copy-two"] {
			t.Fatalf("retry lost from checkpoint %d: %v", i, keys)
		}
	}
	store.failAt = 0
	report, err := orchestration.CheckNotifications(context.Background(), ports, options)
	if err != nil || len(report.State.Pending()) != 2 || len(copies.Sent) != 2 {
		t.Fatalf("restart retries: %+v %v", report, err)
	}
	options.Now += incident.History
	report, err = orchestration.CheckNotifications(context.Background(), ports, options)
	if err != nil || hasCheckerCopy(report.State) {
		t.Fatalf("retry expiry: %+v %v", report, err)
	}
	for _, delivery := range report.Offered {
		if delivery.Retry {
			t.Fatal("expired copy retried")
		}
	}
}
func TestCheckerCopyServiceFailureAndImmediateRecovery(t *testing.T) {
	ports, store, platform, copies, options := checkerRig(t)
	seedChecker(t, store, options, pendingChecker("copy", true))
	copies.Fail = &notifysvc.Error{Reason: "unreachable"}
	for i := 0; i < 7; i++ {
		options.Now = checkerNow + float64(i*60)
		if _, err := orchestration.CheckNotifications(context.Background(), ports, options); err != nil {
			t.Fatal(err)
		}
	}
	if len(copies.Sent) != 7 || len(platform.Sent) != 1 {
		t.Fatalf("failure notices: copies %d platform %d", len(copies.Sent), len(platform.Sent))
	}
	if !strings.Contains(string(platform.Sent[0].Document), "notify service unreachable") {
		t.Fatal("service incident missing")
	}
	copies.Fail = nil
	options.Now += 60
	report, err := orchestration.CheckNotifications(context.Background(), ports, options)
	if err != nil || len(report.State.Pending()) != 1 || report.State.Pending()[0].Title() != "notify service recovered" {
		t.Fatalf("recovery: %+v %v", report, err)
	}
	options.Now += 60
	if _, err := orchestration.CheckNotifications(context.Background(), ports, options); err != nil {
		t.Fatal(err)
	}
	if len(platform.Sent) != 2 {
		t.Fatal("service recovery not delivered next pass")
	}
}
func TestCheckerDryRunAndUnknownSnapshot(t *testing.T) {
	ports, store, platform, copies, options := checkerRig(t)
	seedChecker(t, store, options, pendingChecker("one", false))
	before := store.ConfigMapData[incident.Namespace][0].Data[incident.StateKey]
	options.DryRun = true
	report, err := orchestration.CheckNotifications(context.Background(), ports, options)
	if err != nil || len(report.State.Pending()) != 1 || len(store.saved) != 0 || platform.Attempts != 0 || len(copies.Sent) != 0 {
		t.Fatal("dry-run effects")
	}
	if store.ConfigMapData[incident.Namespace][0].Data[incident.StateKey] != before {
		t.Fatal("dry-run changed state")
	}
	options.DryRun = false
	report, err = orchestration.CheckNotifications(context.Background(), ports, options)
	if err != nil {
		t.Fatal(err)
	}
	doc := storedChecker(t, store.saved[len(store.saved)-1])
	doc["instances"].(map[string]any)["example-staging/example"].(map[string]any)["incident"] = map[string]any{"since": checkerNow - 300, "notified": true, "last_notice": checkerNow, "healthy_since": checkerNow - 100}
	raw, _ := json.Marshal(doc)
	store.ConfigMapData[incident.Namespace][0].Data[incident.StateKey] = string(raw)
	store.Answers.ListReason = observe.ReasonForbidden
	for _, offset := range []float64{1, 301} {
		options.Now = checkerNow + offset
		report, err = orchestration.CheckNotifications(context.Background(), ports, options)
		if err == nil {
			t.Fatal("unknown snapshot succeeded")
		}
	}
	doc = storedChecker(t, store.saved[len(store.saved)-1])
	record := doc["instances"].(map[string]any)["example-staging/example"].(map[string]any)["incident"].(map[string]any)
	if _, has := record["healthy_since"]; has {
		t.Fatal("unknown inferred recovery")
	}
	if record["since"] != checkerNow-300 {
		t.Fatal("incident clock reset")
	}
	if !strings.Contains(string(platform.Sent[len(platform.Sent)-1].Document), "notification monitor cannot read cluster health") {
		t.Fatal("monitor incident absent")
	}
	if doc["last_success"] != checkerNow {
		t.Fatal("unknown pass advanced last success")
	}
}
func TestCheckerStateReadFailureStopsBeforeSnapshot(t *testing.T) {
	ports, store, platform, _, options := checkerRig(t)
	store.Failing = map[string]error{"get notification-state": errors.New("forbidden")}
	if _, err := orchestration.CheckNotifications(context.Background(), ports, options); err == nil {
		t.Fatal("state read failure succeeded")
	}
	if !reflect.DeepEqual(store.Calls, []string{"get notification-state"}) || len(store.Reads) != 0 || platform.Attempts != 0 {
		t.Fatal("effects after failed state read")
	}
}

func hasCheckerCopy(state *incident.State) bool {
	for _, entry := range state.Pending() {
		if len(entry.About()) > 0 {
			return true
		}
	}
	return false
}

type checkedCheckerPublisher struct {
	ntfy.Publisher
	before func()
}

func (p checkedCheckerPublisher) Publish(ctx context.Context, address string, publication notify.Publication) error {
	p.before()
	return p.Publisher.Publish(ctx, address, publication)
}

func TestCheckerRefusesInvalidAndOversizedStateBeforeEffects(t *testing.T) {
	for _, raw := range []string{`{"version":999}`, strings.Repeat(" ", incident.MaxStateBytes) + `{}`} {
		ports, store, platform, copies, options := checkerRig(t)
		store.ConfigMapData[incident.Namespace][0].Data[incident.StateKey] = raw
		if _, err := orchestration.CheckNotifications(context.Background(), ports, options); err == nil {
			t.Fatal("invalid/oversized state accepted")
		}
		if len(store.saved) > 0 || platform.Attempts > 0 || len(copies.Sent) > 0 || len(store.Reads) > 0 {
			t.Fatal("effects after invalid state read")
		}
	}
}

func TestCheckerMalformedHistoryStopsBeforeAllEffects(t *testing.T) {
	for _, scenario := range []string{"missing queued", "missing key", "missing title", "missing message", "missing priority", "wrong notified", "wrong instance retained"} {
		t.Run(scenario, func(t *testing.T) {
			ports, store, platform, copies, options := checkerRig(t)
			entry := pendingChecker("pending@notify", true)
			seedChecker(t, store, options, entry)
			doc := storedChecker(t, store.ConfigMapData[incident.Namespace][0].Data[incident.StateKey])
			switch scenario {
			case "wrong notified":
				doc["monitor"] = map[string]any{"since": checkerNow - 600, "notified": "true"}
			case "wrong instance retained":
				doc["instances"] = map[string]any{"example-staging/example": map[string]any{"incident": map[string]any{}, "retained": "true"}}
			default:
				delete(doc["pending"].([]any)[0].(map[string]any), strings.TrimPrefix(scenario, "missing "))
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			before := string(raw)
			store.ConfigMapData[incident.Namespace][0].Data[incident.StateKey] = before
			report, err := orchestration.CheckNotifications(context.Background(), ports, options)
			if err == nil || report.State != nil {
				t.Fatal("malformed history was evaluated instead of refused")
			}
			if !reflect.DeepEqual(store.Calls, []string{"get notification-state"}) || len(store.Reads) > 0 || len(store.Selectors) > 0 {
				t.Fatal("snapshot read after corrupt state")
			}
			if len(store.saved) > 0 || platform.Attempts > 0 || len(copies.Sent) > 0 {
				t.Fatal("save/send after corrupt state")
			}
			if store.ConfigMapData[incident.Namespace][0].Data[incident.StateKey] != before {
				t.Fatal("corrupt state silently replaced")
			}
		})
	}
}
