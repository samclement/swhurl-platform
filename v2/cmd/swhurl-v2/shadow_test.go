package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// snapshotDocument builds a snapshot in the shape `make v2-snapshot` writes, from the frozen legacy
// fixture: one instance whose records are complete, and whatever the test adds.
func snapshotDocument(t *testing.T, instance map[string]any, extra ...map[string]any) map[string]any {
	t.Helper()
	records := map[string]any{}
	for role, name := range map[string]string{"app": "app.json", "instance": "instance.json",
		"release": "release.json", "helmrelease": "helmrelease.json"} {
		data, err := os.ReadFile(legacyFile(t, name))
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		records[role] = document
	}
	for role, value := range instance {
		records[role] = value
	}
	instances := []any{map[string]any{
		"application": "hello", "environment": "staging", "records": records, "missing": []any{},
		"objects": []any{
			object("v1", "Namespace", "", "hello-staging"),
			object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "hello-staging", "hello"),
			object("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "app-hello-staging"),
		},
	}}
	for _, additional := range extra {
		instances = append(instances, additional)
	}
	return map[string]any{"version": 1, "revision": strings.Repeat("a", 40), "instances": instances, "residual": []any{}}
}

func object(apiVersion, kind, namespace, name string) map[string]any {
	return map[string]any{"apiVersion": apiVersion, "kind": kind, "namespace": namespace, "name": name}
}

// writeSnapshot writes a snapshot document to a temporary file and returns its path.
func writeSnapshot(t *testing.T, document map[string]any) string {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runShadow runs the command and returns its exit status and streams.
func runShadow(t *testing.T, snapshotPath string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := run([]string{"shadow", "--snapshot", snapshotPath, "--profile", filepath.Join("..", "..", "testdata", "profile.json")},
		&out, &diagnostic, os.ReadFile, files.New())
	return status, out.String(), diagnostic.String()
}

// shadowOf decodes the report a successful analysis writes.
func shadowOf(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, stdout)
	}
	return report
}

// instanceOf is one analysed instance of a decoded report.
func instanceOf(t *testing.T, report map[string]any, application string) map[string]any {
	t.Helper()
	for _, raw := range report["instances"].([]any) {
		entry := raw.(map[string]any)
		if entry["application"] == application {
			return entry
		}
	}
	t.Fatalf("no analysis for %s", application)
	return nil
}

// A snapshot is analysed as a whole: the instance is compared with its own committed output, and its
// Namespace and HelmRelease collide with what it already declares, which is reported rather than hidden.
func TestShadowAnalysesTheSnapshot(t *testing.T) {
	path := writeSnapshot(t, snapshotDocument(t, nil))
	status, stdout, stderr := runShadow(t, path)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := shadowOf(t, stdout)
	if report["kind"] != "v2-shadow" || report["version"] != float64(1) || report["revision"] != strings.Repeat("a", 40) {
		t.Fatalf("identity: %v %v %v", report["kind"], report["version"], report["revision"])
	}
	if report["note"] != shadowNote || len(report["coverage"].(map[string]any)["notCovered"].([]any)) == 0 {
		t.Fatalf("note/coverage: %v", report["note"])
	}
	instance := instanceOf(t, report, "hello")
	if instance["available"] != true {
		t.Fatalf("available: %v (%v)", instance["available"], instance["reason"])
	}
	findings := instance["comparison"].(map[string]any)["findings"].([]any)
	if len(findings) != 12 {
		t.Fatalf("%d findings", len(findings))
	}
	ownership := instance["ownership"].([]any)
	if len(ownership) < 2 {
		t.Fatalf("the instance's own collisions are not reported: %v", ownership)
	}
	named := map[string]bool{}
	for _, raw := range ownership {
		entry := raw.(map[string]any)
		named[entry["object"].(string)] = true
		owners := entry["owners"].([]any)
		// One owner, two claims: the instance's committed output and its own preview name the same
		// object, which is the expected collision of comparing an instance with itself.
		if len(owners) != 1 || owners[0] != "hello/staging" || entry["claims"] != float64(2) {
			t.Errorf("an unexpected ownership entry: %v", entry)
		}
	}
	for _, want := range []string{"Namespace hello-staging", "HelmRelease.helm.toolkit.fluxcd.io hello-staging/hello"} {
		if !named[want] {
			t.Errorf("no collision reported for %q: %v", want, named)
		}
	}
	if !strings.Contains(stderr, "ownership collision") {
		t.Fatalf("summary %q", stderr)
	}
}

