package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClickStackDashboardsCLIFlags(t *testing.T) {
	for _, args := range [][]string{
		{"clickstack-dashboards", "--in-cluster"}, {"clickstack-dashboards", "--cluster"},
		{"clickstack-dashboards", "--cluster", "--records", "r", "--in-cluster"},
		{"clickstack-dashboards", "--cluster", "--kubeconfig", "x", "--in-cluster"},
		{"clickstack-dashboards", "--cluster", "--cluster", "--in-cluster"}, {"clickstack-dashboards", "--cluster", "--in-cluster", "extra"},
	} {
		if _, outcome := parse(args); outcome != misuse {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"clickstack-dashboards", "--cluster", "--in-cluster"},
		{"clickstack-dashboards", "--records", "v2-records", "--kubeconfig", "x", "--dry-run"}} {
		if _, outcome := parse(args); outcome != parsed {
			t.Fatalf("refused %v", args)
		}
	}
}

// dashboardAPI is the dashboard job's RBAC as an API server: the HelmRelease and app Namespace lists, the four
// Secrets by named GET, and exec, which it refuses so the pass stops at its first ClickStack request. Anything else
// — a Secret list, a write — fails the test.
type dashboardAPI struct {
	t     *testing.T
	paths []string
}

func (a *dashboardAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.paths = append(a.paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	secrets := map[string]map[string]string{
		"/api/v1/namespaces/observability/secrets/clickstack-runtime-inputs":          {"CLICKSTACK_ADMIN_EMAIL": "admin@example.test"},
		"/api/v1/namespaces/observability/secrets/clickstack-mongodb-hyperdx-hyperdx": {"connectionString.standard": "mongodb://private-uri"},
		"/api/v1/namespaces/console/secrets/notification-ntfy":                        {"NTFY_PLATFORM_URL": "https://ntfy.example.test/private-topic"},
		"/api/v1/namespaces/notify/secrets/notify-alerts":                             {"secret": "private-secret"},
	}
	switch {
	case r.Method != http.MethodGet:
		a.t.Errorf("an effect: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	case r.URL.Path == "/apis/helm.toolkit.fluxcd.io/v2/helmreleases":
		fmt.Fprint(w, `{"items":[{"apiVersion":"helm.toolkit.fluxcd.io/v2","kind":"HelmRelease","metadata":{"name":"web","namespace":"web-staging",`+
			`"labels":{"kustomize.toolkit.fluxcd.io/name":"app-web-staging","kustomize.toolkit.fluxcd.io/namespace":"flux-system"}},"spec":{"values":{"service":{}}}}]}`)
	case r.URL.Path == "/api/v1/namespaces":
		fmt.Fprint(w, `{"items":[]}`)
	case secrets[r.URL.Path] != nil:
		name := filepath.Base(r.URL.Path)
		data := map[string]string{}
		for key, value := range secrets[r.URL.Path] {
			data[key] = base64.StdEncoding.EncodeToString([]byte(value))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]string{"name": name, "namespace": strings.Split(r.URL.Path, "/")[4]}, "data": data})
	default:
		w.WriteHeader(http.StatusForbidden) // exec, and anything the Roles do not allow
	}
}

// One cluster pass through the real adapters: discovery lists with selectors, Secrets by named GET only, then
// exec beside MongoDB, which the API server refuses; the pass stops, exits 1 and logs no credential.
func TestClickStackDashboardsClusterPassReadsByNameAndStopsAtARefusedExec(t *testing.T) {
	api := &dashboardAPI{t: t}
	server := httptest.NewTLSServer(api)
	defer server.Close()
	mount, _ := serviceAccountMount(t, server)
	status, stdout, stderr := runInCluster(t, mount, "clickstack-dashboards", "--cluster", "--in-cluster")
	if status != 1 || stderr != "" || !strings.Contains(stdout, `"level":"ERROR"`) {
		t.Fatalf("%d %s %s", status, stdout, stderr)
	}
	for _, private := range []string{"private-uri", "private-topic", "private-secret"} {
		if strings.Contains(stdout, private) {
			t.Fatalf("a credential in the log: %s", stdout)
		}
	}
	want := []string{
		"GET /apis/helm.toolkit.fluxcd.io/v2/helmreleases?labelSelector=kustomize.toolkit.fluxcd.io%2Fnamespace%3Dflux-system",
		"GET /api/v1/namespaces?labelSelector=platform.swhurl.com%2Fapp",
		"GET /api/v1/namespaces/observability/secrets/clickstack-runtime-inputs?",
		"GET /api/v1/namespaces/observability/secrets/clickstack-mongodb-hyperdx-hyperdx?",
		"GET /api/v1/namespaces/console/secrets/notification-ntfy?",
		"GET /api/v1/namespaces/notify/secrets/notify-alerts?",
	}
	if len(api.paths) != len(want)+1 || !slices.Equal(api.paths[:len(want)], want) ||
		!strings.HasPrefix(api.paths[len(want)], "GET /api/v1/namespaces/observability/pods/clickstack-mongodb-0/exec?") {
		t.Fatalf("requests:\n%s", strings.Join(api.paths, "\n"))
	}
	if exec := api.paths[len(want)]; strings.Contains(exec, "private") {
		t.Fatalf("a credential in the exec request: %s", exec)
	}
}

// writeRecords is a records tree of one web app in staging and production, from the test release.
func writeRecords(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/r42.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, environment := range []string{"staging", "prod"} {
		folder := filepath.Join(root, "demo-orders", environment)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string][]byte{"release.json": raw, "instance.json": []byte("{}")} {
			if err := os.WriteFile(filepath.Join(folder, name), content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, "teams.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A records tree is read whole before anything is asked of the cluster: a release that is missing or not one stops
// the command without a request.
func TestClickStackDashboardsRecordsAreReadBeforeTheCluster(t *testing.T) {
	good := writeRecords(t)
	missing := writeRecords(t)
	if err := os.Remove(filepath.Join(missing, "demo-orders", "prod", "release.json")); err != nil {
		t.Fatal(err)
	}
	invalid := writeRecords(t)
	if err := os.WriteFile(filepath.Join(invalid, "demo-orders", "prod", "release.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	for _, root := range []string{missing, invalid, empty, filepath.Join(empty, "absent")} {
		api := &dashboardAPI{t: t}
		server := httptest.NewTLSServer(api)
		var out, diagnostic bytes.Buffer
		status := run([]string{"clickstack-dashboards", "--records", root, "--kubeconfig", kubeconfig(t, server)}, &out, &diagnostic, os.ReadFile, nil)
		server.Close()
		if status == 0 || len(api.paths) != 0 {
			t.Fatalf("%s: %d, %v\n%s%s", root, status, api.paths, out.String(), diagnostic.String())
		}
	}
	api := &dashboardAPI{t: t}
	server := httptest.NewTLSServer(api)
	defer server.Close()
	var out, diagnostic bytes.Buffer
	run([]string{"clickstack-dashboards", "--records", good, "--kubeconfig", kubeconfig(t, server), "--dry-run"}, &out, &diagnostic, os.ReadFile, nil)
	if len(api.paths) == 0 || !strings.Contains(api.paths[0], "/secrets/clickstack-runtime-inputs") {
		t.Fatalf("a good tree goes straight to the credentials: %v\n%s", api.paths, out.String())
	}
}
