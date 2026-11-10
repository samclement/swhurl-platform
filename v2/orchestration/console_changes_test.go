package orchestration

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/pages"
)

func helmRelease(digest string) []byte {
	return []byte(`{"spec":{"values":{"controllers":{"main":{"containers":{"main":{"image":{"digest":"` + digest + `"}}}}}}}}`)
}

// A tree's renders are its records and committed HelmReleases: an instance without a HelmRelease, or with one for
// another image, is pending; an app without records is absent.
func TestConsoleRenders(t *testing.T) {
	tree := map[string][]byte{
		"v2-records/web/app.json":                      []byte(`{"kind":"app"}`),
		"v2-records/web/prod/instance.json":            []byte(`{"replicas":1,"release":"r"}`),
		"v2-records/web/prod/release.json":             []byte(`{"artifact":{"digest":"sha256:a"}}`),
		"v2-apps/instances/web/prod/helmrelease.json":  helmRelease("sha256:a"),
		"v2-records/web/staging/instance.json":         []byte(`{"replicas":2}`),
		"v2-records/web/staging/release.json":          []byte(`{"artifact":{"digest":"sha256:b"}}`),
		"v2-apps/instances/gone/prod/helmrelease.json": helmRelease("sha256:c"),
		"v2-records/web/staging/offers/openapi.json":   []byte(`{}`),
		"v2-records/orphan/prod/instance.json":         []byte(`{}`),
	}
	renders, err := ConsoleRenders(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(renders) != 2 {
		t.Fatalf("renders = %v", renders)
	}
	if prod := renders["web/prod"]; prod.Pending() || prod.Digest != "sha256:a" || !prod.Release {
		t.Fatalf("prod = %+v", prod)
	}
	if staging := renders["web/staging"]; !staging.Pending() || staging.Release {
		t.Fatalf("staging without a HelmRelease is pending: %+v", staging)
	}
	if renders["web/prod"].Settings == renders["web/staging"].Settings {
		t.Fatal("different instance records are different settings")
	}
	rescaled := map[string][]byte{}
	for path, body := range tree {
		rescaled[path] = body
	}
	rescaled["v2-records/web/prod/instance.json"] = []byte(`{"replicas":1,"release":"s"}`)
	again, _ := ConsoleRenders(rescaled)
	if again["web/prod"].Settings != renders["web/prod"].Settings || again["web/prod"].Records == renders["web/prod"].Records {
		t.Fatal("a new release is new records with the same settings")
	}
	tree["v2-records/web/prod/release.json"] = []byte(`not json`)
	if _, err := ConsoleRenders(tree); err == nil || !strings.Contains(err.Error(), "web/prod") {
		t.Fatalf("a malformed record is an error naming it: %v", err)
	}
}

var changesNow = time.Date(2026, 11, 9, 9, 43, 0, 0, time.UTC)

func mergedJob() pages.Job {
	finished := changesNow
	return pages.Job{ID: 7, Action: "promote", Unit: "web/prod", Started: changesNow, Finished: &finished, State: "succeeded",
		Link: "https://github.com/o/r/pull/41"}
}

// Without a token the open pull requests are not listed, nothing is read by number, and a merged change's progress
// is unknown rather than assumed.
func TestConsoleChangesWithoutAToken(t *testing.T) {
	reads := ChangeReads{OpenPulls: func(context.Context) ([]pages.OpenPull, string) {
		return nil, "No GitHub token is configured, so open pull requests are not listed."
	}, Now: changesNow}
	rows, problem := ConsoleChanges(context.Background(), []pages.Job{mergedJob()}, reads, true)
	if !strings.HasPrefix(problem, "No GitHub token") || len(rows) != 1 || rows[0].Track.Status != "unknown" {
		t.Fatalf("rows = %+v, problem = %q", rows, problem)
	}
}

// Activity reads the historical trees it needs one at a time: a tree already in hand costs nothing, the newest
// change that is not is the one it fetches, and the rest are left unread for a later read rather than every archive
// at once.
func TestActivityReadsOneHistoricalTreePerPage(t *testing.T) {
	if got := toFetch([]string{"a", "b", "c"}, nil); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("the newest, one per page: %v", got)
	}
	inHand := func(commit string) bool { return commit == "a" || commit == "c" }
	if got := toFetch([]string{"a", "b", "c", "d"}, inHand); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("a tree in hand costs no fetch: %v", got)
	}

	render := map[string]pages.Render{"web/prod": {Records: "r", Settings: "s", Digest: "sha256:a", Release: true, Runs: "sha256:a"}}
	healthy := map[string]*pages.AppRow{"web/prod": {State: pages.State{Key: pages.StateHealthy}, Report: &pages.Report{}}}
	jobs := make([]pages.Job, 0, historicalTreesPerPage+2)
	for i := range historicalTreesPerPage + 2 {
		job := mergedJob()
		job.ID, job.Link = i+1, "https://github.com/o/r/pull/"+strconv.Itoa(100+i)
		jobs = append(jobs, job)
	}
	var mu sync.Mutex
	inTree := map[string]bool{}
	var fetched []string
	reads := ChangeReads{
		OpenPulls: func(context.Context) ([]pages.OpenPull, string) { return nil, "" },
		Pull: func(_ context.Context, number int) (pages.Pull, error) {
			return pages.Pull{State: "merged", When: "2026-11-09T09:00:00Z", Commit: "c" + strconv.Itoa(number)}, nil
		},
		Renders: func(_ context.Context, commit string, _ []string) (map[string]pages.Render, error) {
			mu.Lock()
			defer mu.Unlock()
			if !inTree[commit] { // the bundle answers one it holds without reading it again
				fetched = append(fetched, commit)
				inTree[commit] = true
			}
			return render, nil
		},
		Main:   func(context.Context) (map[string]pages.Render, error) { return render, nil },
		Cached: func(commit string, _ []string) bool { mu.Lock(); defer mu.Unlock(); return inTree[commit] },
		Rows:   func(context.Context) (map[string]*pages.AppRow, error) { return healthy, nil },
		Now:    changesNow,
	}
	moved := func(rows []pages.ChangeRow) []pages.ChangeRow {
		var found []pages.ChangeRow
		for _, row := range rows {
			if !row.Track.Settled() {
				found = append(found, row)
			}
		}
		return found
	}
	rows, _ := ConsoleChanges(context.Background(), jobs, reads, false)
	slices.Sort(fetched)
	if len(fetched) != historicalTreesPerPage || !slices.Equal(fetched, []string{"c100"}) {
		t.Fatalf("one page fetches the newest %d: %v", historicalTreesPerPage, fetched)
	}
	if left := moved(rows); len(left) != 2 {
		t.Fatalf("the trees it did not reach are unknown, not assumed: %v", left)
	}
	for page := 2; page <= 3; page++ {
		rows, _ = ConsoleChanges(context.Background(), jobs, reads, false)
		slices.Sort(fetched)
		if want := []string{"c100", "c101", "c102"}[:page]; !slices.Equal(fetched, want) {
			t.Fatalf("page %d fetches the next one and refetches nothing in hand: %v", page, fetched)
		}
		if left := moved(rows); len(left) != 3-page {
			t.Fatalf("page %d leaves %d unread: %v", page, 3-page, left)
		}
	}
}

