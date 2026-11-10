package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The fixture is tests/render_console_goldens.py's activity_jobs and github_answers: jobs in every state, read from
// the store, and their pull requests open, merged, closed and one no job opened. web/prod's promotion merged at
// mergedCommit and runs on the cluster.
const (
	activityRepo = "https://github.com/samclement/swhurl-platform"
	mergedCommit = "cccccccccccccccccccccccccccccccccccccccc"
)

var activityNow = time.Date(2026, 11, 9, 9, 43, 0, 0, time.UTC)

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, time.UTC)
}

func ended(day, hour, minute int) *time.Time {
	moment := at(day, hour, minute)
	return &moment
}

func activityJobs() []pages.Job {
	return []pages.Job{
		{ID: 9, Action: "reconcile", Unit: "infra-base", Identity: "sam@swhurl.com", Started: at(9, 9, 42), State: "running",
			Lines: []string{"Reconciling", "revision 1234567890abcdef1234567890abcdef12345678 <applied>"}},
		{ID: 8, Action: "scale", Unit: "web/staging", Identity: "sam@swhurl.com", Started: at(9, 9, 30), State: "succeeded",
			Finished: ended(9, 9, 31), Link: activityRepo + "/pull/42", Lines: []string{"Opened"}},
		{ID: 7, Action: "promote", Unit: "web/prod", Identity: "sam@swhurl.com", Started: at(9, 8, 0), State: "succeeded",
			Finished: ended(9, 8, 2), Link: activityRepo + "/pull/41", Lines: []string{"Downloading", "Opened"}},
		{ID: 6, Action: "remove", Unit: "my-api/staging", Identity: "sam@swhurl.com", Started: at(9, 7, 0), State: "failed",
			Finished: ended(9, 7, 0), Lines: []string{"[ERROR] <b>refused</b>"}},
		{ID: 5, Action: "new app and repository", Unit: "shop/staging", Identity: "sam@swhurl.com", Started: at(9, 6, 0),
			State: "interrupted", Finished: ended(9, 6, 10), Lines: []string{"created repository"}, Resume: map[string]any{"kind": "new-app"}},
		{ID: 4, Action: "team add", Unit: "team:payments", Identity: "sam@swhurl.com", Started: at(7, 12, 0), State: "succeeded",
			Finished: ended(7, 12, 1), Link: activityRepo + "/pull/40"},
		{ID: 3, Action: "resume", Unit: "infra-base", Identity: "sam@swhurl.com", Started: at(9, 5, 0), State: "succeeded",
			Finished: ended(9, 5, 2), Lines: []string{"✔ applied revision main@sha1:abc1234"}},
	}
}

func activityGitHub() *githubtest.ConsoleFake {
	return &githubtest.ConsoleFake{
		Pulls: []github.Pull{
			{Number: 43, Title: "[console] apps: change access to web/prod", URL: activityRepo + "/pull/43",
				Branch: "console/v2-expose-web-prod-abc1234", Opened: "2026-11-08", Checks: "failed",
				ChecksURL: activityRepo + "/actions/runs/eeee"},
			{Number: 42, Title: "[console] apps: scale web/staging", URL: activityRepo + "/pull/42",
				Branch: "console/v2-scale-web-staging-abc1234", Opened: "2026-11-09", Checks: "passed",
				Body: "Scale it.\n\n**Merge plan:** it needs Secret values to be set.\n", ChecksURL: activityRepo + "/actions/runs/dddd"},
		},
		ByNumber: map[int]github.Pull{
			41: {Number: 41, State: "merged", MergedAt: "2026-11-09T08:30:00Z", MergeCommit: mergedCommit},
			40: {Number: 40, State: "closed", ClosedAt: "2026-11-08T12:00:00Z"},
		},
	}
}

