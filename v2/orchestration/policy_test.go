package orchestration

import (
	"encoding/json"
	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"slices"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/helm/helmtest"
)

func TestPolicyValidatesAllInputsBeforeRendering(t *testing.T) {
	for _, inputs := range [][]Inputs{nil, {input(t, nil, nil), {Instance: []byte(`{}`)}}} {
		renderer := &helmtest.Fake{}
		report, err := CheckPolicy(inputs, "local-chart", renderer)
		if report != nil || err == nil || len(renderer.Calls) != 0 {
			t.Fatal("partially rendered or accepted invalid inputs")
		}
	}
}
func TestSuccessfulRenderWithoutAppIsAPolicyFailure(t *testing.T) {
	renderer := &helmtest.Fake{Output: []json.RawMessage{json.RawMessage(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"harmless"}}`)}}
	report, err := CheckPolicy([]Inputs{input(t, nil, nil)}, "local-chart", renderer)
	if err != nil || len(report) != 1 {
		t.Fatalf("%v %v", report, err)
	}
	result := report[0]
	rules := []string{}
	for _, finding := range result.Findings {
		rules = append(rules, finding.Rule)
	}
	if result.RenderFailure != "" || !slices.Contains(rules, "workload") {
		t.Fatal("non-workload render passed or became a render error")
	}
}

func TestAdapterRefusalKeepsItsFixedReason(t *testing.T) {
	renderer := &helmtest.Fake{Err: &helm.Error{Reason: "chart_mismatch"}}
	results, err := CheckPolicy([]Inputs{input(t, nil, nil)}, "wrong-chart", renderer)
	if err != nil || len(results) != 1 || results[0].RenderFailure != "refused_render: chart_mismatch" {
		t.Fatal("adapter's safe reason was lost")
	}
}
