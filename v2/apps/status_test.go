package apps

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

const revision = "0123456789abcdef0123456789abcdef01234567"

// input is a compiled instance's documents: the fixture records, with the optional documents when given.
func input(t *testing.T, calls, egress []byte) provisioningrules.Inputs {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile("../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return provisioningrules.Inputs{Instance: read("prod.json"), Release: read("r41.json"), App: read("app.json"),
		Profile: read("profile.json"), Calls: calls, Egress: egress}
}

// statusInputs is the prod fixture's status request over a fake cluster, at a fixed time.
func statusInputs(t *testing.T, reader *kubetest.Fake) StatusInputs {
	t.Helper()
	return StatusInputs{Inputs: input(t, nil, nil), Revision: revision, Reader: reader, Source: "fake",
		Now: func() time.Time { return time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC) }, MaxAge: time.Hour}
}

// The use case reads what the compiled records name — the unit, the release, the workload and the instance's own
// pods — and turns every answer into the report, its state included: an unreadable workload is unknown, never healthy,
// and a refused read is evidence, not an error.
func TestStatusReadsWhatTheRecordsName(t *testing.T) {
	unit := observe.Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization", Namespace: "flux-system", Name: "app-demo-orders-prod"}
	release := observe.Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease", Namespace: "demo-orders-prod", Name: "demo-orders"}
	workload := observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "demo-orders-prod", Name: "demo-orders"}
	pods := observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: "demo-orders-prod"}
	fake := kubetest.New(kubetest.Answers{
		Object: unit, Missing: release, Failed: workload, Pods: pods,
		Document: []byte(`{"metadata":{"generation":1},"status":{"lastAppliedRevision":"main@sha1:` + revision +
			`","conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}`),
		Selector: "app.kubernetes.io/instance=demo-orders,app.kubernetes.io/controller=main",
	})
	report, err := Status(context.Background(), statusInputs(t, fake))
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Reads) != 3 || fake.Reads[0] != unit || fake.Reads[1] != release || fake.Reads[2] != workload {
		t.Fatalf("reads %+v", fake.Reads)
	}
	if len(fake.Selectors) != 1 || fake.Selectors[0] != fake.Answers.Selector {
		t.Fatalf("selectors %v", fake.Selectors)
	}
	if report.Source != "fake" || report.ObservedAt != "2026-11-03T10:00:00Z" || report.Target.Revision != revision {
		t.Fatalf("source %q at %q, revision %q", report.Source, report.ObservedAt, report.Target.Revision)
	}
	if report.State != (observe.State{Name: observe.StateUnknown, Field: "workload", Reason: observe.ReasonForbidden}) {
		t.Fatalf("state %+v, findings %+v", report.State, report.Findings)
	}
}

// A request the adapter refuses and a record the compiler refuses are errors, and no report is made.
func TestStatusStopsWhereItCannotRead(t *testing.T) {
	fake := kubetest.New(kubetest.Answers{})
	fake.Refuse = errors.New("refused: resource: unsupported_kind")
	if _, err := Status(context.Background(), statusInputs(t, fake)); !errors.Is(err, fake.Refuse) {
		t.Fatalf("a refused read: %v", err)
	}
	inputs := statusInputs(t, kubetest.New(kubetest.Answers{}))
	inputs.Instance = []byte(`{"schemaVersion":1}`)
	if _, err := Status(context.Background(), inputs); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("a refused record: %v", err)
	}
}
