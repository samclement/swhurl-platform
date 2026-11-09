package gittest_test

import (
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/git/gittest"
)

func TestContract(t *testing.T) {
	gittest.Contract(t, func(t *testing.T) (git.Reader, string) {
		root := t.TempDir()
		checkout := gittest.Scenario()
		checkout.Commits = []string{"0123456789abcdef0123456789abcdef01234567"}
		return gittest.New(root, checkout), root
	})
}

// The fake's snapshots answer only the configured commit.
func TestSnapshotContract(t *testing.T) {
	gittest.SnapshotContract(t, func(t *testing.T) (git.Snapshots, string) {
		root := t.TempDir()
		f := gittest.New(root, gittest.Snapshot())
		f.Revision = "0123456789abcdef0123456789abcdef01234567"
		f.Tree = gittest.Tree("page\n")
		return f, root
	})
}

// The fake records which root it was asked for, so a use case's test can assert what was read, and it can fail on
// purpose.
func TestFakeRecordsTheReadAndCanFail(t *testing.T) {
	f := gittest.New("/repo", git.Checkout{})
	if _, err := f.Read("/repo"); err != nil {
		t.Fatal(err)
	}
	f.Err = &git.Error{Reason: "git_exit_128: fatal"}
	if _, err := f.Read("/repo"); !errors.Is(err, f.Err) {
		t.Fatalf("a configured failure = %v", err)
	}
	if len(f.Reads) != 2 || f.Reads[0] != "/repo" {
		t.Fatalf("the reads: %q", f.Reads)
	}
}
