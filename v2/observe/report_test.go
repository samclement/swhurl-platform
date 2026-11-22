package observe_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	deliveryrules "github.com/samclement/swhurl-platform/v2/delivery/rules"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// The report is one versioned JSON document with a fixed order, and identical reports encode
// identically: a consumer can diff two observations.
func TestEncodeReportIsStableAndComplete(t *testing.T) {
	report := explain(t, healthy(t))
	first, err := observe.EncodeReport(report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := observe.EncodeReport(explain(t, healthy(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the same observation encoded differently")
	}
	if bytes.Contains(first, []byte("\\u00")) {
		t.Fatal("the document escapes HTML or non-ASCII it need not")
	}
	var document map[string]any
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	if document["kind"] != "v2-observation" || document["version"] != float64(5) {
		t.Fatalf("envelope: %v %v", document["kind"], document["version"])
	}
	if document["verdict"] != string(observe.IsRunning) || document["source"] != report.Source {
		t.Fatalf("verdict %v, source %v", document["verdict"], document["source"])
	}
	findings, _ := document["findings"].([]any)
	if len(findings) != len(report.Findings) {
		t.Fatalf("%d findings", len(findings))
	}
	for i, raw := range findings {
		entry := raw.(map[string]any)
		if entry["field"] != report.Findings[i].Field || entry["outcome"] != report.Findings[i].Outcome {
			t.Fatalf("finding %d is %v, want %+v", i, entry, report.Findings[i])
		}
	}
	target, _ := document["target"].(map[string]any)
	if target["digest"] != report.Target.Digest || target["selector"] != report.Target.Selector {
		t.Fatalf("target: %v", target)
	}
	if unit, _ := target["unit"].(map[string]any); unit["name"] != report.Target.Unit.Name || unit["apiVersion"] != report.Target.Unit.APIVersion {
		t.Fatalf("target unit: %v", target["unit"])
	}
}

// Every list is present even when it is empty, so a consumer never treats null and empty as different.
func TestEncodeReportKeepsEmptyLists(t *testing.T) {
	data, err := observe.EncodeReport(observe.Report{})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"findings", "problems", "routes", "certificates", "assessed", "unassessed", "notCovered"} {
		if _, ok := document[field].([]any); !ok {
			t.Errorf("%s is %v, not a list", field, document[field])
		}
	}
}

func TestReportEncodesGoPromotionDecision(t *testing.T) {
	report := explain(t, healthy(t))
	for _, reviewed := range []string{report.Target.Digest, "sha256:" + strings.Repeat("a", 64), ""} {
		data, err := observe.EncodeReviewedReport(report, reviewed)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Version   int
			Promotion observe.PromotionDecision
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		if document.Version != 5 || document.Promotion != observe.PromotionReadiness(report, reviewed) {
			t.Fatalf("%+v", document)
		}
	}
}

func comparisons(paths ...string) *deliveryrules.Changes {
	return &deliveryrules.Changes{Kind: "v2-changes", Version: 1, Comparisons: []deliveryrules.Comparison{
		{From: other, To: commit, Status: "complete", Paths: append([]string{}, paths...)},
	}}
}

func unaffectedReport(t *testing.T, evidence observe.Evidence) observe.Report {
	t.Helper()
	base := target()
	base.Scope = &deliveryrules.ChangeScope{Folders: []string{"v2-records/hello/staging"}}
	evidence = withRead(evidence, present(base.Unit, fluxDocument(t, flux{generation: 3, applied: "main@sha1:" + other})))
	evidence.Changes = comparisons("v2-records/another/staging/instance.json")
	report, err := observe.Explain(base, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestUnaffectedNeverWaivesUnhealthyEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		evidence observe.Evidence
		want     string
	}{
		{"healthy", healthy(t), observe.StateHealthy},
		{"failed", withRead(healthy(t), present(target().Release, []byte(`{"metadata":{"generation":2},"status":{"conditions":[{"type":"Ready","status":"False","reason":"InstallFailed","observedGeneration":2}]}}`))), observe.StateFailing},
		{"suspended", withRead(healthy(t), present(target().Release, fluxDocument(t, flux{generation: 2, suspended: true}))), observe.StateSuspended},
		{"different image", withPods(healthy(t), podList(t, target(), podDocument(t, pod{started: true, image: "docker.io/test/app@sha256:" + strings.Repeat("a", 64)}))), observe.StateUpdating},
		{"unreadable workload", withRead(healthy(t), observe.Read{Ref: target().Workload, Status: observe.Unavailable, Reason: observe.ReasonForbidden}), observe.StateUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := unaffectedReport(t, test.evidence)
			if report.State.Name != test.want || report.Currentness.State != "unaffected" || report.Findings[1].Outcome != observe.Unaffected {
				t.Fatalf("%+v %+v", report.State, report.Currentness)
			}
			if report.Currentness.AppliedRevision != "main@sha1:"+other {
				t.Fatalf("lost literal: %+v", report.Currentness)
			}
		})
	}
}

func TestPromotionReadinessUsesReviewedSelectedAndRunningDigest(t *testing.T) {
	report := unaffectedReport(t, healthy(t))
	if got := observe.PromotionReadiness(report, digest); !got.Ready {
		t.Fatalf("%+v", got)
	}
	if got := observe.PromotionReadiness(report, "sha256:"+strings.Repeat("a", 64)); got.Ready || got.Reason != "reviewed_digest_changed" {
		t.Fatalf("%+v", got)
	}
	for _, state := range []string{observe.StateUnknown, observe.StateFailing, observe.StateSuspended, observe.StateUpdating, observe.StateStopped} {
		unready := report
		unready.State.Name = state
		if got := observe.PromotionReadiness(unready, digest); got.Ready {
			t.Fatalf("accepted %s", state)
		}
	}
	report.Currentness.State = "unknown"
	if got := observe.PromotionReadiness(report, digest); got.Ready {
		t.Fatal("unknown currency accepted")
	}
	report.Currentness.State = "applied"
	report.Live.Running.Digests = []string{}
	if got := observe.PromotionReadiness(report, digest); got.Ready {
		t.Fatal("missing running proof accepted")
	}
}

func TestExplainRefusesInvalidProofInsteadOfTreatingItAsUnavailable(t *testing.T) {
	malformed := comparisons()
	malformed.Comparisons[0].Status = "not-a-status"
	evidence := healthy(t)
	evidence.Changes = malformed
	if _, err := observe.Explain(target(), evidence); err == nil {
		t.Fatal("malformed proof was not refused")
	}
}

func TestPromotionReadinessNeedsTheReviewedDigestRunning(t *testing.T) {
	report := unaffectedReport(t, healthy(t))
	report.Live.Running.Digests = []string{"sha256:" + strings.Repeat("a", 64)}
	if decision := observe.PromotionReadiness(report, digest); decision.Ready || decision.Reason != "reviewed_digest_not_running" {
		t.Fatalf("%+v", decision)
	}
}
