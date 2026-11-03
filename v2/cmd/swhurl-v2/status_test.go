package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/s3/s3test"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
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
	requests   []*http.Request
	changes    []change       // every request that is not a read, in order
	status     map[string]int // path → status override, for the failure cases
	namespace  string         // the instance's namespace; prod's by default
	digest     string         // the artifact the compiled release pins; the prod fixture's by default
	backup     bool           // whether the instance renders a backup CronJob
	scaledTo   *int32         // what a patch last set the Deployment's replica count to
	jobFailed  bool           // whether a restore's own Job reports Failed instead of Complete
	restoreJob bool           // whether a restore's Job (and so its pod) exists
	crashing   bool           // whether the app's container is crashing instead of ready
}

// change is one request that changes something, with its body: what a restore's own objects are created from.
type change struct{ method, path, body string }

// serve answers the paths one observation reads — the four every instance has, and an exposed instance's route
// and certificate — in the shape the API server writes them.
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
		if r.Method != http.MethodGet {
			body, _ := io.ReadAll(r.Body)
			a.changes = append(a.changes, change{r.Method, r.URL.Path, string(body)})
			var patch struct {
				Spec struct {
					Replicas *int32 `json:"replicas"`
				} `json:"spec"`
			}
			if err := json.Unmarshal(body, &patch); err == nil && patch.Spec.Replicas != nil {
				a.scaledTo = patch.Spec.Replicas
			}
			switch {
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs"):
				a.restoreJob = true
			case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/jobs/"):
				a.restoreJob = false
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/apis/batch/v1/namespaces/"+namespace+"/jobs/demo-orders-restore-") {
			// A restore's own Job, as the pod runs it: the command names it by the second it started.
			condition := "Complete"
			if a.jobFailed {
				condition = "Failed"
			}
			writeDocument(t, w, map[string]any{"apiVersion": "batch/v1", "kind": "Job",
				"metadata": map[string]any{"name": strings.TrimPrefix(r.URL.Path,
					"/apis/batch/v1/namespaces/"+namespace+"/jobs/"), "namespace": namespace},
				"status": map[string]any{"conditions": []any{map[string]any{
					"type": condition, "status": "True"}}}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/log") {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, restoreLog)
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
			replicas := int32(1)
			if a.scaledTo != nil {
				replicas = *a.scaledTo
			}
			writeDocument(t, w, map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": "demo-orders", "namespace": namespace, "generation": 4},
				"spec":     map[string]any{"replicas": replicas},
				"status": map[string]any{"observedGeneration": 4, "availableReplicas": replicas,
					"conditions": []any{map[string]any{"type": "Available", "status": "True"}}},
			})
		case "/api/v1/namespaces/" + namespace + "/pods":
			switch selected := r.URL.Query().Get("labelSelector"); {
			case selected == kernel.RestoreLabel:
				// No restore is running: the preflight's own answer.
				writeDocument(t, w, map[string]any{"kind": "PodList", "apiVersion": "v1", "items": []any{}})
			case strings.HasPrefix(selected, kernel.RestoreLabel+"="):
				// The restore's own pod: it exists while its Job does, and goes with it.
				items := []any{}
				if a.restoreJob {
					items = []any{map[string]any{"apiVersion": "v1", "kind": "Pod",
						"metadata": map[string]any{"name": "demo-orders-restore-pod", "namespace": namespace}}}
				}
				writeDocument(t, w, map[string]any{"kind": "PodList", "apiVersion": "v1", "items": items})
			case selected == selector:
				// The app's own pods: gone once the restore has scaled the Deployment to zero, which is what
				// the pods-gone wait is for.
				items := []any{pod(namespace, "docker.io/example/demo-orders@"+digest)}
				if a.crashing {
					items = []any{crashingPod(namespace, "docker.io/example/demo-orders@"+digest)}
				}
				if a.scaledTo != nil && *a.scaledTo == 0 {
					items = []any{}
				}
				writeDocument(t, w, map[string]any{"kind": "PodList", "apiVersion": "v1", "items": items})
			case selected == backupSelector:
				writeDocument(t, w, map[string]any{"kind": "PodList", "apiVersion": "v1",
					"items": []any{backupPod(namespace)}})
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/apis/networking.k8s.io/v1/namespaces/" + namespace + "/ingresses/demo-orders":
			writeDocument(t, w, map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
				"metadata": map[string]any{"name": "demo-orders", "namespace": namespace},
				"spec":     map[string]any{"rules": []any{map[string]any{"host": "demo-orders.homelab.swhurl.com"}}}})
		case "/apis/cert-manager.io/v1/namespaces/" + namespace + "/certificates/demo-orders-tls":
			writeDocument(t, w, map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
				"metadata": map[string]any{"name": "demo-orders-tls", "namespace": namespace, "generation": 1},
				"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True",
					"reason": "Ready", "observedGeneration": 1}}}})
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

