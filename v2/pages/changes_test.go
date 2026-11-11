package pages

import (
	"strings"
	"testing"
	"time"
)

// The cases are tests/test_console_stages.py's, for the renders v2 makes: records and HelmRelease in one commit.

const pullURL = "https://github.com/samclement/swhurl-platform/pull/9"

var stagesNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func stageJob(action, unit, state string, lines ...string) Job {
	job := Job{ID: 1, Action: action, Unit: unit, Identity: "sam@swhurl.com", Started: stagesNow, State: state, Lines: lines}
	if state != "running" {
		job.Finished = &stagesNow
	}
	if state == "succeeded" {
		job.Link = pullURL
	}
	return job
}

func render(records, settings, digest string) *Render {
	return &Render{Records: records, Settings: settings, Digest: digest, Release: true, Runs: digest}
}

func observed(key, detail string) Observed {
	return Observed{Read: true, Row: &AppRow{Instance: Instance{App: "web", Env: "prod"}, State: State{Key: key, Detail: detail},
		Report: &Report{}}}
}

var healthy = observed(StateHealthy, "")

func TestPreparationIsTheJob(t *testing.T) {
	running := TrackOf(stageJob(NewAppJob, "web/staging", "running", "created repository", "building"), nil, nil)
	if got := [3]string{running.Stages[0].Label, running.Stages[0].State, running.Status}; got != [3]string{"Prepared", "running", "running"} {
		t.Fatalf("running = %v", got)
	}
	if last := running.Stages[len(running.Stages)-1].Label; last != "Live in staging" {
		t.Fatalf("ends = %s", last)
	}
	failed := TrackOf(stageJob("promote", "web/prod", "failed", "[ERROR] build failed"), nil, nil)
	if failed.Status != "failed" || failed.Outcome != "Stopped at Prepared: build failed." {
		t.Fatalf("failed = %+v", failed)
	}
	interrupted := TrackOf(stageJob("promote", "web/prod", "interrupted"), nil, nil)
	if interrupted.Status != "interrupted" || !strings.Contains(interrupted.Outcome, "the console restarted") {
		t.Fatalf("interrupted = %+v", interrupted)
	}
	long := Prepared(stageJob("promote", "web/prod", "failed", "[ERROR] "+strings.Repeat("é", 200)))
	if !strings.HasSuffix(long.Note, "…") || len([]rune(long.Note)) != noteChars+1 {
		t.Fatalf("a long reason is cut by characters: %q", long.Note)
	}
}

func TestFluxJobsEndInsideTheConsole(t *testing.T) {
	reconcile := stageJob("reconcile", "infra-base", "succeeded", "✔ applied revision main@sha1:abc")
	reconcile.Link = ""
	done := TrackOf(reconcile, nil, nil)
	if done.Label() != "Applied" || done.Stages[len(done.Stages)-1].Note != "main@sha1:abc" {
		t.Fatalf("reconcile = %+v", done)
	}
	suspend := stageJob("suspend", "infra-base", "succeeded")
	if TrackOf(suspend, nil, nil).Label() != "Suspended" {
		t.Fatal("suspend ends Suspended")
	}
	if TrackOf(stageJob("promote", "web/prod", "succeeded"), nil, nil).Label() != "No pull request opened" {
		t.Fatal("a job without a pull request ends there")
	}
}

func TestANewerReleaseWithTheSameSettingsCarriesTheChange(t *testing.T) {
	mine := render("records-1", "settings-1", "sha256:a")
	deployed := render("records-2", "settings-1", "sha256:b")
	rescaled := render("records-3", "settings-2", "sha256:a")
	reached := stagesNow.Format(time.RFC3339)
	cases := []struct {
		name, action  string
		merged, main  *Render
		live          Observed
		reached, want string
		newer         string
	}{
		{"a seed's own build finishes its creation", "new", mine, deployed, healthy, "", "live", "sha256:b"},
		{"the newer release must itself be applied", "scale", mine, deployed, observed(StateUpdating, "revision not current"), "", "applying", "sha256:b"},
		{"a promotion chooses its release", "promote", mine, deployed, healthy, "", "superseded", ""},
		{"new settings replace it", "scale", mine, rescaled, healthy, "", "superseded", ""},
		{"seen at its end, it ran", "scale", mine, rescaled, healthy, reached, "replaced", ""},
		{"once it ran, a newer image rolling out is not this change", "scale", mine, deployed,
			observed(StateUpdating, "revision not current"), reached, "replaced", ""},
		{"a tree without settings is never followed", "scale", render("records-1", "", "sha256:a"), render("records-2", "", "sha256:a"),
			healthy, "", "superseded", ""},
		{"a v2 render is its records", "promote", mine, mine, healthy, "", "live", ""},
		{"a newer image carries a scale", "v2-scale", mine, deployed, healthy, "", "live", "sha256:b"},
		{"another app's render change carries it", "v2-scale", mine, render("records-4", "settings-1", "sha256:a"), healthy, "", "live", "-"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ProgressOf(c.action, c.merged, c.main, c.live, c.reached)
			if got.State != c.want || got.Newer != c.newer {
				t.Fatalf("progress = %+v", got)
			}
			if c.want == "replaced" && got.Detail != reached {
				t.Fatalf("replaced keeps when it was seen: %+v", got)
			}
		})
	}
}

