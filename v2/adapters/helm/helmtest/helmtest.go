// Package helmtest provides the small rendering fake and shared adapter contract.
package helmtest

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
)

// Fake returns configured complete objects, records owned requests, and can fail atomically.
type Fake struct {
	Output []json.RawMessage
	Err    error
	Calls  []helm.Request
}

var _ helm.Renderer = (*Fake)(nil)

func (f *Fake) Render(request helm.Request) ([]json.RawMessage, error) {
	request.Values = bytes.Clone(request.Values)
	f.Calls = append(f.Calls, request)
	if request.Namespace == "" {
		return nil, &helm.Error{Reason: "invalid_namespace"}
	}
	if f.Err != nil {
		return nil, f.Err
	}
	if len(f.Output) == 0 {
		return nil, &helm.Error{Reason: "empty_output"}
	}
	result := make([]json.RawMessage, len(f.Output))
	for i, object := range f.Output {
		result[i] = bytes.Clone(object)
	}
	return result, nil
}

// Contract checks complete JSON, ownership and atomic refusal with a real renderer and the fake.
func Contract(t *testing.T, open func(*testing.T) helm.Renderer, request helm.Request) {
	t.Helper()
	renderer := open(t)
	first, err := renderer.Render(request)
	if err != nil || len(first) == 0 {
		t.Fatalf("render: %v, %d objects", err, len(first))
	}
	completeJSON(t, first)
	original := bytes.Clone(first[0])
	first[0][0] = '!'
	second, err := renderer.Render(request)
	if err != nil || !bytes.Equal(second[0], original) {
		t.Fatal("results alias or are not repeatable")
	}
	request.Namespace = ""
	result, err := renderer.Render(request)
	refusal, ok := err.(*helm.Error)
	if !ok || refusal.Reason != "invalid_namespace" || result != nil {
		t.Fatal("invalid namespace did not fail atomically")
	}
}

func completeJSON(t *testing.T, objects []json.RawMessage) {
	t.Helper()
	for _, object := range objects {
		var decoded map[string]json.RawMessage
		if json.Unmarshal(object, &decoded) != nil || decoded["kind"] == nil || decoded["metadata"] == nil {
			t.Fatal("not a complete JSON object")
		}
	}
}
