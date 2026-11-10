package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

const (
	evidenceCommit = "83705c4d8ae4e4d3331b46c54d5f0a0d1c38f0af"
	otherCommit    = "ffc3e2ccfbcf40923da0b4bc6c24ff18f66ca2cc"
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
	bundle := &orchestration.ConsoleBundle{Source: orchestration.ConsoleSourceOf{Console: fake, Repository: "owner/repo", Token: "tok"}}
	tree, err := bundle.Tree(context.Background(), evidenceCommit)
	if err != nil {
		t.Fatal(err)
	}
	renders, err := orchestration.ConsoleRenders(tree)
	if err != nil {
		t.Fatal(err)
	}
	return renders
}

// targeted is a fresh evidence cache over GitHub's targeted read of the same commit, and the fake it reads.
func targeted(files map[string]string) (*orchestration.ConsoleEvidence, *githubtest.ConsoleFake) {
	regular := map[string]string{}
	for file, body := range files {
		if !strings.HasSuffix(file, "/") { // GitHub lists a link with its own mode; the fake holds regular files only
			regular[file] = body
		}
	}
	fake := &githubtest.ConsoleFake{Commit: evidenceCommit, Files: regular, Archive: archive(files)}
	source := orchestration.ConsoleSourceOf{Console: fake, Repository: "owner/repo", Token: "tok"}
	return &orchestration.ConsoleEvidence{Bundle: &orchestration.ConsoleBundle{Source: source}}, fake
}

// The targeted read finds what the archive path finds for every unit it is asked for, one at a time or together,
// and the zero Render for one the commit has no instance for; it never downloads the archive.
func TestConsoleEvidenceIsWhatTheArchiveRenders(t *testing.T) {
	files := evidenceTree()
	want := archived(t, files)
	if len(want) != 2 || want["web/prod"].Runs != "sha256:a" || want["web/staging"].Release {
		t.Fatalf("the archive's renders are not the fixture's: %+v", want)
	}
	evidence, fake := targeted(files)
	together, err := evidence.Renders(context.Background(), evidenceCommit, evidenceUnits)
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range evidenceUnits {
		if together[unit] != want[unit] {
			t.Errorf("%s together: %+v, the archive: %+v", unit, together[unit], want[unit])
		}
		alone, _ := targeted(files)
		one, err := alone.Renders(context.Background(), evidenceCommit, []string{unit})
		if err != nil || len(one) != 1 || one[unit] != want[unit] {
			t.Errorf("%s alone: %+v, %v; the archive: %+v", unit, one, err, want[unit])
		}
	}
	if !slices.Equal(fake.Reads, []string{"files " + evidenceCommit}) {
		t.Fatalf("one targeted read and no archive: %q", fake.Reads)
	}
}

// Each of the four files a render takes, missing, means what it means in the archive: no app or instance or release
// record is no instance (absent), and no HelmRelease is an instance not yet applied.
func TestConsoleEvidenceMissingFilesMeanWhatTheyMeanInTheArchive(t *testing.T) {
	for _, missing := range []string{"v2-records/web/app.json", "v2-records/web/prod/instance.json",
		"v2-records/web/prod/release.json", "v2-apps/instances/web/prod/helmrelease.json"} {
		t.Run(missing, func(t *testing.T) {
			files := evidenceTree()
			delete(files, missing)
			want := archived(t, files)["web/prod"]
			evidence, _ := targeted(files)
			got, err := evidence.Renders(context.Background(), evidenceCommit, []string{"web/prod"})
			if err != nil || got["web/prod"] != want {
				t.Fatalf("%+v, %v; the archive: %+v", got, err, want)
			}
			if missing != "v2-apps/instances/web/prod/helmrelease.json" && want != (pages.Render{}) {
				t.Fatalf("the archive kept an instance without %s: %+v", missing, want)
			}
		})
	}
}

// A malformed record of a unit asked for is an error, as it is in the archive, and nothing is kept for it.
func TestConsoleEvidenceRefusesAMalformedRecord(t *testing.T) {
	files := evidenceTree()
	files["v2-records/web/prod/release.json"] = "not json"
	evidence, _ := targeted(files)
	if _, err := evidence.Renders(context.Background(), evidenceCommit, []string{"web/prod"}); err == nil ||
		!strings.Contains(err.Error(), "web/prod") {
		t.Fatalf("a malformed record: %v", err)
	}
	if evidence.Cached(evidenceCommit, []string{"web/prod"}) {
		t.Fatal("a refused read was kept")
	}
}

