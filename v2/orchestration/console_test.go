package orchestration_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// archive builds one tarball with GitHub's leading folder, from name → text; a name ending in "/" is a symlink.
func archive(entries map[string]string) []byte {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for name, text := range entries {
		header := &tar.Header{Name: "owner-repo-abc/" + name, Mode: 0o644, Size: int64(len(text)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(name, "/") {
			header = &tar.Header{Name: "owner-repo-abc/" + name, Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink}
			text = ""
		}
		_ = writer.WriteHeader(header)
		_, _ = writer.Write([]byte(text))
	}
	_ = writer.Close()
	return buffer.Bytes()
}

// Only the paths a page may show are kept, a path that leaves the tree is refused and a link is never read.
func TestFilterTarball(t *testing.T) {
	files, err := orchestration.FilterTarball(archive(map[string]string{
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
	if _, err := orchestration.FilterTarball(archive(map[string]string{"../escape": "x"}), true); err == nil {
		t.Fatal("a path leaving the tree was accepted")
	}
}

// A record folder is one instance's three records; the folders are listed in app and environment order, and the
// documents every compilation takes are shared.
func TestRecords(t *testing.T) {
	records, err := orchestration.Records(map[string][]byte{
		"v2/testdata/profile.json":             []byte(`{"kind":"profile"}`),
		"v2-records/calls.json":                []byte(`{"kind":"calls"}`),
		"platform/egress/allocation.json":      []byte(`{"kind":"allocation"}`),
		"v2-records/web/staging/instance.json": []byte(`{"application":"web"}`),
		"v2-records/web/staging/release.json":  []byte(`{"kind":"release"}`),
		"v2-records/web/app.json":              []byte(`{"team":"shop"}`),
		"v2-records/api/prod/instance.json":    []byte(`{"application":"api"}`),
		"v2-records/api/prod/release.json":     []byte(`{"kind":"release"}`),
		"v2-records/teams.json":                []byte(`{"teams":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(records.Shared.Profile) != `{"kind":"profile"}` || string(records.Shared.Egress) != `{"kind":"allocation"}` {
		t.Fatalf("shared documents = %#v", records.Shared)
	}
	if len(records.Instances) != 2 || string(records.Instances[0].Instance) != `{"application":"api"}` ||
		string(records.Instances[1].App) != `{"team":"shop"}` || records.Instances[1].Calls == nil {
		t.Fatalf("instances = %#v", records.Instances)
	}
}

type source struct {
	revision string
	trees    map[string]map[string][]byte
	reads    []string
	err      error
	mu       sync.Mutex
}

func (s *source) Revision(context.Context) (string, error) { return s.revision, nil }

func (s *source) Tree(_ context.Context, revision string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, revision)
	if s.err != nil {
		return nil, s.err
	}
	return s.trees[revision], nil
}

// Files answers the paths a known commit's tree holds, and records each read as "files <commit>: <paths>".
func (s *source) Files(_ context.Context, revision string, paths []string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, "files "+revision+": "+strings.Join(paths, " "))
	tree, known := s.trees[revision]
	if s.err != nil || !known {
		return nil, errors.Join(s.err, errors.New("no such commit"))
	}
	found := map[string][]byte{}
	for _, file := range paths {
		if body, held := tree[file]; held {
			found[file] = body
		}
	}
	return found, nil
}

// A commit's tree is read once and kept; the oldest goes when the bound is reached, and a failed read is not kept.
func TestConsoleBundle(t *testing.T) {
	source := &source{revision: "a", trees: map[string]map[string][]byte{"a": {"a": {}}, "b": {"b": {}}}}
	bundle := &orchestration.ConsoleBundle{Source: source, Max: 1}
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
