package observe_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

func comparisons(paths ...string) *observe.Changes {
	return &observe.Changes{Kind: "v2-changes", Version: 1, Comparisons: []observe.Comparison{
		{From: other, To: commit, Status: "complete", Paths: append([]string{}, paths...)},
	}}
}

func TestCurrentnessRequiresCompleteMatchingProof(t *testing.T) {
	scope := observe.ChangeScope{Files: []string{"v2-records/hello/app.json", "clusters/home/app-hello-staging.json"},
		Folders: []string{"v2-records/hello/staging", "v2/instances/hello/staging"}}
	for _, test := range []struct {
		name, literal, availability string
		evidence                    *observe.Changes
		state, reason               string
	}{
		{"equal needs no comparison", "main@sha1:" + commit, observe.Present, nil, "applied", "revision_matches"},
		{"unrelated", "main@sha1:" + other, observe.Present, comparisons("v2-records/other/staging/instance.json"), "unaffected", "complete_comparison_unaffected"},
		{"own input", "main@sha1:" + other, observe.Present, comparisons("v2-records/hello/staging/instance.json"), "pending", "relevant_path_changed"},
		{"app", "main@sha1:" + other, observe.Present, comparisons("v2-records/hello/app.json"), "pending", "relevant_path_changed"},
		{"removed output", "main@sha1:" + other, observe.Present, comparisons("v2/instances/hello/staging/consumer-removed.json"), "pending", "relevant_path_changed"},
		{"own unit json not v1 yaml", "main@sha1:" + other, observe.Present, comparisons("clusters/home/app-hello-staging.json"), "pending", "relevant_path_changed"},
		{"no proof conservative", "main@sha1:" + other, observe.Present, nil, "unknown", "comparison_not_supplied"},
		{"unit moved after caller read", "main@sha1:" + strings.Repeat("a", 40), observe.Present, comparisons(), "unknown", "comparison_endpoints_mismatch"},
		{"unrecognised", "not-a-revision", observe.Present, comparisons(), "unknown", observe.ReasonRevisionShape},
		{"missing applied", "", observe.Present, comparisons(), "pending", observe.ReasonNotAppliedRev},
		{"unit unavailable", "", observe.Unavailable, comparisons(), "unknown", observe.ReasonUnitDown},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := currentness(t, commit, test.literal, test.availability, scope, test.evidence)
			if got.State != test.state || got.Reason != test.reason || got.AppliedRevision != test.literal {
				t.Fatalf("%+v", got)
			}
		})
	}
	for _, status := range []string{"incomplete", "unavailable"} {
		proof := comparisons()
		proof.Comparisons[0].Status = status
		got := currentness(t, commit, "main@sha1:"+other, observe.Present, scope, proof)
		if got.State != "unknown" || got.Reason != "comparison_"+status {
			t.Fatalf("%+v", got)
		}
	}
	got := currentness(t, commit, "main@sha1:"+other, observe.Present, observe.ChangeScope{}, comparisons())
	if got.State != "unknown" || got.Reason != "relevance_unknown" {
		t.Fatalf("%+v", got)
	}
}

func TestDecodeChangesIsStrictButUnavailableIsValid(t *testing.T) {
	data, _ := json.Marshal(comparisons())
	if _, err := observe.DecodeChanges(data); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []string{
		`null`, `[]`, `{}`, string(data) + ` {}`, strings.Replace(string(data), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(data), `"kind"`, `"Kind"`, 1), strings.Replace(string(data), `"version":1`, `"version":1,"extra":true`, 1),
		strings.Replace(string(data), `"paths":[]`, `"paths":null`, 1), strings.Replace(string(data), `"paths":[]`, `"paths":[null]`, 1),
		strings.Replace(string(data), `"paths":[]`, `"paths":["../escape"]`, 1), strings.Replace(string(data), `"paths":[]`, `"paths":["/absolute"]`, 1),
		strings.Replace(string(data), other, other[:7], 1), strings.Replace(string(data), `"status":"complete"`, `"status":"guess"`, 1),
		strings.Replace(string(data), `"from":`, `"from":"`+other+`","from":`, 1),
	} {
		if _, err := observe.DecodeChanges([]byte(broken)); err == nil {
			t.Errorf("accepted %s", broken)
		}
	}
	for _, status := range []string{"unavailable", "incomplete"} {
		proof := comparisons()
		proof.Comparisons[0].Status = status
		data, _ := json.Marshal(proof)
		if _, err := observe.DecodeChanges(data); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
	proof := comparisons()
	proof.Comparisons = append(proof.Comparisons, proof.Comparisons[0])
	data, _ = json.Marshal(proof)
	if _, err := observe.DecodeChanges(data); err == nil {
		t.Fatal("accepted duplicate endpoints")
	}
}

func unaffectedReport(t *testing.T, evidence observe.Evidence) observe.Report {
	t.Helper()
	base := target()
	base.Scope = &observe.ChangeScope{Folders: []string{"v2-records/hello/staging"}}
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

func currentness(t *testing.T, expected, literal, availability string, scope observe.ChangeScope, changes *observe.Changes) observe.Currentness {
	t.Helper()
	result, err := observe.CurrentnessOf(expected, literal, availability, scope, changes)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCurrentnessRefusesInvalidProofInsteadOfTreatingItAsUnavailable(t *testing.T) {
	malformed := comparisons()
	malformed.Comparisons[0].Status = "not-a-status"
	if _, err := observe.CurrentnessOf(commit, "main@sha1:"+other, observe.Present, observe.ChangeScope{}, malformed); err == nil {
		t.Fatal("malformed proof accepted")
	}
	evidence := healthy(t)
	evidence.Changes = malformed
	if _, err := observe.Explain(target(), evidence); err == nil {
		t.Fatal("malformed proof was not refused")
	}
}

func TestComparisonWrongExpectedEndpointAndUnavailableProofStayConservative(t *testing.T) {
	proof := comparisons()
	proof.Comparisons[0].To = strings.Repeat("a", 40)
	got := currentness(t, commit, "main@sha1:"+other, observe.Present, observe.ChangeScope{Files: []string{"app.json"}}, proof)
	if got.State != "unknown" || got.Reason != "comparison_endpoints_mismatch" {
		t.Fatalf("%+v", got)
	}
	report := unaffectedReport(t, healthy(t))
	report.Live.Running.Digests = []string{"sha256:" + strings.Repeat("a", 64)}
	if decision := observe.PromotionReadiness(report, digest); decision.Ready || decision.Reason != "reviewed_digest_not_running" {
		t.Fatalf("%+v", decision)
	}
}