// restoreLog is what the restore image prints on success: the fact the report carries as evidence.
const restoreLog = "[OK] Restored the archive into /data/app.db (2 tables, integrity ok); " +
	"the replaced files are in /data/before-restore-20261027T030405Z\n"

// backupSelector is what the observation asks for: the compiled backup job's own label, without the instance's
// restore pods (which carry it so the backup policy admits them).
const backupSelector = "platform.swhurl.com/backup=true,!" + kernel.RestoreLabel

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

// crashingPod is the app's pod with its container in a crash loop: started once, not ready, and saying why.
func crashingPod(namespace, image string) map[string]any {
	document := pod(namespace, image)
	document["status"] = map[string]any{"containerStatuses": []any{map[string]any{"name": "main", "image": image,
		"imageID": "docker-pullable://" + image, "ready": false, "state": map[string]any{"waiting": map[string]any{
			"reason": "CrashLoopBackOff", "message": "back-off 5m0s restarting failed container=main"}}}}}
	return document
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

// statusArgs is the command line for the prod instance's records.
func statusArgs(t *testing.T, kubeconfigPath string, extra ...string) []string {
	return statusArgsOf(t, kubeconfigPath, "prod.json", "r41.json", extra...)
}

// statusArgsOf is the command line for one fixture instance, with its record pair and the app record written into
// a records tree as v2-records holds them, asking for the JSON document.
func statusArgsOf(t *testing.T, kubeconfigPath, instance, release string, extra ...string) []string {
	t.Helper()
	return statusArgsWith(t, kubeconfigPath, fixture(t, instance), release, append([]string{"--json"}, extra...)...)
}

// statusArgsWith writes the given instance record into a records tree and names it on the command line.
func statusArgsWith(t *testing.T, kubeconfigPath string, instance []byte, release string, extra ...string) []string {
	t.Helper()
	records := t.TempDir()
	var spec struct{ Application, Environment string }
	if err := json.Unmarshal(instance, &spec); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(records, spec.Application, spec.Environment)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		filepath.Join(folder, instanceFile):               instance,
		filepath.Join(folder, releaseFile):                fixture(t, release),
		filepath.Join(records, spec.Application, appFile): fixture(t, "app.json"),
		filepath.Join(records, "profile.json"):            fixture(t, "profile.json"),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"status", "--records", records, "--app", spec.Application, "--env", spec.Environment,
		"--profile", filepath.Join(records, "profile.json"), "--revision", observedRevision, "--kubeconfig", kubeconfigPath}
	return append(args, extra...)
}

// instanceArgsOf is the command line restore takes: one compiled fixture instance, with its documents written beside
// each other and named by flag.
func instanceArgsOf(t *testing.T, kubeconfigPath, instance, release string, extra ...string) []string {
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
	args := []string{"restore", "--instance", written["instance.json"], "--release", written["release.json"],
		"--profile", written["profile.json"], "--revision", observedRevision, "--kubeconfig", kubeconfigPath}
	return append(args, extra...)
}