// A read that fails after a change was seen at its end does not undo that: it stays where it ends, and the sentence
// says a later read could not check it again. A change never seen there is still unknown.
func TestAFailedReadKeepsAChangeThatWasSeenAtItsEnd(t *testing.T) {
	mine := render("records-1", "settings-1", "sha256:a")
	reached := stagesNow.Format(time.RFC3339)
	cases := map[string]struct {
		merged, main *Render
		live         Observed
	}{
		"the merge commit's tree": {nil, mine, healthy},
		"main's tree":             {mine, nil, healthy},
		"the cluster":             {mine, mine, Observed{}},
	}
	for name, c := range cases {
		if got := ProgressOf("v2-scale", c.merged, c.main, c.live, reached); got.State != "unchecked" || got.Detail != reached {
			t.Errorf("%s: progress = %+v", name, got)
		}
		if got := ProgressOf("v2-scale", c.merged, c.main, c.live, ""); got.State != "unread" {
			t.Errorf("%s, never seen: progress = %+v", name, got)
		}
	}
}

func TestAMissingOrStaleHelmReleaseIsNotApplied(t *testing.T) {
	for _, main := range []*Render{{Records: "records-1", Settings: "settings-1", Digest: "sha256:a"},
		{Records: "records-1", Settings: "settings-1", Digest: "sha256:a", Release: true, Runs: "sha256:old"}} {
		if got := ProgressOf("promote", main, main, healthy, ""); got.State != "applying" {
			t.Fatalf("progress = %+v", got)
		}
	}
}

func TestTheClusterDecidesHowFarItGot(t *testing.T) {
	mine := render("records-1", "settings-1", "sha256:a")
	cases := []struct {
		live Observed
		want string
	}{
		{observed(StateUpdating, "stale condition"), "applying"},
		{healthy, "live"},
		{observed(StateStopped, ""), "zero"},
		{observed(StateFailing, "crash"), "failing"},
		{observed(StateSuspended, ""), "held"},
		{observed(StateUnknown, ""), "unread"},
		{Observed{Read: true, Row: &AppRow{State: State{Key: StateHealthy}}}, "unread"}, // no Go report
		{Observed{Read: true}, "creating"},
		{Observed{}, "unread"},
	}
	for _, c := range cases {
		if got := ProgressOf("promote", mine, mine, c.live, ""); got.State != c.want {
			t.Errorf("%+v: progress = %+v", c.live, got)
		}
	}
	if got := ProgressOf("promote", mine, render("records-2", "settings-1", "sha256:b"), healthy, ""); got.State != "superseded" {
		t.Fatalf("main supersedes an unfinished change: %+v", got)
	}
	if got := ProgressOf("promote", mine, &Render{}, healthy, ""); got.State != "superseded" {
		t.Fatalf("main without it supersedes it: %+v", got)
	}
}

func TestRemoveNeedsGitAndLiveAbsence(t *testing.T) {
	gone, absent := &Render{}, Observed{Read: true}
	cases := []struct {
		merged, main *Render
		live         Observed
		want         string
	}{
		{gone, gone, healthy, "removing"},
		{gone, gone, absent, "removed"},
		{gone, render("records-1", "settings-1", "sha256:a"), absent, "superseded"},
		{nil, gone, absent, "unread"},
		{gone, nil, absent, "unread"},
		{gone, gone, Observed{}, "unread"},
	}
	for _, c := range cases {
		if got := ProgressOf("remove", c.merged, c.main, c.live, ""); got.State != c.want {
			t.Errorf("%+v: progress = %+v", c, got)
		}
	}
	if got := ProgressOf("new", render("records-1", "settings-1", "sha256:a"), render("records-1", "settings-1", "sha256:a"), absent, ""); got.State != "creating" {
		t.Fatalf("new = %+v", got)
	}
	if got := ProgressOf("new", gone, nil, absent, ""); got.State != "historical" {
		t.Fatalf("a merge with no records of its own is history: %+v", got)
	}
}

