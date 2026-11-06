package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/observe"
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

// verifyPlatform runs the platform scope against the test server and the checkout at the repository's root, with a
// registry and an object store that answer without a network.
func verifyPlatform(t *testing.T, server *httptest.Server, extra ...string) (int, string, string, *[]observe.ArchiveQuery) {
	t.Helper()
	listed := &[]observe.ArchiveQuery{}
	c := cli{readFile: readInput, writer: files.New(), encrypter: sops.New(), seed: randomSeed, inCluster: podServiceAccount(),
		registries: func(string) (registry.Reader, error) { return registrytest.New(registrytest.Scenario{}), nil },
		archives: func(bucket, region string) (s3.Reader, error) {
			if region != "" {
				t.Errorf("the region %q was stated: it is the bucket's own", region)
			}
			return listingStore{listed}, nil
		}}
	args := append([]string{"verify", "--scope", "platform", "--kubeconfig", kubeconfig(t, server), "--root", "../../..",
		"--json"}, extra...)
	var out, diagnostic bytes.Buffer
	status := runWith(args, &out, &diagnostic, c)
	return status, out.String(), diagnostic.String(), listed
}

// listingStore answers every listing with an empty prefix, and records it.
type listingStore struct{ listed *[]observe.ArchiveQuery }

func (l listingStore) List(_ context.Context, query observe.ArchiveQuery) (observe.ArchiveList, error) {
	*l.listed = append(*l.listed, query)
	return observe.ArchiveList{Query: query, Status: observe.Present, Objects: []observe.Archive{}}, nil
}
func (listingStore) Get(context.Context, observe.ArchiveQuery, string) ([]byte, error) {
	return nil, nil
}
func (listingStore) PresignURL(context.Context, observe.ArchiveQuery, string, time.Duration) (string, error) {
	return "", nil
}

// platformReport is the v2-platform-checks document's checks, by name.
func platformReport(t *testing.T, stdout string) map[string][]struct{ Level, Message string } {
	t.Helper()
	var report struct {
		Checks []struct {
			Name    string
			Entries []struct{ Level, Message string }
		}
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	checks := map[string][]struct{ Level, Message string }{}
	names := []string{}
	for _, check := range report.Checks {
		checks[check.Name] = check.Entries
		names = append(names, check.Name)
	}
	if strings.Join(names, " ") != "flux-units ingress alerts notify-service dashboards notifications image-automation "+
		"isolation signing-keys events egress channels backups sqlite-backups console push-webhook image-webhooks console-token" {
		t.Fatalf("the checks and their order: %v", names)
	}
	return checks
}

// The platform scope reads the checkout: the databases it backs up are the releases in Git that declare SQLite, each
// listed under its namespace's prefix of the profile's location, in the bucket's own region; a worker has none.
func TestVerifyPlatformReadsTheCheckout(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "example-secret")
	server, _ := platformAPI(t)
	status, stdout, diagnostic, listed := verifyPlatform(t, server, "--console-tag", "src-abc")
	if status != 1 {
		t.Fatalf("exit %d (the test server holds none of the scope's objects): %s", status, diagnostic)
	}
	checks := platformReport(t, stdout)
	databases := []string{}
	for _, entry := range checks["sqlite-backups"] {
		databases = append(databases, strings.SplitN(entry.Message, ":", 2)[0])
	}
	if !slices.Contains(databases, "demo-orders-prod") || slices.Contains(databases, "demo-shopper-staging") {
		t.Fatalf("databases %v", databases)
	}
	if len(*listed) != len(databases) || !strings.HasSuffix((*listed)[0].Prefix, "/"+databases[0]+"/") {
		t.Fatalf("listed %v for %v", *listed, databases)
	}
	if console := checks["console"]; console[len(console)-1].Message != "console image src-abc is built from the current tooling" {
		t.Fatalf("console %+v", console)
	}
	// The backup job is absent from the test server: missing, never a backup that is merely unlisted.
	if backups := checks["backups"]; !strings.Contains(backups[0].Message, "is missing") {
		t.Fatalf("backups %+v", backups)
	}
}

// Without credentials or the checkout's tag, the checks that need them say they were not supplied: a report, not a
// usage error.
func TestVerifyPlatformWithoutInputsSaysNotSupplied(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	server, _ := platformAPI(t)
	status, stdout, _, listed := verifyPlatform(t, server)
	if status != 1 || len(*listed) != 0 {
		t.Fatalf("exit %d, listed %v", status, *listed)
	}
	checks := platformReport(t, stdout)
	for _, entry := range checks["sqlite-backups"] {
		if entry.Level != "bad" || !strings.Contains(entry.Message, "AWS credentials not supplied") {
			t.Fatalf("%+v", entry)
		}
	}
	if console := checks["console"]; !strings.Contains(console[len(console)-1].Message, "content tag was not supplied") {
		t.Fatalf("console %+v", console)
	}
}