// runStatus runs the command and returns its exit status and streams.
func runStatus(t *testing.T, args ...string) (int, string, string) {
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

// The recorded instance is running: every fact positive, the state healthy, and the report says what it read and
// when.
func TestStatusReportsTheRunningInstance(t *testing.T) {
	api := &api{}
	server := api.serve(t)
	status, stdout, stderr := runStatus(t, statusArgs(t, kubeconfig(t, server))...)
	if status != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	if report["kind"] != "v2-observation" || report["version"] != float64(3) {
		t.Fatalf("the report envelope is %v/%v", report["kind"], report["version"])
	}
	if report["verdict"] != string(observe.IsRunning) || report["state"].(map[string]any)["name"] != observe.StateHealthy {
		t.Fatalf("verdict %v, state %v, findings %v", report["verdict"], report["state"], report["findings"])
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
	// A private instance's records expose no route, so none is read and the lists are empty, not missing.
	if len(report["routes"].([]any)) != 0 || len(report["certificates"].([]any)) != 0 || len(report["problems"].([]any)) != 0 {
		t.Fatalf("routes %v, certificates %v, problems %v", report["routes"], report["certificates"], report["problems"])
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
func TestStatusLeavesAFailedReadUnknown(t *testing.T) {
	api := &api{status: map[string]int{
		"/apis/apps/v1/namespaces/demo-orders-prod/deployments/demo-orders": http.StatusForbidden,
	}}
	server := api.serve(t)
	status, stdout, stderr := runStatus(t, statusArgs(t, kubeconfig(t, server))...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	if report["verdict"] != string(observe.Unknown) {
		t.Fatalf("verdict %v", report["verdict"])
	}
	if state := report["state"].(map[string]any); state["name"] != observe.StateUnknown || state["field"] != "workload" ||
		state["reason"] != observe.ReasonForbidden {
		t.Fatalf("state %v", state)
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
func TestStatusReportsAbsence(t *testing.T) {
	api := &api{status: map[string]int{
		"/apis/apps/v1/namespaces/demo-orders-prod/deployments/demo-orders": http.StatusNotFound,
	}}
	server := api.serve(t)
	status, stdout, stderr := runStatus(t, statusArgs(t, kubeconfig(t, server))...)
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
func TestStatusNamesTheSource(t *testing.T) {
	server := (&api{}).serve(t)
	_, stdout, stderr := runStatus(t, statusArgs(t, kubeconfig(t, server), "--source", "homelab-prod")...)
	if document(t, stdout)["source"] != "homelab-prod" {
		t.Fatalf("source: %v; %s", document(t, stdout)["source"], stderr)
	}
}

// A kubeconfig the adapter cannot use is refused before any request, as a usage/validation error.
func TestStatusRefusesAnUnusableKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig.json")
	if err := os.WriteFile(path, []byte("not a kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := runStatus(t, statusArgs(t, path)...)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "kubeconfig") {
		t.Fatalf("exit %d, stdout %q, stderr %q", status, stdout, stderr)
	}
}

// The command needs the commit the records were read at: without it the revision cannot be assessed, so a
// usage error is clearer than a report that can never be healthy.
func TestStatusRequiresARevision(t *testing.T) {
	args := statusArgs(t, "/nonexistent")
	for i, arg := range args {
		if arg == "--revision" {
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	if status, stdout, _ := runStatus(t, args...); status != 2 || stdout != "" {
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
func TestStatusReportsTheBackupOfADatabaseInstance(t *testing.T) {
	api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true}
	server := api.serve(t)
	args := statusArgsOf(t, kubeconfig(t, server), "sqlite.json", "r43.json",
		"--egress", filepath.Join("..", "..", "..", "platform", "egress", "allocation.json"))
	status, stdout, stderr := runStatus(t, args...)
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
	if report["state"].(map[string]any)["name"] != observe.StateHealthy {
		t.Fatalf("state %v", report["state"])
	}
	// Without --archive the object store is not read, and the report says so rather than implying it was.
	if archive := backup["archive"].(map[string]any); archive["state"] != observe.BackupNotAssessed ||
		archive["reason"] != observe.NotRead {
		t.Fatalf("archive %v", archive)
	}
	// The last success is two hours old, so an hour's threshold is not met — and the verdict is untouched.
	status, stdout, stderr = runStatus(t, append(args, "--max-age", "1h")...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report = document(t, stdout)
	backup = report["backup"].(map[string]any)
	if backup["state"] != observe.BackupStale || backup["reason"] != observe.ReasonTooOld {
		t.Fatalf("one hour: %v", backup)
	}
	if report["verdict"] != string(observe.IsRunning) || report["state"].(map[string]any)["name"] != observe.StateHealthy {
		t.Fatalf("verdict %v, state %v", report["verdict"], report["state"])
	}
	// A threshold the command cannot parse is a usage error, not a report judged by nothing.
	if status, stdout, _ := runStatus(t, append(args, "--max-age", "soon")...); status != 2 || stdout != "" {
		t.Fatalf("an unreadable threshold: exit %d, stdout %q", status, stdout)
	}
}

// runObserveArchive runs the command with a supplied object-store reader, so a listing needs no credential.
func runObserveArchive(t *testing.T, archives func(bucket, region string) (s3.Reader, error), args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := runWithArchives(args, &out, &diagnostic, os.ReadFile, files.New(), archives)
	return status, out.String(), diagnostic.String()
}

// With --archive the report carries the object store's answer: the newest archive under the instance's own
// prefix, and how old it is.
func TestStatusReadsTheArchiveWhenAsked(t *testing.T) {
	api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true}
	server := api.serve(t)
	prefix := "app-sqlite/demo-orders-staging/"
	snapshot := s3test.Scenario{
		Query: observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: prefix},
		Objects: []observe.Archive{{Key: prefix + "sqlite-20261026T071500Z.db.age",
			LastModified: time.Now().UTC().Add(-2 * time.Hour)}},
		Forbidden: observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: "app-sqlite/refused/"},
	}
	fake := s3test.New(snapshot)
	args := statusArgsOf(t, kubeconfig(t, server), "sqlite.json", "r43.json",
		"--egress", filepath.Join("..", "..", "..", "platform", "egress", "allocation.json"), "--archive")
	status, stdout, stderr := runObserveArchive(t, func(string, string) (s3.Reader, error) { return fake, nil }, args...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	archive := report["backup"].(map[string]any)["archive"].(map[string]any)
	if archive["state"] != observe.BackupCurrent || archive["reason"] != nil {
		t.Fatalf("archive %v", archive)
	}
	if archive["newest"] != prefix+"sqlite-20261026T071500Z.db.age" || archive["prefix"] != prefix {
		t.Fatalf("archive facts %v", archive)
	}
	if archive["ageSeconds"] == nil || archive["maxAgeSeconds"] != float64(26*3600) {
		t.Fatalf("archive ages %v", archive)
	}
	// The reader was asked for the instance's own directory, and only it.
	if len(fake.Lists) != 1 || fake.Lists[0] != snapshot.Query {
		t.Fatalf("the listing was asked for %v", fake.Lists)
	}
	if stderr != "" {
		t.Fatalf("stderr %q", stderr)
	}
}

// Without --archive the report says the archive was not read, and no object-store reader is built; without
// credentials the command refuses before listing anything.
func TestStatusWithoutArchiveOrCredentials(t *testing.T) {
	api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true}
	server := api.serve(t)
	args := statusArgsOf(t, kubeconfig(t, server), "sqlite.json", "r43.json",
		"--egress", filepath.Join("..", "..", "..", "platform", "egress", "allocation.json"))
	status, stdout, stderr := runStatus(t, args...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	archive := document(t, stdout)["backup"].(map[string]any)["archive"].(map[string]any)
	if archive["state"] != observe.BackupNotAssessed || archive["reason"] != observe.NotRead {
		t.Fatalf("archive %v", archive)
	}
	// The operator's own credentials come from the environment; without them the read is refused, and the
	// message names no value.
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	if status, stdout, stderr := runStatus(t, append(args, "--archive")...); status != 2 || stdout != "" ||
		!strings.Contains(stderr, "invalid: credentials: aws_credentials_are_not_set") {
		t.Fatalf("no credentials: exit %d, stdout %q, stderr %q", status, stdout, stderr)
	}
}

// The default output is text for a person: the state and what decided it, the commit and image the records expect,
// every finding, and the route. It is a reading of the same report, so the state agrees with the document's.
func TestStatusPrintsTextByDefault(t *testing.T) {
	server := (&api{}).serve(t)
	status, stdout, stderr := runStatus(t, statusArgsWith(t, kubeconfig(t, server), fixture(t, "prod.json"), "r41.json")...)
	if status != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	want := []string{
		"Instance   demo-orders/prod (namespace demo-orders-prod)",
		"State      healthy",
		"Revision   " + observedRevision + ", the commit the records were read at",
		"Image      " + compiledDigest,
		"Replicas   1 wanted",
		"Findings   unit           reconciled",
		"           unit.revision  applied",
		"           release        settled",
		"           workload       present",
		"           image          running",
		"           ready          ready",
		"           replicas       as_intended",
		"Route      none: the records expose no route",
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != len(want)+1 || !strings.HasPrefix(lines[len(want)], "Observed   homelab at ") {
		t.Fatalf("output:\n%s", stdout)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d is %q, want %q", i, lines[i], want[i])
		}
	}
}

// An exposed instance's route is the compiled Ingress and its certificate, read by the names the records give them;
// a crashing container is failing, with the container and its reason printed.
func TestStatusReadsTheRouteAndNamesAFailingContainer(t *testing.T) {
	api := &api{crashing: true}
	server := api.serve(t)
	exposed := []byte(strings.Replace(string(fixture(t, "prod.json")), `"private"`, `"authenticated-web"`, 1))
	status, stdout, stderr := runStatus(t, statusArgsWith(t, kubeconfig(t, server), exposed, "r41.json")...)
	if status != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	for _, line := range []string{
		"State      failing: containers " + observe.ReasonContainers,
		"Route      https://demo-orders.homelab.swhurl.com",
		"TLS        demo-orders-tls: ready",
		"Problems   demo-orders-abc/main: CrashLoopBackOff back-off 5m0s restarting failed container=main",
	} {
		if !strings.Contains(stdout, line+"\n") {
			t.Errorf("missing %q in:\n%s", line, stdout)
		}
	}
	paths := map[string]bool{}
	for _, request := range api.requests {
		paths[request.URL.Path] = true
	}
	for _, path := range []string{"/apis/networking.k8s.io/v1/namespaces/demo-orders-prod/ingresses/demo-orders",
		"/apis/cert-manager.io/v1/namespaces/demo-orders-prod/certificates/demo-orders-tls"} {
		if !paths[path] {
			t.Errorf("%s was not read: %v", path, paths)
		}
	}
	// The same failure in the document: the state, and the container as a problem.
	status, stdout, _ = runStatus(t, statusArgsWith(t, kubeconfig(t, server), exposed, "r41.json", "--json")...)
	report := document(t, stdout)
	problems := report["problems"].([]any)
	if status != 0 || len(problems) != 1 || problems[0].(map[string]any)["reason"] != "CrashLoopBackOff" ||
		report["state"].(map[string]any)["name"] != observe.StateFailing {
		t.Fatalf("exit %d, state %v, problems %v", status, report["state"], problems)
	}
	if routes := report["routes"].([]any); len(routes) != 1 || routes[0].(map[string]any)["outcome"] != observe.Present {
		t.Fatalf("routes %v", routes)
	}
}

// An application and environment with no record under the tree is refused before anything is read, and so is a
// record that names another instance.
func TestStatusRefusesAnInstanceWithNoRecord(t *testing.T) {
	api := &api{}
	server := api.serve(t)
	args := statusArgs(t, kubeconfig(t, server))
	for i, arg := range args {
		if arg == "--env" {
			args[i+1] = "staging"
		}
	}
	status, stdout, stderr := runStatus(t, args...)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "invalid: records: no instance record for demo-orders/staging") {
		t.Fatalf("exit %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	other := []byte(strings.Replace(string(fixture(t, "prod.json")), `"prod"`, `"staging"`, 1))
	args = statusArgsWith(t, kubeconfig(t, server), other, "r41.json")
	records := args[2]
	if err := os.Rename(filepath.Join(records, "demo-orders", "staging"), filepath.Join(records, "demo-orders", "prod")); err != nil {
		t.Fatal(err)
	}
	for i, arg := range args {
		if arg == "--env" {
			args[i+1] = "prod"
		}
	}
	status, stdout, stderr = runStatus(t, args...)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "names demo-orders/staging") {
		t.Fatalf("another instance's record: exit %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	if len(api.requests) != 0 {
		t.Fatalf("%d requests for a refused instance", len(api.requests))
	}
}

// serviceAccountMount is a pod's ServiceAccount for the test server: the kubelet's three files in a folder of the
// test's own, and the environment naming the server's address. Nothing here reads the real mount.
func serviceAccountMount(t *testing.T, server *httptest.Server) (inCluster, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	for name, data := range map[string][]byte{"token": []byte("test-token\n"), "ca.crt": certificate,
		"namespace": []byte("console\n")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"KUBERNETES_SERVICE_HOST": host, "KUBERNETES_SERVICE_PORT": port}
	return inCluster{dir: dir, getenv: func(name string) string { return environment[name] }}, environment
}

// runInCluster runs the command with the given ServiceAccount mount; everything else is what the binary does.
func runInCluster(t *testing.T, mount inCluster, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := runWith(args, &out, &diagnostic, cli{readFile: os.ReadFile, writer: files.New(), archives: archiveReader,
		registries: registryReader, seed: randomSeed, encrypter: sops.New(), inCluster: mount})
	return status, out.String(), diagnostic.String()
}

// inClusterArgs is a status command line for the prod instance's records with --in-cluster instead of a kubeconfig.
func inClusterArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	args := statusArgsWith(t, "unused", fixture(t, "prod.json"), "r41.json")
	for i, arg := range args {
		if arg == "--kubeconfig" {
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	return append(append(args, "--in-cluster"), extra...)
}

// In a pod, status reads with its ServiceAccount: the token reaches the server, the CA is the one trusted, and the
// report names the ServiceAccount's namespace as its source unless the operator names another.
func TestStatusReadsWithThePodServiceAccount(t *testing.T) {
	api := &api{}
	server := api.serve(t)
	mount, _ := serviceAccountMount(t, server)
	status, stdout, stderr := runInCluster(t, mount, inClusterArgs(t, "--json")...)
	if status != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	if state := report["state"].(map[string]any); state["name"] != observe.StateHealthy {
		t.Fatalf("state %v, findings %v", state, report["findings"])
	}
	if report["source"] != "serviceaccount in console" {
		t.Fatalf("source %v", report["source"])
	}
	if len(api.requests) != 4 {
		t.Fatalf("%d requests", len(api.requests))
	}
	status, stdout, _ = runInCluster(t, mount, inClusterArgs(t)...)
	if status != 0 || !strings.Contains(stdout, "\nObserved   serviceaccount in console at ") {
		t.Fatalf("exit %d, text:\n%s", status, stdout)
	}
	_, stdout, stderr = runInCluster(t, mount, inClusterArgs(t, "--json", "--source", "console-pod")...)
	if document(t, stdout)["source"] != "console-pod" {
		t.Fatalf("source: %v; %s", document(t, stdout)["source"], stderr)
	}
}

// Each part of the ServiceAccount a pod may lack is named, and refused before any request: outside a pod, or with
// the token's automount off, there is nothing to read with.
func TestStatusInClusterNamesAMissingPart(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		change     func(dir string, environment map[string]string) error
	}{
		{"token", "token is not mounted", func(dir string, _ map[string]string) error {
			return os.Remove(filepath.Join(dir, "token"))
		}},
		{"ca.crt", "ca.crt is not mounted", func(dir string, _ map[string]string) error {
			return os.Remove(filepath.Join(dir, "ca.crt"))
		}},
		{"namespace", "namespace is not mounted", func(dir string, _ map[string]string) error {
			return os.Remove(filepath.Join(dir, "namespace"))
		}},
		{"an empty token", "token is empty", func(dir string, _ map[string]string) error {
			return os.WriteFile(filepath.Join(dir, "token"), []byte("\n"), 0o600)
		}},
		{"KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_HOST is not set", func(_ string, environment map[string]string) error {
			delete(environment, "KUBERNETES_SERVICE_HOST")
			return nil
		}},
		{"KUBERNETES_SERVICE_PORT", "KUBERNETES_SERVICE_PORT is not set", func(_ string, environment map[string]string) error {
			delete(environment, "KUBERNETES_SERVICE_PORT")
			return nil
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := &api{}
			server := api.serve(t)
			mount, environment := serviceAccountMount(t, server)
			if err := tt.change(mount.dir, environment); err != nil {
				t.Fatal(err)
			}
			status, stdout, stderr := runInCluster(t, mount, inClusterArgs(t)...)
			if status != 2 || stdout != "" || !strings.Contains(stderr, "invalid: in-cluster: ") ||
				!strings.Contains(stderr, tt.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q", status, stdout, stderr)
			}
			if len(api.requests) != 0 {
				t.Fatalf("%d requests without a credential", len(api.requests))
			}
		})
	}
}

// The credential is exactly one of a kubeconfig and the pod's ServiceAccount: neither or both is a usage error,
// and nothing is detected.
func TestStatusTakesExactlyOneCredential(t *testing.T) {
	server := (&api{}).serve(t)
	mount, _ := serviceAccountMount(t, server)
	both := append(statusArgs(t, kubeconfig(t, server)), "--in-cluster")
	neither := inClusterArgs(t)
	neither = neither[:len(neither)-1]
	for name, args := range map[string][]string{"both": both, "neither": neither} {
		status, stdout, stderr := runInCluster(t, mount, args...)
		if status != 2 || stdout != "" || !strings.Contains(stderr, "(--kubeconfig KUBECONFIG.json | --in-cluster)") {
			t.Fatalf("%s: exit %d, stdout %q, stderr %q", name, status, stdout, stderr)
		}
	}
}

// Only status reads with the pod's ServiceAccount: a restore writes, and takes no such flag.
func TestRestoreRefusesInCluster(t *testing.T) {
	args := instanceArgsOf(t, "kubeconfig.json", "sqlite.json", "r43.json")
	if _, outcome := parse(args); outcome != parsed {
		t.Fatalf("the restore line without the flag: outcome %d", outcome)
	}
	if _, outcome := parse(append(args, "--in-cluster")); outcome != misuse {
		t.Fatalf("restore --in-cluster: outcome %d", outcome)
	}
}
