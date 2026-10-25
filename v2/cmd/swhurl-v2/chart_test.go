package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
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

// deploymentName is the Deployment's metadata.name, from the rendered manifest.
func deploymentName(rendered string) string {
	if !strings.Contains(rendered, "kind: Deployment") {
		return ""
	}
	for _, line := range strings.Split(rendered[strings.Index(rendered, "kind: Deployment"):], "\n") {
		if rest, found := strings.CutPrefix(line, "  name: "); found {
			return strings.TrimSpace(rest)
		}
	}
	return ""
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
	var releaseDocument json.RawMessage
	var release struct {
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			Chart struct {
				Spec struct{ Chart, Version string }
			}
			Values json.RawMessage
		}
	}
	var policy struct {
		Spec struct {
			PodSelector struct{ MatchLabels map[string]string }
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
		var into any
		switch {
		case strings.HasSuffix(file.Path, "/helmrelease.json"):
			into, releaseDocument = &release, file.Document
		case strings.HasSuffix(file.Path, "/networkpolicy.json"):
			into = &policy
		default:
			continue
		}
		if err := json.Unmarshal(file.Document, into); err != nil {
			t.Fatal(err)
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
	// Flux names the Helm release after the HelmRelease (it sets no targetNamespace); render it so.
	rendered := output(t, helm, "template", release.Metadata.Name, chart, "-n", release.Metadata.Namespace, "-f", values)
	for _, want := range []string{
		"kind: Deployment", "kind: Service", "replicas: 1", "automountServiceAccountToken: false", "runAsUser: 65532",
		"image: ghcr.io/samclement/demo-orders@sha256:" + strings.Repeat("a", 64), "path: /healthz", "memory: 256Mi",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the chart's output lacks %q", want)
		}
	}
	// Every label the compiled NetworkPolicy selects must be on the Deployment's pods.
	pods := rendered[strings.Index(rendered, "kind: Deployment"):]
	pods = pods[strings.Index(pods, "  template:"):]
	labels := pods[:strings.Index(pods, "    spec:")]
	if len(policy.Spec.PodSelector.MatchLabels) == 0 {
		t.Fatal("the NetworkPolicy selects no labels")
	}
	for key, value := range policy.Spec.PodSelector.MatchLabels {
		if !strings.Contains(labels, key+": "+value+"\n") {
			t.Errorf("the pods lack %s: %s, which the NetworkPolicy selects:\n%s", key, value, labels)
		}
	}
	// The target observation derives from this HelmRelease must name what the chart renders: a chart
	// change that moved a name or a label fails here rather than in a live read.
	unit := observe.Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization",
		Namespace: "flux-system", Name: "app-" + release.Metadata.Name + "-" + strings.TrimPrefix(release.Metadata.Namespace, release.Metadata.Name+"-")}
	target, err := observe.TargetOf(releaseDocument, unit, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if name := deploymentName(rendered); name != target.Workload.Name {
		t.Errorf("the chart renders the Deployment %q, but the target names %q", name, target.Workload.Name)
	}
	for _, term := range strings.Split(target.Selector, ",") {
		key, value, _ := strings.Cut(term, "=")
		if !strings.Contains(labels, key+": "+value+"\n") {
			t.Errorf("the pods lack %s: %s, which the target selects:\n%s", key, value, labels)
		}
	}

	built := output(t, kubectl, "kustomize", filepath.Join(dir, "v2-apps/instances/demo-orders/prod"))
	for _, want := range []string{"kind: Namespace", "kind: HelmRelease", "kind: ResourceQuota", "kind: NetworkPolicy"} {
		if !strings.Contains(built, want) {
			t.Errorf("kustomize's output lacks %q", want)
		}
	}
}