// activityServer is a console with the fixture's GitHub and token, whose cluster and trees are the fixture's.
func activityServer(t *testing.T) (*consoleServer, orchestration.ChangeReads) {
	t.Helper()
	server, err := newConsoleServer(consoleConfig{Dev: true, Now: func() time.Time { return activityNow }})
	if err != nil {
		t.Fatal(err)
	}
	server.github, server.hasGitHub, server.token, server.appRepos = activityGitHub(), true, "tok", true
	server.repository = "owner/repo"
	reads := server.changeReads()
	render := map[string]pages.Render{"web/prod": {Records: "records-1", Settings: "settings-1", Digest: "sha256:x",
		Release: true, Runs: "sha256:x"}}
	reads.Renders = func(_ context.Context, commit string, _ []string) (map[string]pages.Render, error) {
		if commit != mergedCommit {
			t.Errorf("read the tree at %s", commit)
		}
		return render, nil
	}
	reads.Main = func(context.Context) (map[string]pages.Render, error) { return render, nil }
	reads.Rows = func(context.Context) (map[string]*pages.AppRow, error) {
		return map[string]*pages.AppRow{"web/prod": {Instance: pages.Instance{App: "web", Env: "prod"},
			State: pages.State{Key: pages.StateHealthy}, Report: &pages.Report{}}}, nil
	}
	return server, reads
}

func activityShell(path string, autoRefresh bool, jobs []pages.Job) shell {
	return shell{Path: path, Identity: "sam@swhurl.com", ReadAt: "09:43", RefreshURL: path, Assets: "92d0885d31c0",
		AutoRefresh: autoRefresh, HasGithub: true, Running: runningJobs(jobViewsOf(jobs))}
}

func sameAsGolden(t *testing.T, server *consoleServer, page, golden string, data any) {
	t.Helper()
	var rendered bytes.Buffer
	if err := server.templates[page].ExecuteTemplate(&rendered, "base", data); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/" + golden)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.String() != string(want) {
		os.WriteFile("/tmp/"+golden, rendered.Bytes(), 0o644)
		t.Fatalf("the page differs from the Python console's; wrote /tmp/%s", golden)
	}
}

// Activity renders the same bytes as the Python console for the same jobs, pull requests, trees and cluster.
func TestActivityIsByteIdentical(t *testing.T) {
	server, reads := activityServer(t)
	jobs := activityJobs()
	data := server.readActivity(context.Background(), jobs, reads)
	data.shell = activityShell("/activity", true, jobs)
	sameAsGolden(t, server, "activity.html", "activity.html", data)
}

// A job's page renders the same bytes as the Python console's: one waiting for its merge, one still running in
// another pod, one a new app can be finished from, and one that is live.
func TestJobPagesAreByteIdentical(t *testing.T) {
	for golden, id := range map[string]int{"job-open.html": 8, "job-running.html": 9, "job-resumable.html": 5, "job-merged.html": 7} {
		t.Run(golden, func(t *testing.T) {
			server, reads := activityServer(t)
			jobs := activityJobs()
			data, found := server.readJob(context.Background(), jobs, id, reads)
			if !found {
				t.Fatalf("job %d not found", id)
			}
			data.shell = activityShell("/jobs/"+strconv.Itoa(id), false, jobs)
			sameAsGolden(t, server, "job.html", golden, data)
		})
	}
}

func TestAMissingJobIsTheErrorPage(t *testing.T) {
	server, _ := activityServer(t)
	jobs := activityJobs()
	data := errorData{shell: activityShell("/jobs/99", false, jobs), Error: "No such job (only the newest jobs are kept)."}
	sameAsGolden(t, server, "error.html", "job-missing.html", data)
}

// A change seen at its end is remembered by this server, so a later read can say it ran even once replaced; nothing
// is written for it.
func TestAReachedChangeIsRememberedInMemory(t *testing.T) {
	server, reads := activityServer(t)
	jobs := activityJobs()
	server.readActivity(context.Background(), jobs, reads)
	again := server.withReached(activityJobs())
	if again[2].ID != 7 || again[2].Reached != activityNow.Format(time.RFC3339Nano) {
		t.Fatalf("job 7 reached = %q", again[2].Reached)
	}
	if again[1].Reached != "" {
		t.Fatalf("an open change did not reach its end: %q", again[1].Reached)
	}
}

