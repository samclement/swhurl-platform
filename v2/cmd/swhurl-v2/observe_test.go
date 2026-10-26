package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// The revision the observation expects Flux to have applied, and the digest the compiled prod release
// pins. They are stated here, not derived, so the test says what the command must read.
const (
	observedRevision = "9f1c0e2d34b7a5c86e4f0d2b9a17c3e5d8b6f04a"
	compiledDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	selector         = "app.kubernetes.io/instance=demo-orders,app.kubernetes.io/controller=main"
)

// api is the server-side scenario: the documents for one instance, and the requests that were made.
type api struct {
	requests  []*http.Request
	status    map[string]int // path → status override, for the failure cases
	namespace string         // the instance's namespace; prod's by default
	digest    string         // the artifact the compiled release pins; the prod fixture's by default
	backup    bool           // whether the instance renders a backup CronJob
}

// serve answers the four paths one observation reads, in the shape the API server writes them.
func (a *api) serve(t *testing.T) *httptest.Server {
	t.Helper()
	namespace := a.namespace
	if namespace == "" {
		namespace = "demo-orders-prod"
	}
	digest := a.digest
	if digest == "" {
		digest = compiledDigest
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests = append(a.requests, r)
		if status, overridden := a.status[r.URL.Path]; overridden {
			w.WriteHeader(status)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations/app-" + namespace:
			writeDocument(t, w, flux("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "app-"+namespace, "flux-system", map[string]any{
				"lastAppliedRevision": "main@sha1:" + observedRevision,
			}))
		case "/apis/helm.toolkit.fluxcd.io/v2/namespaces/" + namespace + "/helmreleases/demo-orders":
			writeDocument(t, w, flux("helm.toolkit.fluxcd.io/v2", "HelmRelease", "demo-orders", namespace, nil))
		case "/apis/apps/v1/namespaces/" + namespace + "/deployments/demo-orders":
			writeDocument(t, w, map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": "demo-orders", "namespace": namespace, "generation": 4},
				"status": map[string]any{"observedGeneration": 4, "availableReplicas": 1,
					"conditions": []any{map[string]any{"type": "Available", "status": "True"}}},
			})
		case "/api/v1/namespaces/" + namespace + "/pods":
			switch r.URL.Query().Get("labelSelector") {
			case selector:
				writeDocument(t, w, map[string]any{"kind": "PodList", "apiVersion": "v1",
					"items": []any{pod(namespace, "docker.io/example/demo-orders@"+digest)}})
			case backupSelector:
				writeDocument(t, w, map[string]any{"kind": "PodList", "apiVersion": "v1",
					"items": []any{backupPod(namespace)}})
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/apis/batch/v1/namespaces/" + namespace + "/cronjobs/demo-orders-backup":
			if !a.backup {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeDocument(t, w, backupCronJob(namespace))
		case "/apis/batch/v1/namespaces/" + namespace + "/jobs/demo-orders-backup-29345678":
			if !a.backup {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeDocument(t, w, backupRun(namespace))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// backupSelector is the label the compiled backup job and its runs carry.
const backupSelector = "platform.swhurl.com/backup=true"

// backupCronJob is a database instance's backup job, as the cluster holds it: scheduled, and last run
// finishing successfully two hours ago.
func backupCronJob(namespace string) map[string]any {
	twoHoursAgo := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	return map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]any{"name": "demo-orders-backup", "namespace": namespace,
			"labels": map[string]any{"platform.swhurl.com/backup": "true"}},
		"spec":   map[string]any{"schedule": "58 3 * * *", "suspend": false},
		"status": map[string]any{"lastScheduleTime": twoHoursAgo, "lastSuccessfulTime": twoHoursAgo},
	}
}

// backupPod is the pod that run created: its owner reference names the run's Job, which is how the
// observation reaches a Job the CronJob's labels cannot select.
func backupPod(namespace string) map[string]any {
	twoHoursAgo := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "demo-orders-backup-29345678-x7k2p", "namespace": namespace,
			"creationTimestamp": twoHoursAgo,
			"ownerReferences":   []any{map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": "demo-orders-backup-29345678"}}},
	}
}

// backupRun is the Job that run created.
func backupRun(namespace string) map[string]any {
	twoHoursAgo := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	return map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": "demo-orders-backup-29345678", "namespace": namespace,
			"creationTimestamp": twoHoursAgo},
		"status": map[string]any{"completionTime": twoHoursAgo, "succeeded": 1,
			"conditions": []any{map[string]any{"type": "Complete", "status": "True"}}},
	}
}

