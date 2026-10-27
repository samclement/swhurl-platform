package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/s3/s3test"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// restoreScenario is the bucket a restore plan reads: one archive two hours old, its metadata, and the keys
// for the cases that must not be ready.
func restoreScenario(t *testing.T, metadataKey, metadata string) s3test.Scenario {
	t.Helper()
	prefix := "app-sqlite/demo-orders-staging/"
	return s3test.Scenario{
		Query: observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: prefix},
		Objects: []observe.Archive{{Key: prefix + "sqlite-20261026T071500Z.db.age",
			LastModified: time.Now().UTC().Add(-2 * time.Hour)},
			{Key: prefix + "sqlite-20261025T035800Z.db.age", LastModified: time.Now().UTC().Add(-30 * time.Hour)}},
		Key:     prefix + metadataKey,
		Body:    metadata,
		Missing: prefix + "sqlite-20261026T071500Z.json",
	}
}

// readyMetadata is the record the backup job publishes beside an archive: both checksums, the table count and
// the instance it came from, which is what a restore verifies against.
const readyMetadata = `{"sha256":"` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
	`","plain_sha256":"` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" +
	`","tables":2,"source":"demo-orders-staging/demo-orders"}`

// runRestore runs the command with a supplied object-store reader.
func runRestore(t *testing.T, archives func(bucket, region string) (s3.Reader, error), args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := runWithArchives(args, &out, &diagnostic, os.ReadFile, files.New(), archives)
	return status, out.String(), diagnostic.String()
}

// restoreArgs is the command line for the compiled database instance, over the test cluster: the same document
// flags observe takes, with the command named restore.
func restoreArgs(t *testing.T, kubeconfigPath string, extra ...string) []string {
	t.Helper()
	args := observeArgsOf(t, kubeconfigPath, "sqlite.json", "r43.json",
		append([]string{"--egress", filepath.Join("..", "..", "..", "platform", "egress", "allocation.json")}, extra...)...)
	args[0] = "restore"
	return args
}

// The plan for a verifiable archive is ready, and states the instance, the archive, its metadata and the
// command that performs the restore.
func TestRestorePlanForAVerifiableArchive(t *testing.T) {
	api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true}
	server := api.serve(t)
	scenario := restoreScenario(t, "sqlite-20261026T071500Z.json", readyMetadata)
	args := restoreArgs(t, kubeconfig(t, server))
	status, stdout, stderr := runRestore(t, func(string, string) (s3.Reader, error) { return s3test.New(scenario), nil }, args...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	if report["kind"] != "v2-restore" || report["version"] != float64(2) {
		t.Fatalf("envelope %v/%v", report["kind"], report["version"])
	}
	// A plan states that nothing ran, and keeps the v1 command that would.
	action := report["action"].(map[string]any)
	if action["state"] != "not_attempted" || len(action["steps"].([]any)) != 0 || action["job"] != nil {
		t.Fatalf("the action block of a plan is %v", action)
	}
	plan := report["plan"].(map[string]any)
	if plan["state"] != observe.RestoreReady || plan["reason"] != nil {
		t.Fatalf("plan %v", plan)
	}
	archive := plan["archive"].(map[string]any)
	if archive["key"] != "app-sqlite/demo-orders-staging/sqlite-20261026T071500Z.db.age" || archive["ageSeconds"] == nil {
		t.Fatalf("archive %v", archive)
	}
	metadata := plan["metadata"].(map[string]any)
	if metadata["tables"] != float64(2) || metadata["source"] != "demo-orders-staging/demo-orders" ||
		metadata["sha256"] == nil || metadata["plainSha256"] == nil {
		t.Fatalf("metadata %v", metadata)
	}
	instance := report["instance"].(map[string]any)
	// The instance record names a size, not a claim: the pinned chart names the generated claim after the
	// release, so the plan states that name and the compiler's own mount and path.
	if instance["claim"] != "demo-orders" || instance["mount"] != "/data" ||
		instance["databasePath"] != "/data/app.db" || instance["uid"] != float64(65532) {
		t.Fatalf("instance %v", instance)
	}
	if observed := report["observation"].(map[string]any); observed["verdict"] != "running" ||
		observed["archive"] != observe.BackupCurrent {
		t.Fatalf("observation %v", observed)
	}
	steps := report["steps"].([]any)
	command := steps[0].(map[string]any)["command"].(string)
	if !strings.Contains(command, "make restore-sqlite APP=demo-orders ENV=staging CONFIRM=demo-orders/staging") {
		t.Fatalf("step %q", command)
	}
	if !strings.Contains(stderr, "restore: ready") {
		t.Fatalf("summary %q", stderr)
	}
}