// This server runs no job, so no job has a stream here; the route still needs the oauth2 identity.
func TestJobEventsAreAnotherPodsJob(t *testing.T) {
	server, _ := activityServer(t)
	server.identity = ""
	mux := http.NewServeMux()
	mux.HandleFunc("/jobs/{id}/events", server.jobEvents)
	handler := server.identityMiddleware(mux)
	for _, c := range []struct {
		header string
		status int
		body   string
	}{{"sam@swhurl.com", http.StatusNotFound, "No such job here.\n"}, {"", http.StatusUnauthorized, "sign-in required"}} {
		request := httptest.NewRequest(http.MethodGet, "/jobs/9/events", nil)
		if c.header != "" {
			request.Header.Set(identityHeader, c.header)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != c.status || !strings.Contains(recorder.Body.String(), c.body) {
			t.Fatalf("%q: %d %q", c.header, recorder.Code, recorder.Body.String())
		}
	}
}

func TestResumable(t *testing.T) {
	jobs := activityJobs()
	if !resumable(jobs[4], jobs) {
		t.Fatal("an interrupted new app with no later job can be finished")
	}
	later := append([]pages.Job{{ID: 10, Unit: "shop/staging", State: "running"}}, jobs...)
	if resumable(jobs[4], later) {
		t.Fatal("a later job on the same app takes its place")
	}
	if resumable(jobs[3], jobs) {
		t.Fatal("only a new app's job is finished")
	}
}

// scriptedJobs is a registry of one job: it answers the job until the script says to answer what came after it.
type scriptedJobs struct {
	job       pages.Job
	remaining int
	then      pages.Job
}

func (s *scriptedJobs) Get(_ context.Context, id int) (pages.Job, bool) {
	if id != s.job.ID {
		return pages.Job{}, false
	}
	if s.remaining > 0 {
		s.remaining--
		return s.job, true
	}
	return s.then, true
}

// eventsServer is the job-event route over a registry of one job, paced as fast as a test needs.
func eventsServer(t *testing.T, live liveJobs, keepAlive time.Duration) http.Handler {
	t.Helper()
	server, _ := activityServer(t)
	server.live = live
	server.events = consoleEvents{poll: time.Millisecond, keepAlive: keepAlive}
	mux := http.NewServeMux()
	mux.HandleFunc("/jobs/{id}/events", server.jobEvents)
	return server.identityMiddleware(mux)
}

// eventsGet is one request to the job-event route as a signed-in browser makes it.
func eventsGet(handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set(identityHeader, "sam@swhurl.com")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// eventData is every data event of one kind in a stream, decoded as the browser decodes it.
func eventData(t *testing.T, body, event string) []map[string]any {
	t.Helper()
	var found []map[string]any
	for _, block := range strings.Split(body, "\n\n") {
		if !strings.Contains(block, "event: "+event+"\n") {
			continue
		}
		raw := block[strings.Index(block, "data: ")+len("data: "):]
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("event %q in %q: %v", event, body, err)
		}
		found = append(found, decoded)
	}
	return found
}

// A finished job's stream is its lines, their commit ids shortened as the page shortens them, then how it ended.
func TestJobEventsStreamALiveJobsLines(t *testing.T) {
	finished := activityNow
	job := pages.Job{ID: 7, Action: "reconcile", Unit: "gw", Identity: "sam@swhurl.com", Started: activityNow,
		State: "succeeded", Finished: &finished, Link: "https://github.com/owner/repo/pull/42", Live: true,
		Lines: []string{"a", "<b>", "applied main@sha1:" + strings.Repeat("1edf3705", 5)}}
	handler := eventsServer(t, &scriptedJobs{job: job, then: job}, 0)
	response := eventsGet(handler, "/jobs/7/events", nil)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") ||
		response.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("%d %v", response.Code, response.Header())
	}
	body := response.Body.String()
	lines := eventData(t, body, "line")
	if len(lines) != 3 || lines[0]["html"] != "a" || lines[1]["html"] != "&lt;b&gt;" ||
		!strings.Contains(lines[2]["html"].(string), `<span class="hash" title="1edf3705`) {
		t.Fatalf("lines %v", lines)
	}
	done := eventData(t, body, "done")
	if len(done) != 1 || done[0]["state"] != "succeeded" || done[0]["link"] != "https://github.com/owner/repo/pull/42" ||
		done[0]["finished"] != "09:43:00" {
		t.Fatalf("done %v", done)
	}
}

