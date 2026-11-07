package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/incident"
)

// checkerAPI is an RBAC-shaped API endpoint: state supports named GET/PATCH only, never a ConfigMap list, and
// collections permit exactly the kinds this pass needs. Any post, Secret, exec or unused workload read fails.
type checkerAPI struct {
	t            *testing.T
	state        string
	methods      []string
	paths        []string
	patchFail    bool
	snapshotFail bool
}

func (a *checkerAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.methods = append(a.methods, r.Method)
	a.paths = append(a.paths, r.URL.Path)
	switch r.URL.Path {
	case "/api/v1/namespaces/console/configmaps/notification-state":
		if r.Method == http.MethodPatch {
			if a.patchFail {
				w.WriteHeader(403)
				return
			}
			var patch struct{ Data map[string]string }
			if r.URL.Query().Get("fieldManager") != incident.FieldManager || json.NewDecoder(r.Body).Decode(&patch) != nil {
				a.t.Error("invalid state patch")
			}
			a.state = patch.Data[incident.StateKey]
		} else if r.Method != http.MethodGet {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]string{"name": "notification-state", "namespace": "console"}, "data": map[string]string{incident.StateKey: a.state, "unrelated": "keep"}})
	case "/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations", "/apis/helm.toolkit.fluxcd.io/v2/helmreleases", "/apis/apps/v1/deployments", "/apis/apps/v1/statefulsets", "/api/v1/pods":
		if r.Method != "GET" || r.URL.RawQuery != "" {
			a.t.Error("collection not unfiltered GET")
		}
		if a.snapshotFail {
			w.WriteHeader(403)
			return
		}
		fmt.Fprint(w, `{"items":[]}`)
	case "/api/v1/events":
		if r.Method != "GET" || r.URL.Query().Get("labelSelector") != "" {
			a.t.Error("Event read used labels")
		}
		selector := r.URL.Query().Get("fieldSelector")
		if selector != "involvedObject.kind=HelmRelease" && selector != "involvedObject.kind=Kustomization" {
			a.t.Error("Event read unfiltered")
		}
		fmt.Fprint(w, `{"items":[]}`)
	default:
		a.t.Errorf("unexpected read/effect: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(403)
	}
}
func checkerEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CONSOLE_URL", "https://console.example.test/")
	t.Setenv("NTFY_PLATFORM_URL", "")
	t.Setenv("SWHURL_NOTIFY_URL", "")
}
func checkerPendingState() string {
	return `{"version":1,"started":1791028800.0,"last_success":0,"instances":{},"seen":{},"monitor":{},"pending":[{"key":"one","title":"A pending notice","message":"m","priority":4,"queued":1791028800.0}]}`
}
func TestNotificationsCheckCLIFlags(t *testing.T) {
	for _, args := range [][]string{
		{"notifications-check"}, {"notifications-check", "--kubeconfig", "x", "--in-cluster"}, {"notifications-check", "--in-cluster", "--dry-run", "--dry-run"}, {"notifications-check", "--kubeconfig", ""}, {"notifications-check", "--in-cluster", "extra"},
	} {
		if _, outcome := parse(args); outcome != misuse {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"notifications-check", "--kubeconfig", "x", "--dry-run"}, {"notifications-check", "--in-cluster"}} {
		if _, outcome := parse(args); outcome != parsed {
			t.Fatalf("refused %v", args)
		}
	}
}
func TestNotificationsCheckDryRunBothCredentials(t *testing.T) {
	checkerEnvironment(t)
	for _, pod := range []bool{false, true} {
		t.Run(fmt.Sprint(pod), func(t *testing.T) {
			api := &checkerAPI{t: t, state: checkerPendingState()}
			server := httptest.NewTLSServer(api)
			defer server.Close()
			before := api.state
			var status int
			var stdout, stderr string
			if pod {
				mount, _ := serviceAccountMount(t, server)
				status, stdout, stderr = runInCluster(t, mount, "notifications-check", "--in-cluster", "--dry-run")
			} else {
				var out, diagnostic bytes.Buffer
				status = run([]string{"notifications-check", "--kubeconfig", kubeconfig(t, server), "--dry-run"}, &out, &diagnostic, os.ReadFile, nil)
				stdout, stderr = out.String(), diagnostic.String()
			}
			if status != 0 || stderr != "" || !strings.Contains(stdout, "Would notify: A pending notice (priority 4)") {
				t.Fatalf("dry-run: %d %s %s", status, stdout, stderr)
			}
			if api.state != before || len(api.methods) != 8 {
				t.Fatalf("dry-run changed state/read extra data: %v", api.methods)
			}
			for _, method := range api.methods {
				if method != "GET" {
					t.Fatalf("dry-run effect: %s", method)
				}
			}
			for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
				var record map[string]any
				if json.Unmarshal([]byte(line), &record) != nil || record["logger"] != "notifications-check" {
					t.Fatalf("not checker JSON log: %s", line)
				}
			}
		})
	}
}
func TestNotificationsCheckRealPassAndFailures(t *testing.T) {
	checkerEnvironment(t)
	t.Setenv("NTFY_PLATFORM_URL", "https://ntfy.example.test/synthetic-private-topic")
	for _, scenario := range []string{"success", "snapshot", "state", "patch"} {
		t.Run(scenario, func(t *testing.T) {
			api := &checkerAPI{t: t, patchFail: scenario == "patch", snapshotFail: scenario == "snapshot"}
			if scenario == "state" {
				api.state = `{"version":999}`
			}
			server := httptest.NewTLSServer(api)
			defer server.Close()
			mount, _ := serviceAccountMount(t, server)
			status, stdout, stderr := runInCluster(t, mount, "notifications-check", "--in-cluster")
			want := 1
			if scenario == "success" {
				want = 0
			}
			if status != want || stderr != "" {
				t.Fatalf("pass %s: %d %s %s", scenario, status, stdout, stderr)
			}
			if strings.Contains(stdout, "synthetic-private-topic") {
				t.Fatal("destination in log")
			}
			if scenario == "state" && len(api.paths) != 1 {
				t.Fatal("invalid state did not stop snapshot reads")
			}
			if scenario == "success" && !strings.Contains(api.state, `"last_success":`) {
				t.Fatal("pass not saved")
			}
		})
	}
}
func TestCheckerExplicitEnvironmentAndFreshToken(t *testing.T) {
	checkerEnvironment(t)
	for _, base := range []string{"", "http://user:private@console.example.test/", "not-a-url"} {
		t.Setenv("CONSOLE_URL", base)
		var out, diagnostic bytes.Buffer
		status := run([]string{"notifications-check", "--in-cluster", "--dry-run"}, &out, &diagnostic, func(string) ([]byte, error) { t.Fatal("read before validating environment"); return nil, nil }, nil)
		if status != 1 || !strings.Contains(out.String(), "CONSOLE_URL") || strings.Contains(out.String(), "private") {
			t.Fatalf("invalid base: %d %s", status, out.String())
		}
	}
	t.Setenv("CONSOLE_URL", "https://console.example.test")
	var out, diagnostic bytes.Buffer
	if status := run([]string{"notifications-check", "--in-cluster"}, &out, &diagnostic, os.ReadFile, nil); status != 1 || !strings.Contains(out.String(), "NTFY_PLATFORM_URL") {
		t.Fatal("missing topic not refused")
	}
	empty := func(string) string { return "" }
	if checkerTokenPath(empty) != "/var/run/swhurl/tokens/notify/token" {
		t.Fatal("default token mount changed")
	}
	path := filepath.Join(t.TempDir(), "token")
	getenv := func(key string) string {
		if key == "SWHURL_NOTIFY_TOKEN_FILE" {
			return path
		}
		return ""
	}
	for _, token := range []string{"first", "rotated"} {
		if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if checkerToken(getenv) != token {
			t.Fatal("token not read fresh")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if checkerToken(getenv) != "" {
		t.Fatal("missing token fabricated")
	}
}

// Read the current policy text only; YAML parsing stays with the repository's existing policy checks. These
// assertions freeze the exact relevant ownership/permission/alert-selector lines rather than introduce a parser.
func TestCheckerStateRBACAndSourceAlertWiring(t *testing.T) {
	read := func(path string) string {
		raw, err := os.ReadFile("../../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	state := read("platform/console/notification-state.yaml")
	if strings.Contains(state, "\ndata:") || !strings.Contains(state, "kustomize.toolkit.fluxcd.io/ssa: Merge") {
		t.Fatal("Flux owns incident state data")
	}
	rbac := read("platform/console/notification-rbac.yaml")
	if !strings.Contains(rbac, "resourceNames: [notification-state]\n    verbs: [get, patch]") {
		t.Fatal("state permissions no longer named get/patch only")
	}
	cluster := strings.Split(strings.Split(rbac, "kind: ClusterRole\n")[1], "---")[0]
	if strings.Contains(cluster, "secrets") || strings.Contains(cluster, "exec") || strings.Count(cluster, "verbs: [list]") != 5 {
		t.Fatal("checker read role gained other effects")
	}
	alerts := read("platform/alerts/alerts.yaml")
	if strings.Count(alerts, "kind: Alert\n") != 1 || !strings.Contains(alerts, "- kind: Kustomization\n      name: '*'\n      matchLabels:\n        platform.swhurl.com/alert: failures") {
		t.Fatal("source alerts duplicate checker scope")
	}
	if strings.Contains(alerts, "production-alert") {
		t.Fatal("obsolete promotion alert returned")
	}
	// The retained Python wiring case checks every unit; the Go regression proves current app units are unlabelled.
	paths, err := filepath.Glob("../../../clusters/home/app-*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("platform.swhurl.com/alert")) {
			t.Fatalf("app unit duplicated by source alert: %s", path)
		}
	}
}

func TestCheckerEveryFluxUnitHasTheExpectedAlertOwnership(t *testing.T) {
	paths, err := filepath.Glob("../../../clusters/home/*")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, "../../../clusters/home/flux-system/kustomizations.yaml")
	count := 0
	for _, path := range paths {
		if !strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range strings.Split(string(raw), "\n---\n") {
			if strings.HasPrefix(strings.TrimSpace(doc), "{") {
				var unit struct {
					APIVersion, Kind string
					Metadata         struct {
						Name   string
						Labels map[string]string
					}
				}
				if err := json.Unmarshal([]byte(doc), &unit); err != nil {
					t.Fatal(err)
				}
				if unit.APIVersion != "kustomize.toolkit.fluxcd.io/v1" || unit.Kind != "Kustomization" {
					continue
				}
				want := "failures"
				if strings.HasPrefix(unit.Metadata.Name, "app-") || unit.Metadata.Name == "platform-console" {
					want = ""
				}
				if unit.Metadata.Labels["platform.swhurl.com/alert"] != want {
					t.Fatalf("wrong alert owner: %s", unit.Metadata.Name)
				}
			} else {
				if !strings.Contains(doc, "apiVersion: kustomize.toolkit.fluxcd.io/v1\n") || !strings.Contains(doc, "kind: Kustomization\n") {
					continue
				}
				console := strings.Contains(doc, "\n  name: platform-console\n")
				app := strings.Contains(doc, "\n  name: app-")
				if strings.Contains(doc, "platform.swhurl.com/alert: failures") == (console || app) {
					t.Fatalf("wrong alert owner in %s: %s", path, doc)
				}
			}
			count++
		}
	}
	if count < 10 {
		t.Fatal("unit inventory was not exercised")
	}
	paths, err = filepath.Glob("../../../apps/*/prod/kustomization.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("production-alert.yaml")) {
			t.Fatal("obsolete promotion alert returned")
		}
	}
}