// A read that fails proves nothing: the cluster unread leaves a merged change unknown, and the merge commit's tree
// unread does too; a change seen live is recorded once.
func TestConsoleChangesFollowAMergeToTheCluster(t *testing.T) {
	render := map[string]pages.Render{"web/prod": {Records: "r", Settings: "s", Digest: "sha256:a", Release: true, Runs: "sha256:a"}}
	healthy := map[string]*pages.AppRow{"web/prod": {State: pages.State{Key: pages.StateHealthy}, Report: &pages.Report{}}}
	var reached []int
	reads := func(rowsErr, treeErr error) ChangeReads {
		return ChangeReads{
			OpenPulls: func(context.Context) ([]pages.OpenPull, string) { return nil, "" },
			Pull: func(context.Context, int) (pages.Pull, error) {
				return pages.Pull{State: "merged", When: "2026-11-09T09:00:00Z", Commit: "c"}, nil
			},
			Renders: func(context.Context, string, []string) (map[string]pages.Render, error) { return render, treeErr },
			Main:    func(context.Context) (map[string]pages.Render, error) { return render, nil },
			Rows:    func(context.Context) (map[string]*pages.AppRow, error) { return healthy, rowsErr },
			Reach:   func(job pages.Job) { reached = append(reached, job.ID) },
			Now:     changesNow,
		}
	}
	rows, _ := ConsoleChanges(context.Background(), []pages.Job{mergedJob()}, reads(nil, nil), false)
	if rows[0].Track.Label() != "Live in prod" || len(reached) != 1 {
		t.Fatalf("track = %+v, reached = %v", rows[0].Track, reached)
	}
	if note := rows[0].Track.Stages[2].Note; note != "merged 43 min ago" {
		t.Fatalf("merge note = %q", note)
	}
	if link := rows[0].Track.Stages[2].Link; link != "https://github.com/o/r/pull/41" {
		t.Fatalf("a merged pull request links where the job said: %q", link)
	}
	for name, failing := range map[string]ChangeReads{"cluster": reads(errors.New("down"), nil), "tree": reads(nil, errors.New("gone"))} {
		job := mergedJob()
		rows, _ := ConsoleChanges(context.Background(), []pages.Job{job}, failing, false)
		if rows[0].Track.Status != "unknown" {
			t.Errorf("%s unread: %+v", name, rows[0].Track)
		}
	}
	if len(reached) != 1 {
		t.Fatalf("only what was seen is recorded: %v", reached)
	}
}

