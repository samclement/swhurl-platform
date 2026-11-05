package git_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/git/gittest"
)

// The real adapter runs the contract on a temporary repository built from the raw fixture in testdata/checkout and
// changed into gittest.Scenario's state.
func TestContract(t *testing.T) {
	requireGit(t)
	gittest.Contract(t, func(t *testing.T) (git.Reader, string) { return git.New(), scenario(t) })
}

// Reading changes nothing: the index and every file are what they were, so `git status` refreshed no index even
// where a file's stat information is stale.
func TestReadWritesNothing(t *testing.T) {
	requireGit(t)
	root := scenario(t)
	stale := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, "docs/page.txt"), stale, stale); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)
	if _, err := git.New().Read(root); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, root); !maps(before, after) {
		t.Fatal("reading the checkout changed a file under it")
	}
}

// A folder Git cannot read is refused with what Git said, and an answer over MaxOutput is refused, not truncated.
func TestRefusalsSayWhy(t *testing.T) {
	requireGit(t)
	_, err := git.New().Read(t.TempDir())
	var refusal *git.Error
	if !errors.As(err, &refusal) || refusal.Reason != git.NotARepository || !strings.Contains(refusal.Detail, "not a git repository") {
		t.Fatalf("an empty folder: %#v", err)
	}
	big := filepath.Join(t.TempDir(), "big")
	script := "#!/bin/sh\nhead -c " + strconv.Itoa(git.MaxOutput+1) + " /dev/zero\n"
	if err := os.WriteFile(big, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (&git.Command{Binary: big}).Read(t.TempDir()); !refused(err, "output_too_large") {
		t.Fatalf("an oversized answer: %v", err)
	}
}

// A shallow clone says so, and its commits are the ones it has.
func TestShallowClone(t *testing.T) {
	requireGit(t)
	source := scenario(t)
	commitAll(t, source, "second")
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, "", "clone", "--quiet", "--depth=1", "file://"+source, clone)
	read, err := git.New().Read(clone)
	if err != nil {
		t.Fatal(err)
	}
	if !read.Shallow || len(read.Commits) != 1 {
		t.Fatalf("a depth-1 clone: shallow %v, %d commits", read.Shallow, len(read.Commits))
	}
}

// Mid-merge, a file one side deleted and the other changed is still on disk and in the index once, whichever side
// deleted it.
func TestAnUnmergedFileIsPresentOnce(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	write(t, root, "a.txt", "base\n")
	write(t, root, "b.txt", "base\n")
	gitIn(t, root, "init", "--quiet", "--object-format=sha1", "--initial-branch=main")
	commitAll(t, root, "base")
	gitIn(t, root, "checkout", "--quiet", "-b", "side")
	write(t, root, "a.txt", "side\n")
	gitIn(t, root, "rm", "--quiet", "b.txt")
	commitAll(t, root, "side")
	gitIn(t, root, "checkout", "--quiet", "main")
	gitIn(t, root, "rm", "--quiet", "a.txt")
	write(t, root, "b.txt", "main\n")
	commitAll(t, root, "main")
	command := exec.Command("git", "-C", root, "merge", "--quiet", "side")
	command.Env = setupEnvironment()
	if out, err := command.CombinedOutput(); err == nil || !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("the merge did not conflict: %v\n%s", err, out)
	}
	read, err := git.New().Read(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "b.txt"}
	if !slices.Equal(read.Present, want) || !slices.Equal(read.Tracked, want) || !slices.Equal(read.Uncommitted, want) {
		t.Fatalf("mid-merge: tracked %q, present %q, uncommitted %q", read.Tracked, read.Present, read.Uncommitted)
	}
}

// GIT_DIR and GIT_INDEX_FILE in the environment do not reach Git: the root alone names the repository.
func TestTheEnvironmentDoesNotChooseTheRepository(t *testing.T) {
	requireGit(t)
	root, other := scenario(t), t.TempDir()
	gitIn(t, other, "init", "--quiet")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	read, err := git.New().Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(read.Tracked, gittest.Scenario().Tracked) {
		t.Fatalf("tracked %q", read.Tracked)
	}
}

// A binary that is not there, and one that fails, are refusals with fixed reasons.
func TestMissingOrFailingCommand(t *testing.T) {
	root := t.TempDir()
	if _, err := (&git.Command{Binary: "git-that-is-not-installed"}).Read(root); !refused(err, "git_not_available") {
		t.Fatalf("a missing binary: %v", err)
	}
	if _, err := (&git.Command{Binary: "false"}).Read(root); !refused(err, git.NotARepository) {
		t.Fatalf("a failing command: %v", err)
	}
}

func refused(err error, reason string) bool {
	var refusal *git.Error
	return errors.As(err, &refusal) && refusal.Reason == reason
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// scenario copies testdata/checkout into a new repository, commits it, and leaves it in gittest.Scenario's state.
func scenario(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("testdata/checkout")); err != nil {
		t.Fatal(err)
	}
	write(t, root, ".gitignore", "*.log\n")
	gitIn(t, root, "init", "--quiet", "--object-format=sha1")
	commitAll(t, root, "first")
	write(t, root, "changed.txt", "after\n")
	if err := os.Remove(filepath.Join(root, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "mv", "moved.txt", "renamed.txt")
	if err := os.Rename(filepath.Join(root, "kept.txt"), filepath.Join(root, "kept-moved.txt")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "--intent-to-add", "kept-moved.txt")
	write(t, root, "new.txt", "new\n")
	write(t, root, "fresh/deep.txt", "deep\n")
	write(t, root, "debug.log", "ignored\n")
	return root
}

func commitAll(t *testing.T, root, message string) {
	t.Helper()
	gitIn(t, root, "add", "--all")
	gitIn(t, root, "commit", "--quiet", "--allow-empty", "-m", message)
}

// gitIn runs one setup command with no user or system configuration, so a signing or hook setting cannot reach it.
func gitIn(t *testing.T, root string, args ...string) {
	t.Helper()
	if root != "" {
		args = append([]string{"-C", root}, args...)
	}
	command := exec.Command("git", args...)
	command.Env = setupEnvironment()
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func setupEnvironment() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
}

func write(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshot is every file under root, .git included, by its bytes.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		found[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func maps(a, b map[string]string) bool {
	keys := func(m map[string]string) []string {
		var k []string
		for key := range m {
			k = append(k, key)
		}
		slices.Sort(k)
		return k
	}
	if !slices.Equal(keys(a), keys(b)) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
