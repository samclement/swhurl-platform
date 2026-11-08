package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

func writeStatusFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestStatusChangesCLIUsesExactEndpointsAndRepositoryRelativeV2Paths(t *testing.T) {
	old := strings.Repeat("b", 40)
	api := &api{appliedRevision: "main@sha1:" + old}
	server := api.serve(t)
	args := statusArgs(t, kubeconfig(t, server))
	root := flagValue(args, "--records")
	// The fixture uses its temporary directory as the records root; make its repository-relative name explicit.
	recordsName := filepath.Base(root)
	proofPath := filepath.Join(t.TempDir(), "changes.json")
	for _, test := range []struct {
		paths []string
		state string
	}{
		{[]string{"v2-records/another/staging/instance.json"}, "unaffected"},
		{[]string{recordsName + "/demo-orders/prod/instance.json"}, "pending"},
		{[]string{"clusters/home/app-demo-orders-prod.json"}, "pending"},
		{[]string{"v2-apps/instances/demo-orders/prod/helmrelease.json"}, "pending"},
		{[]string{recordsName + "/profile.json"}, "pending"},
	} {
		proof := observe.Changes{Kind: "v2-changes", Version: 1, Comparisons: []observe.Comparison{{From: old, To: observedRevision, Status: "complete", Paths: test.paths}}}
		data, _ := json.Marshal(proof)
		writeStatusFile(t, proofPath, data)
		status, stdout, stderr := runStatus(t, append(args, "--changes", proofPath, "--repo-root", filepath.Dir(root))...)
		if status != 0 || stderr != "" {
			t.Fatalf("exit %d: %s", status, stderr)
		}
		result := document(t, stdout)
		currency := result["currentness"].(map[string]any)
		if currency["state"] != test.state || currency["appliedRevision"] != "main@sha1:"+old {
			t.Fatalf("%v", currency)
		}
	}
	api.appliedRevision = "main@sha1:" + strings.Repeat("c", 40)
	status, stdout, stderr := runStatus(t, append(args, "--changes", proofPath, "--repo-root", filepath.Dir(root))...)
	if status != 0 || stderr != "" || document(t, stdout)["currentness"].(map[string]any)["reason"] != "comparison_endpoints_mismatch" {
		t.Fatalf("%d %s %s", status, stdout, stderr)
	}
	count := len(api.requests)
	writeStatusFile(t, proofPath, []byte(`{"kind":"v2-changes","version":1,"comparisons":[{"from":"bad"}]}`))
	status, stdout, stderr = runStatus(t, append(args, "--changes", proofPath, "--repo-root", filepath.Dir(root))...)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "invalid: changes:") || len(api.requests) != count {
		t.Fatalf("%d %s %s", status, stdout, stderr)
	}
}

func TestStatusFlagCombinationsAreUnambiguous(t *testing.T) {
	base := []string{"status", "--records", "records", "--profile", "profile", "--revision", observedRevision, "--in-cluster"}
	for _, extra := range [][]string{
		{"--all", "--app", "hello"}, {"--all", "--env", "staging"}, {"--app", "hello"}, {"--env", "staging"},
		{"--all", "--repo-root", "root"}, {"--all", "--all"}, {"--all", "--kubeconfig", "config"},
	} {
		if _, outcome := parse(append(append([]string{}, base...), extra...)); outcome != misuse {
			t.Fatalf("accepted %v", extra)
		}
	}
	if _, outcome := parse(append(base, "--all")); outcome != parsed {
		t.Fatalf("all rejected: %v", outcome)
	}
}

// bulkStatusAPI serves the same raw objects to direct and label-selected requests. Unlike the
// orchestration fake this runs URL building, credential handling and list validation end to end.
type bulkStatusAPI struct {
	requests   []*http.Request
	objects    []map[string]any
	listBody   []byte
	listStatus int
}

