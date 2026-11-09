// Package gittest is the fake for git.Reader and git.Snapshots, and the contract suites both implementations run.
package gittest

import (
	"archive/tar"
	"bytes"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
)

// Fake answers each checkout it was given and refuses every other root as the real adapter does. It never runs a
// command, so a use case's tests do not need Git; the contract suite is what keeps its behaviour honest.
type Fake struct {
	Checkouts map[string]git.Checkout
	Err       error
	Reads     []string
	// Revision and Tree are the one commit Snapshots answers: a commit id and that commit's tar archive.
	Revision string
	Tree     []byte
	Heads    []string
}

// New returns a fake that knows one checkout at root.
func New(root string, checkout git.Checkout) *Fake {
	return &Fake{Checkouts: map[string]git.Checkout{root: checkout}}
}

var _ git.Reader = (*Fake)(nil)
var _ git.Snapshots = (*Fake)(nil)

// Read records the root and answers it: a configured failure first, then a known checkout, then the real adapter's
// two refusals, a folder inside a known checkout or one that is not a checkout at all.
func (f *Fake) Read(root string) (git.Checkout, error) {
	f.Reads = append(f.Reads, root)
	if f.Err != nil {
		return git.Checkout{}, f.Err
	}
	if checkout, found := f.Checkouts[root]; found {
		return checkout, nil
	}
	for known := range f.Checkouts {
		if prefix, err := filepath.Rel(known, root); err == nil && !strings.HasPrefix(prefix, "..") {
			return git.Checkout{}, &git.Error{Reason: git.NotTheTopLevel, Detail: filepath.ToSlash(prefix) + "/"}
		}
	}
	return git.Checkout{}, &git.Error{Reason: git.NotARepository}
}

// Scenario is the checkout the contract reads: testdata/checkout committed once, then changed.txt modified,
// removed.txt deleted, moved.txt renamed to renamed.txt in the index, kept.txt renamed to kept-moved.txt in the working
// tree only (added with --intent-to-add), new.txt and fresh/deep.txt left untracked and debug.log ignored.
func Scenario() git.Checkout {
	return git.Checkout{
		Tracked: []string{".gitignore", "changed.txt", "docs/page.txt", "kept-moved.txt", "kept.txt", "removed.txt",
			"renamed.txt"},
		Present: []string{".gitignore", "changed.txt", "docs/page.txt", "fresh/deep.txt", "kept-moved.txt", "new.txt",
			"renamed.txt"},
		Uncommitted: []string{"changed.txt", "fresh/deep.txt", "kept-moved.txt", "kept.txt", "moved.txt", "new.txt",
			"removed.txt", "renamed.txt"},
	}
}

var commit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Contract is the behaviour every git.Reader must have: the Scenario read exactly, one full commit hash reached, and
// a root that is not a checkout's top refused by reason. open returns the reader and the Scenario's root.
func Contract(t *testing.T, open func(*testing.T) (git.Reader, string)) {
	t.Helper()
	reader, root := open(t)
	read, err := reader.Read(root)
	if err != nil {
		t.Fatalf("reading the scenario: %v", err)
	}
	want := Scenario()
	for _, field := range []struct {
		name      string
		got, want []string
	}{{"tracked", read.Tracked, want.Tracked}, {"present", read.Present, want.Present},
		{"uncommitted", read.Uncommitted, want.Uncommitted}} {
		if !slices.Equal(field.got, field.want) {
			t.Errorf("%s = %q, want %q", field.name, field.got, field.want)
		}
	}
	if read.Shallow || len(read.Commits) != 1 || !commit.MatchString(read.Commits[0]) {
		t.Errorf("history: shallow %v, commits %q; want one full hash", read.Shallow, read.Commits)
	}
	if refusal := refused(t, reader, filepath.Join(root, "docs"), git.NotTheTopLevel); refusal != nil && refusal.Detail != "docs/" {
		t.Errorf("a folder inside the checkout names %q, want docs/", refusal.Detail)
	}
	refused(t, reader, t.TempDir(), git.NotARepository)
}

// Head answers the configured commit for a known checkout and the real adapter's refusals otherwise.
func (f *Fake) Head(root string) (string, error) {
	f.Heads = append(f.Heads, root)
	if err := f.known(root); err != nil {
		return "", err
	}
	if !commit.MatchString(f.Revision) {
		return "", &git.Error{Reason: "unrecognised_head"}
	}
	return f.Revision, nil
}

// Archive answers the configured tree only for the configured commit.
func (f *Fake) Archive(root, revision string) ([]byte, error) {
	if err := f.known(root); err != nil {
		return nil, err
	}
	if revision != f.Revision || !commit.MatchString(revision) {
		return nil, &git.Error{Reason: "invalid_revision"}
	}
	return bytes.Clone(f.Tree), nil
}

// known applies Read's root rules to the snapshot reads too.
func (f *Fake) known(root string) error {
	if f.Err != nil {
		return f.Err
	}
	if _, found := f.Checkouts[root]; found {
		return nil
	}
	return &git.Error{Reason: git.NotARepository}
}

// Snapshot is the archive the snapshot contract reads: one commit holding docs/page.txt.
func Snapshot() git.Checkout {
	return git.Checkout{Tracked: []string{"docs/page.txt"}, Present: []string{"docs/page.txt"}}
}

// Tree is a tar archive holding docs/page.txt with the given text.
func Tree(text string) []byte {
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	_ = archive.WriteHeader(&tar.Header{Name: "docs/page.txt", Mode: 0o644, Size: int64(len(text))})
	_, _ = archive.Write([]byte(text))
	_ = archive.Close()
	return buffer.Bytes()
}

// SnapshotContract is the behaviour every git.Snapshots must have: the configured commit's full id and its tree as a
// tar archive, and a revision that is not that commit refused. open returns the reader and the Scenario's root.
func SnapshotContract(t *testing.T, open func(*testing.T) (git.Snapshots, string)) {
	t.Helper()
	snapshots, root := open(t)
	head, err := snapshots.Head(root)
	if err != nil || !commit.MatchString(head) {
		t.Fatalf("head = %q, %v; want one full hash", head, err)
	}
	tree, err := snapshots.Archive(root, head)
	if err != nil {
		t.Fatalf("archiving %s: %v", head, err)
	}
	if !archiveHolds(tree, "docs/page.txt") {
		t.Fatal("the archive holds no docs/page.txt")
	}
	for _, revision := range []string{"main", strings.Repeat("0", 39)} {
		if _, err := snapshots.Archive(root, revision); !refusedSnapshot(err) {
			t.Errorf("archiving %q = %v, want invalid_revision", revision, err)
		}
	}
}

// archiveHolds reports whether a tar archive has a regular file by that name.
func archiveHolds(tree []byte, name string) bool {
	reader := tar.NewReader(bytes.NewReader(tree))
	for {
		entry, err := reader.Next()
		if err != nil {
			return false
		}
		if entry.Typeflag == tar.TypeReg && entry.Name == name {
			return true
		}
	}
}

func refusedSnapshot(err error) bool {
	var refusal *git.Error
	return errors.As(err, &refusal) && refusal.Reason == "invalid_revision"
}

func refused(t *testing.T, reader git.Reader, root, reason string) *git.Error {
	t.Helper()
	_, err := reader.Read(root)
	var refusal *git.Error
	if !errors.As(err, &refusal) || refusal.Reason != reason {
		t.Errorf("reading %s = %v, want %s: %s", root, err, git.RefusedRead, reason)
		return nil
	}
	return refusal
}