// An archive without its metadata is not restorable, and a named archive older than the threshold is stale:
// neither is silently traded for the newest.
func TestRestoreRefusesWhatItCannotVerify(t *testing.T) {
	api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true}
	server := api.serve(t)
	for _, tt := range []struct {
		name     string
		scenario s3test.Scenario
		args     []string
		state    string
		reason   string
	}{
		{"no metadata", restoreScenario(t, "elsewhere.json", readyMetadata), nil,
			observe.RestoreIncomplete, observe.ReasonMetadataAbsent},
		{"chosen too old", restoreScenario(t, "sqlite-20261026T071500Z.json", readyMetadata),
			[]string{"--from", "app-sqlite/demo-orders-staging/sqlite-20261025T035800Z.db.age"},
			observe.RestoreStale, observe.ReasonNotRestorableStale},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := restoreArgs(t, kubeconfig(t, server), tt.args...)
			status, stdout, stderr := runRestore(t,
				func(string, string) (s3.Reader, error) { return s3test.New(tt.scenario), nil }, args...)
			if status != 0 {
				t.Fatalf("exit %d: %s", status, stderr)
			}
			plan := document(t, stdout)["plan"].(map[string]any)
			if plan["state"] != tt.state || plan["reason"] != tt.reason {
				t.Fatalf("plan %v", plan)
			}
		})
	}
	// An instance that keeps no database cannot be restored at all.
	args := observeArgsOf(t, kubeconfig(t, server), "prod.json", "r41.json")
	args[0] = "restore"
	if status, stdout, stderr := runRestore(t, func(string, string) (s3.Reader, error) {
		return s3test.New(
			restoreScenario(t, "sqlite-20261026T071500Z.json", readyMetadata)), nil
	}, args...); status != 2 ||
		stdout != "" || !strings.Contains(stderr, "unsupported: instance: it keeps no database to restore") {
		t.Fatalf("a stateless instance: exit %d, stdout %q, stderr %q", status, stdout, stderr)
	}
}

// ageKeyFile is the operator's identity as age-keygen writes it: comment lines, then the key. It is the one
// credential the restore pod decrypts with.
func ageKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "age.agekey")
	key := "# created: 2026-10-26T20:00:00Z\n# public key: age1" + strings.Repeat("q", 58) + "\n" +
		"AGE-SECRET-KEY-1" + strings.Repeat("Q", 58) + "\n"
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// performRestore runs one confirmed restore over the test cluster, with a scripted bucket.
func performRestore(t *testing.T, api *api, extra ...string) (int, string, string, *s3test.Fake) {
	t.Helper()
	server := api.serve(t)
	bucket := s3test.New(restoreScenario(t, "sqlite-20261026T071500Z.json", readyMetadata))
	args := restoreArgs(t, kubeconfig(t, server), append([]string{"--confirm", "demo-orders/staging"}, extra...)...)
	status, stdout, stderr := runRestore(t, func(string, string) (s3.Reader, error) { return bucket, nil }, args...)
	return status, stdout, stderr, bucket
}

