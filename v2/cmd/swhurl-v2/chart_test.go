package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// yamlDocuments reads a committed YAML file as JSON objects, with PyYAML: it parses locally, where
// kubectl's client-side dry run still asks the server to recognise an unknown kind, so a test must not
// depend on a cluster for it. The repository has PyYAML for its own tooling, and CI installs it.
func yamlDocuments(t *testing.T, path string) []map[string]any {
	t.Helper()
	python := tool(t, "python3")
	out := output(t, python, "-c",
		"import json,sys,yaml; print(json.dumps([d for d in yaml.safe_load_all(open(sys.argv[1]).read()) if d]))", path)
	var documents []map[string]any
	if err := json.Unmarshal([]byte(out), &documents); err != nil {
		t.Fatalf("%s is not YAML: %v\n%s", path, err, out)
	}
	if len(documents) == 0 {
		t.Fatalf("%s holds no objects", path)
	}
	return documents
}

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

// withoutReplicasJSON is the compiled values without the controller's replica count, so the chart's
// default is what renders.
func withoutReplicasJSON(values []byte) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(values, &document); err != nil {
		return nil, err
	}
	controllers, _ := document["controllers"].(map[string]any)
	main, _ := controllers["main"].(map[string]any)
	delete(main, "replicas")
	return json.Marshal(document)
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
		"kind: Deployment", "kind: Service", "name: " + release.Metadata.Name + "\n", "replicas: 1", "automountServiceAccountToken: false", "runAsUser: 65532",
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
	// legacy.Translate materialises a replica count neither record nor committed output states from the
	// pinned chart's default, so that default must be one: render the same values without it.
	withoutReplicas, err := withoutReplicasJSON(release.Spec.Values)
	if err != nil {
		t.Fatal(err)
	}
	values = filepath.Join(dir, "values-without-replicas.json")
	if err := os.WriteFile(values, withoutReplicas, 0o644); err != nil {
		t.Fatal(err)
	}
	if rendered := output(t, helm, "template", release.Metadata.Name, chart, "-n", release.Metadata.Namespace, "-f", values); !strings.Contains(rendered, "replicas: 1") {
		t.Error("the pinned chart's default replica count is not one")
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

// serviceOf is a rendered Service's identity, from the chart's own output.
func serviceOf(rendered string) (name, namespace string) {
	start := strings.Index(rendered, "kind: Service")
	if start < 0 {
		return "", ""
	}
	service := rendered[start:]
	if end := strings.Index(service, "\n---"); end > 0 {
		service = service[:end]
	}
	for _, line := range strings.Split(service, "\n") {
		if rest, found := strings.CutPrefix(line, "  name: "); found && name == "" {
			name = strings.TrimSpace(rest)
		}
		if rest, found := strings.CutPrefix(line, "  namespace: "); found && namespace == "" {
			namespace = strings.TrimSpace(rest)
		}
	}
	return name, namespace
}

// TestProxyManifests ties what v2 renders to the platform's hand-written proxy: the ConfigMap the
// HelmRelease reads, the Service the pinned chart deploys at the address the profile states, and the
// pod labels the rendered policy selects. Each of those is a fact written twice — once by v2, once by
// the platform's own file — so a rename on either side fails here instead of silently breaking egress.
func TestProxyManifests(t *testing.T) {
	helm := tool(t, "helm")
	profile, err := kernel.DecodeProfile(fixture(t, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := kernel.RenderProxy([]kernel.EgressAllocation{
		{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"api.stripe.com"}},
		{Application: "orders", Environment: "staging", Port: 30102},
	}, profile)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, file := range rendered.Files {
		path := filepath.Join(dir, file.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Document, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The proxy's own deployment is the platform's file, not v2's: read it as the tools do.
	var release struct {
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			Chart struct {
				Spec struct{ Chart, Version string }
			}
			ValuesFrom []struct{ Kind, Name, ValuesKey string }
			Values     map[string]any
		}
	}
	live := yamlDocuments(t, "../../../platform/egress/helmrelease.yaml")[0]
	encoded, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &release); err != nil {
		t.Fatalf("the HelmRelease is not what this test reads: %v", err)
	}
	var configMap struct {
		Metadata struct{ Name, Namespace string }
		Data     map[string]string
	}
	configMapDocument, err := os.ReadFile(filepath.Join(dir, "egress/configmap.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(configMapDocument, &configMap); err != nil {
		t.Fatal(err)
	}
	// Tie 1: the HelmRelease reads the ConfigMap v2 renders, by this name and this key.
	if len(release.Spec.ValuesFrom) == 0 {
		t.Fatal("the proxy's HelmRelease reads no values")
	}
	if source := release.Spec.ValuesFrom[0]; source.Kind != "ConfigMap" || source.Name != configMap.Metadata.Name ||
		source.ValuesKey != "values.yaml" || source.ValuesKey != proxyValuesKey(t, configMap.Data) {
		t.Fatalf("the HelmRelease reads %+v; v2 renders %s/%s", source, configMap.Metadata.Namespace, configMap.Metadata.Name)
	}
	values := filepath.Join(dir, "egress/values.yaml")
	if err := os.WriteFile(values, []byte(configMap.Data["values.yaml"]), 0o644); err != nil {
		t.Fatal(err)
	}
	static, err := json.Marshal(release.Spec.Values)
	if err != nil {
		t.Fatal(err)
	}
	staticPath := filepath.Join(dir, "static.json")
	if err := os.WriteFile(staticPath, static, 0o644); err != nil {
		t.Fatal(err)
	}
	cache, _ := os.UserCacheDir()
	chart := filepath.Join(cache, "swhurl-platform", "charts", release.Spec.Chart.Spec.Chart+"-"+release.Spec.Chart.Spec.Version)
	if _, err := os.Stat(filepath.Join(chart, "Chart.yaml")); err != nil {
		need(t, "the pinned chart in "+chart+" (make check-apps fetches it)")
	}
	manifests := output(t, helm, "template", release.Metadata.Name, chart, "-n", release.Metadata.Namespace,
		"-f", staticPath, "-f", values)
	// Tie 2: the Service the chart deploys is the one the profile's address names.
	host, _, _ := strings.Cut(profile.Egress.Address, ":")
	parts := strings.SplitN(host, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("the profile's proxy address is not a Service's name: %q", profile.Egress.Address)
	}
	if service, where := serviceOf(manifests); service != parts[0] || where != parts[1] {
		t.Errorf("the chart deploys Service %s/%s, but the profile's address is %q", where, service, profile.Egress.Address)
	}
	for _, want := range []string{"    - port: 30101\n", "    - port: 30102\n", "    - port: 3128\n",
		"acl shop-staging myportname shop-staging", "acl orders-staging myportname orders-staging",
		"acl shop-staging-443 dstdomain -n api.stripe.com", "http_access allow shop-staging shop-staging-443 port-443",
		"http_access deny all"} {
		if !strings.Contains(manifests, want) {
			t.Errorf("the chart's output lacks %q", want)
		}
	}
	if strings.Contains(manifests, "http_access allow all") {
		t.Error("the chart's output allows every host: grants are not enforced")
	}
	// Tie 4: the probe port the HelmRelease probes is the one the rendered configuration serves.
	if probe := probePort(release.Spec.Values); probe == 0 || !strings.Contains(manifests, fmt.Sprintf("http_port %d name=health\n", probe)) {
		t.Errorf("the HelmRelease probes port %d, which the configuration does not serve", probe)
	}
	// Tie 3: the labels the rendered policy selects are on the pods the chart deploys.
	var policy struct {
		Spec struct {
			PodSelector struct{ MatchLabels map[string]string }
		}
	}
	policyDocument, err := os.ReadFile(filepath.Join(dir, "egress/networkpolicy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(policyDocument, &policy); err != nil {
		t.Fatal(err)
	}
	pods := manifests[strings.Index(manifests, "kind: Deployment"):]
	pods = pods[strings.Index(pods, "  template:"):]
	labels := pods[:strings.Index(pods, "    spec:")]
	if len(policy.Spec.PodSelector.MatchLabels) == 0 {
		t.Fatal("the NetworkPolicy selects no labels")
	}
	for key, value := range policy.Spec.PodSelector.MatchLabels {
		if !strings.Contains(labels, key+": "+value+"\n") {
			t.Errorf("the pods lack %s: %s, which the policy selects:\n%s", key, value, labels)
		}
	}
}

// probePort is the tcpSocket port the HelmRelease's readiness probe reads.
func probePort(values map[string]any) int {
	controllers, _ := values["controllers"].(map[string]any)
	main, _ := controllers["main"].(map[string]any)
	containers, _ := main["containers"].(map[string]any)
	container, _ := containers["main"].(map[string]any)
	probes, _ := container["probes"].(map[string]any)
	readiness, _ := probes["readiness"].(map[string]any)
	spec, _ := readiness["spec"].(map[string]any)
	socket, _ := spec["tcpSocket"].(map[string]any)
	port, _ := socket["port"].(float64)
	return int(port)
}

// proxyValuesKey is the key the ConfigMap holds its values under, so the tie above compares the
// rendered data rather than the constant.
func proxyValuesKey(t *testing.T, data map[string]string) string {
	t.Helper()
	for key := range data {
		return key
	}
	t.Fatal("the ConfigMap holds no values")
	return ""
}