// treeSource is a console source of fixed trees that records each targeted read, and fails while err is set.
type treeSource struct {
	mu    sync.Mutex
	trees map[string]map[string][]byte
	reads []string
	err   error
}

func (s *treeSource) Revision(context.Context) (string, error) {
	return "", errors.New("not read here")
}

func (s *treeSource) Tree(context.Context, string) (map[string][]byte, error) {
	return nil, errors.New("a page's merge commits are never read whole")
}

func (s *treeSource) Files(_ context.Context, revision string, paths []string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, revision+": "+strings.Join(paths, " "))
	if s.err != nil {
		return nil, s.err
	}
	found := map[string][]byte{}
	for _, file := range paths {
		if body, held := s.trees[revision][file]; held {
			found[file] = body
		}
	}
	return found, nil
}

func (s *treeSource) taken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reads)
}

// unitTree is one commit with web/prod and web/staging both applied.
func unitTree() map[string][]byte {
	return map[string][]byte{
		"v2-records/web/app.json":                        []byte(`{"kind":"app"}`),
		"v2-records/web/prod/instance.json":              []byte(`{"replicas":1}`),
		"v2-records/web/prod/release.json":               []byte(`{"artifact":{"digest":"sha256:a"}}`),
		"v2-apps/instances/web/prod/helmrelease.json":    helmRelease("sha256:a"),
		"v2-records/web/staging/instance.json":           []byte(`{"replicas":2}`),
		"v2-records/web/staging/release.json":            []byte(`{"artifact":{"digest":"sha256:a"}}`),
		"v2-apps/instances/web/staging/helmrelease.json": helmRelease("sha256:a"),
	}
}

// evidencePage is the reads one Activity page takes through a real evidence cache: each job's pull request merged at
// the commit merges names by job, main the same tree, and both instances healthy on the cluster.
func evidencePage(source *treeSource, merges map[int]string) ChangeReads {
	evidence := &ConsoleEvidence{Bundle: &ConsoleBundle{Source: source}}
	main, _ := ConsoleRenders(unitTree())
	healthy := map[string]*pages.AppRow{}
	for _, unit := range []string{"web/prod", "web/staging"} {
		healthy[unit] = &pages.AppRow{State: pages.State{Key: pages.StateHealthy}, Report: &pages.Report{}}
	}
	return ChangeReads{
		OpenPulls: func(context.Context) ([]pages.OpenPull, string) { return nil, "" },
		Pull: func(_ context.Context, number int) (pages.Pull, error) {
			return pages.Pull{State: "merged", When: "2026-11-09T09:00:00Z", Commit: merges[number]}, nil
		},
		Renders: evidence.Renders, Cached: evidence.Cached,
		Main: func(context.Context) (map[string]pages.Render, error) { return main, nil },
		Rows: func(context.Context) (map[string]*pages.AppRow, error) { return healthy, nil },
		Now:  changesNow,
	}
}

func landingJob(id int, unit string) pages.Job {
	job := mergedJob()
	job.ID, job.Unit, job.Link = id, unit, "https://github.com/o/r/pull/"+strconv.Itoa(id)
	return job
}

func states(found map[int]*pages.Progress) map[int]string {
	named := map[int]string{}
	for id, progress := range found {
		named[id] = progress.State
	}
	return named
}

