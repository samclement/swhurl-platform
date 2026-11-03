package observe_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

func TestLiveFactsPreserveMissingUnreadableEmptyAndZero(t *testing.T) {
	base := target()
	evidence := withRead(healthy(t), present(base.Workload, []byte(`{"metadata":{"generation":4},"status":{"observedGeneration":4,"availableReplicas":1,"readyReplicas":0,"conditions":[{"type":"Available","status":"True"}]}}`)))
	report, err := observe.Explain(base, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if report.Live.Workload.ReadyReplicas == nil || *report.Live.Workload.ReadyReplicas != 0 {
		t.Fatalf("%+v", report.Live.Workload)
	}
	if report.Live.Unit.Suspended == nil || *report.Live.Unit.Suspended || len(report.Live.Unit.Conditions) != 1 {
		t.Fatalf("%+v", report.Live.Unit)
	}
	if len(report.Live.Running.Digests) != 1 || report.Live.Running.Digests[0] != digest {
		t.Fatalf("%+v", report.Live.Running)
	}
	for _, status := range []string{observe.Absent, observe.Unavailable, observe.NotRead} {
		evidence := healthy(t)
		evidence.Objects = nil
		if status != observe.NotRead {
			reason := observe.ReasonForbidden
			if status == observe.Absent {
				reason = observe.ReasonNotFound
			}
			evidence.Objects = []observe.Read{{Ref: base.Unit, Status: status, Reason: reason}}
		}
		evidence.Pods = []observe.List{{Ref: base.Pods, Selector: base.Selector, Status: observe.Unavailable, Reason: observe.ReasonForbidden}}
		report, err := observe.Explain(base, evidence)
		if err != nil {
			t.Fatal(err)
		}
		if report.Live.Unit.Status != status || report.Live.Unit.Suspended != nil || report.Live.Unit.Conditions != nil || report.Live.Running.Digests != nil {
			t.Fatalf("%+v", report.Live)
		}
	}
	evidence = withRead(healthy(t), present(base.Unit, []byte(`{"metadata":{"generation":3},"status":{"conditions":[]}}`)))
	evidence = withPods(evidence, podList(t, base))
	report, err = observe.Explain(base, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if report.Live.Unit.Suspended != nil || report.Live.Unit.Conditions == nil || len(report.Live.Unit.Conditions) != 0 {
		t.Fatalf("%+v", report.Live.Unit)
	}
	if report.Live.Running.Digests == nil || len(report.Live.Running.Digests) != 0 || report.Live.Workload.ReadyReplicas != nil {
		t.Fatalf("%+v", report.Live)
	}
	data, err := observe.EncodeReport(report)
	if err != nil || !json.Valid(data) || !bytes.Contains(data, []byte(`"readyReplicas":null`)) || !bytes.Contains(data, []byte(`"suspended":null,"conditions":[]`)) {
		t.Fatalf("%s, %v", data, err)
	}
}