// flux is a Flux object document: a Ready condition about the generation it observed.
func flux(apiVersion, kind, name, namespace string, status map[string]any) map[string]any {
	if status == nil {
		status = map[string]any{}
	}
	status["conditions"] = []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 2}}
	return map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": namespace, "generation": 2},
		"status":   status,
	}
}

// pod is the one pod that runs the instance's container, already pulled.
func pod(namespace, image string) map[string]any {
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "demo-orders-abc", "namespace": namespace},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "main", "image": image}}},
		"status": map[string]any{"containerStatuses": []any{
			map[string]any{"name": "main", "image": image, "imageID": "docker-pullable://" + image}}},
	}
}

// writeDocument answers with one JSON document, as the API server does.
func writeDocument(t *testing.T, w http.ResponseWriter, document map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(document); err != nil {
		t.Errorf("writing the answer: %v", err)
	}
}

// kubeconfig names the test server as the current context, trusting its certificate. It is the JSON
// form the operator's own tool prints; nothing here is a real credential.
func kubeconfig(t *testing.T, server *httptest.Server) string {
	t.Helper()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	encoded := base64.StdEncoding.EncodeToString(certificate)
	document := map[string]any{
		"kind": "Config", "apiVersion": "v1", "current-context": "homelab",
		"clusters": []any{map[string]any{"name": "homelab", "cluster": map[string]any{
			"server": server.URL, "certificate-authority-data": encoded}}},
		"users": []any{map[string]any{"name": "operator", "user": map[string]any{"token": "test-token"}}},
		"contexts": []any{map[string]any{"name": "homelab", "context": map[string]any{
			"cluster": "homelab", "user": "operator"}}},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// observeArgs is the command line for the compiled prod instance.
func observeArgs(t *testing.T, kubeconfigPath string, extra ...string) []string {
	return observeArgsOf(t, kubeconfigPath, "prod.json", "r41.json", extra...)
}

// observeArgsOf is the command line for one compiled fixture instance, with its documents written beside
// each other.
func observeArgsOf(t *testing.T, kubeconfigPath, instance, release string, extra ...string) []string {
	t.Helper()
	dir := t.TempDir()
	paths := map[string][]byte{
		"instance.json": fixture(t, instance),
		"release.json":  fixture(t, release),
		"profile.json":  fixture(t, "profile.json"),
	}
	written := map[string]string{}
	for name, data := range paths {
		written[name] = filepath.Join(dir, name)
		if err := os.WriteFile(written[name], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"observe", "--instance", written["instance.json"], "--release", written["release.json"],
		"--profile", written["profile.json"], "--revision", observedRevision, "--kubeconfig", kubeconfigPath}
	return append(args, extra...)
}

// runObserve runs the command and returns its exit status and streams.
func runObserve(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := run(args, &out, &diagnostic, os.ReadFile, files.New())
	return status, out.String(), diagnostic.String()
}

// document decodes the report a successful observation writes.
func document(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, stdout)
	}
	return report
}

// finding is one finding of a decoded report.
func finding(t *testing.T, report map[string]any, field string) map[string]any {
	t.Helper()
	for _, raw := range report["findings"].([]any) {
		entry := raw.(map[string]any)
		if entry["field"] == field {
			return entry
		}
	}
	t.Fatalf("no finding for %s", field)
	return nil
}

// The recorded instance is running: every fact positive, and the report says what it read and when.
func TestObserveReportsTheRunningInstance(t *testing.T) {
	api := &api{}
	server := api.serve(t)
	status, stdout, stderr := runObserve(t, observeArgs(t, kubeconfig(t, server))...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	if report["kind"] != "v2-observation" || report["version"] != float64(2) {
		t.Fatalf("the report envelope is %v/%v", report["kind"], report["version"])
	}
	if report["verdict"] != string(observe.IsRunning) {
		t.Fatalf("verdict %v, findings %v", report["verdict"], report["findings"])
	}
	if report["source"] != "homelab" || report["observedAt"] == "" || len(report["notCovered"].([]any)) == 0 {
		t.Fatalf("the report does not identify its evidence: %v", report)
	}
	if len(report["findings"].([]any)) != 7 || len(report["unassessed"].([]any)) != 0 {
		t.Fatalf("findings %v, unassessed %v", report["findings"], report["unassessed"])
	}
	target := report["target"].(map[string]any)
	if target["namespace"] != "demo-orders-prod" || target["digest"] != compiledDigest {
		t.Fatalf("target %v", target)
	}
	if !strings.Contains(stderr, "verdict running") {
		t.Fatalf("summary %q", stderr)
	}
	// A stateless instance renders no backup, and the report says so rather than leaving a gap.
	if backup := report["backup"].(map[string]any); backup["state"] != observe.BackupNotApplicable {
		t.Fatalf("backup %v", backup)
	}
	// The four reads went to the four stated paths, with the token, and the list asked for this
	// instance's pods.
	if len(api.requests) != 4 {
		t.Fatalf("%d requests", len(api.requests))
	}
	for _, request := range api.requests {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("a request carried no token: %s", request.URL.Path)
		}
	}
}

// A read that fails leaves the fact it answers unassessed, not absent: the verdict is unknown, and the
// command still produced a report.
func TestObserveLeavesAFailedReadUnknown(t *testing.T) {
	api := &api{status: map[string]int{
		"/apis/apps/v1/namespaces/demo-orders-prod/deployments/demo-orders": http.StatusForbidden,
	}}
	server := api.serve(t)
	status, stdout, stderr := runObserve(t, observeArgs(t, kubeconfig(t, server))...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	if report["verdict"] != string(observe.Unknown) {
		t.Fatalf("verdict %v", report["verdict"])
	}
	for _, field := range []string{"workload", "ready", "replicas"} {
		entry := finding(t, report, field)
		if entry["outcome"] == string(observe.Absent) {
			t.Fatalf("%s read a failed request as absence: %v", field, entry)
		}
	}
	if ready := finding(t, report, "ready"); ready["outcome"] != string(observe.NotAssessed) || ready["reason"] != observe.ReasonWorkloadDown {
		t.Fatalf("ready: %v", ready)
	}
}

// A successful answer that the object is not there is absence, and it decides the verdict.
func TestObserveReportsAbsence(t *testing.T) {
	api := &api{status: map[string]int{
		"/apis/apps/v1/namespaces/demo-orders-prod/deployments/demo-orders": http.StatusNotFound,
	}}
	server := api.serve(t)
	status, stdout, stderr := runObserve(t, observeArgs(t, kubeconfig(t, server))...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	if report["verdict"] != string(observe.IsNotRunning) {
		t.Fatalf("verdict %v", report["verdict"])
	}
	if workload := finding(t, report, "workload"); workload["outcome"] != string(observe.Absent) || workload["reason"] != observe.ReasonNotFound {
		t.Fatalf("workload: %v", workload)
	}
}

// A source can be named explicitly, so a report says which evidence it is talking about.
func TestObserveNamesTheSource(t *testing.T) {
	server := (&api{}).serve(t)
	_, stdout, stderr := runObserve(t, observeArgs(t, kubeconfig(t, server), "--source", "homelab-prod")...)
	if document(t, stdout)["source"] != "homelab-prod" {
		t.Fatalf("source: %v; %s", document(t, stdout)["source"], stderr)
	}
}

// A kubeconfig the adapter cannot use is refused before any request, as a usage/validation error.
func TestObserveRefusesAnUnusableKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig.json")
	if err := os.WriteFile(path, []byte("not a kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := runObserve(t, observeArgs(t, path)...)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "kubeconfig") {
		t.Fatalf("exit %d, stdout %q, stderr %q", status, stdout, stderr)
	}
}

// The command needs the revision Flux should be at: without it the fact cannot be assessed, so a
// usage error is clearer than a report that can never be running.
func TestObserveRequiresARevision(t *testing.T) {
	args := observeArgs(t, "/nonexistent")
	for i, arg := range args {
		if arg == "--revision" {
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	if status, stdout, _ := runObserve(t, args...); status != 2 || stdout != "" {
		t.Fatalf("exit %d, stdout %q", status, stdout)
	}
}

// The server's certificate is what the kubeconfig trusts; a self-signed certificate is enough for the
// tests, and asserting it here keeps the helper honest.
func TestTestServerCertificateParses(t *testing.T) {
	server := (&api{}).serve(t)
	if _, err := x509.ParseCertificate(server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
}

// digestOf is the artifact a release fixture pins, so the scenario serves the image the instance intends.
func digestOf(t *testing.T, release string) string {
	t.Helper()
	var document struct {
		Artifact struct {
			Digest string `json:"digest"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(fixture(t, release), &document); err != nil {
		t.Fatal(err)
	}
	return document.Artifact.Digest
}

// A database instance's report also answers whether its backup job runs: current under the default
// threshold, stale under one the operator states, and the running verdict is unchanged by either.
func TestObserveReportsTheBackupOfADatabaseInstance(t *testing.T) {
	api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true}
	server := api.serve(t)
	args := observeArgsOf(t, kubeconfig(t, server), "sqlite.json", "r43.json",
		"--egress", filepath.Join("..", "..", "..", "platform", "egress", "allocation.json"))
	status, stdout, stderr := runObserve(t, args...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	backup := report["backup"].(map[string]any)
	if backup["state"] != observe.BackupCurrent || backup["reason"] != nil {
		t.Fatalf("backup %v", backup)
	}
	target := report["target"].(map[string]any)["backup"].(map[string]any)
	if backup["schedule"] != target["schedule"] || backup["schedule"] == "" || backup["maxAgeSeconds"] != float64(26*3600) {
		t.Fatalf("backup facts %v", backup)
	}
	newest := backup["newestRun"].(map[string]any)
	if newest["name"] != "demo-orders-backup-29345678" || newest["outcome"] != observe.RunComplete {
		t.Fatalf("newest run %v", newest)
	}
	if target["cronJob"].(map[string]any)["name"] != "demo-orders-backup" ||
		target["pods"].(map[string]any)["kind"] != "Pod" || target["selector"] != backupSelector {
		t.Fatalf("the target's protection %v", target)
	}
	if !strings.Contains(stderr, "backup current") {
		t.Fatalf("summary %q", stderr)
	}
	// The last success is two hours old, so an hour's threshold is not met — and the verdict is untouched.
	status, stdout, stderr = runObserve(t, append(args, "--max-age", "1h")...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report = document(t, stdout)
	backup = report["backup"].(map[string]any)
	if backup["state"] != observe.BackupStale || backup["reason"] != observe.ReasonTooOld {
		t.Fatalf("one hour: %v", backup)
	}
	if report["verdict"] != string(observe.IsRunning) || !strings.Contains(stderr, "backup stale/last_success_too_old") {
		t.Fatalf("verdict %v, summary %q", report["verdict"], stderr)
	}
	// A threshold the command cannot parse is a usage error, not a report judged by nothing.
	if status, stdout, _ := runObserve(t, append(args, "--max-age", "soon")...); status != 2 || stdout != "" {
		t.Fatalf("an unreadable threshold: exit %d, stdout %q", status, stdout)
	}
}
