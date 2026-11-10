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
		Renders: func(_ context.Context, commit string) (map[string]pages.Render, error) {
			mu.Lock()
			defer mu.Unlock()
			if !inTree[commit] { // the bundle answers one it holds without reading it again
				fetched = append(fetched, commit)
				inTree[commit] = true
			}
			return render, nil
		},
		Main:   func(context.Context) (map[string]pages.Render, error) { return render, nil },
		Cached: func(commit string) bool { mu.Lock(); defer mu.Unlock(); return inTree[commit] },
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
	for page, want := range map[int][]string{2: {"c100", "c101"}, 3: {"c100", "c101", "c102"}} {
		rows, _ = ConsoleChanges(context.Background(), jobs, reads, false)
		slices.Sort(fetched)
		if !slices.Equal(fetched, want) {
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
			Renders: func(context.Context, string) (map[string]pages.Render, error) { return render, treeErr },
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