func (a *bulkStatusAPI) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests = append(a.requests, r)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		kinds := map[string]string{"kustomizations": "Kustomization", "helmreleases": "HelmRelease", "deployments": "Deployment", "pods": "Pod"}
		parts := strings.Split(r.URL.Path, "/")
		kind, name, namespace := "", "", ""
		for i, part := range parts {
			if value, ok := kinds[part]; ok {
				kind = value
				if i+1 < len(parts) {
					name = parts[i+1]
				}
			}
			if part == "namespaces" && i+1 < len(parts) {
				namespace = parts[i+1]
			}
		}
		if name == "" && a.listStatus != 0 {
			w.WriteHeader(a.listStatus)
			return
		}
		if name == "" && a.listBody != nil {
			_, _ = w.Write(a.listBody)
			return
		}
		items := []any{}
		for _, object := range a.objects {
			metadata := object["metadata"].(map[string]any)
			if object["kind"] != kind || (namespace != "" && metadata["namespace"] != namespace) {
				continue
			}
			if name != "" {
				if metadata["name"] == name {
					writeDocument(t, w, object)
					return
				}
				continue
			}
			if selectedCLIObject(metadata, r.URL.Query().Get("labelSelector")) {
				items = append(items, object)
			}
		}
		if name != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeDocument(t, w, map[string]any{"items": items})
	}))
	t.Cleanup(server.Close)
	return server
}
func selectedCLIObject(metadata map[string]any, selector string) bool {
	labels, _ := metadata["labels"].(map[string]any)
	for _, term := range strings.Split(selector, ",") {
		key, value, equal := strings.Cut(term, "=")
		actual, ok := labels[key]
		if !ok || (equal && actual != value) {
			return false
		}
	}
	return true
}
func (a *bulkStatusAPI) add(namespace string) {
	owner := map[string]any{"kustomize.toolkit.fluxcd.io/name": "app-" + namespace, "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
	labels := map[string]any{"app.kubernetes.io/instance": "demo-orders", "app.kubernetes.io/controller": "main"}
	unit := flux("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "app-"+namespace, "flux-system", map[string]any{"lastAppliedRevision": "main@sha1:" + observedRevision})
	release := flux("helm.toolkit.fluxcd.io/v2", "HelmRelease", "demo-orders", namespace, nil)
	unit["metadata"].(map[string]any)["labels"], release["metadata"].(map[string]any)["labels"] = owner, owner
	deployment := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "demo-orders", "namespace": namespace, "generation": 1, "labels": labels},
		"status": map[string]any{"observedGeneration": 1, "availableReplicas": 1, "readyReplicas": 1, "conditions": []any{map[string]any{"type": "Available", "status": "True"}}}}
	main := pod(namespace, "ghcr.io/test/app@"+compiledDigest)
	main["metadata"].(map[string]any)["labels"] = labels
	a.objects = append(a.objects, unit, release, deployment, main)
}
func bulkStatusArgs(t *testing.T, config string, empty bool) []string {
	t.Helper()
	root := t.TempDir()
	records := filepath.Join(root, "v2-records")
	if err := os.MkdirAll(records, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStatusFile(t, filepath.Join(root, "profile.json"), fixture(t, "profile.json"))
	if !empty {
		writeStatusFile(t, filepath.Join(records, "demo-orders", appFile), fixture(t, "app.json"))
		for _, env := range []string{"prod", "staging"} {
			instance := []byte(strings.Replace(string(fixture(t, "prod.json")), `"prod"`, `"`+env+`"`, 1))
			writeStatusFile(t, filepath.Join(records, "demo-orders", env, instanceFile), instance)
			writeStatusFile(t, filepath.Join(records, "demo-orders", env, releaseFile), fixture(t, "r41.json"))
		}
	}
	return []string{"status", "--all", "--records", records, "--profile", filepath.Join(root, "profile.json"), "--revision", observedRevision, "--kubeconfig", config, "--json"}
}

func TestStatusAllCLIStableCollectionAndSingleAgreement(t *testing.T) {
	api := &bulkStatusAPI{}
	api.add("demo-orders-prod")
	api.add("demo-orders-staging")
	api.add("foreign-staging")
	server := api.serve(t)
	args := bulkStatusArgs(t, kubeconfig(t, server), false)
	status, stdout, stderr := runStatus(t, args...)
	if status != 0 || stderr != "" {
		t.Fatalf("%d %s", status, stderr)
	}
	var bulk struct {
		Kind             string
		Version          int
		Source, Revision string
		Instances        []struct {
			Application, Environment string
			Report                   map[string]any
			Inventory                map[string]any
		}
		Removing []struct{ Application, Environment, Reason string }
	}
	if err := json.Unmarshal([]byte(stdout), &bulk); err != nil {
		t.Fatal(err)
	}
	if bulk.Kind != "v2-observations" || bulk.Version != 2 || bulk.Source != "homelab" || bulk.Revision != observedRevision || len(bulk.Instances) != 2 || len(api.requests) != 4 {
		t.Fatalf("%s, requests %d", stdout, len(api.requests))
	}
	// The unit of an instance no record names is still there, so the collection keeps it visible. Its release is not
	// this app's (the fixture names another application), so only the unit is unaccounted for.
	if len(bulk.Removing) != 1 || bulk.Removing[0].Application != "foreign" || bulk.Removing[0].Environment != "staging" || bulk.Removing[0].Reason != observe.RemovedUnit {
		t.Fatalf("removing %+v", bulk.Removing)
	}
	for i, env := range []string{"prod", "staging"} {
		entry := bulk.Instances[i]
		if entry.Application != "demo-orders" || entry.Environment != env || entry.Report["state"].(map[string]any)["name"] != observe.StateHealthy {
			t.Fatalf("%+v", entry)
		}
		if entry.Inventory["team"] != "demo" || entry.Inventory["kind"] != "web" || entry.Inventory["port"] != float64(8080) {
			t.Fatalf("inventory %+v", entry.Inventory)
		}
		singleArgs := []string{}
		for _, arg := range args {
			if arg != "--all" {
				singleArgs = append(singleArgs, arg)
			}
		}
		singleArgs = append(singleArgs, "--app", "demo-orders", "--env", env)
		status, stdout, stderr := runStatus(t, singleArgs...)
		if status != 0 || stderr != "" {
			t.Fatalf("%d %s", status, stderr)
		}
		single := document(t, stdout)
		delete(single, "observedAt")
		delete(entry.Report, "observedAt")
		got, _ := json.Marshal(single)
		want, _ := json.Marshal(entry.Report)
		if string(got) != string(want) {
			t.Fatalf("single/bulk differ\n%s\n%s", got, want)
		}
	}
	textArgs := []string{}
	for _, arg := range args {
		if arg != "--json" {
			textArgs = append(textArgs, arg)
		}
	}
	status, stdout, stderr = runStatus(t, textArgs...)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "Instance   demo-orders/prod") || !strings.Contains(stdout, "Instance   demo-orders/staging") {
		t.Fatalf("%d %s %s", status, stdout, stderr)
	}
}

