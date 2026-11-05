package orchestration

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/git/gittest"
)

// The use case reads what the check uses, in the rounds Needs names: the registry, then its page; never a present
// Markdown file nothing names.
func TestCheckWorkstreamsReadsWhatTheCheckUses(t *testing.T) {
	registry := "| Workstream | Kind | State | Current slice | Next gate | Needs | Page | Paths |\n" +
		"| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
		"| Plan | main | approved | a slice | the next item | approval | [plan](plan.md) | — |\n"
	present := []string{"README.md", "docs/plan.md", "docs/workstreams.md"}
	reader := gittest.New("/repo", git.Checkout{Tracked: present, Present: present})
	var read []string
	results, err := CheckWorkstreams("/repo", reader, func(path string) ([]byte, error) {
		read = append(read, path)
		if path == "docs/workstreams.md" {
			return []byte(registry), nil
		}
		return []byte("# Plan\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(read, []string{"docs/workstreams.md", "docs/plan.md"}) || !slices.Equal(reader.Reads, []string{"/repo"}) {
		t.Fatalf("read %q from %q", read, reader.Reads)
	}
	for _, result := range results {
		if len(result.Problems) > 0 {
			t.Fatalf("results: %+v", results)
		}
	}
}

// A missing registry is a finding about the record, not a failed read.
func TestCheckWorkstreamsWithoutARegistryIsAFinding(t *testing.T) {
	results, err := CheckWorkstreams("/repo", gittest.New("/repo", git.Checkout{}), func(string) ([]byte, error) {
		t.Fatal("nothing to read")
		return nil, nil
	})
	if err != nil || len(results) != 1 || results[0].Problems[0] != "docs/workstreams.md is missing" {
		t.Fatalf("results %+v, error %v", results, err)
	}
}

// A refused Git read and a failed file read each stop the use case with their own error.
func TestCheckWorkstreamsStopsOnAFailedRead(t *testing.T) {
	if _, err := CheckWorkstreams("/elsewhere", gittest.New("/repo", git.Checkout{}), nil); err == nil ||
		!strings.Contains(err.Error(), "not_a_repository") {
		t.Fatalf("an unknown root: %v", err)
	}
	failed := errors.New("unreadable")
	reader := gittest.New("/repo", git.Checkout{Present: []string{"docs/workstreams.md"}})
	if _, err := CheckWorkstreams("/repo", reader, func(string) ([]byte, error) { return nil, failed }); !errors.Is(err, failed) {
		t.Fatalf("a failed read: %v", err)
	}
}
