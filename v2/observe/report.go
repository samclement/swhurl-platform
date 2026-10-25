package observe

import (
	"bytes"
	"encoding/json"
)

// EncodeReport is one observation as a versioned JSON document: the target it is about, the evidence's
// source and time, every finding in order, what was assessed, and what this slice never looks at.
// Identical reports encode to identical bytes, and no list is ever null.
func EncodeReport(report Report) ([]byte, error) {
	document := reportDocument{
		Kind:       "v2-observation",
		Version:    1,
		Source:     report.Source,
		ObservedAt: report.ObservedAt,
		Verdict:    report.Verdict,
		Findings:   make([]findingDocument, 0, len(report.Findings)),
		Assessed:   nonNil(report.Assessed),
		Unassessed: nonNil(report.Unassessed),
		NotCovered: nonNil(report.NotCovered),
		Target:     targetDocumentOf(report.Target),
	}
	for _, finding := range report.Findings {
		document.Findings = append(document.Findings, findingDocument{finding.Field, finding.Outcome, finding.Reason})
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// reportDocument is the report's shape on stdout: field order is the document order, and a change to it
// raises Version.
type reportDocument struct {
	Kind       string            `json:"kind"`
	Version    int               `json:"version"`
	Source     string            `json:"source"`
	ObservedAt string            `json:"observedAt"`
	Verdict    Verdict           `json:"verdict"`
	Findings   []findingDocument `json:"findings"`
	Assessed   []string          `json:"assessed"`
	Unassessed []string          `json:"unassessed"`
	NotCovered []string          `json:"notCovered"`
	Target     targetDocument    `json:"target"`
}

type findingDocument struct {
	Field   string `json:"field"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

type targetDocument struct {
	Namespace string      `json:"namespace"`
	Unit      refDocument `json:"unit"`
	Release   refDocument `json:"release"`
	Workload  refDocument `json:"workload"`
	Pods      refDocument `json:"pods"`
	Selector  string      `json:"selector"`
	Container string      `json:"container"`
	Digest    string      `json:"digest"`
	Replicas  int32       `json:"replicas"`
	Revision  string      `json:"revision,omitempty"`
}

type refDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

func targetDocumentOf(target Target) targetDocument {
	return targetDocument{
		Namespace: target.Namespace,
		Unit:      refDocumentOf(target.Unit),
		Release:   refDocumentOf(target.Release),
		Workload:  refDocumentOf(target.Workload),
		Pods:      refDocumentOf(target.Pods),
		Selector:  target.Selector,
		Container: target.Container,
		Digest:    target.Digest,
		Replicas:  target.Replicas,
		Revision:  target.Revision,
	}
}

func refDocumentOf(ref Ref) refDocument {
	return refDocument{ref.APIVersion, ref.Kind, ref.Namespace, ref.Name}
}

// nonNil keeps a list in the document even when it is empty, so a consumer never has to treat null and
// empty as different.
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
