package helm_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/adapters/helm/helmtest"
)

func requireHelm(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		if os.Getenv("REQUIRE_HELM") != "" {
			t.Fatal("REQUIRE_HELM is set but Helm is missing")
		}
		t.Skip("Helm is not installed")
	}
}

func TestContract(t *testing.T) {
	requireHelm(t)
	helmtest.Contract(t, func(*testing.T) helm.Renderer { return helm.New() },
		helm.Request{Chart: "testdata/chart", ChartName: "boundary", ChartVersion: "0.1.0", Release: "boundary", Namespace: "staging", Values: json.RawMessage(`{"retained":"unchanged"}`)})
}

func TestRealTinyChartRetainsExactIntegersAndUnknownKind(t *testing.T) {
	requireHelm(t)
	result, err := helm.New().Render(helm.Request{Chart: "testdata/chart", ChartName: "boundary", ChartVersion: "0.1.0", Release: "boundary", Namespace: "staging", Values: json.RawMessage(`{"retained":"unchanged"}`)})
	if err != nil || len(result) != 3 {
		t.Fatalf("%v, %d objects", err, len(result))
	}
	for _, literal := range []string{`"upstreamFloat":9007199254740992`, "9007199254740993", "9223372036854775807", "18446744073709551615", "-9223372036854775808", `"kind":"Unfamiliar"`, `"kind":"CustomResourceDefinition"`} {
		if !bytes.Contains(bytes.Join(rawBytes(result), nil), []byte(literal)) {
			t.Errorf("lost %s", literal)
		}
	}
}

func rawBytes(objects []json.RawMessage) [][]byte {
	result := make([][]byte, len(objects))
	for i := range objects {
		result[i] = objects[i]
	}
	return result
}

// This ties the new boundary to real compiled values and the pinned, unmodified app chart. It is not a
// policy test: policy and CLI suites separately judge these objects, including real whole-tree compilation.
func TestPinnedAppChart(t *testing.T) {
	requireHelm(t)
	raw, err := os.ReadFile("../../testdata/compiled/prod.json")
	if err != nil {
		t.Fatal(err)
	}
	var compiled struct {
		Files []struct {
			Path     string
			Document json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &compiled); err != nil {
		t.Fatal(err)
	}
	var release struct {
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			Chart struct {
				Spec struct{ Chart, Version string }
			}
			Values json.RawMessage
		}
	}
	for _, file := range compiled.Files {
		if filepath.Base(file.Path) == "helmrelease.json" {
			if err := json.Unmarshal(file.Document, &release); err != nil {
				t.Fatal(err)
			}
		}
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(cache, "swhurl-platform", "charts", release.Spec.Chart.Spec.Chart+"-"+release.Spec.Chart.Spec.Version)
	if _, err := os.Stat(filepath.Join(chart, "Chart.yaml")); err != nil {
		if os.Getenv("REQUIRE_HELM") != "" {
			t.Fatal("REQUIRE_HELM is set but the pinned app chart is missing")
		}
		t.Skip("pinned app chart is missing; make check-apps fetches it")
	}
	result, err := helm.New().Render(helm.Request{Chart: chart, ChartName: release.Spec.Chart.Spec.Chart, ChartVersion: release.Spec.Chart.Spec.Version, Release: release.Metadata.Name,
		Namespace: release.Metadata.Namespace, Values: release.Spec.Values})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, raw := range result {
		var doc struct {
			Kind     string
			Metadata struct{ Name, Namespace string }
			Spec     json.RawMessage
		}
		if json.Unmarshal(raw, &doc) != nil {
			t.Fatal("invalid object")
		}
		kinds[doc.Kind] = true
		if doc.Metadata.Name != release.Metadata.Name || doc.Metadata.Namespace != release.Metadata.Namespace {
			t.Fatal("chart identity changed")
		}
		if doc.Kind == "Deployment" {
			for _, want := range []string{`"automountServiceAccountToken":false`, `"runAsUser":65532`, `"memory":"256Mi"`, `"replicas":1`, `"image":"ghcr.io/samclement/demo-orders@sha256:`} {
				if !bytes.Contains(doc.Spec, []byte(want)) {
					t.Errorf("chart omitted %s", want)
				}
			}
		}
	}
	if !kinds["Deployment"] || !kinds["Service"] {
		t.Fatal("chart workloads/services omitted")
	}
}

func TestRealHelmFailureNeverReturnsItsDiagnostic(t *testing.T) {
	requireHelm(t)
	chart := t.TempDir()
	if err := os.Mkdir(filepath.Join(chart, "templates"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"Chart.yaml":             "apiVersion: v2\nname: failing\nversion: 0.1.0\n",
		"templates/failing.yaml": `{{ fail "sensitive-sentinel" }}`,
	} {
		if err := os.WriteFile(filepath.Join(chart, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := helm.New().Render(helm.Request{Chart: chart, ChartName: "failing", ChartVersion: "0.1.0", Release: "boundary", Namespace: "staging", Values: json.RawMessage(`{}`)})
	if result != nil || err == nil || err.Error() != "refused_render: helm_failed" {
		t.Fatal("Helm failure was not atomic and redacted")
	}
}

func TestRealRendererEscapedAndLiteralEmojiAreEqual(t *testing.T) {
	requireHelm(t)
	var first []json.RawMessage
	for _, values := range []string{`{"retained":"\ud83d\ude00"}`, `{"retained":"😀"}`} {
		result, err := helm.New().Render(helm.Request{Chart: "testdata/chart", ChartName: "boundary", ChartVersion: "0.1.0", Release: "boundary", Namespace: "staging", Values: json.RawMessage(values)})
		if err != nil {
			t.Fatalf("equivalent Unicode values failed to render: %v", err)
		}
		if first == nil {
			first = result
			continue
		}
		if !bytes.Equal(bytes.Join(rawBytes(first), nil), bytes.Join(rawBytes(result), nil)) {
			t.Fatal("JSON Unicode spelling changed real chart output")
		}
	}
	if !bytes.Contains(bytes.Join(rawBytes(first), nil), []byte(`"retained":"😀"`)) {
		t.Fatal("real renderer did not retain the decoded emoji")
	}
}
