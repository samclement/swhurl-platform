package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// waitAPI answers a wait's two reads: the platform's GitRepository at one revision, and the units in flux-system,
// each Ready at the revision applied names (failing at it with a message when that is "failed").
func waitAPI(t *testing.T, applied map[string]string) (*[]string, *httptest.Server) {
	t.Helper()
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/source.toolkit.fluxcd.io/v1/namespaces/flux-system/gitrepositories/swhurl-platform":
			fmt.Fprint(w, `{"apiVersion":"source.toolkit.fluxcd.io/v1","kind":"GitRepository",`+
				`"metadata":{"name":"swhurl-platform","namespace":"flux-system"},"status":{"artifact":{"revision":"main@sha1:new"}}}`)
		case "/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations":
			items := []string{}
			for _, name := range []string{"a", "b"} {
				ready, revision := `"status":"True"`, applied[name]
				if revision == "failed" {
					ready, revision = `"status":"False","reason":"BuildFailed","message":"kustomize build failed"`, "old"
				}
				items = append(items, fmt.Sprintf(`{"apiVersion":"kustomize.toolkit.fluxcd.io/v1","kind":"Kustomization",`+
					`"metadata":{"name":%q,"namespace":"flux-system","generation":1},`+
					`"spec":{"sourceRef":{"kind":"GitRepository","name":"swhurl-platform"}},`+
					`"status":{"observedGeneration":1,"lastAppliedRevision":%q,"lastAttemptedRevision":"main@sha1:new",`+
					`"conditions":[{"type":"Ready",%s}]}}`, name, revision, ready))
			}
			fmt.Fprintf(w, `{"apiVersion":"kustomize.toolkit.fluxcd.io/v1","kind":"KustomizationList","items":[%s]}`,
				strings.Join(items, ","))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return &requests, server
}

func runWait(t *testing.T, server *httptest.Server) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"flux-wait", "--kubeconfig", kubeconfig(t, server)}, &out, &diagnostic,
		cli{readFile: os.ReadFile, sleep: func(time.Duration) {}})
	return status, out.String(), diagnostic.String()
}

// Every unit Ready at the fetched revision: a heading and one [OK] line each, read through the API server.
func TestFluxWaitReadsTheSourceAndTheUnits(t *testing.T) {
	requests, server := waitAPI(t, map[string]string{"a": "main@sha1:new", "b": "main@sha1:new"})
	status, out, diagnostic := runWait(t, server)
	if status != 0 || diagnostic != "" {
		t.Fatalf("exit %d, stderr %q", status, diagnostic)
	}
	if !strings.HasPrefix(out, "\n== Flux units at main@sha1:new ==\n[OK] a (") || !strings.Contains(out, "\n[OK] b (") {
		t.Fatalf("stdout %q", out)
	}
	for _, request := range *requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("a wait only reads, but made %s", request)
		}
	}
}

// A unit failing at the revision is [BAD] with its message and exit 1.
func TestFluxWaitExitsOneAtAFailedUnit(t *testing.T) {
	_, server := waitAPI(t, map[string]string{"a": "main@sha1:new", "b": "failed"})
	status, out, _ := runWait(t, server)
	if status != 1 || !strings.HasSuffix(out, "[BAD] b failed: kustomize build failed\n") {
		t.Fatalf("exit %d, stdout %q", status, out)
	}
}

// A read that fails is an error on standard error, not a verdict.
func TestFluxWaitReportsAnUnreadableSource(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	status, out, diagnostic := runWait(t, server)
	if status != 1 || out != "" || !strings.HasPrefix(diagnostic, "[ERROR] could not read Flux state: ") {
		t.Fatalf("exit %d, stdout %q, stderr %q", status, out, diagnostic)
	}
}

// The one credential is a kubeconfig; without it the usage is printed.
func TestFluxWaitNeedsAKubeconfig(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if status := runWith([]string{"flux-wait"}, &out, &diagnostic, cli{readFile: os.ReadFile}); status != 2 {
		t.Fatalf("exit %d, stderr %q", status, diagnostic.String())
	}
}
