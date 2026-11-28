package console

import (
	"context"
	"errors"
	"strings"
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
	return pages.Job{ID: 7, Action: "promote", Unit: "web/prod", Started: changesNow, Finished: &finished, State: "succeeded", Outcomes: true,
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
			Target: func(context.Context, string, string) (pages.Render, error) { return render["web/prod"], treeErr },
			Main:   func(context.Context) (map[string]pages.Render, error) { return render, nil },
			Rows:   func(context.Context) (map[string]*pages.AppRow, error) { return healthy, rowsErr },
			Reach:  func(_ context.Context, job pages.Job) { reached = append(reached, job.ID) },
			Now:    changesNow,
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

// Historical records do not ask for merge files, main or the cluster; even missing readers suffice.
func TestHistoricalChangesNeedNoRenderReads(t *testing.T) {
	job := mergedJob()
	job.Outcomes = false
	for _, reached := range []string{"", "2026-11-09T09:00:00Z"} {
		job.Reached = reached
		got := landings(context.Background(), []pages.Job{job}, map[int]pages.Pull{job.ID: {State: "merged"}}, ChangeReads{})
		want := "historical"
		if reached != "" {
			want = "unchecked"
		}
		if got[job.ID].State != want {
			t.Fatalf("progress = %+v", got[job.ID])
		}
	}
}

// A stored target, including proven absence, is never read again. A failed first read is retried.
func TestActivityCapturesTargetsAndRetriesFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := pages.Render{Records: "r", Settings: "s", Digest: "sha256:a", Release: true, Runs: "sha256:a"}
	job := mergedJob()
	pulls := map[int]pages.Pull{job.ID: {State: "merged", Commit: "merge"}}
	reads, captures, reaches := 0, 0, 0
	fail := true
	checkContext := func(got context.Context) {
		if got != ctx {
			t.Error("page context not forwarded")
		}
	}
	one := ChangeReads{
		Target: func(got context.Context, commit, unit string) (pages.Render, error) {
			checkContext(got)
			reads++
			if commit != "merge" || unit != job.Unit {
				t.Error("wrong target")
			}
			if fail {
				return pages.Render{}, errors.New("away")
			}
			return target, nil
		},
		Main: func(context.Context) (map[string]pages.Render, error) {
			return map[string]pages.Render{job.Unit: target}, nil
		},
		Rows: func(context.Context) (map[string]*pages.AppRow, error) {
			return map[string]*pages.AppRow{job.Unit: {State: pages.State{Key: pages.StateHealthy}, Report: &pages.Report{}}}, nil
		},
		Capture: func(got context.Context, captured pages.Job) {
			checkContext(got)
			captures++
			job.Target = captured.Target
		},
		Reach: func(got context.Context, _ pages.Job) {
			checkContext(got)
			reaches++
			job.Reached = "2026-11-09T09:00:00Z"
		},
	}
	if got := landings(ctx, []pages.Job{job}, pulls, one)[job.ID]; got.State != "unread" || captures != 0 || reaches != 0 {
		t.Fatalf("failed read = %+v", got)
	}
	fail = false
	for range 2 {
		if got := landings(ctx, []pages.Job{job}, pulls, one)[job.ID]; got.State != "live" {
			t.Fatalf("progress = %+v", got)
		}
	}
	if reads != 2 || captures != 1 || reaches != 1 {
		t.Fatalf("reads %d, captures %d, reaches %d", reads, captures, reaches)
	}
	job.Action, job.Target = "remove", &pages.Render{}
	one.Main = func(context.Context) (map[string]pages.Render, error) { return map[string]pages.Render{}, nil }
	one.Rows = func(context.Context) (map[string]*pages.AppRow, error) { return nil, nil }
	if got := landings(ctx, []pages.Job{job}, pulls, one)[job.ID]; got.State != "removed" || reads != 2 {
		t.Fatalf("absence = %+v, reads %d", got, reads)
	}
}
