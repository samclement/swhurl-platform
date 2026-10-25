package kubetest_test

import (
	"errors"
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
		Items:      [][]byte{[]byte(`{"kind":"Pod","metadata":{"name":"hello-main-1"}}`)},
	}
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
