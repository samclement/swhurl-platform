package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// platformAPI answers the eight reads the platform checks make, as the API server writes them; a path in failing
// answers 403 instead.
func platformAPI(t *testing.T, failing ...string) (*httptest.Server, *[]string) {
	t.Helper()
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	cron := func(name string) map[string]any {
		return map[string]any{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": map[string]any{"name": name, "namespace": "console"},
			"spec": map[string]any{"suspend": false}, "status": map[string]any{"lastSuccessfulTime": recent}}
	}
	list := func(items ...any) map[string]any { return map[string]any{"items": items} }
	documents := map[string]map[string]any{
		"/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations": list(map[string]any{
			"metadata": map[string]any{"name": "flux-system", "namespace": "flux-system"},
			"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}),
		"/apis/notification.toolkit.fluxcd.io/v1beta3/namespaces/flux-system/alerts": list(map[string]any{
			"metadata": map[string]any{"name": "failures", "namespace": "flux-system"}, "spec": map[string]any{"providerRef": map[string]any{"name": "ntfy"}}}),
		"/apis/notification.toolkit.fluxcd.io/v1beta3/namespaces/flux-system/providers": list(map[string]any{
			"metadata": map[string]any{"name": "ntfy", "namespace": "flux-system"}}),
		"/apis/apps/v1/namespaces/kube-system/deployments/traefik": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "traefik", "namespace": "kube-system"},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
				"args": []any{"--entryPoints.web.http.redirections.entryPoint.scheme=https"}}}}}}},
		"/apis/apps/v1/namespaces/notify/deployments/notify": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "notify", "namespace": "notify"}, "status": map[string]any{"readyReplicas": 2},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
				"image": "ghcr.io/samclement/swhurl-console:src-abc@sha256:00"}}}}}},
		"/apis/helm.toolkit.fluxcd.io/v2/namespaces/console/helmreleases/console": map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind": "HelmRelease", "metadata": map[string]any{"name": "console", "namespace": "console"},
			"spec": map[string]any{"values": map[string]any{"controllers": map[string]any{"main": map[string]any{"containers": map[string]any{
				"main": map[string]any{"image": map[string]any{"tag": "src-abc"}}}}}}}},
		"/apis/batch/v1/namespaces/console/cronjobs/console-dashboards":    cron("console-dashboards"),
		"/apis/batch/v1/namespaces/console/cronjobs/console-notifications": cron("console-notifications"),
	}
	paths := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		document, found := documents[r.URL.Path]
		for _, path := range failing {
			if path == r.URL.Path {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		if !found || r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeDocument(t, w, document)
	}))
	t.Cleanup(server.Close)
	return server, &paths
}

func TestVerifyPassesAHealthyPlatform(t *testing.T) {
	server, paths := platformAPI(t)
	status, stdout, diagnostic := runStatus(t, "verify", "--kubeconfig", kubeconfig(t, server), "--json")
	if status != 0 {
		t.Fatalf("exit %d: %s%s", status, diagnostic, stdout)
	}
	var report struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
		Source  string `json:"source"`
		Passed  bool   `json:"passed"`
		Checks  []struct {
			Name    string `json:"name"`
			Entries []struct{ Level, Message string }
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if report.Kind != "v2-platform-checks" || report.Version != 1 || report.Source != "homelab" || !report.Passed || len(report.Checks) != 6 {
		t.Fatalf("%s", stdout)
	}
	if len(*paths) != 8 {
		t.Fatalf("eight reads, independent of the instances: %v", *paths)
	}
	for _, path := range *paths {
		if strings.Contains(path, "labelSelector") {
			t.Fatalf("a namespace collection is read whole: %s", path)
		}
	}
}

func TestVerifyFailsOnAFailedReadAfterPrintingTheReport(t *testing.T) {
	server, _ := platformAPI(t, "/apis/apps/v1/namespaces/kube-system/deployments/traefik")
	status, stdout, _ := runStatus(t, "verify", "--kubeconfig", kubeconfig(t, server), "--source", "laptop")
	if status != 1 {
		t.Fatalf("exit %d", status)
	}
	if !strings.Contains(stdout, "== Ingress ==\n[BAD] cannot read Traefik (kube-system/traefik) (forbidden)") ||
		!strings.Contains(stdout, "== Notifications ==\n[OK] notification checker succeeded in the last 5 minutes") {
		t.Fatalf("%s", stdout)
	}
}

func TestVerifyNeedsExactlyOneCredential(t *testing.T) {
	for _, args := range [][]string{{"verify"}, {"verify", "--kubeconfig", "k.json", "--in-cluster"}, {"verify", "--in-cluster", "extra"}} {
		if status, _, _ := runStatus(t, args...); status != 2 {
			t.Fatalf("%v: exit %d", args, status)
		}
	}
}
