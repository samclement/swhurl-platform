package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// runMigrate runs the command against real files and returns its status and streams.
func runMigrate(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := run(append([]string{"migrate"}, args...), &out, &diagnostic, os.ReadFile, files.New())
	return status, out.String(), diagnostic.String()
}

// migrationOf decodes the report a successful migration writes.
func migrationOf(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, stdout)
	}
	if document["kind"] != "v2-migration" || document["version"] != float64(2) {
		t.Fatalf("kind %v version %v", document["kind"], document["version"])
	}
	return document
}

// readFile is a named file's bytes, for comparing a migration's output with what Git holds.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestMigrateReproducesCommittedInstances runs the migration over the frozen predecessor records of every
// v2-owned instance (testdata/legacy, the v1 documents the handover removed) and asserts it writes, byte for
// byte, the record pair that migration committed (frozen beside them in migrated/) and exactly the files
// compile writes for that pair. An instance whose committed pair is still the migrated one must also match every
// file Git holds; one that has since selected another release or gained a broker identity v2 minted has moved on,
// and TestOwnedInstancesRecompileFromGit covers what it holds now. The two real migrations are then the translation's own acceptance test, the
// stateful one included.
func TestMigrateReproducesCommittedInstances(t *testing.T) {
	instances, err := filepath.Glob("../../../v2-records/*/*/instance.json")
	if err != nil || len(instances) == 0 {
		t.Fatalf("no v2-owned instance records: %v", err)
	}
	for _, instancePath := range instances {
		folder := filepath.Dir(instancePath)
		segments := strings.Split(filepath.ToSlash(folder), "/")
		application, environment := segments[len(segments)-2], segments[len(segments)-1]
		frozen := filepath.Join("..", "..", "testdata", "legacy", application, environment, "migrated")
		committedRecord, committedInstance := readFile(t, filepath.Join(frozen, "instance.json")),
			readFile(t, filepath.Join(frozen, "release.json"))
		// The pair has moved on when either record Git holds is not the migrated one: a new release selected, or
		// a broker identity minted into the instance after the handover.
		current := bytes.Equal(committedInstance, readFile(t, filepath.Join(folder, "release.json"))) &&
			bytes.Equal(committedRecord, readFile(t, filepath.Join(folder, "instance.json")))
		release, err := kernel.DecodeRelease(committedInstance)
		if err != nil {
			t.Fatalf("%s: %v", folder, err)
		}
		if _, err := kernel.DecodeInstance(committedRecord); err != nil {
			t.Fatalf("%s: %v", folder, err)
		}
		snapshot := writeSnapshot(t, predecessor(t, application, environment))
		destination := t.TempDir()
		args := []string{"--snapshot", snapshot, "--app", application, "--env", environment,
			"--profile", filepath.Join("..", "..", "testdata", "profile.json"),
			"--source-repository", release.Source.Repository, "--source-commit", release.Source.Commit,
			"--calls", "../../../v2-records/calls.json", "--out", destination}
		if statedEventsKey(t, predecessor(t, application, environment)) {
			// An instance that signs in to the broker travels with its seed: v2 copies the encrypted file and
			// never reads it. Use the ciphertext Git keeps beside the record when this instance has one, so the
			// copied file is compared byte for byte; otherwise a placeholder is all a test needs.
			seed := []byte("a ciphertext v2 never reads\n")
			if kept, err := os.ReadFile(filepath.Join("../../../v2-records", application, environment,
				"events.sops.yaml")); err == nil {
				seed = kept
			}
			secret := filepath.Join(destination, "events.sops.yaml")
			if err := os.WriteFile(secret, seed, 0o600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--events-secret", secret)
		}
		if release.Database != "" {
			args = append(args, "--egress", "../../../platform/egress/allocation.json")
		}
		if documents := filepath.Join("..", "..", "testdata", "legacy", application, environment, "offers"); folderExists(documents) {
			// The predecessor's published documents, as the operator supplies them from the commit they were read at.
			args = append(args, "--offers", documents)
		}
		status, stdout, diagnostic := runMigrate(t, args...)
		if status != 0 {
			t.Fatalf("%s: exit %d: %s", folder, status, diagnostic)
		}
		report := migrationOf(t, stdout)
		if release.Database != "" {
			// A database instance renders from its allocation: the report states the port the proxy
			// names it by, not a port re-derived from the record.
			allocated := allocatedPort(t, application, environment)
			if egress, ok := report["egress"].(map[string]any); !ok || egress["port"] != allocated {
				t.Errorf("%s: egress %v, the allocation names port %v", folder, report["egress"], allocated)
			}
		}
		if got, want := report["source"].(map[string]any)["corroborated"], float64(len(release.Source.Commit)); got == nil {
			t.Errorf("%s: corroboration %v (commit length %v)", folder, got, want)
		}
		// The committed pair predates the events facts: the platform could not render them then, so the two
		// instances v2 already owns keep no broker identity until the slice that can mint a key pair. The
		// migration now states them, so the committed record is what today's output says once they are removed.
		migrated := filepath.Join(destination, "v2-records", application, environment)
		withoutEvents, err := kernel.DecodeRelease(readFile(t, filepath.Join(migrated, "release.json")))
		if err != nil {
			t.Fatal(err)
		}
		produced := withoutEvents
		written, err := kernel.DecodeInstance(readFile(t, filepath.Join(migrated, "instance.json")))
		if err != nil {
			t.Fatal(err)
		}
		identities := report["identities"].(map[string]any)
		if identities["release"] != string(produced.ReleaseID) || identities["instance"] != string(mustRevision(t, written)) {
			t.Errorf("%s: the report names %v, not what it wrote", folder, identities)
		}
		withoutEvents.Publishes, withoutEvents.ReleaseID = nil, ""
		if withoutEvents.ReleaseID, err = withoutEvents.ID(); err != nil {
			t.Fatal(err)
		}
		committedRelease, err := kernel.DecodeRelease(readFile(t, filepath.Join(frozen, "release.json")))
		if err != nil {
			t.Fatal(err)
		}
		committedRelease.Publishes, committedRelease.ReleaseID = nil, withoutEvents.ReleaseID
		if !reflect.DeepEqual(withoutEvents, committedRelease) {
			t.Errorf("%s: the release record differs from the committed one by more than its publications", folder)
		}
		stripped, err := kernel.DecodeInstance(readFile(t, filepath.Join(migrated, "instance.json")))
		if err != nil {
			t.Fatal(err)
		}
		declared := statesEvents(produced, stripped)
		committedSpec, err := kernel.DecodeInstance(readFile(t, filepath.Join(frozen, "instance.json")))
		if err != nil {
			t.Fatal(err)
		}
		stripped.Events, stripped.Release = kernel.Events{}, withoutEvents.ReleaseID
		committedSpec.Events, committedSpec.Release = kernel.Events{}, withoutEvents.ReleaseID
		if !reflect.DeepEqual(stripped, committedSpec) {
			t.Errorf("%s: the instance record differs from the committed one by more than its events identity", folder)
		}
		if !bytes.Equal(readFile(t, filepath.Join(destination, "v2-records", application, "app.json")),
			readFile(t, filepath.Join(frozen, "app.json"))) {
			t.Errorf("%s: the app record is not what the migration committed", folder)
		}
		// Every other file is what compile writes for the migrated pair, and, while the instance still names
		// that release, what Git holds: the translation reproduces a real migration.
		compiled := t.TempDir()
		compileArgs := []string{"compile", "--instance", filepath.Join(frozen, "instance.json"),
			"--release", filepath.Join(frozen, "release.json"), "--app", filepath.Join(frozen, "app.json"),
			"--profile", filepath.Join("..", "..", "testdata", "profile.json"),
			"--calls", "../../../v2-records/calls.json", "--out", compiled}
		if release.Database != "" {
			compileArgs = append(compileArgs, "--egress", "../../../platform/egress/allocation.json")
		}
		if from, err := os.ReadFile(filepath.Join(frozen, "instance.json")); err == nil {
			var settings struct{ Events struct{ Key string } }
			if json.Unmarshal(from, &settings) == nil && settings.Events.Key != "" {
				// The committed pair's seed is the file Git holds beside its manifests.
				compileArgs = append(compileArgs, "--events-secret",
					filepath.Join("../../../v2-apps/instances", application, environment, "events.sops.yaml"))
			}
		}
		if documents := filepath.Join("..", "..", "testdata", "legacy", application, environment, "offers"); folderExists(documents) {
			compileArgs = append(compileArgs, "--offers", documents)
		}
		var out, compileDiagnostic bytes.Buffer
		if status := run(compileArgs, &out, &compileDiagnostic, os.ReadFile, files.New()); status != 0 {
			t.Fatalf("%s: compile exit %d: %s", folder, status, compileDiagnostic.String())
		}
		compared := 0
		for _, written := range report["files"].([]any) {
			path := written.(map[string]any)["path"].(string)
			if strings.HasPrefix(path, "v2-records/") {
				continue
			}
			got := string(readFile(t, filepath.Join(destination, filepath.FromSlash(path))))
			if declared && dependsOnEvents(path) {
				// The committed pair predates the events facts, so its files cannot show what the migration now
				// renders for the same records: the stream, the seed, the variables and the unit's dependency.
				// Everything else must still be what compile writes for that pair.
				continue
			}
			if got != string(readFile(t, filepath.Join(compiled, filepath.FromSlash(path)))) {
				t.Errorf("%s: %s is not what compile writes for the migrated pair", folder, path)
			}
			if current && got != string(readFile(t, filepath.Join("../../../", filepath.FromSlash(path)))) {
				t.Errorf("%s: %s is not what Git holds", folder, path)
			}
			compared++
		}
		if compared == 0 {
			t.Fatalf("%s: nothing was compared", folder)
		}
	}
}

// mustRevision is an instance record's revision, which the caller has already decoded.
func mustRevision(t *testing.T, spec kernel.InstanceSpec) kernel.InstanceRevision {
	t.Helper()
	revision, err := spec.Revision()
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

// allocatedPort is the port the committed allocation gives one instance.
func allocatedPort(t *testing.T, application, environment string) float64 {
	t.Helper()
	ports := portsOf(t, allocationOf(t, string(readFile(t, "../../../platform/egress/allocation.json"))))
	port, found := ports[environment+"/"+application]
	if !found {
		t.Fatalf("the allocation names %s/%s nowhere", application, environment)
	}
	return port
}

// predecessor is a snapshot in the exporter's shape for one instance: the predecessor's frozen records
// (testdata/legacy) beside the committed output v2 holds for it. Frozen, because a migration's input is what
// the predecessor said, not what a record says today.
func predecessor(t *testing.T, application, environment string) map[string]any {
	t.Helper()
	documents := map[string]any{}
	for role, path := range map[string]string{
		"app":         filepath.Join("..", "..", "testdata", "legacy", application, environment, "app.json"),
		"instance":    filepath.Join("..", "..", "testdata", "legacy", application, environment, "instance.json"),
		"release":     filepath.Join("..", "..", "testdata", "legacy", application, environment, "release.json"),
		"helmrelease": filepath.Join("../../../v2-apps/instances", application, environment, "helmrelease.json"),
	} {
		var document any
		if err := json.Unmarshal(readFile(t, path), &document); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		documents[role] = document
	}
	return map[string]any{"version": 1, "revision": strings.Repeat("c", 40), "residual": []any{},
		"instances": []any{map[string]any{"application": application, "environment": environment,
			"records": documents, "missing": []any{}, "objects": []any{}}}}
}

// A migration writes a record pair `compile` reads back: the same files, so a migrated instance is
// maintainable from Git without hand-written JSON.
func TestMigrateWritesAPairCompileCanRecompile(t *testing.T) {
	snapshot := writeSnapshot(t, snapshotDocument(t, map[string]any{}))
	profile := filepath.Join("..", "..", "testdata", "profile.json")
	destination := t.TempDir()
	status, stdout, diagnostic := runMigrate(t,
		"--snapshot", snapshot, "--app", "hello", "--env", "staging", "--profile", profile,
		"--source-repository", "nginxinc/nginx-unprivileged", "--source-commit", strings.Repeat("b", 40),
		"--out", destination)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	report := migrationOf(t, stdout)
	if report["snapshot"] != strings.Repeat("a", 40) {
		t.Errorf("snapshot revision %v", report["snapshot"])
	}
	source := report["source"].(map[string]any)
	if source["repository"] != "nginxinc/nginx-unprivileged" || source["commit"] != strings.Repeat("b", 40) {
		t.Errorf("source %v", source)
	}
	if corroborated, ok := source["corroborated"].([]any); !ok || len(corroborated) != 0 {
		t.Errorf("the fixture's tag abbreviates nothing: %v", source["corroborated"])
	}
	if report["egress"] != nil {
		t.Errorf("a stateless instance has no allocation: %v", report["egress"])
	}
	if report["note"] != migrationNote || len(report["gaps"].([]any)) == 0 {
		t.Errorf("note %v, gaps %v", report["note"], report["gaps"])
	}
	pair := filepath.Join(destination, "v2-records", "hello", "staging")
	recompiled := t.TempDir()
	// recompile reads the pair back through the compile command, into a second destination.
	status = run([]string{"compile",
		"--instance", filepath.Join(pair, "instance.json"), "--release", filepath.Join(pair, "release.json"),
		"--app", filepath.Join(destination, "v2-records", "hello", "app.json"),
		"--profile", profile, "--out", recompiled}, &bytes.Buffer{}, &bytes.Buffer{}, os.ReadFile, files.New())
	if status != 0 {
		t.Fatalf("the record pair does not compile: exit %d", status)
	}
	written := 0
	for _, entry := range report["files"].([]any) {
		path := entry.(map[string]any)["path"].(string)
		if strings.HasPrefix(path, "v2-records/") {
			continue
		}
		first, second := readFile(t, filepath.Join(destination, filepath.FromSlash(path))),
			readFile(t, filepath.Join(recompiled, filepath.FromSlash(path)))
		if !bytes.Equal(first, second) {
			t.Errorf("%s differs between the migration and a recompilation", path)
		}
		written++
	}
	if written == 0 {
		t.Fatal("the migration wrote no compiled file")
	}
}

// A declaration the resolution does not connect is reported as such: the migration never lets a relationship
// disappear because a document was not supplied.
func TestMigrateReportsUnconnectedDeclarations(t *testing.T) {
	snapshot := writeSnapshot(t, snapshotDocument(t, map[string]any{"instance": map[string]any{
		"version": 1, "exposure": "authenticated-web", "replicas": 1,
		"calls": []any{"billing"}, "allows": []any{"swhurl-try-12", "swhurl-try-11"},
	}}))
	status, stdout, diagnostic := runMigrate(t, "--snapshot", snapshot, "--app", "hello", "--env", "staging",
		"--profile", filepath.Join("..", "..", "testdata", "profile.json"),
		"--source-repository", "nginxinc/nginx-unprivileged", "--source-commit", strings.Repeat("b", 40),
		"--out", t.TempDir())
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	edges := migrationOf(t, stdout)["relationships"].(map[string]any)
	if unmet := edges["unmetCalls"].([]any); len(unmet) != 1 || unmet[0] != "billing" {
		t.Errorf("unmet calls %v", unmet)
	}
	// Both granted apps are unconnected in this fixture, so both are reported.
	if unmet := edges["unmetCallers"].([]any); len(unmet) != 2 || unmet[0] != "swhurl-try-11" ||
		unmet[1] != "swhurl-try-12" {
		t.Errorf("unmet callers %v", unmet)
	}
	if declared := edges["declaredCallers"].([]any); len(declared) != 2 {
		t.Errorf("declared callers %v", declared)
	}
	if !strings.Contains(diagnostic, "unresolved declarations reported") {
		t.Errorf("the summary does not name them: %q", diagnostic)
	}
}

// A grant by team travels as the record states it. The record names a team, not a member list, so the
// migration neither widens nor narrows what the instance accepts: the resolution matches the grant against
// each caller's own app record when the pair is decided.
func TestMigrateCarriesATeamGrant(t *testing.T) {
	snapshot := writeSnapshot(t, snapshotDocument(t, map[string]any{"instance": map[string]any{
		"version": 1, "exposure": "authenticated-web", "replicas": 1,
		"allows": []any{"team:demo", "swhurl-try-11"},
	}}))
	destination := t.TempDir()
	status, stdout, diagnostic := runMigrate(t, "--snapshot", snapshot, "--app", "hello", "--env", "staging",
		"--profile", filepath.Join("..", "..", "testdata", "profile.json"),
		"--source-repository", "nginxinc/nginx-unprivileged", "--source-commit", strings.Repeat("b", 40),
		"--out", destination)
	if status != 0 {
		t.Fatalf("exit %d, diagnostic %q", status, diagnostic)
	}
	// The record is the one the migration wrote, and it states the grant beside the app name, sorted.
	written := filepath.Join(destination, "v2-records", "hello", "staging", "instance.json")
	instance, err := kernel.DecodeInstance(readFile(t, written))
	if err != nil {
		t.Fatalf("the written record: %v", err)
	}
	if want := []string{"swhurl-try-11", "team:demo"}; !slices.Equal(instance.Allows, want) {
		t.Fatalf("the record's grants: %v, want %v", instance.Allows, want)
	}
	// The report names it as declared and nothing as unmet: a team grant has no caller of its own to be
	// missing, and the resolution says whether anyone asked and was turned away.
	relationships := migrationOf(t, stdout)["relationships"].(map[string]any)
	if declared := relationships["declaredCallers"].([]any); len(declared) != 2 || declared[1] != "team:demo" {
		t.Errorf("declared callers %v", declared)
	}
	if unmet := relationships["unmetCallers"].([]any); len(unmet) != 1 || unmet[0] != "swhurl-try-11" {
		t.Errorf("unmet callers %v", unmet)
	}
}

// Every migration refusal names what is missing or refused, writes nothing, and leaves the destination
// untouched.
func TestMigrateRefusals(t *testing.T) {
	profile := filepath.Join("..", "..", "testdata", "profile.json")
	snapshot := writeSnapshot(t, snapshotDocument(t, map[string]any{}))
	abbreviated := snapshotDocument(t, map[string]any{"release": map[string]any{
		"version": 1, "image": map[string]any{"repository": "docker.io/nginxinc/nginx-unprivileged",
			"tag": "12-9f3ab21", "digest": "sha256:" + strings.Repeat("e", 64)},
		"kind": "web", "uid": 101, "port": 8080, "healthPath": "/", "offers": "9f3ab21",
	}})
	missingRecords := snapshotDocument(t, map[string]any{})
	missingRecords["instances"].([]any)[0].(map[string]any)["missing"] = []any{"apps/hello/staging/helmrelease.yaml"}
	for _, tt := range []struct {
		name               string
		snapshot           map[string]any
		app, env           string
		repository, commit string
		want               string
	}{
		{"absent", snapshotDocument(t, map[string]any{}), "nope", "staging", "nginxinc/nginx-unprivileged", strings.Repeat("b", 40), "missing: snapshot: instance_not_found"},
		{"missing record", missingRecords, "hello", "staging", "nginxinc/nginx-unprivileged", strings.Repeat("b", 40), "missing: snapshot: apps/hello/staging/helmrelease.yaml"},
		{"source", abbreviated, "hello", "staging", "nginxinc/other", strings.Repeat("b", 40), "invalid: supplied: source.repository"},
		{"commit", abbreviated, "hello", "staging", "nginxinc/nginx-unprivileged", strings.Repeat("b", 40), "invalid: supplied: source.commit: does_not_match_the_tag"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			destination := t.TempDir()
			status, stdout, diagnostic := runMigrate(t, "--snapshot", writeSnapshot(t, tt.snapshot),
				"--app", tt.app, "--env", tt.env, "--profile", profile, "--source-repository", tt.repository,
				"--source-commit", tt.commit, "--out", destination)
			if status != 2 || !strings.Contains(diagnostic, tt.want) {
				t.Fatalf("exit %d: %q, want %q", status, diagnostic, tt.want)
			}
			if stdout != "" {
				t.Fatalf("a refusal wrote a report: %q", stdout)
			}
			if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
				t.Fatalf("a refusal wrote %v files: %v", len(entries), err)
			}
		})
	}
	// A destination inside this checkout's v1 app tree is this checkout's rule, refused before anything is
	// written.
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	status, _, diagnostic := runMigrate(t, "--snapshot", snapshot, "--app", "hello", "--env", "staging",
		"--profile", profile, "--source-repository", "nginxinc/nginx-unprivileged",
		"--source-commit", strings.Repeat("b", 40), "--out", filepath.Join(checkout, "apps", "hello"))
	if status != 2 || !strings.Contains(diagnostic, files.RefusedDestination) {
		t.Fatalf("exit %d: %q", status, diagnostic)
	}
	// The flags a migration cannot run without are a usage error, not a half-checked migration.
	for _, args := range [][]string{
		{"--snapshot", snapshot, "--app", "hello", "--env", "staging", "--profile", profile, "--out", t.TempDir()},
		{"--snapshot", snapshot, "--app", "hello", "--env", "staging", "--profile", profile,
			"--source-repository", "nginxinc/nginx-unprivileged", "--source-commit", strings.Repeat("b", 40)},
	} {
		if status, _, diagnostic := runMigrate(t, args...); status != 2 || !strings.Contains(diagnostic, "usage") {
			t.Fatalf("%v: exit %d: %q", args, status, diagnostic)
		}
	}
}

