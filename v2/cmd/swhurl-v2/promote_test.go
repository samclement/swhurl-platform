package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/console"
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
	if !slices.Contains(extra, "--expect") {
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
		extra = append(extra, "--expect", revision)
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

// A promotion compiles production from the allocation only when the document records a grant for production, which
// is render's and deploy's rule too: the allocation keeps a port for an instance whose records are gone, and a port
// with nothing to reach must not put that instance behind the proxy. Before this rule promotion passed the document
// whole, so a promoted worker gained proxy variables and a policy rule the next whole-tree render removed.
func TestPromoteGivesProductionTheAllocationOnlyWhenItGrantsSomething(t *testing.T) {
	for _, test := range []struct {
		name        string
		grants      []string
		expectProxy bool
	}{
		{"a port with nothing to reach stays unused", []string{}, false},
		{"a granted host puts production behind the proxy", []string{"example.com:443"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			allocation := filepath.Join(t.TempDir(), "allocation.json")
			document := map[string]any{"kind": "v2-egress", "version": 1, "revision": strings.Repeat("c", 40),
				"instances": []map[string]any{{"application": recordsFixture, "environment": "prod",
					"port": 30107, "grants": test.grants}}}
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(allocation, data, 0o644); err != nil {
				t.Fatal(err)
			}
			status, _, diagnostic, writer := promote(t, promoteTree(t), "--egress", allocation)
			if status != 0 {
				t.Fatalf("exit %d: %s", status, diagnostic)
			}
			helmrelease := string(writer.Contents["v2-apps/instances/demo-orders/prod/helmrelease.json"])
			if got := strings.Contains(helmrelease, "HTTP_PROXY"); got != test.expectProxy {
				t.Fatalf("HTTP_PROXY present: %v, want %v", got, test.expectProxy)
			}
		})
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
	status, _, diagnostic, writer := promote(t, root)
	if status != 2 || !strings.Contains(diagnostic, "demo-orders/prod has no instance.json") || writer.Writes != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
}

// A folder holds the instance its path names: production's folder holding staging's record is refused.
func TestPromoteRefusesAFolderHoldingAnotherInstance(t *testing.T) {
	root := promoteTree(t)
	if err := os.WriteFile(filepath.Join(root, recordsFixture, "prod", "instance.json"), fixture(t, "staging.json"),
		0o644); err != nil {
		t.Fatal(err)
	}
	status, _, diagnostic, writer := promote(t, root)
	if status != 2 || !strings.Contains(diagnostic, "demo-orders/prod holds demo-orders/staging") || writer.Writes != 0 {
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

// The image a person reviewed travels from the console's proposal to the job, which checks it against the commit it
// downloads: a staging that still selects it opens the promotion, one that moved since the review opens nothing.
func TestPromoteProposalCarriesTheReviewedImage(t *testing.T) {
	tree := changeTreeFiles(t)
	tree["v2-records/demo-orders/prod/instance.json"] = string(fixture(t, "prod.json"))
	tree["v2-records/demo-orders/prod/release.json"] = string(fixture(t, "r41.json"))
	staging := "ghcr.io/samclement/demo-orders@sha256:" + strings.Repeat("b", 64)
	stale := "ghcr.io/samclement/demo-orders@sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		image   string
		refusal string
	}{{staging, ""}, {stale, "refused: image: stale_review: staging selects " + staging + ", not the reviewed " + stale}} {
		source := &githubtest.ConsoleFake{Commit: changeRevision, Archive: changeTarball(t, tree)}
		publisher := githubtest.NewPublisher(githubtest.PublisherScenario())
		var stdout, diagnostic bytes.Buffer
		c := cli{out: &stdout, diagnostic: &diagnostic, readFile: os.ReadFile, writer: files.Disk{},
			consoleSource: source, publisher: publisher, stdin: strings.NewReader(""),
			getenv: func(name string) string {
				if name == consoleTokenEnv {
					return "fixture-token"
				}
				return ""
			}}
		proposal := console.ChangeProposal{Kind: "v2-promote", Identity: "operator@example.test", Repository: "owner/repo",
			Request: console.Request{App: "demo-orders", Env: "staging", Image: test.image}}
		_, status := c.openChange(proposalSpec(proposal))
		if test.refusal == "" && (status != 0 || len(publisher.Pulls) != 1) {
			t.Fatalf("the reviewed image exited %d with %d pull requests: %s", status, len(publisher.Pulls), &diagnostic)
		}
		if test.refusal != "" && (status == 0 || len(publisher.Pulls) != 0 || !strings.Contains(diagnostic.String(), test.refusal)) {
			t.Fatalf("a stale review exited %d with %d pull requests: %s", status, len(publisher.Pulls), &diagnostic)
		}
	}
}
