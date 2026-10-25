package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// need skips a test that lacks an external tool or the cached chart, or fails it under
// REQUIRE_HELM, as CI sets it, so the check cannot silently stop running there.
func need(t *testing.T, what string) {
	t.Helper()
	if os.Getenv("REQUIRE_HELM") != "" {
		t.Fatalf("REQUIRE_HELM is set but %s is missing", what)
	}
	t.Skipf("%s is missing", what)
}

func tool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		need(t, name)
	}
	return path
}

func output(t *testing.T, program string, args ...string) string {
	t.Helper()
	out, err := exec.Command(program, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(program), args, err, out)
	}
	return string(out)
}

// TestCompiledManifests renders the golden prod compilation with the tools Flux uses, offline: the
// HelmRelease through the pinned chart from the swhurl chart cache, the files through kustomize.
func TestCompiledManifests(t *testing.T) {
	helm, kubectl := tool(t, "helm"), tool(t, "kubectl")
	var compiled struct {
		Files []struct {
			Path     string
			Document json.RawMessage
		}
	}
	if err := json.Unmarshal(fixture(t, "compiled/prod.json"), &compiled); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var release struct {
		Spec struct {
			Chart struct {
				Spec struct{ Chart, Version string }
			}
			Values json.RawMessage
		}
	}
	for _, file := range compiled.Files {
		path := filepath.Join(dir, file.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Document, 0o644); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(file.Path, "/helmrelease.json") {
			if err := json.Unmarshal(file.Document, &release); err != nil {
				t.Fatal(err)
			}
		}
	}
	cache, _ := os.UserCacheDir()
	chart := filepath.Join(cache, "swhurl-platform", "charts", release.Spec.Chart.Spec.Chart+"-"+release.Spec.Chart.Spec.Version)
	if _, err := os.Stat(filepath.Join(chart, "Chart.yaml")); err != nil {
		need(t, "the pinned chart in "+chart+" (make check-apps fetches it)")
	}
	values := filepath.Join(dir, "values.json")
	if err := os.WriteFile(values, release.Spec.Values, 0o644); err != nil {
		t.Fatal(err)
	}
	rendered := output(t, helm, "template", "demo-orders", chart, "-n", "demo-orders-prod", "-f", values)
	for _, want := range []string{
		"kind: Deployment", "kind: Service", "replicas: 1", "automountServiceAccountToken: false", "runAsUser: 65532",
		"image: ghcr.io/samclement/demo-orders@sha256:" + strings.Repeat("a", 64), "path: /healthz", "memory: 256Mi",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the chart's output lacks %q", want)
		}
	}
	// The NetworkPolicy selects app.kubernetes.io/instance; the Deployment's pods must carry it.
	pods := rendered[strings.Index(rendered, "kind: Deployment"):]
	pods = pods[strings.Index(pods, "  template:"):]
	if labels := pods[:strings.Index(pods, "    spec:")]; !strings.Contains(labels, "app.kubernetes.io/instance: demo-orders") {
		t.Errorf("the pods lack the label the NetworkPolicy selects:\n%s", labels)
	}
	built := output(t, kubectl, "kustomize", filepath.Join(dir, "v2-apps/demo-orders/prod"))
	for _, want := range []string{"kind: Namespace", "kind: HelmRelease", "kind: ResourceQuota", "kind: NetworkPolicy"} {
		if !strings.Contains(built, want) {
			t.Errorf("kustomize's output lacks %q", want)
		}
	}
}