func TestBackupMaxAgeRejectsInvalidDurations(t *testing.T) {
	for _, stated := range []string{"NaN", "+Inf", "-Inf", "1e30", "0", "-1", "a day"} {
		t.Run(stated, func(t *testing.T) {
			t.Setenv("BACKUP_MAX_AGE_HOURS", stated)
			var diagnostic bytes.Buffer
			if _, status := (cli{diagnostic: &diagnostic}).backupMaxAge(); status != 2 {
				t.Fatalf("%q: exit %d, %s", stated, status, diagnostic.String())
			}
		})
	}
	for stated, want := range map[string]time.Duration{"": 26 * time.Hour, "1.5": 90 * time.Minute} {
		t.Setenv("BACKUP_MAX_AGE_HOURS", stated)
		var diagnostic bytes.Buffer
		if got, status := (cli{diagnostic: &diagnostic}).backupMaxAge(); status != 0 || got != want {
			t.Fatalf("%q: duration %s, exit %d, %s", stated, got, status, diagnostic.String())
		}
	}
}

// The scope decides what is needed: the platform scope needs a kubeconfig and a checkout and refuses the pod's
// ServiceAccount; the cluster scope compares with no checkout; there is no third scope.
func TestVerifyScopeUsage(t *testing.T) {
	for _, args := range [][]string{
		{"verify", "--scope", "platform", "--in-cluster", "--root", "."},
		{"verify", "--scope", "platform", "--kubeconfig", "k.json"},
		{"verify", "--scope", "platform", "--root", "."},
		{"verify", "--kubeconfig", "k.json", "--root", "."},
		{"verify", "--scope", "cluster", "--kubeconfig", "k.json", "--console-tag", "src-abc"},
		{"verify", "--scope", "everything", "--kubeconfig", "k.json"},
		{"verify", "--scope", "platform", "--scope", "platform", "--kubeconfig", "k.json", "--root", "."},
	} {
		if status, _, _ := runStatus(t, args...); status != 2 {
			t.Fatalf("%v: exit %d", args, status)
		}
	}
	server, _ := platformAPI(t)
	if status, _, diagnostic := runStatus(t, "verify", "--scope", "platform", "--kubeconfig", kubeconfig(t, server),
		"--root", "verify_test.go"); status != 2 || !strings.Contains(diagnostic, "is not a directory") {
		t.Fatalf("a root that is no directory: exit %d %s", status, diagnostic)
	}
	t.Setenv("BACKUP_MAX_AGE_HOURS", "a day")
	if status, _, diagnostic := runStatus(t, "verify", "--scope", "platform", "--kubeconfig", kubeconfig(t, server),
		"--root", "../../.."); status != 2 || !strings.Contains(diagnostic, "BACKUP_MAX_AGE_HOURS") {
		t.Fatalf("an unreadable age: exit %d %s", status, diagnostic)
	}
}

// The default scope never constructs the platform ports or consults GitHub, even with GH_TOKEN set.
func TestVerifyClusterNeverReadsGitHubOrData(t *testing.T) {
	t.Setenv("GH_TOKEN", "operator-fixture")
	server, paths := platformAPI(t)
	fake := githubtest.New()
	c := cli{readFile: readInput, inCluster: podServiceAccount(), github: fake}
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"verify", "--kubeconfig", kubeconfig(t, server), "--json"}, &out, &diagnostic, c)
	if status != 0 || len(fake.Calls) != 0 || len(*paths) != 8 {
		t.Fatalf("exit %d; GitHub calls %d; paths %v; %s", status, len(fake.Calls), *paths, diagnostic.String())
	}
	for _, path := range *paths {
		if strings.Contains(path, "/secrets") || strings.Contains(path, "/configmaps") {
			t.Fatalf("Data read: %s", path)
		}
	}
}

func TestVerifyMissingGitHubTokenReportsBadAndContinues(t *testing.T) {
	for _, token := range []string{"", " \t\n"} {
		t.Run(token, func(t *testing.T) {
			t.Setenv("GH_TOKEN", token)
			server, _ := platformAPI(t)
			status, stdout, diagnostic, _ := verifyPlatform(t, server, "--console-tag", "src-abc")
			if status != 1 {
				t.Fatalf("exit %d: %s", status, diagnostic)
			}
			checks := platformReport(t, stdout)
			for _, name := range []string{"push-webhook", "image-webhooks"} {
				if !slices.ContainsFunc(checks[name], func(e struct{ Level, Message string }) bool {
					return e.Level == "bad" && e.Message == "GitHub token not supplied"
				}) {
					t.Fatalf("%s: %+v", name, checks[name])
				}
			}
			if len(checks["console-token"]) == 0 || len(checks["sqlite-backups"]) == 0 || len(checks["notifications"]) == 0 {
				t.Fatal("unrelated checks stopped")
			}
		})
	}
}
