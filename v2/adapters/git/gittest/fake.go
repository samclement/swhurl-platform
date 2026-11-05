// Package gittest is the fake for git.Reader and the contract suite both implementations run.
package gittest

import (
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
}

// New returns a fake that knows one checkout at root.
func New(root string, checkout git.Checkout) *Fake {
	return &Fake{Checkouts: map[string]git.Checkout{root: checkout}}
}

var _ git.Reader = (*Fake)(nil)

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