func pullTrack(landed *Progress, action, unit string, pull Pull) Track {
	pull.Number, pull.URL = 9, pullURL
	return TrackOf(stageJob(action, unit, "succeeded"), &pull, landed)
}

func TestAnOpenReviewWaitsForThePerson(t *testing.T) {
	if got := pullTrack(nil, "promote", "web/prod", Pull{State: "open", Checks: "running"}); got.Status != "waiting" {
		t.Fatalf("running checks = %+v", got)
	}
	manual := pullTrack(nil, "promote", "web/prod", Pull{State: "open", Checks: "passed", Plan: "needs Secret values to be set"})
	if manual.Status != "waiting" || !strings.Contains(manual.Outcome, "merge #9 on GitHub (needs Secret values to be set)") {
		t.Fatalf("manual = %+v", manual)
	}
	if got := pullTrack(nil, "promote", "web/prod", Pull{State: "open", Checks: "failed"}); got.Status != "blocked" {
		t.Fatalf("failed = %+v", got)
	}
	if got := pullTrack(nil, "promote", "web/prod", Pull{State: "open", Checks: "conflict"}); !strings.Contains(got.Outcome, "conflicts with main") {
		t.Fatalf("conflict = %+v", got)
	}
	if got := pullTrack(nil, "promote", "web/prod", Pull{State: "unknown"}); got.Status != "unknown" {
		t.Fatalf("unread = %+v", got)
	}
	team := pullTrack(nil, "team-add", "team:payments", Pull{State: "open"})
	if team.Outcome != "Waiting for you: merge #9 on GitHub; it is then on main." {
		t.Fatalf("team = %q", team.Outcome)
	}
}

func TestMergedChangesShowAppliedAndRunningApart(t *testing.T) {
	cases := []struct{ state, status, label string }{
		{"live", "done", "Live in prod"}, {"zero", "done", "Live in prod"}, {"applying", "running", "In progress"},
		{"creating", "running", "In progress"}, {"removing", "running", "In progress"}, {"failing", "blocked", "Failed"},
		{"held", "waiting", "Waiting for you"}, {"unread", "unknown", "Unknown"}, {"historical", "done", "Merged (historical)"},
		{"superseded", "done", "Superseded"}, {"replaced", "done", "Live in prod"}, {"unchecked", "done", "Live in prod"},
	}
	for _, c := range cases {
		got := pullTrack(&Progress{State: c.state}, "promote", "web/prod", Pull{State: "merged"})
		if got.Status != c.status || got.Label() != c.label {
			t.Errorf("%s: %s %s", c.state, got.Status, got.Label())
		}
	}
	replaced := pullTrack(&Progress{State: "replaced", Detail: stagesNow.Format(time.RFC3339)}, "promote", "web/prod", Pull{State: "merged"})
	if replaced.Outcome != "Live in prod from 18 Oct 12:00 UTC; a newer configuration has since replaced it." {
		t.Fatalf("replaced = %q", replaced.Outcome)
	}
	if last := replaced.Stages[len(replaced.Stages)-1]; last.State != "reached" || last.Note != "then replaced" {
		t.Fatalf("replaced ends = %+v", last)
	}
	superseded := pullTrack(&Progress{State: "superseded"}, "promote", "web/prod", Pull{State: "merged", When: "just now"})
	if superseded.Outcome != "Merged just now; a newer configuration of web/prod replaced it before it was seen live in prod." {
		t.Fatalf("superseded = %q", superseded.Outcome)
	}
	if got := pullTrack(&Progress{State: "live", Newer: "sha256:b"}, "promote", "web/prod", Pull{State: "merged"}); !strings.Contains(got.Outcome, "runs it, with a newer image") {
		t.Fatalf("newer = %q", got.Outcome)
	}
	unchecked := pullTrack(&Progress{State: "unchecked", Detail: stagesNow.Format(time.RFC3339)}, "promote", "web/prod", Pull{State: "merged"})
	if !unchecked.Settled() || unchecked.Outcome != "Live in prod from 18 Oct 12:00 UTC; a later read could not check it again." {
		t.Fatalf("unchecked = %q", unchecked.Outcome)
	}
	if last := unchecked.Stages[len(unchecked.Stages)-1]; last.State != "reached" || last.Note != "not re-read" {
		t.Fatalf("unchecked ends = %+v", last)
	}
	if got := pullTrack(&Progress{State: "unchecked", Detail: stagesNow.Format(time.RFC3339), Newer: "sha256:b"},
		"promote", "web/prod", Pull{State: "merged"}); got.Outcome != "Live in prod from 18 Oct 12:00 UTC, with a newer image; a later read could not check it again." {
		t.Fatalf("unchecked with a newer image = %q", got.Outcome)
	}
	applied := pullTrack(&Progress{State: "failing", Detail: "crash", Applied: true}, "promote", "web/prod", Pull{State: "merged"})
	if got := [4]string{applied.Stages[3].Key, applied.Stages[3].State, applied.Stages[4].Key, applied.Stages[4].State}; got != [4]string{"rollout", "done", "running", "failed"} {
		t.Fatalf("applied and running are separate facts: %v", got)
	}
	removed := pullTrack(&Progress{State: "removed"}, "remove", "web/staging", Pull{State: "merged"})
	if removed.Label() != "Removed from staging" {
		t.Fatalf("removed = %s", removed.Label())
	}
	if team := pullTrack(nil, "team-add", "team:payments", Pull{State: "merged"}); team.Label() != "On main" {
		t.Fatalf("team = %s", team.Label())
	}
}