// Two instances that declare the same object are a real conflict: the report names both owners.
func TestShadowReportsACrossInstanceConflict(t *testing.T) {
	other := map[string]any{
		"application": "other", "environment": "staging", "missing": []any{},
		"records": map[string]any{},
		"objects": []any{object("v1", "Namespace", "", "hello-staging")},
	}
	path := writeSnapshot(t, snapshotDocument(t, nil, other))
	status, stdout, stderr := runShadow(t, path)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := shadowOf(t, stdout)
	hello := instanceOf(t, report, "hello")
	found := false
	for _, raw := range hello["ownership"].([]any) {
		entry := raw.(map[string]any)
		if entry["object"] != "Namespace hello-staging" {
			continue
		}
		owners := entry["owners"].([]any)
		if len(owners) != 2 || owners[0] != "hello/staging" || owners[1] != "other/staging" || entry["claims"] != float64(3) {
			t.Fatalf("owners: %v, claims %v", owners, entry["claims"])
		}
		found = true
	}
	if !found {
		t.Fatalf("the cross-instance conflict is not reported: %v", hello["ownership"])
	}
	// The second instance has no records, so it cannot be previewed; it is unavailable with a reason,
	// and the analysis of the first still stands.
	otherReport := instanceOf(t, report, "other")
	if otherReport["available"] != false || otherReport["reason"] == "" {
		t.Fatalf("an instance without records: %v", otherReport)
	}
	if coverage := report["coverage"].(map[string]any); coverage["unavailable"] != float64(1) || coverage["compared"] != float64(1) {
		t.Fatalf("coverage: %v", coverage)
	}
}

