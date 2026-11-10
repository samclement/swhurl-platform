package policy_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/policy"
)

// The offline mutation fixture is full output of the real pinned chart, not a handwritten Helm substitute.
func TestFullFeatureFixtureMatchesRealPinnedChart(t *testing.T) {
	raw, err := os.ReadFile("testdata/chart-prod.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Source, Rendered []json.RawMessage
		Platform         policy.Platform
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(cache, "swhurl-platform/charts/app-template-5.3.0")
	_, binary := exec.LookPath("helm")
	_, metadata := os.Stat(filepath.Join(chart, "Chart.yaml"))
	if binary != nil || metadata != nil {
		if os.Getenv("REQUIRE_HELM") != "" {
			t.Fatal("REQUIRE_HELM needs Helm and the cached pinned chart")
		}
		t.Skip("requires Helm and the pinned chart cache from make check-apps")
	}
	var release struct {
		Kind     string
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			Chart struct {
				Spec struct{ Chart, Version string }
			}
			Values json.RawMessage
		}
	}
	for _, source := range fixture.Source {
		if json.Unmarshal(source, &release) != nil {
			t.Fatal("invalid fixture source")
		}
		if release.Kind == "HelmRelease" {
			break
		}
	}
	actual, err := helm.New().Render(helm.Request{Chart: chart, ChartName: release.Spec.Chart.Spec.Chart, ChartVersion: release.Spec.Chart.Spec.Version,
		Release: release.Metadata.Name, Namespace: release.Metadata.Namespace, Values: release.Spec.Values})
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(fixture.Rendered) {
		t.Fatal("fixture omits chart objects")
	}
	for i, one := range actual {
		var expected, got bytes.Buffer
		if json.Compact(&expected, fixture.Rendered[i]) != nil || json.Compact(&got, one) != nil {
			t.Fatal("invalid object")
		}
		if !bytes.Equal(expected.Bytes(), got.Bytes()) {
			t.Fatalf("fixture is not real chart output at document %d", i)
		}
	}
	findings, err := policy.Check(fixture.Source, fixture.Platform, actual)
	if err != nil || len(findings) > 0 {
		t.Fatalf("real full-feature policy failed: %v %v", findings, err)
	}
}