// A malformed record refuses only the read of the unit it belongs to. The archive path refuses the whole commit, so
// every change merged at it was unread; the targeted read follows the other units' changes, deliberately.
func TestConsoleEvidenceRefusesOnlyTheUnitAskedFor(t *testing.T) {
	files := evidenceTree()
	files["v2-records/web/staging/release.json"] = "not json"
	if _, err := orchestration.ConsoleRenders(mustFilter(t, files)); err == nil {
		t.Fatal("the archive path no longer refuses the whole commit; this test's premise is gone")
	}
	evidence, _ := targeted(files)
	found, err := evidence.Renders(context.Background(), evidenceCommit, []string{"web/prod"})
	if err != nil || found["web/prod"].Runs != "sha256:a" {
		t.Fatalf("another unit's malformed record refused web/prod: %+v, %v", found, err)
	}
	if _, err := evidence.Renders(context.Background(), evidenceCommit, []string{"web/prod", "web/staging"}); err == nil ||
		!strings.Contains(err.Error(), "web/staging") {
		t.Fatalf("a unit asked for with a malformed record: %v", err)
	}
	if evidence.Cached(evidenceCommit, []string{"web/staging"}) {
		t.Fatal("a refused unit was kept")
	}
}

func mustFilter(t *testing.T, files map[string]string) map[string][]byte {
	t.Helper()
	tree, err := orchestration.FilterTarball(archive(files), true)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// evidenceOver is a fresh evidence cache over a recording source holding the commits' trees.
func evidenceOver(max int, trees map[string]map[string]string) (*orchestration.ConsoleEvidence, *source) {
	held := &source{trees: map[string]map[string][]byte{}}
	for commit, files := range trees {
		held.trees[commit] = map[string][]byte{}
		for file, body := range files {
			held.trees[commit][file] = []byte(body)
		}
	}
	return &orchestration.ConsoleEvidence{Bundle: &orchestration.ConsoleBundle{Source: held}, Max: max}, held
}

func (s *source) taken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reads)
}

func filesRead(commit string, units ...string) string {
	var paths []string
	for _, unit := range units {
		app, _, _ := strings.Cut(unit, "/")
		paths = append(paths, "v2-records/"+app+"/app.json", "v2-records/"+unit+"/instance.json",
			"v2-records/"+unit+"/release.json", "v2-apps/instances/"+unit+"/helmrelease.json")
	}
	unique := []string{}
	for _, file := range paths {
		if !slices.Contains(unique, file) {
			unique = append(unique, file)
		}
	}
	return "files " + commit + ": " + strings.Join(unique, " ")
}

// A render is kept by its commit and its unit: one unit in hand says nothing of another at the same commit, or of the
// same unit at another commit, and only the units not in hand are read.
func TestConsoleEvidenceKeepsEachUnitAtEachCommit(t *testing.T) {
	evidence, held := evidenceOver(0, map[string]map[string]string{evidenceCommit: evidenceTree(), otherCommit: evidenceTree()})
	ctx := context.Background()
	if _, err := evidence.Renders(ctx, evidenceCommit, []string{"web/prod"}); err != nil {
		t.Fatal(err)
	}
	if !evidence.Cached(evidenceCommit, []string{"web/prod"}) || evidence.Cached(evidenceCommit, []string{"web/prod", "web/staging"}) ||
		evidence.Cached(otherCommit, []string{"web/prod"}) {
		t.Fatal("a render was kept under the wrong commit or unit")
	}
	both, err := evidence.Renders(ctx, evidenceCommit, []string{"web/prod", "web/staging"})
	if err != nil || both["web/staging"].Records == "" || both["web/prod"].Runs != "sha256:a" {
		t.Fatalf("both: %+v, %v", both, err)
	}
	if _, err := evidence.Renders(ctx, evidenceCommit, []string{"web/staging", "web/prod"}); err != nil {
		t.Fatal(err)
	}
	want := []string{filesRead(evidenceCommit, "web/prod"), filesRead(evidenceCommit, "web/staging")}
	if got := held.taken(); !slices.Equal(got, want) {
		t.Fatalf("reads %q, want %q", got, want)
	}
	if evidence.Bundle.Cached(evidenceCommit) {
		t.Fatal("a unit's files were kept as the commit's whole tree")
	}
}

// An instance the commit was read and proved not to hold is kept as absent; a unit that cannot name an instance is
// absent without a read.
func TestConsoleEvidenceKeepsAProvedAbsence(t *testing.T) {
	evidence, held := evidenceOver(0, map[string]map[string]string{evidenceCommit: evidenceTree()})
	for range 2 {
		found, err := evidence.Renders(context.Background(), evidenceCommit, []string{"no/such"})
		if err != nil || found["no/such"] != (pages.Render{}) {
			t.Fatalf("%+v, %v", found, err)
		}
	}
	if len(held.taken()) != 1 || !evidence.Cached(evidenceCommit, []string{"no/such"}) {
		t.Fatalf("a proved absence is read once and kept: %q", held.taken())
	}
	for _, unit := range []string{"web", "../web/prod", "web/../prod", "web/prod/x", "./prod", "web/", "/prod"} {
		found, err := evidence.Renders(context.Background(), evidenceCommit, []string{unit})
		if err != nil || found[unit] != (pages.Render{}) {
			t.Fatalf("%q: %+v, %v", unit, found, err)
		}
	}
	if len(held.taken()) != 1 {
		t.Fatalf("a unit no instance can have was read: %q", held.taken())
	}
}

