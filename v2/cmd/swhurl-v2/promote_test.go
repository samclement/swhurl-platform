package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// promoteTree lays the fixture pair out as a checkout keeps them: demo-orders staging selects r42, production r41.
func promoteTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "v2-records")
	for name, files := range map[string]map[string]string{
		"":        {"app.json": "app.json"},
		"staging": {"instance.json": "staging.json", "release.json": "r42.json"},
		"prod":    {"instance.json": "prod.json", "release.json": "r41.json"},
	} {
		folder := filepath.Join(root, recordsFixture, name)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		for to, from := range files {
			if err := os.WriteFile(filepath.Join(folder, to), fixture(t, from), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// promote runs the command against one tree; the expectation is production's own revision unless a test says otherwise.
func promote(t *testing.T, root string, extra ...string) (int, promoteDocument, string, *filestest.Fake) {
	t.Helper()
	if len(extra) == 0 {
		// The revision the caller reviewed is production's, as the tree holds it now.
		revision := "inst-sha256:" + strings.Repeat("0", 64)
		if recorded, err := os.ReadFile(filepath.Join(root, recordsFixture, "prod", "instance.json")); err == nil {
			prod, err := kernel.DecodeInstance(recorded)
			if err != nil {
				t.Fatal(err)
			}
			if computed, err := prod.Revision(); err != nil {
				t.Fatal(err)
			} else {
				revision = string(computed)
			}
		}
		extra = []string{"--expect", revision}
	}
	writer := filestest.New()
	var stdout, diagnostic bytes.Buffer
	args := append([]string{"promote", "--records", root, "--app", recordsFixture, "--env", "prod",
		"--profile", "../../testdata/profile.json", "--out", t.TempDir()}, extra...)
	status := run(args, &stdout, &diagnostic, os.ReadFile, writer)
	var document promoteDocument
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
			t.Fatalf("the report is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return status, document, diagnostic.String(), writer
}

func TestPromoteMovesProductionToStagingsRelease(t *testing.T) {
	status, document, diagnostic, writer := promote(t, promoteTree(t))
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	candidate, err := kernel.DecodeRelease(fixture(t, "r42.json"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := kernel.DecodeRelease(fixture(t, "r41.json"))
	if err != nil {
		t.Fatal(err)
	}
	if document.Kind != promoteKind || document.Version != 1 || !document.Changed {
		t.Fatalf("not one promotion: %+v", document)
	}
	if document.Staging.ReleaseID != string(candidate.ReleaseID) || document.Before.ReleaseID != string(current.ReleaseID) {
		t.Fatalf("staging %s, before %s", document.Staging.ReleaseID, document.Before.ReleaseID)
	}
	if _, found := writer.Contents["v2-records/demo-orders/prod/release.json"]; !found {
		t.Fatalf("no production record was written: %v", keys(writer.Contents))
	}
	release, err := kernel.DecodeRelease(writer.Contents["v2-records/demo-orders/prod/release.json"])
	if err != nil {
		t.Fatal(err)
	}
	if release.ReleaseID != candidate.ReleaseID {
		t.Fatalf("production selects %s, want %s", release.ReleaseID, candidate.ReleaseID)
	}
	if !strings.Contains(string(writer.Contents["v2-apps/instances/demo-orders/prod/helmrelease.json"]),
		candidate.Artifact.Digest) {
		t.Error("the compiled production HelmRelease does not name the promoted digest")
	}
}

func TestPromoteSaysWhenProductionAlreadySelectsIt(t *testing.T) {
	root := promoteTree(t)
	status, _, diagnostic, first := promote(t, root)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	if err := os.WriteFile(filepath.Join(root, recordsFixture, "prod", "instance.json"),
		first.Contents["v2-records/demo-orders/prod/instance.json"], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, recordsFixture, "prod", "release.json"),
		first.Contents["v2-records/demo-orders/prod/release.json"], 0o644); err != nil {
		t.Fatal(err)
	}
	status, document, diagnostic, writer := promote(t, root)
	if status != 0 || document.Changed || writer.Writes != 0 {
		t.Fatalf("exit %d, changed %v, %d writes: %s", status, document.Changed, writer.Writes, diagnostic)
	}
	if !strings.Contains(diagnostic, "already selects") {
		t.Fatalf("the summary does not say it was already selected: %q", diagnostic)
	}
}

func TestPromoteRefusesAReleaseStagingDoesNotSelect(t *testing.T) {
	root := promoteTree(t)
	if err := os.WriteFile(filepath.Join(root, recordsFixture, "staging", "release.json"),
		fixture(t, "r41.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, _, diagnostic, writer := promote(t, root)
	if status != 2 || !strings.Contains(diagnostic, "not the release staging selects") {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	if writer.Writes != 0 {
		t.Fatalf("a refusal wrote %d times", writer.Writes)
	}
}

func TestPromoteRefusesAStaleProductionRevision(t *testing.T) {
	stale := "inst-sha256:" + strings.Repeat("0", 64)
	status, _, diagnostic, _ := promote(t, promoteTree(t), "--expect", stale)
	if status != 2 || !strings.Contains(diagnostic, "instance changed") {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
}

func TestPromoteNeedsProductionInTheTree(t *testing.T) {
	root := promoteTree(t)
	if err := os.Remove(filepath.Join(root, recordsFixture, "prod", "instance.json")); err != nil {
		t.Fatal(err)
	}
	status, _, diagnostic, _ := promote(t, root)
	if status != 1 || !strings.Contains(diagnostic, "instance") {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
}

func TestPromoteRefusesAnIncompleteCommandLine(t *testing.T) {
	writer := filestest.New()
	var stdout, diagnostic bytes.Buffer
	status := run([]string{"promote", "--records", promoteTree(t), "--app", recordsFixture,
		"--profile", "../../testdata/profile.json", "--expect", "x", "--out", t.TempDir()},
		&stdout, &diagnostic, os.ReadFile, writer)
	if status != 2 || !strings.Contains(diagnostic.String(), "usage:") {
		t.Fatalf("a command line without --env: exit %d", status)
	}
}
