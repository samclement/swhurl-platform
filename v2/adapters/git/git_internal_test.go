package git

import (
	"os"
	"slices"
	"testing"
)

// Copies are reported only with status.renames=copies, so the raw answer is a fixture: a copy in either column names
// its source as the next field, and paths keep their spaces.
func TestParseStatusReadsCopiesInEitherColumn(t *testing.T) {
	raw, err := os.ReadFile("testdata/status-copies.z")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"both.txt", "copy.txt", "new.txt", "origin with spaces.txt", "source.txt", "worktree copy.txt"}
	if got := uncommitted(entries); !slices.Equal(got, want) {
		t.Fatalf("uncommitted %q", got)
	}
	if _, err := parseStatus(append(raw, "C  orphan.txt\x00"...)); err == nil {
		t.Fatal("a copy without its source was read")
	}
}