// Units sharing a merge commit are one read on one page, and both changes are followed from it.
func TestActivityReadsAMergeCommitsUnitsTogether(t *testing.T) {
	commit := strings.Repeat("a", 40)
	source := &treeSource{trees: map[string]map[string][]byte{commit: unitTree()}}
	jobs := []pages.Job{landingJob(2, "web/staging"), landingJob(1, "web/prod")}
	reads := evidencePage(source, map[int]string{1: commit, 2: commit})
	pulls := map[int]pages.Pull{1: {State: "merged", Commit: commit}, 2: {State: "merged", Commit: commit}}
	found := landings(context.Background(), jobs, pulls, reads)
	if got := states(found); got[1] != "live" || got[2] != "live" {
		t.Fatalf("progress %v", got)
	}
	want := commit + ": v2-records/web/app.json v2-records/web/staging/instance.json v2-records/web/staging/release.json " +
		"v2-apps/instances/web/staging/helmrelease.json v2-records/web/prod/instance.json v2-records/web/prod/release.json " +
		"v2-apps/instances/web/prod/helmrelease.json"
	if got := source.taken(); !slices.Equal(got, []string{want}) {
		t.Fatalf("reads %q", got)
	}
}

// A commit in hand for one unit is not in hand for another: the new unit spends the page's one read, and the older
// commit waits for the next page, which reads it without reading the first again.
func TestActivityCountsANewUnitAtACommitInHand(t *testing.T) {
	newer, older := strings.Repeat("a", 40), strings.Repeat("b", 40)
	source := &treeSource{trees: map[string]map[string][]byte{newer: unitTree(), older: unitTree()}}
	merges := map[int]string{1: newer, 2: older, 3: newer}
	reads := evidencePage(source, merges)
	pulls := map[int]pages.Pull{}
	for number, commit := range merges {
		pulls[number] = pages.Pull{State: "merged", Commit: commit}
	}
	landings(context.Background(), []pages.Job{landingJob(1, "web/prod")}, pulls, reads)
	jobs := []pages.Job{landingJob(3, "web/staging"), landingJob(2, "web/prod"), landingJob(1, "web/prod")}
	if got := states(landings(context.Background(), jobs, pulls, reads)); got[3] != "live" || got[1] != "live" || got[2] != "unread" {
		t.Fatalf("second page: %v", got)
	}
	if got := states(landings(context.Background(), jobs, pulls, reads)); got[1] != "live" || got[2] != "live" || got[3] != "live" {
		t.Fatalf("third page: %v", got)
	}
	taken := source.taken()
	if len(taken) != 3 || !strings.HasPrefix(taken[0], newer+": v2-records/web/app.json v2-records/web/prod/") ||
		taken[1] != newer+": v2-records/web/app.json v2-records/web/staging/instance.json v2-records/web/staging/release.json v2-apps/instances/web/staging/helmrelease.json" ||
		!strings.HasPrefix(taken[2], older+": ") {
		t.Fatalf("reads %q", taken)
	}
}

// A read that fails leaves a change already seen at its end where it was, says nothing new, and keeps nothing: the
// next page reads again.
func TestAFailedEvidenceReadKeepsAReachedChange(t *testing.T) {
	commit := strings.Repeat("a", 40)
	source := &treeSource{trees: map[string]map[string][]byte{commit: unitTree()}, err: errors.New("GitHub is away")}
	reads := evidencePage(source, map[int]string{1: commit, 2: commit})
	var recorded []int
	reads.Reach = func(job pages.Job) { recorded = append(recorded, job.ID) }
	seen, unseen := landingJob(1, "web/prod"), landingJob(2, "web/staging")
	seen.Reached = "2026-11-09T09:10:00Z"
	pulls := map[int]pages.Pull{1: {State: "merged", Commit: commit}, 2: {State: "merged", Commit: commit}}
	jobs := []pages.Job{unseen, seen}
	if got := states(landings(context.Background(), jobs, pulls, reads)); got[1] != "unchecked" || got[2] != "unread" {
		t.Fatalf("while GitHub is away: %v", got)
	}
	if len(recorded) != 0 || reads.Cached(commit, []string{"web/prod"}) {
		t.Fatalf("a failed read recorded %v or was kept", recorded)
	}
	source.mu.Lock()
	source.err = nil
	source.mu.Unlock()
	if got := states(landings(context.Background(), jobs, pulls, reads)); got[1] != "live" || got[2] != "live" {
		t.Fatalf("once GitHub answers: %v", got)
	}
	if !slices.Equal(recorded, []int{2}) || len(source.taken()) != 2 {
		t.Fatalf("recorded %v, reads %q", recorded, source.taken())
	}
}