// A stream resumes from the browser's Last-Event-ID or the page's after, never beyond the job's lines.
func TestJobEventsResumeFromTheBrowser(t *testing.T) {
	finished := activityNow
	job := pages.Job{ID: 7, Action: "reconcile", Unit: "gw", Identity: "x", Started: activityNow, State: "succeeded",
		Finished: &finished, Live: true, Lines: []string{"first", "second"}}
	handler := eventsServer(t, &scriptedJobs{job: job, then: job}, 0)
	for _, c := range []struct {
		name, path string
		headers    map[string]string
		sent       []string
	}{
		{"after", "/jobs/7/events?after=1", nil, []string{"second"}},
		{"last event id", "/jobs/7/events?after=0", map[string]string{"Last-Event-ID": "1"}, []string{"second"}},
		{"a bad header is the start", "/jobs/7/events?after=1", map[string]string{"Last-Event-ID": "x"}, []string{"first", "second"}},
		{"a negative after is the start", "/jobs/7/events?after=-5", nil, []string{"first", "second"}},
		{"beyond the end is the end", "/jobs/7/events?after=99", nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := eventsGet(handler, c.path, c.headers).Body.String()
			lines := eventData(t, body, "line")
			if len(lines) != len(c.sent) {
				t.Fatalf("%d lines, want %d: %q", len(lines), len(c.sent), body)
			}
			for i, want := range c.sent {
				if lines[i]["html"] != want {
					t.Fatalf("line %d is %v, want %q", i, lines[i]["html"], want)
				}
			}
			if len(eventData(t, body, "done")) != 1 {
				t.Fatalf("stream %q never ended", body)
			}
		})
	}
}

// A running job with nothing new to say is kept alive, and the stream ends when the job does.
func TestJobEventsKeepARunningJobAlive(t *testing.T) {
	finished := activityNow
	running := pages.Job{ID: 7, Action: "reconcile", Unit: "gw", Identity: "x", Started: activityNow, State: "running",
		Live: true, Lines: []string{"working"}}
	ended := running
	ended.State, ended.Finished = "succeeded", &finished
	handler := eventsServer(t, &scriptedJobs{job: running, remaining: 2, then: ended}, 0)
	body := eventsGet(handler, "/jobs/7/events", nil).Body.String()
	if !strings.Contains(body, ": keep-alive") || len(eventData(t, body, "done")) != 1 {
		t.Fatalf("stream %q", body)
	}
	if lines := eventData(t, body, "line"); len(lines) != 1 || lines[0]["html"] != "working" {
		t.Fatalf("a line was sent twice: %q", body)
	}
}

// A record read back is not a job this server runs: its page re-reads itself, so its stream is 404.
func TestJobEventsAreNotAStoredRecord(t *testing.T) {
	stored := pages.Job{ID: 9, Action: "reconcile", Unit: "gw", Identity: "x", Started: activityNow,
		State: "succeeded", Lines: []string{"old"}}
	response := eventsGet(eventsServer(t, &scriptedJobs{job: stored, then: stored}, 0), "/jobs/9/events", nil)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "No such job here.") {
		t.Fatalf("%d %q", response.Code, response.Body.String())
	}
}
