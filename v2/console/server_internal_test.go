package console

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

// A record folder is one instance's three records; the folders are listed in app and environment order, and the
// documents every compilation takes are shared. An instance without its app record refuses the page's tree.
func TestRecords(t *testing.T) {
	tree := map[string][]byte{
		"v2/testdata/profile.json":             []byte(`{"kind":"profile"}`),
		"v2-records/calls.json":                []byte(`{"kind":"calls"}`),
		"platform/egress/allocation.json":      []byte(`{"kind":"allocation"}`),
		"v2-records/web/staging/instance.json": []byte(`{"application":"web"}`),
		"v2-records/web/staging/release.json":  []byte(`{"kind":"release"}`),
		"v2-records/web/app.json":              []byte(`{"team":"shop"}`),
		"v2-records/api/prod/instance.json":    []byte(`{"application":"api"}`),
		"v2-records/api/prod/release.json":     []byte(`{"kind":"release"}`),
		"v2-records/api/app.json":              []byte(`{"team":"core"}`),
		"v2-records/teams.json":                []byte(`{"teams":{}}`),
	}
	read, err := records(tree)
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Shared.Profile) != `{"kind":"profile"}` || string(read.Shared.Egress) != `{"kind":"allocation"}` {
		t.Fatalf("shared documents = %#v", read.Shared)
	}
	if len(read.Instances) != 2 || string(read.Instances[0].Instance) != `{"application":"api"}` ||
		string(read.Instances[1].App) != `{"team":"shop"}` || read.Instances[1].Calls == nil {
		t.Fatalf("instances = %#v", read.Instances)
	}
	delete(tree, "v2-records/api/app.json")
	if found, err := records(tree); err == nil {
		t.Fatalf("an instance without its app record was read: %#v", found)
	}
}
