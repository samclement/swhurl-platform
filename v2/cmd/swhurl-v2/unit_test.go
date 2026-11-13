package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// unitAPI answers one unit action's reads and patches as a Flux controller does: a patched object reports the
// request handled and Ready on its next read, so the command's wait never sleeps.
type unitAPI struct {
	mu       sync.Mutex
	spec     string
	tokens   map[string]string
	patches  []string
	requests []string
}

func newUnitAPI(t *testing.T, spec string) (*unitAPI, *httptest.Server) {
	t.Helper()
	api := &unitAPI{spec: spec, tokens: map[string]string{}}
	server := httptest.NewTLSServer(http.HandlerFunc(api.answer))
	t.Cleanup(server.Close)
	return api, server
}

func (a *unitAPI) answer(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, r.Method+" "+r.URL.Path)
	if r.Method == http.MethodPatch {
		body, _ := io.ReadAll(r.Body)
		a.patches = append(a.patches, string(body))
		var change struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if json.Unmarshal(body, &change) == nil {
			if token := change.Metadata.Annotations[fluxRequestedAt()]; token != "" {
				a.tokens[r.URL.Path] = token
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
		return
	}
	status := map[string]any{"observedGeneration": 3, "lastAppliedRevision": "0123456789abcdef",
		"artifact":   map[string]any{"revision": "0123456789abcdef"},
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "message": ""}}}
	if token := a.tokens[r.URL.Path]; token != "" {
		status["lastHandledReconcileAt"] = token
	}
	// A read identifies the object it names: the adapter refuses a document that is not the one it asked for.
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	kind, name, namespace := "GitRepository", parts[len(parts)-1], parts[len(parts)-3]
	version := "source.toolkit.fluxcd.io/v1"
	switch parts[len(parts)-2] {
	case "kustomizations":
		kind, version = "Kustomization", "kustomize.toolkit.fluxcd.io/v1"
	case "helmreleases":
		kind, version = "HelmRelease", "helm.toolkit.fluxcd.io/v2"
		status["lastAttemptedRevision"] = "1.2.3"
	}
	document := map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{"name": name, "namespace": namespace, "generation": 3}, "status": status}
	if kind == "Kustomization" {
		document["spec"] = json.RawMessage(a.spec)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(document)
}

// fluxRequestedAt is the annotation a request is made with; the command's own constant is unexported there.
func fluxRequestedAt() string { return "reconcile.fluxcd.io/requestedAt" }

// unitMount is the pod ServiceAccount that reaches one test server.
func unitMount(t *testing.T, server *httptest.Server) inCluster {
	t.Helper()
	mount, _ := serviceAccountMount(t, server)
	return mount
}

