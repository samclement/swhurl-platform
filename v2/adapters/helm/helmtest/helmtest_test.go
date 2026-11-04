package helmtest_test

import (
	"encoding/json"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/adapters/helm/helmtest"
)

func TestContract(t *testing.T) {
	helmtest.Contract(t, func(*testing.T) helm.Renderer {
		return &helmtest.Fake{Output: []json.RawMessage{json.RawMessage(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"boundary"}}`)}}
	}, helm.Request{Chart: "chart", ChartName: "boundary", ChartVersion: "0.1.0", Release: "boundary", Namespace: "staging", Values: json.RawMessage(`{}`)})
}

func TestFailureAndOwnedCalls(t *testing.T) {
	fake := &helmtest.Fake{Err: &helm.Error{Reason: "helm_failed"}}
	request := helm.Request{Namespace: "staging", Values: json.RawMessage(`{}`)}
	result, err := fake.Render(request)
	if result != nil || err == nil {
		t.Fatal("failure returned objects")
	}
	request.Values[0] = '!'
	if string(fake.Calls[0].Values) != `{}` {
		t.Fatal("recorded call aliases input")
	}
}
