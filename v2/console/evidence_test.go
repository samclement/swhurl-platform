package console_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/console"
	"github.com/samclement/swhurl-platform/v2/pages"
)

const (
	evidenceCommit = "83705c4d8ae4e4d3331b46c54d5f0a0d1c38f0af"
)

func helm(digest string) string {
	return `{"spec":{"values":{"controllers":{"main":{"containers":{"main":{"image":{"digest":"` + digest + `"}}}}}}}}`
}

// evidenceTree is one merge commit as the console reads it: whole instances, ones missing a record or their
// HelmRelease, stale output without records, and files no render takes.
func evidenceTree() map[string]string {
	return map[string]string{
		"v2-records/web/app.json":                        `{"team":"shop"}`,
		"v2-records/web/prod/instance.json":              `{"replicas":1,"release":"r1"}`,
		"v2-records/web/prod/release.json":               `{"artifact":{"digest":"sha256:a"}}`,
		"v2-apps/instances/web/prod/helmrelease.json":    helm("sha256:a"),
		"v2-records/web/staging/instance.json":           `{"replicas":2,"release":"r2"}`,
		"v2-records/web/staging/release.json":            `{"artifact":{"digest":"sha256:b"}}`,
		"v2-apps/instances/web/staging/offers/api.json":  `{"openapi":"3"}`,
		"v2-records/api/prod/instance.json":              `{"replicas":1}`,
		"v2-records/api/prod/release.json":               `{"artifact":{"digest":"sha256:c"}}`,
		"v2-apps/instances/api/prod/helmrelease.json":    helm("sha256:c"),
		"v2-apps/instances/gone/prod/helmrelease.json":   helm("sha256:d"),
		"v2-records/teams.json":                          `{"teams":{}}`,
		"v2/testdata/profile.json":                       `{"kind":"profile"}`,
		"v2/swhurl-v2":                                   "a binary no render takes",
		"docs/notes.md":                                  "not a page's file",
		"v2-apps/instances/web/prod/deployment.json":     `{"kind":"Deployment"}`,
		"v2-records/web/staging/link.json/":              "", // a link: never read
		"v2-records/orphan/prod/instance.json":           `{}`,
		"v2-apps/instances/orphan/prod/helmrelease.json": helm("sha256:e"),
	}
}

var evidenceUnits = []string{"web/prod", "web/staging", "api/prod", "gone/prod", "orphan/prod", "no/such"}

// archived is the renders the archive path finds: GitHub's tarball, FilterTarball, then ConsoleRenders.
func archived(t *testing.T, files map[string]string) map[string]pages.Render {
	t.Helper()
	fake := &githubtest.ConsoleFake{Commit: evidenceCommit, Archive: archive(files)}
	bundle := &console.Bundle{Source: console.SourceOf{Console: fake, Repository: "owner/repo", Token: "tok"}}
	tree, err := bundle.Tree(context.Background(), evidenceCommit)
	if err != nil {
		t.Fatal(err)
	}
	renders, err := console.ConsoleRenders(tree)
	if err != nil {
		t.Fatal(err)
	}
	return renders
}

func targeted(files map[string]string) (*console.Bundle, *githubtest.ConsoleFake) {
	regular := map[string]string{}
	for file, body := range files {
		if !strings.HasSuffix(file, "/") {
			regular[file] = body
		}
	}
	fake := &githubtest.ConsoleFake{Commit: evidenceCommit, Files: regular, Archive: archive(files)}
	return &console.Bundle{Source: console.SourceOf{Console: fake, Repository: "owner/repo", Token: "tok"}}, fake
}

// A target is exactly the archive's render for that unit, without downloading the archive.
func TestMergeTargetMatchesTheArchive(t *testing.T) {
	files := evidenceTree()
	want := archived(t, files)
	for _, unit := range evidenceUnits {
		bundle, fake := targeted(files)
		got, err := console.MergeTarget(context.Background(), bundle, evidenceCommit, unit)
		if err != nil || got != want[unit] {
			t.Fatalf("%s: %+v, %v; want %+v", unit, got, err, want[unit])
		}
		if !slices.Equal(fake.Reads, []string{"files " + evidenceCommit}) {
			t.Fatalf("reads %q", fake.Reads)
		}
		if bundle.Cached(evidenceCommit) {
			t.Fatal("targeted files kept as a whole tree")
		}
	}
}