func TestStatusAllCLIEmptyRecordsAndRefusals(t *testing.T) {
	api := &bulkStatusAPI{}
	server := api.serve(t)
	args := bulkStatusArgs(t, kubeconfig(t, server), true)
	status, stdout, stderr := runStatus(t, args...)
	if status != 0 || stderr != "" || !strings.Contains(stdout, `"instances":[]`) || len(api.requests) != 0 {
		t.Fatalf("%d %s %s requests %d", status, stdout, stderr, len(api.requests))
	}
	// A failed filesystem read and an invalid record are not an empty successful collection.
	records := flagValue(args, "--records")
	if err := os.Remove(records); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = runStatus(t, args...)
	if status != 1 || stdout != "" || !strings.Contains(stderr, "io: records:") {
		t.Fatalf("%d %s %s", status, stdout, stderr)
	}
	writeStatusFile(t, filepath.Join(records, "hello", "staging", instanceFile), []byte(`{}`))
	writeStatusFile(t, filepath.Join(records, "hello", "staging", releaseFile), fixture(t, "r41.json"))
	writeStatusFile(t, filepath.Join(records, "hello", appFile), fixture(t, "app.json"))
	status, stdout, stderr = runStatus(t, args...)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "invalid:") {
		t.Fatalf("%d %s %s", status, stdout, stderr)
	}
}

func TestStatusAllRefusesUnreadableRecordDiscovery(t *testing.T) {
	api := &bulkStatusAPI{}
	server := api.serve(t)
	args := bulkStatusArgs(t, kubeconfig(t, server), false)
	instance := filepath.Join(flagValue(args, "--records"), "demo-orders", "prod", instanceFile)
	if err := os.Remove(instance); err != nil {
		t.Fatal(err)
	}
	// ELOOP is deterministic even for root; a failed stat must not silently erase this instance.
	if err := os.Symlink(instance, instance); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := runStatus(t, args...)
	if status != 1 || stdout != "" || !strings.Contains(stderr, "io: records:") || len(api.requests) != 0 {
		t.Fatalf("%d %s %s", status, stdout, stderr)
	}
}

func TestStatusAllSharesInClusterCredentialsAndSourceOverride(t *testing.T) {
	api := &bulkStatusAPI{}
	api.add("demo-orders-prod")
	api.add("demo-orders-staging")
	server := api.serve(t)
	mount, _ := serviceAccountMount(t, server)
	args := bulkStatusArgs(t, "unused", false)
	args = append(withoutStatusFlag(args, "--kubeconfig"), "--in-cluster", "--source", "console-request")
	status, stdout, stderr := runInCluster(t, mount, args...)
	if status != 0 || stderr != "" {
		t.Fatalf("%d %s", status, stderr)
	}
	result := document(t, stdout)
	if result["source"] != "console-request" || len(api.requests) != 4 {
		t.Fatalf("%s requests %d", stdout, len(api.requests))
	}
	for _, raw := range result["instances"].([]any) {
		report := raw.(map[string]any)["report"].(map[string]any)
		if report["source"] != "console-request" || report["state"].(map[string]any)["name"] != observe.StateHealthy {
			t.Fatalf("%v", report)
		}
	}
}
func withoutStatusFlag(args []string, flag string) []string {
	result := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			i++
			continue
		}
		result = append(result, args[i])
	}
	return result
}

func TestStatusChangesRefusesInputsOutsideRepositoryRoot(t *testing.T) {
	api := &api{}
	server := api.serve(t)
	args := statusArgs(t, kubeconfig(t, server))
	proof := filepath.Join(t.TempDir(), "changes.json")
	writeStatusFile(t, proof, []byte(`{"kind":"v2-changes","version":1,"comparisons":[]}`))
	status, stdout, stderr := runStatus(t, append(args, "--changes", proof, "--repo-root", t.TempDir())...)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "invalid: records:") || len(api.requests) != 0 {
		t.Fatalf("%d %s %s", status, stdout, stderr)
	}
}