// A reconcile reads the unit, requests its source, then the unit, and prints what each fetched or applied.
func TestUnitReconcileThroughTheAPIServer(t *testing.T) {
	api, server := newUnitAPI(t, `{"suspend": false, "sourceRef": {"kind": "GitRepository", "name": "swhurl-platform"}}`)
	status, out, diagnostic := runInCluster(t, unitMount(t, server), "unit", "--action", "reconcile", "--unit", "gw", "--in-cluster")
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	want := strings.Join([]string{
		"► annotating GitRepository swhurl-platform in flux-system namespace",
		"◎ waiting for GitRepository reconciliation",
		"✔ fetched revision 0123456789abcdef",
		"► annotating Kustomization gw in flux-system namespace",
		"◎ waiting for Kustomization reconciliation",
		"✔ applied revision 0123456789abcdef",
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("stdout %q, want %q", out, want)
	}
	root := "/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations/gw"
	source := "/apis/source.toolkit.fluxcd.io/v1/namespaces/flux-system/gitrepositories/swhurl-platform"
	if strings.Join(api.requests, "\n") != strings.Join([]string{
		"GET " + root, "PATCH " + source, "GET " + source, "PATCH " + root, "GET " + root}, "\n") {
		t.Fatalf("requests %v", api.requests)
	}
	if len(api.patches) != 2 || !strings.Contains(api.patches[0], "reconcile.fluxcd.io/requestedAt") ||
		!strings.Contains(api.patches[1], "reconcile.fluxcd.io/requestedAt") {
		t.Fatalf("patches %v", api.patches)
	}
}

// A root unit is refused in the console's words, before anything reaches the cluster.
func TestUnitRefusesARootUnitWithoutARequest(t *testing.T) {
	api, server := newUnitAPI(t, `{}`)
	status, out, diagnostic := runInCluster(t, unitMount(t, server), "unit", "--action", "reconcile", "--unit", "cluster-stack", "--in-cluster")
	if status != 2 || out != "" || !strings.Contains(diagnostic, "cluster-stack is applied by make flux-bootstrap, not by Flux") {
		t.Fatalf("exit %d, stdout %q, stderr %q", status, out, diagnostic)
	}
	if len(api.requests) != 0 {
		t.Fatalf("a refused unit made %v", api.requests)
	}
}

// An action the console does not have is refused as a command-line mistake, not guessed.
func TestUnitRefusesAnUnknownAction(t *testing.T) {
	api, server := newUnitAPI(t, `{}`)
	status, _, diagnostic := runInCluster(t, unitMount(t, server), "unit", "--action", "delete", "--unit", "gw", "--in-cluster")
	if status != 2 || !strings.Contains(diagnostic, `invalid: action: "delete" is not reconcile, suspend or resume`) {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	if len(api.requests) != 0 {
		t.Fatalf("a refused action made %v", api.requests)
	}
}

// Exactly one credential and a unit are required, and the usage is printed rather than guessed.
func TestUnitNeedsOneCredentialAndAUnit(t *testing.T) {
	_, server := newUnitAPI(t, `{}`)
	for _, args := range [][]string{
		{"unit", "--action", "reconcile", "--unit", "gw"},
		{"unit", "--unit", "gw", "--in-cluster"},
		{"unit", "--action", "reconcile", "--in-cluster"},
		{"unit", "--action", "suspend", "--unit", "gw", "--target", "kustomization/gw", "--in-cluster"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			status, out, diagnostic := runInCluster(t, unitMount(t, server), args...)
			if status != 2 || out != "" || !strings.Contains(diagnostic, "usage") {
				t.Fatalf("exit %d, stdout %q, stderr %q", status, out, diagnostic)
			}
		})
	}
}

// make suspend's target names a unit or one HelmRelease; a release is patched and waited for in its own namespace.
func TestUnitTargetsAUnitOrARelease(t *testing.T) {
	api, server := newUnitAPI(t, `{}`)
	status, out, diagnostic := runInCluster(t, unitMount(t, server), "unit", "--action", "suspend", "--target", "kustomization/gw", "--in-cluster")
	if status != 0 || out != "► suspending Kustomization gw\n✔ Kustomization gw suspended\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", status, out, diagnostic)
	}
	release := "/apis/helm.toolkit.fluxcd.io/v2/namespaces/ns/helmreleases/r"
	api.requests = nil
	status, out, diagnostic = runInCluster(t, unitMount(t, server), "unit", "--action", "resume", "--target", "helmrelease/ns/r", "--in-cluster")
	want := strings.Join([]string{
		"► resuming HelmRelease r in ns namespace",
		"► annotating HelmRelease r in ns namespace",
		"◎ waiting for HelmRelease reconciliation",
		"✔ applied revision 1.2.3",
	}, "\n") + "\n"
	if status != 0 || out != want {
		t.Fatalf("exit %d, stdout %q, stderr %q", status, out, diagnostic)
	}
	if strings.Join(api.requests, "\n") != strings.Join([]string{"PATCH " + release, "PATCH " + release, "GET " + release}, "\n") {
		t.Fatalf("requests %v", api.requests)
	}
}

// A target of another shape, or a name the API cannot hold, is refused before anything reaches the cluster.
func TestUnitRefusesAMalformedTargetWithoutARequest(t *testing.T) {
	api, server := newUnitAPI(t, `{}`)
	for target, message := range map[string]string{
		"pvc/ns/c":           "use kustomization/<name> or helmrelease/<namespace>/<name>",
		"kustomization/":     "use kustomization/<name>",
		"kustomization/a/b":  "use kustomization/<name>",
		"helmrelease/r":      "use helmrelease/<namespace>/<name>",
		"helmrelease//r":     "use helmrelease/<namespace>/<name>",
		"helmrelease/ns/r/x": "use helmrelease/<namespace>/<name>",
		"kustomization/-A":   "refused_request: resource: invalid_name",
		"helmrelease/-n/r":   "refused_request: resource: invalid_namespace",
		"helmrelease/ns/-r":  "refused_request: resource: invalid_name",
	} {
		status, _, diagnostic := runInCluster(t, unitMount(t, server), "unit", "--action", "resume", "--target", target, "--in-cluster")
		if status != 2 || !strings.Contains(diagnostic, message) {
			t.Fatalf("%s: exit %d: %s", target, status, diagnostic)
		}
	}
	if len(api.requests) != 0 {
		t.Fatalf("a refused target made %v", api.requests)
	}
}
