package observe_test

import (
	"bytes"
	"encoding/json"
	"testing"

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
	if document["kind"] != "v2-observation" || document["version"] != float64(2) {
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
	for _, field := range []string{"findings", "assessed", "unassessed", "notCovered"} {
		if _, ok := document[field].([]any); !ok {
			t.Errorf("%s is %v, not a list", field, document[field])
		}
	}
}
