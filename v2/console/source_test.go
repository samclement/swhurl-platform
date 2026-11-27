package console_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/samclement/swhurl-platform/v2/console"
)

// Only the paths a page may show are kept, a path that leaves the tree is refused and a link is never read.
func TestFilterTarball(t *testing.T) {
	files, err := console.FilterTarball(archive(map[string]string{
		"v2-records/web/staging/instance.json":           `{"kind":"instance"}`,
		"v2-records/teams.json":                          `{"teams":{}}`,
		"v2-records/web/staging/release.json":            `{"kind":"release"}`,
		"v2-apps/instances/web/staging/helmrelease.json": `{"kind":"HelmRelease"}`,
		"v2-apps/instances/web/staging/offers/api.json":  `{"openapi":"3"}`,
		"v2-apps/instances/web/staging/deployment.json":  `{"kind":"Deployment"}`,
		"v2/testdata/profile.json":                       `{"kind":"profile"}`,
		"platform/egress/allocation.json":                `{"kind":"allocation"}`,
		"docs/notes.md":                                  "not a page's file",
		"v2-records/web/staging/link.json/":              "",
	}), true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"platform/egress/allocation.json", "v2-apps/instances/web/staging/helmrelease.json",
		"v2-apps/instances/web/staging/offers/api.json", "v2-records/teams.json", "v2-records/web/staging/instance.json",
		"v2-records/web/staging/release.json", "v2/testdata/profile.json"}
	got := make([]string, 0, len(files))
	for file := range files {
		got = append(got, file)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("filtered = %q, want %q", got, want)
	}
	if _, err := console.FilterTarball(archive(map[string]string{"../escape": "x"}), true); err == nil {
		t.Fatal("a path leaving the tree was accepted")
	}
}

// A commit's tree is read once and kept; the oldest goes when the bound is reached, and a failed read is not kept.
func TestBundle(t *testing.T) {
	source := &source{revision: "a", trees: map[string]map[string][]byte{"a": {"a": {}}, "b": {"b": {}}}}
	bundle := &console.Bundle{Source: source, Max: 1}
	if revision, err := bundle.Revision(context.Background()); err != nil || revision != "a" {
		t.Fatalf("revision = %q, %v", revision, err)
	}
	for range 2 {
		if _, err := bundle.Tree(context.Background(), "a"); err != nil {
			t.Fatal(err)
		}
	}
	if len(source.reads) != 1 {
		t.Fatalf("the tree was read %d times", len(source.reads))
	}
	if _, err := bundle.Tree(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	_, _ = bundle.Tree(context.Background(), "a")
	if len(source.reads) != 3 {
		t.Fatalf("the bound did not evict the oldest: %q", source.reads)
	}
	source.err = errors.New("GitHub is away")
	if _, err := bundle.Tree(context.Background(), "c"); err == nil {
		t.Fatal("a failed read was answered")
	}
	source.err = nil
	if _, err := bundle.Tree(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
}