func TestMergeTargetMissingFilesMatchTheArchive(t *testing.T) {
	for _, missing := range []string{"v2-records/web/app.json", "v2-records/web/prod/instance.json", "v2-records/web/prod/release.json", "v2-apps/instances/web/prod/helmrelease.json"} {
		t.Run(missing, func(t *testing.T) {
			files := evidenceTree()
			delete(files, missing)
			want := archived(t, files)["web/prod"]
			bundle, _ := targeted(files)
			got, err := console.MergeTarget(context.Background(), bundle, evidenceCommit, "web/prod")
			if err != nil || got != want {
				t.Fatalf("%+v, %v; want %+v", got, err, want)
			}
		})
	}
}

// Malformed data refuses only its own unit, including when the whole tree is already held.
func TestMergeTargetRefusesMalformedDataOnlyForItsUnit(t *testing.T) {
	for _, malformed := range []string{"v2-records/web/staging/instance.json", "v2-records/web/staging/release.json", "v2-apps/instances/web/staging/helmrelease.json"} {
		for _, held := range []bool{false, true} {
			files := evidenceTree()
			files[malformed] = "not json"
			bundle, _ := targeted(files)
			if held {
				if _, err := bundle.Tree(context.Background(), evidenceCommit); err != nil {
					t.Fatal(err)
				}
			}
			got, err := console.MergeTarget(context.Background(), bundle, evidenceCommit, "web/prod")
			if err != nil || got.Runs != "sha256:a" {
				t.Fatalf("other unit: %+v, %v", got, err)
			}
			if _, err := console.MergeTarget(context.Background(), bundle, evidenceCommit, "web/staging"); err == nil || !strings.Contains(err.Error(), "web/staging") {
				t.Fatalf("malformed %s: %v", malformed, err)
			}
		}
	}
}

func TestMergeTargetUsesTheWholeTreeInHand(t *testing.T) {
	bundle, fake := targeted(evidenceTree())
	if _, err := bundle.Tree(context.Background(), evidenceCommit); err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(fake.Reads)
	for _, unit := range []string{"web/prod", "no/such"} {
		got, err := console.MergeTarget(context.Background(), bundle, evidenceCommit, unit)
		if err != nil || got != archived(t, evidenceTree())[unit] {
			t.Fatalf("%s: %+v, %v", unit, got, err)
		}
	}
	if !slices.Equal(fake.Reads, before) {
		t.Fatalf("extra reads %q", fake.Reads)
	}
}

func TestMergeTargetInvalidUnitsNeedNoRead(t *testing.T) {
	bundle, fake := targeted(evidenceTree())
	for _, unit := range []string{"web", "../prod", "web/../prod", "web/prod/x", "./prod", "web/", "/prod", `web\x/prod`} {
		got, err := console.MergeTarget(context.Background(), bundle, evidenceCommit, unit)
		if err != nil || got != (pages.Render{}) {
			t.Fatalf("%s: %+v, %v", unit, got, err)
		}
	}
	if len(fake.Reads) != 0 {
		t.Fatalf("reads %q", fake.Reads)
	}
}

func TestMergeTargetDoesNotTurnReadFailureIntoAbsence(t *testing.T) {
	held := &source{err: errors.New("away")}
	bundle := &console.Bundle{Source: held}
	if _, err := console.MergeTarget(context.Background(), bundle, evidenceCommit, "web/prod"); err == nil {
		t.Fatal("failed read proved absence")
	}
	held.err = nil
	held.trees = map[string]map[string][]byte{evidenceCommit: {}}
	got, err := console.MergeTarget(context.Background(), bundle, evidenceCommit, "web/prod")
	if err != nil || got != (pages.Render{}) || len(held.reads) != 2 {
		t.Fatalf("retry: %+v, %v, reads %q", got, err, held.reads)
	}
}

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