// A failed read keeps nothing, so the next read tries again, and then keeps what it finds.
func TestConsoleEvidenceRetriesAFailedRead(t *testing.T) {
	evidence, held := evidenceOver(0, map[string]map[string]string{evidenceCommit: evidenceTree()})
	held.err = errors.New("GitHub is away")
	if found, err := evidence.Renders(context.Background(), evidenceCommit, []string{"web/prod"}); err == nil || found != nil {
		t.Fatalf("a failed read answered %+v", found)
	}
	if evidence.Cached(evidenceCommit, []string{"web/prod"}) {
		t.Fatal("a failed read was kept")
	}
	held.err = nil
	found, err := evidence.Renders(context.Background(), evidenceCommit, []string{"web/prod"})
	if err != nil || found["web/prod"].Runs != "sha256:a" || !evidence.Cached(evidenceCommit, []string{"web/prod"}) {
		t.Fatalf("the retry: %+v, %v", found, err)
	}
	if len(held.taken()) != 2 {
		t.Fatalf("reads %q", held.taken())
	}
}

// The cache keeps at most Max renders, the oldest going first.
func TestConsoleEvidenceIsBounded(t *testing.T) {
	evidence, held := evidenceOver(2, map[string]map[string]string{evidenceCommit: evidenceTree()})
	for _, unit := range []string{"web/prod", "web/staging", "api/prod"} {
		if _, err := evidence.Renders(context.Background(), evidenceCommit, []string{unit}); err != nil {
			t.Fatal(err)
		}
	}
	if evidence.Cached(evidenceCommit, []string{"web/prod"}) || !evidence.Cached(evidenceCommit, []string{"web/staging", "api/prod"}) {
		t.Fatal("the oldest render is the one forgotten")
	}
	if _, err := evidence.Renders(context.Background(), evidenceCommit, []string{"web/prod"}); err != nil || len(held.taken()) != 4 {
		t.Fatalf("a forgotten render is read again: %q, %v", held.taken(), err)
	}
}

// A commit whose whole tree the bundle already holds is answered from it, with no read.
func TestConsoleEvidenceUsesAWholeTreeInHand(t *testing.T) {
	evidence, held := evidenceOver(0, map[string]map[string]string{evidenceCommit: evidenceTree()})
	if _, err := evidence.Bundle.Tree(context.Background(), evidenceCommit); err != nil {
		t.Fatal(err)
	}
	if !evidence.Cached(evidenceCommit, []string{"web/prod", "api/prod"}) {
		t.Fatal("a whole tree in hand costs no read")
	}
	found, err := evidence.Renders(context.Background(), evidenceCommit, []string{"web/prod", "no/such"})
	if err != nil || found["web/prod"].Runs != "sha256:a" || found["no/such"] != (pages.Render{}) {
		t.Fatalf("%+v, %v", found, err)
	}
	if got := held.taken(); !slices.Equal(got, []string{evidenceCommit}) {
		t.Fatalf("only the bundle's own read: %q", got)
	}
}

// Pages read at once: every answer is the commit's renders, and what one kept another can use.
func TestConsoleEvidenceIsSafeForPagesAtOnce(t *testing.T) {
	evidence, _ := evidenceOver(3, map[string]map[string]string{evidenceCommit: evidenceTree(), otherCommit: evidenceTree()})
	want := archived(t, evidenceTree())
	var reading sync.WaitGroup
	failures := make(chan string, 64)
	for i := range 32 {
		reading.Go(func() {
			commit := []string{evidenceCommit, otherCommit}[i%2]
			units := []string{evidenceUnits[i%len(evidenceUnits)], evidenceUnits[(i+1)%len(evidenceUnits)]}
			found, err := evidence.Renders(context.Background(), commit, units)
			if err != nil || len(found) != 2 {
				failures <- fmt.Sprintf("%v: %+v, %v", units, found, err)
				return
			}
			for unit, render := range found {
				if render != want[unit] {
					failures <- unit
				}
			}
			evidence.Cached(commit, units)
		})
	}
	reading.Wait()
	close(failures)
	for failure := range failures {
		t.Errorf("an answer read at once: %s", failure)
	}
}