// statesEvents is whether the predecessor's records give an instance a broker identity its committed pair could
// not state.
func statesEvents(release kernel.ReleaseBundle, spec kernel.InstanceSpec) bool {
	return len(release.Publishes) > 0 || len(spec.Events.Subscriptions) > 0
}

// dependsOnEvents is whether a compiled file's content depends on an instance's broker identity.
func dependsOnEvents(path string) bool {
	return strings.HasSuffix(path, "/helmrelease.json") || strings.HasSuffix(path, "/kustomization.yaml") ||
		strings.HasSuffix(path, "/networkpolicy.json") || strings.HasSuffix(path, "/stream.json") ||
		strings.Contains(path, "/consumer-") || strings.HasSuffix(path, "/events.sops.yaml") ||
		strings.HasPrefix(path, "clusters/home/")
}

// statedEventsKey is whether a predecessor's records give the instance a broker identity.
func statedEventsKey(t *testing.T, snapshot map[string]any) bool {
	t.Helper()
	instances := snapshot["instances"].([]any)
	var settings struct {
		Allocations struct{ EventsKey string } `json:"allocations"`
	}
	records := instances[0].(map[string]any)["records"].(map[string]any)
	data, err := json.Marshal(records["instance"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	return settings.Allocations.EventsKey != ""
}