// A unit whose output no record explains is named in the report: residual output is ownership evidence.
func TestShadowNamesResidualUnits(t *testing.T) {
	document := snapshotDocument(t, nil)
	document["residual"] = []any{map[string]any{"unit": "app-orphan-prod",
		"object": object("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "app-orphan-prod")}}
	status, stdout, stderr := runShadow(t, writeSnapshot(t, document))
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	residual := shadowOf(t, stdout)["residual"].([]any)
	if len(residual) != 1 || !strings.Contains(residual[0].(string), "app-orphan-prod") {
		t.Fatalf("residual: %v", residual)
	}
}

// A snapshot this command cannot analyse is refused before anything is rendered.
func TestShadowRefusesAnUnusableSnapshot(t *testing.T) {
	for _, tt := range []struct {
		name     string
		document map[string]any
		reason   string
	}{
		{"a version it does not know", map[string]any{"version": 2, "revision": strings.Repeat("a", 40)}, "only_version_1"},
		{"no revision", map[string]any{"version": 1, "instances": []any{}}, "revision_required"},
		{"a revision that is not a commit", map[string]any{"version": 1, "revision": "HEAD"}, "revision_required"},
		{"an instance with no identity", map[string]any{"version": 1, "revision": strings.Repeat("a", 40),
			"instances": []any{map[string]any{"application": "", "environment": "staging"}}}, "instance_identity_required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, stdout, stderr := runShadow(t, writeSnapshot(t, tt.document))
			if status != 2 || stdout != "" || !strings.Contains(stderr, tt.reason) {
				t.Fatalf("exit %d, stdout %q, stderr %q", status, stdout, stderr)
			}
		})
	}
}

// The live check reads every object the preview would create: presence, absence and a failed read are
// three different answers, and only a successful 404 is absence.
func TestShadowChecksLiveOwnership(t *testing.T) {
	// Every claim answers with its own identity, so a present object passes the adapter's check; the
	// others fail in the three ways a read can.
	present := map[string]map[string]any{
		"/api/v1/namespaces/hello-staging": {"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": "hello-staging"}},
		"/apis/networking.k8s.io/v1/namespaces/hello-staging/networkpolicies/hello": {"apiVersion": "networking.k8s.io/v1",
			"kind": "NetworkPolicy", "metadata": map[string]any{"name": "hello", "namespace": "hello-staging"}},
		"/apis/networking.k8s.io/v1/namespaces/hello-staging/ingresses/hello": {"apiVersion": "networking.k8s.io/v1",
			"kind": "Ingress", "metadata": map[string]any{"name": "hello", "namespace": "hello-staging"}},
		"/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations/app-hello-staging": {"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind": "Kustomization", "metadata": map[string]any{"name": "app-hello-staging", "namespace": "flux-system"}},
	}
	status := map[string]int{
		"/apis/helm.toolkit.fluxcd.io/v2/namespaces/hello-staging/helmreleases/hello": http.StatusNotFound,
		"/api/v1/namespaces/hello-staging/resourcequotas/hello":                       http.StatusForbidden,
	}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if code, failed := status[r.URL.Path]; failed {
			w.WriteHeader(code)
			return
		}
		document, found := present[r.URL.Path]
		if !found {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(document); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	args := append(previewArgsForShadow(t), "--kubeconfig", kubeconfig(t, server))
	var out, diagnostic bytes.Buffer
	if code := run(args, &out, &diagnostic, os.ReadFile, files.New()); code != 0 {
		t.Fatalf("exit %d: %s", code, diagnostic.String())
	}
	instance := instanceOf(t, shadowOf(t, out.String()), "hello")
	live := map[string]map[string]any{}
	for _, raw := range instance["live"].([]any) {
		entry := raw.(map[string]any)
		live[entry["object"].(string)] = entry
	}
	for object, want := range map[string]string{
		"Namespace hello-staging":                                                 "present",
		"HelmRelease.helm.toolkit.fluxcd.io hello-staging/hello":                  "absent",
		"ResourceQuota hello-staging/hello":                                       "unavailable",
		"NetworkPolicy.networking.k8s.io hello-staging/hello":                     "present",
		"Kustomization.kustomize.toolkit.fluxcd.io flux-system/app-hello-staging": "present",
	} {
		entry, found := live[object]
		if !found {
			t.Errorf("no live answer for %q: %v", object, live)
			continue
		}
		if entry["status"] != want {
			t.Errorf("%s is %v, want %s", object, entry["status"], want)
		}
	}
	if live["ResourceQuota hello-staging/hello"]["reason"] != "forbidden" {
		t.Errorf("a failed read without its reason: %v", live["ResourceQuota hello-staging/hello"])
	}
	if live["HelmRelease.helm.toolkit.fluxcd.io hello-staging/hello"]["reason"] != "not_found" {
		t.Errorf("absence without not_found: %v", live["HelmRelease.helm.toolkit.fluxcd.io hello-staging/hello"])
	}
	if requests != 6 {
		t.Fatalf("%d requests for six claims", requests)
	}
}

// previewArgsForShadow is the shadow command line over the fixture snapshot.
func previewArgsForShadow(t *testing.T) []string {
	t.Helper()
	return []string{"shadow", "--snapshot", writeSnapshot(t, snapshotDocument(t, nil)),
		"--profile", filepath.Join("..", "..", "testdata", "profile.json")}
}

// Without a kubeconfig nothing is read: the live list is present and empty, and no claim is reported.
func TestShadowWithoutKubeconfigReadsNothingLive(t *testing.T) {
	status, stdout, stderr := runShadow(t, writeSnapshot(t, snapshotDocument(t, nil)))
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	instance := instanceOf(t, shadowOf(t, stdout), "hello")
	if live, ok := instance["live"].([]any); !ok || len(live) != 0 {
		t.Fatalf("live: %v", instance["live"])
	}
}

// The analysis is reproducible: the same snapshot encodes to the same bytes.
func TestShadowIsReproducible(t *testing.T) {
	path := writeSnapshot(t, snapshotDocument(t, nil))
	_, first, _ := runShadow(t, path)
	_, second, _ := runShadow(t, path)
	if first != second {
		t.Fatal("two analyses of one snapshot differ")
	}
}
