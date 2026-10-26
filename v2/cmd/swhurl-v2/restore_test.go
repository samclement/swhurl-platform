package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/s3/s3test"
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

// readyMetadata is the record the backup job publishes beside an archive.
const readyMetadata = `{"sha256":"` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","tables":2,"source":"demo-orders-staging/demo-orders"}`

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
	if report["kind"] != "v2-restore" || report["version"] != float64(1) {
		t.Fatalf("envelope %v/%v", report["kind"], report["version"])
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
	if metadata["tables"] != float64(2) || metadata["source"] != "demo-orders-staging/demo-orders" || metadata["sha256"] == nil {
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
