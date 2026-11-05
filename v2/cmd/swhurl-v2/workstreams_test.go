package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/git/gittest"
)

const workstreamRegistry = "# Workstreams\n\n" +
	"| Workstream | Kind | State | Current slice | Next gate | Needs | Page | Paths |\n" +
	"| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
	"| Plan | main | approved | a slice | the next item | approval | [plan](plan.md) | — |\n"

// workstreamTree writes a registry naming a plain page into a temporary checkout, and a fake Git that lists both.
func workstreamTree(t *testing.T, uncommitted ...string) (string, *gittest.Fake) {
	t.Helper()
	root := t.TempDir()
	for name, text := range map[string]string{"docs/workstreams.md": workstreamRegistry, "docs/plan.md": "# Plan\n"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"docs/plan.md", "docs/workstreams.md"}
	return root, gittest.New(root, git.Checkout{Tracked: files, Present: files, Uncommitted: uncommitted})
}

func runWorkstreams(t *testing.T, reader git.Reader, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := runWith(append([]string{"workstreams"}, args...), &out, &diagnostic, cli{git: reader})
	return status, out.String(), diagnostic.String()
}

func TestWorkstreamsPassesAConsistentRecord(t *testing.T) {
	root, reader := workstreamTree(t)
	status, out, diagnostic := runWorkstreams(t, reader, "--repo-root", root)
	if status != 0 || diagnostic != "" || !strings.Contains(out, "[OK] docs/workstreams.md declares 1 workstreams\n") ||
		!strings.HasSuffix(out, "\n0 error(s), 0 warning(s).\n") {
		t.Fatalf("exit %d\n%s%s", status, out, diagnostic)
	}
}

// A problem is a [BAD] line, counted, and exit 1: here the page the row names is gone.
func TestWorkstreamsFailsOnAProblem(t *testing.T) {
	root, reader := workstreamTree(t)
	if err := os.Remove(filepath.Join(root, "docs/plan.md")); err != nil {
		t.Fatal(err)
	}
	reader.Checkouts[root] = git.Checkout{Tracked: []string{"docs/workstreams.md"}, Present: []string{"docs/workstreams.md"}}
	status, out, _ := runWorkstreams(t, reader, "--repo-root", root)
	if status != 1 || !strings.Contains(out, "[BAD] docs/workstreams.md: Plan: Page plan.md does not exist\n") ||
		!strings.Contains(out, "\n1 error(s), 0 warning(s).\n") {
		t.Fatalf("exit %d\n%s", status, out)
	}
}

func TestWorkstreamsWarnsOnAShallowCheckout(t *testing.T) {
	root, reader := workstreamTree(t)
	checkout := reader.Checkouts[root]
	checkout.Shallow = true
	reader.Checkouts[root] = checkout
	status, out, _ := runWorkstreams(t, reader, "--repo-root", root)
	if status != 0 || !strings.Contains(out, "[WARN] the checkout is shallow") || !strings.Contains(out, "0 error(s), 1 warning(s).") {
		t.Fatalf("exit %d\n%s", status, out)
	}
}

// A root Git refuses is a refused input (exit 2); a failing Git command and an absent root are I/O (exit 1). Each
// says why on stderr and prints nothing on stdout.
func TestWorkstreamsStopsWhenTheCheckoutCannotBeRead(t *testing.T) {
	root, reader := workstreamTree(t)
	status, out, diagnostic := runWorkstreams(t, reader, "--repo-root", filepath.Join(root, "docs"))
	if status != 2 || out != "" || !strings.Contains(diagnostic, "invalid: workstreams: refused_read: not_the_top_level: docs/") {
		t.Fatalf("a folder inside the checkout: exit %d\n%s%s", status, out, diagnostic)
	}
	reader.Err = &git.Error{Reason: "git_exit_128", Detail: "fatal: bad object"}
	status, out, diagnostic = runWorkstreams(t, reader, "--repo-root", root)
	if status != 1 || out != "" || !strings.Contains(diagnostic, "io: workstreams: refused_read: git_exit_128") {
		t.Fatalf("a failing Git command: exit %d\n%s%s", status, out, diagnostic)
	}
	status, _, diagnostic = runWorkstreams(t, reader, "--repo-root", filepath.Join(root, "absent"))
	if status != 1 || !strings.Contains(diagnostic, "cannot open") {
		t.Fatalf("an absent root: exit %d\n%s", status, diagnostic)
	}
}

// Files are opened inside the checkout only: a page the check needs that is a symlink out of it is a failed read,
// and a stray one nothing names is never opened.
func TestWorkstreamsReadsNothingOutsideTheCheckout(t *testing.T) {
	root, reader := workstreamTree(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("# outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs/stray.md")); err != nil {
		t.Fatal(err)
	}
	checkout := reader.Checkouts[root]
	checkout.Present = append(checkout.Present, "docs/stray.md")
	reader.Checkouts[root] = checkout
	if status, out, diagnostic := runWorkstreams(t, reader, "--repo-root", root); status != 0 {
		t.Fatalf("a stray symlink: exit %d\n%s%s", status, out, diagnostic)
	}
	if err := os.Remove(filepath.Join(root, "docs/plan.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs/plan.md")); err != nil {
		t.Fatal(err)
	}
	status, out, diagnostic := runWorkstreams(t, reader, "--repo-root", root)
	if status != 1 || out != "" || !strings.Contains(diagnostic, "io: workstreams:") {
		t.Fatalf("a needed page out of the checkout: exit %d\n%s%s", status, out, diagnostic)
	}
}

func TestWorkstreamsUsage(t *testing.T) {
	_, reader := workstreamTree(t)
	for _, args := range [][]string{{}, {"--repo-root", ""}, {"--repo-root", ".", "--repo-root", "."}, {"--repo-root", ".", "extra"}} {
		if status, out, _ := runWorkstreams(t, reader, args...); status != 2 || out != "" {
			t.Errorf("%q: exit %d, stdout %q", args, status, out)
		}
	}
	if len(reader.Reads) != 0 {
		t.Fatalf("a usage error read the checkout: %q", reader.Reads)
	}
}
