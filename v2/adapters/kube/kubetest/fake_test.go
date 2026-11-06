package kubetest_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// errRefused stands in for a failure the adapter would report instead of an answer.
var errRefused = errors.New("refused")

// scenario is a small stand-in for the adapter's recorded documents: the contract is about the four
// reports and the refusals, not about what one object happens to contain.
func scenario() kubetest.Answers {
	workload := observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "hello-staging", Name: "hello-main"}
	return kubetest.Answers{
		Object:     workload,
		Document:   []byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"hello-main","namespace":"hello-staging","generation":1}}`),
		Missing:    observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "hello-staging", Name: "missing"},
		Failed:     observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "hello-staging", Name: "failed"},
		Malformed:  observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "hello-staging", Name: "malformed"},
		Mismatched: observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "hello-staging", Name: "mismatched"},
		Pods:       observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: "hello-staging"},
		Selector:   "app.kubernetes.io/instance=hello",
		Items:      [][]byte{[]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"hello-main-1","namespace":"hello-staging"}}`)},
	}
}

// The fake runs the writer contract too, so a caller's fake cannot drift from the real adapter.
func TestWriteContract(t *testing.T) {
	kubetest.WriteContract(t, "2 tables, integrity ok\n", func(t *testing.T, log string) kube.Writer {
		fake := kubetest.New(scenario())
		fake.Log = log
		return fake
	})
}

func TestContract(t *testing.T) {
	kubetest.Contract(t, scenario(), func(t *testing.T, answers kubetest.Answers) kube.Reader {
		return kubetest.New(answers)
	})
}

// TestFakeRecordsTheCallAndCanFail: the fake reports the calls made on it, and a configured failure is
// returned without an answer, as the adapter's refusal is.
func TestFakeRecordsTheCallAndCanFail(t *testing.T) {
	fake := kubetest.New(scenario())
	if _, err := fake.Read(t.Context(), scenario().Object); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.List(t.Context(), scenario().Pods, scenario().Selector); err != nil {
		t.Fatal(err)
	}
	if len(fake.Reads) != 1 || len(fake.Selectors) != 1 {
		t.Fatalf("the calls were not recorded: %d reads, %d selectors", len(fake.Reads), len(fake.Selectors))
	}
	fake.Refuse = errRefused
	if _, err := fake.Read(t.Context(), scenario().Object); err != errRefused {
		t.Fatalf("a configured refusal was not returned: %v", err)
	}
}

func TestCollectionContract(t *testing.T) {
	kubetest.CollectionContract(t, func(t *testing.T, body []byte, reason string) kube.Reader {
		return kubetest.New(kubetest.Answers{Pods: observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: "hello-staging"}, Collection: body, ListReason: reason})
	})
}

func TestReviewContract(t *testing.T) {
	answers := kubetest.ReviewAnswers{Token: "checker-token", Audience: "notify",
		Username: "system:serviceaccount:console:notification-check"}
	kubetest.ReviewContract(t, answers, func(t *testing.T, answers kubetest.ReviewAnswers) kube.Reviewer {
		fake := kubetest.New(kubetest.Answers{})
		fake.Tokens = map[string]kube.TokenReview{answers.Token: {Authenticated: true,
			Audiences: []string{answers.Audience}, Username: answers.Username}}
		return fake
	})
}

func TestDataContract(t *testing.T) {
	answers := kubetest.DataAnswers{
		Namespace: "notify",
		Labels:    "platform.swhurl.com/notify-transport=ntfy",
		ConfigMap: kube.ConfigMap{Name: "notify-outbox", Data: map[string]string{"state.json": `{"version":1,"seen":{},"pending":[]}`}},
		Secret: kube.Secret{Name: "channel-payments", Labels: map[string]string{"platform.swhurl.com/notify-transport": "ntfy"},
			Data: map[string][]byte{"address": []byte("https://ntfy.example/private-topic-0123")}},
		Other: "notify-alerts",
	}
	kubetest.DataContract(t, answers, func(t *testing.T, answers kubetest.DataAnswers) kube.Data {
		fake := kubetest.New(kubetest.Answers{})
		fake.ConfigMapData = map[string][]kube.ConfigMap{answers.Namespace: {answers.ConfigMap}}
		fake.SecretData = map[string][]kube.Secret{answers.Namespace: {answers.Secret,
			{Name: answers.Other, Data: map[string][]byte{"secret": []byte("webhook-secret")}}}}
		return fake
	})
}

// The fake records each review, list and change as its operation, and a failure the scenario names is returned
// after the call is recorded.
func TestDataFakeRecordsAndCanFail(t *testing.T) {
	fake := kubetest.New(kubetest.Answers{})
	fake.ConfigMapData = map[string][]kube.ConfigMap{"notify": {{Name: "notify-outbox"}}}
	fake.Failing = map[string]error{"set notify-outbox": errRefused, "secrets notify-alerts": errRefused}
	if err := fake.SetConfigMapKey(t.Context(), "notify", "notify-outbox", "state.json", "{}", "notify"); err != errRefused {
		t.Fatalf("a failing change: %v", err)
	}
	if _, err := fake.Secrets(t.Context(), "notify", kube.Selector{Name: "notify-alerts"}); err != errRefused {
		t.Fatalf("a failing list: %v", err)
	}
	if _, err := fake.Review(t.Context(), "token", "notify"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"set notify-outbox", "secrets notify-alerts", "review"}; !slices.Equal(fake.Calls, want) {
		t.Fatalf("calls %v, want %v", fake.Calls, want)
	}
}

func TestIssuerContract(t *testing.T) {
	want := kube.SigningKeys{Issuer: "https://kubernetes.default.svc.cluster.local", Keys: []byte(`{"keys":[{"kid":"a"}]}`)}
	kubetest.IssuerContract(t, want, func(t *testing.T, want kube.SigningKeys) kube.Issuer {
		fake := kubetest.New(kubetest.Answers{})
		fake.Signing = want
		return fake
	})
}

// A collection the scenario names is answered by List and ListAllNamespaces too, whatever the selector.
func TestFakeAnswersNamedCollectionsToEveryList(t *testing.T) {
	fake := kubetest.New(kubetest.Answers{})
	item := []byte(`{"metadata":{"name":"hello-staging"}}`)
	namespaces := observe.Ref{APIVersion: "v1", Kind: "Namespace"}
	fake.Collections = map[observe.Ref][][]byte{namespaces: {item}}
	list, err := fake.List(t.Context(), namespaces, "platform.swhurl.com/app")
	if err != nil || list.Status != observe.Present || len(list.Items) != 1 {
		t.Fatalf("%+v, %v", list, err)
	}
	fake.Failing = map[string]error{"signing-keys": errRefused}
	if _, err := fake.SigningKeys(t.Context()); err != errRefused {
		t.Fatalf("a failing read: %v", err)
	}
}
