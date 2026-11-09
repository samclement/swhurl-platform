package orchestration

import (
	"slices"
	"strings"
	"testing"
)

// The applied revision is the commit Flux named; two units on one commit are compared once, and a comparison GitHub
// could not list completely is incomplete rather than unaffected.
func TestConsoleComparisons(t *testing.T) {
	commit, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	changes, err := consoleChanges([][]byte{
		[]byte(`{"status":{"lastAppliedRevision":"main@sha1:` + commit + `"}}`),
		[]byte(`{"status":{"lastAppliedRevision":"main@sha1:` + commit + `"}}`),
		[]byte(`{"status":{"lastAppliedRevision":"` + head + `"}}`),
	}, strings.Repeat("c", 40), func(from, to string) ([]string, bool, error) {
		if from == commit {
			return []string{"v2-records/web/staging/instance.json"}, true, nil
		}
		return nil, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Comparisons) != 2 {
		t.Fatalf("comparisons = %#v", changes.Comparisons)
	}
	if !slices.Equal(changes.Comparisons[0].Paths, []string{"v2-records/web/staging/instance.json"}) ||
		changes.Comparisons[0].Status != "complete" || changes.Comparisons[1].Status != "incomplete" {
		t.Fatalf("comparisons = %#v", changes.Comparisons)
	}
}

// A revision that is not one full commit names no comparison.
func TestCommitOf(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for revision, want := range map[string]string{
		"main@sha1:" + commit: commit,
		commit:                commit,
		"main":                "",
		"main@sha1:abc":       "",
	} {
		if got := commitOf(revision); got != want {
			t.Errorf("commitOf(%q) = %q, want %q", revision, got, want)
		}
	}
}
