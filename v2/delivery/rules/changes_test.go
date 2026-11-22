package rules

import (
	"encoding/json"
	"strings"
	"testing"
)

// commit is the revision Git wants; other is the one a unit applied before it.
const (
	commit = "9f1c0e2d34b7a5c86e4f0d2b9a17c3e5d8b6f04a"
	other  = "0123456789abcdef0123456789abcdef01234567"
)

func comparisons(paths ...string) *Changes {
	return &Changes{Kind: "v2-changes", Version: 1, Comparisons: []Comparison{
		{From: other, To: commit, Status: "complete", Paths: append([]string{}, paths...)},
	}}
}

func TestCurrentnessRequiresCompleteMatchingProof(t *testing.T) {
	scope := ChangeScope{Files: []string{"v2-records/hello/app.json", "clusters/home/app-hello-staging.json"},
		Folders: []string{"v2-records/hello/staging", "v2/instances/hello/staging"}}
	for _, test := range []struct {
		name, literal, availability string
		evidence                    *Changes
		state, reason               string
	}{
		{"equal needs no comparison", "main@sha1:" + commit, Present, nil, "applied", "revision_matches"},
		{"unrelated", "main@sha1:" + other, Present, comparisons("v2-records/other/staging/instance.json"), "unaffected", "complete_comparison_unaffected"},
		{"own input", "main@sha1:" + other, Present, comparisons("v2-records/hello/staging/instance.json"), "pending", "relevant_path_changed"},
		{"app", "main@sha1:" + other, Present, comparisons("v2-records/hello/app.json"), "pending", "relevant_path_changed"},
		{"removed output", "main@sha1:" + other, Present, comparisons("v2/instances/hello/staging/consumer-removed.json"), "pending", "relevant_path_changed"},
		{"own unit json not v1 yaml", "main@sha1:" + other, Present, comparisons("clusters/home/app-hello-staging.json"), "pending", "relevant_path_changed"},
		{"no proof conservative", "main@sha1:" + other, Present, nil, "unknown", "comparison_not_supplied"},
		{"unit moved after caller read", "main@sha1:" + strings.Repeat("a", 40), Present, comparisons(), "unknown", "comparison_endpoints_mismatch"},
		{"unrecognised", "not-a-revision", Present, comparisons(), "unknown", ReasonRevisionShape},
		{"missing applied", "", Present, comparisons(), "pending", ReasonNotAppliedRev},
		{"unit unavailable", "", Unavailable, comparisons(), "unknown", ReasonUnitDown},
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
		got := currentness(t, commit, "main@sha1:"+other, Present, scope, proof)
		if got.State != "unknown" || got.Reason != "comparison_"+status {
			t.Fatalf("%+v", got)
		}
	}
	got := currentness(t, commit, "main@sha1:"+other, Present, ChangeScope{}, comparisons())
	if got.State != "unknown" || got.Reason != "relevance_unknown" {
		t.Fatalf("%+v", got)
	}
}

func TestDecodeChangesIsStrictButUnavailableIsValid(t *testing.T) {
	data, _ := json.Marshal(comparisons())
	if _, err := DecodeChanges(data); err != nil {
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
		if _, err := DecodeChanges([]byte(broken)); err == nil {
			t.Errorf("accepted %s", broken)
		}
	}
	for _, status := range []string{"unavailable", "incomplete"} {
		proof := comparisons()
		proof.Comparisons[0].Status = status
		data, _ := json.Marshal(proof)
		if _, err := DecodeChanges(data); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
	proof := comparisons()
	proof.Comparisons = append(proof.Comparisons, proof.Comparisons[0])
	data, _ = json.Marshal(proof)
	if _, err := DecodeChanges(data); err == nil {
		t.Fatal("accepted duplicate endpoints")
	}
}

func currentness(t *testing.T, expected, literal, availability string, scope ChangeScope, changes *Changes) Currentness {
	t.Helper()
	result, err := CurrentnessOf(expected, literal, availability, scope, changes)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCurrentnessRefusesInvalidProofInsteadOfTreatingItAsUnavailable(t *testing.T) {
	malformed := comparisons()
	malformed.Comparisons[0].Status = "not-a-status"
	if _, err := CurrentnessOf(commit, "main@sha1:"+other, Present, ChangeScope{}, malformed); err == nil {
		t.Fatal("malformed proof accepted")
	}
}

func TestComparisonWithTheWrongExpectedEndpointStaysConservative(t *testing.T) {
	proof := comparisons()
	proof.Comparisons[0].To = strings.Repeat("a", 40)
	got := currentness(t, commit, "main@sha1:"+other, Present, ChangeScope{Files: []string{"app.json"}}, proof)
	if got.State != "unknown" || got.Reason != "comparison_endpoints_mismatch" {
		t.Fatalf("%+v", got)
	}
}