// A confirmed restore performs the sequence: suspend, stop, the two transient objects, the wait, the log, the
// deletes, and the app put back. The credentials travel in the Secret, and never in the Job.
func TestRestorePerformsItWhenConfirmed(t *testing.T) {
	api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true}
	status, stdout, stderr, bucket := performRestore(t, api, "--age-key-file", ageKeyFile(t))
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if len(bucket.Presigns) != 1 || bucket.Presigns[0] != "app-sqlite/demo-orders-staging/sqlite-20261026T071500Z.db.age" {
		t.Fatalf("the presigned reads are %v", bucket.Presigns)
	}
	report := document(t, stdout)
	action := report["action"].(map[string]any)
	if action["state"] != "restored" || action["reason"] != nil {
		t.Fatalf("the action block is %v", action)
	}
	app := action["app"].(map[string]any)
	if app["state"] != "running" || app["available"] != true || app["replicas"] != float64(1) ||
		app["suspended"] != false {
		t.Fatalf("the app block is %v", app)
	}
	job := action["job"].(map[string]any)
	if job["state"] != "complete" || !strings.Contains(job["log"].(string), "[OK] Restored") {
		t.Fatalf("the job's evidence is %v", job)
	}
	steps := action["steps"].([]any)
	if len(steps) == 0 || steps[0].(map[string]any)["name"] != "suspend" ||
		steps[len(steps)-1].(map[string]any)["name"] != "available" {
		t.Fatalf("the steps are %v", steps)
	}
	// The plan's v1 command is not in a document v2 performed.
	if report["steps"] != nil || report["note"] != restorePerformedNote {
		t.Fatalf("a performed document still plans: %v", report["steps"])
	}

	// The order of the changes is the safety contract, and the two bodies carry what each is allowed to.
	var order []string
	var secretBody, jobBody string
	for _, changed := range api.changes {
		switch {
		case changed.method == http.MethodPatch:
			order = append(order, "patch "+changedKind(changed.path)+" "+changed.body)
		case changed.method == http.MethodPost && strings.Contains(changed.path, "/secrets"):
			order = append(order, "create secret")
			secretBody = changed.body
		case changed.method == http.MethodPost && strings.Contains(changed.path, "/jobs"):
			order = append(order, "create job")
			jobBody = changed.body
		case changed.method == http.MethodDelete && strings.Contains(changed.path, "/jobs/"):
			order = append(order, "delete job")
		case changed.method == http.MethodDelete && strings.Contains(changed.path, "/secrets/"):
			order = append(order, "delete secret")
		}
	}
	want := []string{
		`patch helmreleases {"spec":{"suspend":true}}`, `patch cronjobs {"spec":{"suspend":true}}`,
		`patch deployments {"spec":{"replicas":0}}`, "create secret", "create job", "delete job", "delete secret",
		`patch cronjobs {"spec":{"suspend":false}}`, `patch deployments {"spec":{"replicas":1}}`,
		`patch helmreleases {"spec":{"suspend":false}}`,
	}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Fatalf("the changes are\n%s\nwant\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(secretBody, "AGE-SECRET-KEY-1") || !strings.Contains(secretBody, "X-Amz-Signature") {
		t.Fatalf("the Secret does not hold the two credentials: %s", secretBody)
	}
	for _, credential := range []string{"AGE-SECRET-KEY-1", "X-Amz-Signature"} {
		if strings.Contains(jobBody, credential) {
			t.Fatalf("the Job document carries a credential: %s", jobBody)
		}
	}
	for name, output := range map[string]string{"document": stdout, "diagnostic": stderr} {
		for _, credential := range []string{"AGE-SECRET-KEY-1", "X-Amz-Signature"} {
			if strings.Contains(output, credential) {
				t.Fatalf("the %s carries a credential", name)
			}
		}
	}
}