func TestAClosedNewAppKeepsItsRepository(t *testing.T) {
	closed := Pull{State: "closed", When: "1 h ago"}
	if got := pullTrack(nil, "v2-scale", "web/staging", closed); !strings.Contains(got.Outcome, "nothing changed") {
		t.Fatalf("scale = %q", got.Outcome)
	}
	for _, action := range []string{NewAppJob, FinishAppJob} {
		if got := pullTrack(nil, action, "web/staging", closed); !strings.Contains(got.Outcome, "repository and image made for it still exist") {
			t.Fatalf("%s = %q", action, got.Outcome)
		}
	}
	track := PullTrack(Pull{Number: 9, URL: pullURL, State: "open", Checks: "passed"}, "console/v2-promote-web-staging-abc1234")
	if last := track.Stages[len(track.Stages)-1].Label; last != "Live in prod" {
		t.Fatalf("a promotion's branch ends in production: %s", last)
	}
}

func TestBranchTarget(t *testing.T) {
	cases := map[string][2]string{
		"console/new-my-app-staging-abc1234":         {"new", "my-app/staging"},
		"console/v2-promote-web-staging-abc1234":     {"v2-promote", "web/prod"},
		"console/v2-scale-my-api-prod-0123456789abc": {"v2-scale", "my-api/prod"},
		"console/team-channel-set-payments-abc1234":  {"team-channel-set", "team:payments"},
		"console/team-add-payments-abc1234":          {"team-add", "team:payments"},
		"console/v2-scale-web-dev-abc1234":           {"", ""},
		"console/v2-scale-Web-prod-abc1234":          {"", ""},
		"renovate/flux-2.x":                          {"", ""},
	}
	for branch, want := range cases {
		if action, target := BranchTarget(branch); [2]string{action, target} != want {
			t.Errorf("%s = %s %s", branch, action, target)
		}
	}
}

func TestMergePlan(t *testing.T) {
	body := "Opened by the console.\n\n**Merge plan:** it needs Secret values to be set.\n"
	if got := MergeMode(body); got != "You merge it on GitHub; it needs Secret values to be set" {
		t.Fatalf("mode = %q", got)
	}
	if got := MergeMode("no plan"); got != "You merge it on GitHub" {
		t.Fatalf("mode = %q", got)
	}
}

// A stage's name says what it ends in, so only a stage that happened lets the name stand alone: every other state's
// words are shown beside it.
func TestOnlyAStageThatHappenedHidesItsState(t *testing.T) {
	for state := range stageMarks {
		settled := Stage{State: state}.Settled()
		if want := state == "done" || state == "reached"; settled != want {
			t.Errorf("%s: settled = %v, want %v", state, settled, want)
		}
	}
}