// A confirmed restore refuses before it changes anything: a confirmation for another instance, a plan that is
// not ready, or an identity that is not one. Nothing is presigned and nothing is written.
func TestRestoreChecksTheConfirmationAndCredentialsFirst(t *testing.T) {
	for _, tt := range []struct {
		name     string
		args     func(*testing.T) []string
		scenario func(*testing.T) s3test.Scenario
		want     string
	}{
		{"another instance", func(t *testing.T) []string {
			return []string{"--confirm", "demo-orders/prod", "--age-key-file", ageKeyFile(t)}
		}, nil, "confirm"},
		{"not ready", func(t *testing.T) []string {
			return []string{"--confirm", "demo-orders/staging", "--age-key-file", ageKeyFile(t)}
		}, func(t *testing.T) s3test.Scenario {
			return restoreScenario(t, "sqlite-20261026T071500Z.json",
				`{"sha256":"`+strings.Repeat("a", 64)+`","tables":2,"source":"demo-orders-staging/demo-orders"}`)
		}, "restore.plan"},
		{"too short a read URL", func(t *testing.T) []string {
			return []string{"--confirm", "demo-orders/staging", "--age-key-file", ageKeyFile(t),
				"--url-ttl", "90s"}
		}, nil, "url-ttl"},
		{"no identity", func(t *testing.T) []string {
			path := filepath.Join(t.TempDir(), "age.agekey")
			if err := os.WriteFile(path, []byte("AGE-SECRET-KEY-1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{"--confirm", "demo-orders/staging", "--age-key-file", path}
		}, nil, "age-key-file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true}
			server := api.serve(t)
			scenario := restoreScenario(t, "sqlite-20261026T071500Z.json", readyMetadata)
			if tt.scenario != nil {
				scenario = tt.scenario(t)
			}
			bucket := s3test.New(scenario)
			args := restoreArgs(t, kubeconfig(t, server), tt.args(t)...)
			status, stdout, stderr := runRestore(t, func(string, string) (s3.Reader, error) { return bucket, nil }, args...)
			if status != 2 || stdout != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", status, stdout, stderr)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Fatalf("the diagnostic is %q, want it to name %q", stderr, tt.want)
			}
			if len(bucket.Presigns) != 0 || len(api.changes) != 0 {
				t.Fatalf("a refused restore presigned %v and changed %v", bucket.Presigns, api.changes)
			}
		})
	}
}

// A Job that fails is an exit 1 with the document written: the archive's checks did not pass, so the live
// database is untouched, the app is put back, and the report says which step stopped and what the job said.
func TestRestoreReportsAFailedJob(t *testing.T) {
	api := &api{namespace: "demo-orders-staging", digest: digestOf(t, "r43.json"), backup: true, jobFailed: true}
	status, stdout, stderr, _ := performRestore(t, api, "--age-key-file", ageKeyFile(t))
	if status != 1 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := document(t, stdout)
	action := report["action"].(map[string]any)
	if action["state"] != "failed" || action["reason"] != "job-result" {
		t.Fatalf("the action block is %v", action)
	}
	if job := action["job"].(map[string]any); job["state"] != "failed" ||
		!strings.Contains(job["log"].(string), "[OK] Restored") {
		t.Fatalf("the job's evidence is %v", job)
	}
	// The undo ran: the app's replicas are back and the release is not left suspended.
	for _, name := range []string{"delete-job", "delete-secret", "backup-resume", "scale-up", "resume"} {
		if step := stepOf(action["steps"].([]any), name); step == nil || step["outcome"] != "done" {
			t.Fatalf("the undo's %s is %v", name, step)
		}
	}
	if !strings.Contains(stderr, "restore: failed/job-result") {
		t.Fatalf("summary %q", stderr)
	}
}

// changedKind is which object a change's path names, so two merge patches with the same body are not confused:
// the release and the backup schedule are both suspended with {"spec":{"suspend":true}}.
func changedKind(path string) string {
	for _, kind := range []string{"helmreleases", "deployments", "cronjobs"} {
		if strings.Contains(path, "/"+kind+"/") {
			return kind
		}
	}
	return "unknown"
}

// stepOf is one recorded step by name, or nil.
func stepOf(steps []any, name string) map[string]any {
	for _, raw := range steps {
		step := raw.(map[string]any)
		if step["name"] == name {
			return step
		}
	}
	return nil
}

// The backup's own selector excludes the restore pods by the label the kernel puts on them. The two words are
// stated in packages that may not import each other, so this is the tie between them: without the exclusion a
// restore pod is read as a backup run, and a failed restore makes the instance's protection look broken.
func TestTheBackupSelectorExcludesTheRestorePodLabel(t *testing.T) {
	derived, err := observe.BackupTargetOf([]byte(`{"apiVersion":"batch/v1","kind":"CronJob",
		"metadata":{"name":"demo-orders-backup","namespace":"demo-orders-staging"},
		"spec":{"schedule":"35 3 * * *","jobTemplate":{"spec":{"template":{"metadata":{"labels":{
			"platform.swhurl.com/backup":"true"}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "!" + kernel.RestoreLabel; !strings.Contains(derived.Selector, want) {
		t.Fatalf("the backup selector is %q, want it to exclude %q", derived.Selector, want)
	}
}
