package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// keptFacts is how many merged pull requests and first-seen ends the server remembers between page reads; the oldest
// goes first. Both are facts that never change once read, never progress verdicts.
const keptFacts = 60

// recentJobs is how many finished jobs Overview lists.
const recentJobs = 5

// newApp is the kind a new-app job's resume record names: the New app form's choices, which a later job can finish.
const newApp = "new-app"

// activityData is Activity: every change, what is still moving or waiting for someone first.
type activityData struct {
	shell
	Moving, Ended []changeView
	PRsError      string
}

// jobData is one job's page: its change's whole path and its output.
type jobData struct {
	shell
	Job       changeJobView
	Track     pages.Track
	Stage     pages.Stage
	PR        *openPullView
	CanResume bool
}

// changeView is one change's row.
type changeView struct {
	ID    string
	Track pages.Track
	Open  bool // its pull request waits for someone: the row opens to its controls
	Job   *changeJobView
	PR    *openPullView
}

// changeJobView is a change's job as its row and its page show it.
type changeJobView struct {
	ID                           int
	Title, Label, Link, Unit     string
	Identity, State, PullLink    string
	StartedAt, StartedClock, Ago string
	Finished                     bool
	FinishedClock, Took          string
	Running                      bool
	Lines                        []string
}

// openPullView is an open console pull request as a change's row shows it.
type openPullView struct {
	Number                                int
	URL, Branch, Title, Opened, OpenedAgo string
	ChecksURL, Checks, Merging            string
}

// activity shows every change the console started, as the stages it goes through and where it ends.
func (s *consoleServer) activity(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	jobs := s.jobsOf(ctx)
	data := s.readActivity(ctx, jobs, s.changeReads())
	data.shell = s.pageWith(r, true, jobs)
	s.render(w, http.StatusOK, "activity.html", data)
}

// readActivity is every job as a change, and the open pull requests no remembered job opened: what is still moving
// or waiting for someone first, then what ended, newest first.
func (s *consoleServer) readActivity(ctx context.Context, jobs []pages.Job, reads orchestration.ChangeReads) activityData {
	rows, prsError := orchestration.ConsoleChanges(ctx, jobs, reads, true)
	data := activityData{PRsError: prsError}
	for _, row := range rows {
		if row.Track.Settled() {
			data.Ended = append(data.Ended, changeViewOf(row, reads.Now))
		} else {
			data.Moving = append(data.Moving, changeViewOf(row, reads.Now))
		}
	}
	return data
}

// job shows one job: its change's whole path, its pull request while open, and its output.
func (s *consoleServer) job(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	jobs := s.jobsOf(ctx)
	data, found := s.readJob(ctx, jobs, id, s.changeReads())
	if !found {
		page := errorData{shell: s.pageWith(r, false, jobs), Error: "No such job (only the newest jobs are kept)."}
		s.render(w, http.StatusNotFound, "error.html", page)
		return
	}
	data.shell = s.pageWith(r, false, jobs)
	s.render(w, http.StatusOK, "job.html", data)
}

// readJob is one job's page; found is false when no remembered job has the id.
func (s *consoleServer) readJob(ctx context.Context, jobs []pages.Job, id int, reads orchestration.ChangeReads) (jobData, bool) {
	at := slices.IndexFunc(jobs, func(job pages.Job) bool { return job.ID == id })
	if at < 0 {
		return jobData{}, false
	}
	job := jobs[at]
	rows, _ := orchestration.ConsoleChanges(ctx, []pages.Job{job}, reads, false)
	row := changeViewOf(rows[0], reads.Now)
	return jobData{Job: *row.Job, Track: row.Track, Stage: pages.Prepared(job), PR: row.PR,
		CanResume: resumable(job, jobs) && s.hasGitHub && s.appRepos}, true
}

// liveJobs is the registry of the jobs this server runs: the one a job page's stream re-reads.
type liveJobs interface {
	Get(ctx context.Context, id int) (pages.Job, bool)
}

// consoleEvents is how a job's event stream is paced: how often it re-reads the job, and how long it waits before a
// keep-alive, so a connection a proxy would cut stays open.
type consoleEvents struct{ poll, keepAlive time.Duration }

// jobEvents follows a running job as its output arrives, and closes with how it ended. Another pod's job is 404: its
// page re-reads itself, from the pod that answers.
func (s *consoleServer) jobEvents(w http.ResponseWriter, r *http.Request) {
	job, found := s.liveJob(r)
	if !found {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("No such job here.\n"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "refused: streaming is unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s.streamJob(w, r, flusher, job)
}

// liveJob is the job one stream is for, when this server runs it: a job of our own, not a record read back.
func (s *consoleServer) liveJob(r *http.Request) (pages.Job, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || s.live == nil {
		return pages.Job{}, false
	}
	job, found := s.live.Get(r.Context(), id)
	return job, found && job.Live
}

// streamJob sends each line the job has not sent yet, a keep-alive while there is nothing to send, and a done event
// once it has ended. The browser resumes from its Last-Event-ID, or from the page's own after.
func (s *consoleServer) streamJob(w http.ResponseWriter, r *http.Request, flusher http.Flusher, job pages.Job) {
	sent := streamFrom(r, len(job.Lines))
	idle := s.now()
	ticker := time.NewTicker(s.events.poll)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		current, found := s.live.Get(r.Context(), job.ID)
		if !found {
			return
		}
		for sent < len(current.Lines) {
			sent++
			writeLine(w, sent, current.Lines[sent-1])
			idle = s.now()
		}
		if !current.Running() {
			writeDone(w, current)
			flusher.Flush()
			return
		}
		if s.now().Sub(idle) >= s.events.keepAlive {
			fmt.Fprint(w, ": keep-alive\n\n")
			idle = s.now()
		}
		flusher.Flush()
	}
}

// streamFrom is where one stream resumes: the browser's Last-Event-ID, else the page's own after, never more than the
// job has written.
func streamFrom(r *http.Request, total int) int {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	sent, err := strconv.Atoi(raw)
	if err != nil {
		sent = 0
	}
	return min(max(sent, 0), total)
}

// writeLine is one job line, its commit ids shortened as the page shortens them.
func writeLine(w io.Writer, id int, text string) {
	body, _ := json.Marshal(map[string]string{"html": string(shortValue(text))})
	fmt.Fprintf(w, "id: %d\nevent: line\ndata: %s\n\n", id, body)
}

// writeDone is how the job ended, and the link it opened if it opened one.
func writeDone(w io.Writer, job pages.Job) {
	finished := ""
	if job.Finished != nil {
		finished = job.Finished.Format("15:04:05")
	}
	body, _ := json.Marshal(map[string]string{"state": job.State, "finished": finished, "link": job.Link})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", body)
}

// resumable is whether a new-app job ended without its pull request, with nothing since taking its place.
func resumable(job pages.Job, jobs []pages.Job) bool {
	if (job.State != "failed" && job.State != "interrupted") || job.Link != "" || job.Resume["kind"] != newApp {
		return false
	}
	for _, later := range jobs {
		if later.Unit == job.Unit && later.ID > job.ID && (later.State == "running" || later.State == "succeeded") {
			return false
		}
	}
	return true
}

// changeViewOf is one change as its row shows it.
func changeViewOf(row pages.ChangeRow, now time.Time) changeView {
	view := changeView{ID: row.ID, Track: row.Track, Open: row.PR != nil && (row.Track.Status == "waiting" || row.Track.Status == "blocked")}
	if row.PR != nil {
		view.PR = openPullViewOf(*row.PR, now)
	}
	if row.Job != nil {
		view.Job = changeJobViewOf(*row.Job, now)
	}
	return view
}

func openPullViewOf(pr pages.OpenPull, now time.Time) *openPullView {
	checksURL := pr.ChecksURL
	if checksURL == "" {
		checksURL = pr.URL
	}
	return &openPullView{Number: pr.Number, URL: pr.URL, Branch: pr.Branch, Title: strings.ReplaceAll(pr.Title, "[console] ", ""),
		Opened: pr.Opened, OpenedAgo: pages.Ago(pr.Opened, now), ChecksURL: checksURL, Checks: pr.Checks, Merging: pages.MergeMode(pr.Body)}
}

func changeJobViewOf(job pages.Job, now time.Time) *changeJobView {
	label, link := pages.Target(job.Unit)
	view := &changeJobView{ID: job.ID, Title: capitalize(job.Action), Label: label, Link: link, Unit: job.Unit,
		Identity: job.Identity, State: job.State, PullLink: job.Link, Running: job.Running(), Lines: job.Lines,
		StartedAt: job.Started.Format("2006-01-02 15:04:05"), StartedClock: job.Started.Format("15:04:05"),
		Ago: pages.Ago(job.Started.Format(time.RFC3339Nano), now)}
	if job.Finished != nil {
		view.Finished, view.FinishedClock, view.Took = true, job.Finished.Format("15:04:05"), pages.Took(job.Started, *job.Finished)
	}
	return view
}

// capitalize is Python's str.capitalize: the first letter upper case and the rest lower.
func capitalize(text string) string {
	if text == "" {
		return ""
	}
	runes := []rune(strings.ToLower(text))
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

// changeReads are the reads a change's row takes through this server: GitHub with its token, the pinned bundle and
// the cluster. Without a token there are no pull request reads, so a merged change's progress is not known.
func (s *consoleServer) changeReads() orchestration.ChangeReads {
	reads := orchestration.ChangeReads{OpenPulls: s.consolePulls, Renders: s.renders, Main: s.mainRenders,
		Cached: s.bundle.Cached, Rows: s.rowsByInstance, Reach: s.reach, Now: s.now()}
	if s.github != nil {
		reads.Pull = s.pull
	}
	return reads
}

// consolePulls lists the console's open pull requests, or says why it could not.
func (s *consoleServer) consolePulls(ctx context.Context) ([]pages.OpenPull, string) {
	if s.github == nil {
		return nil, "No GitHub token is configured, so open pull requests are not listed."
	}
	pulls, err := s.github.OpenPulls(ctx, s.repository, s.token)
	if err != nil {
		return nil, "Could not list open pull requests: " + err.Error()
	}
	found := make([]pages.OpenPull, 0, len(pulls))
	for _, pull := range pulls {
		found = append(found, pages.OpenPull{Number: pull.Number, Title: pull.Title, URL: pull.URL, Branch: pull.Branch,
			Opened: pull.Opened, Body: pull.Body, Checks: pull.Checks, ChecksURL: pull.ChecksURL})
	}
	return found, ""
}

// pull is a pull request that is no longer open: a merged one is kept, since it cannot change again (a closed one
// can be reopened).
func (s *consoleServer) pull(ctx context.Context, number int) (pages.Pull, error) {
	s.factsMu.Lock()
	kept, known := s.merged[number]
	s.factsMu.Unlock()
	if known {
		return kept, nil
	}
	read, err := s.github.Pull(ctx, s.repository, s.token, number)
	if err != nil {
		return pages.Pull{}, err
	}
	found := pullFacts(read)
	if found.State == "merged" {
		s.factsMu.Lock()
		keep(s.merged, number, found)
		s.factsMu.Unlock()
	}
	return found, nil
}

// pullFacts is a pull request read by number as the track reads it: how it ended, when, and the commit that merged it.
func pullFacts(read github.Pull) pages.Pull {
	found := pages.Pull{Number: read.Number, URL: read.URL, State: read.State}
	switch read.State {
	case "merged":
		found.When, found.Commit = read.MergedAt, read.MergeCommit
	case "closed":
		found.When = read.ClosedAt
	}
	return found
}

// keep remembers one fact, forgetting the lowest key once keptFacts are held.
func keep[V any](facts map[int]V, key int, value V) {
	if len(facts) >= keptFacts {
		oldest := key
		for held := range facts {
			oldest = min(oldest, held)
		}
		delete(facts, oldest)
	}
	facts[key] = value
}

// renders is one commit's instances, from the bundle that pins this server's reads.
func (s *consoleServer) renders(ctx context.Context, commit string) (map[string]pages.Render, error) {
	tree, err := s.bundle.Tree(ctx, commit)
	if err != nil {
		return nil, err
	}
	return orchestration.ConsoleRenders(tree)
}

// mainRenders is main's instances, at the commit every other read on the page is pinned to.
func (s *consoleServer) mainRenders(ctx context.Context) (map[string]pages.Render, error) {
	revision, err := s.bundle.Revision(ctx)
	if err != nil {
		return nil, err
	}
	return s.renders(ctx, revision)
}

// rowsByInstance is every instance on the cluster, by <app>/<env>.
func (s *consoleServer) rowsByInstance(ctx context.Context) (map[string]*pages.AppRow, error) {
	_, rows, err := s.readRows(ctx)
	if err != nil {
		return nil, err
	}
	found := make(map[string]*pages.AppRow, len(rows))
	for i := range rows {
		found[rows[i].Instance.Name()] = &rows[i]
	}
	return found, nil
}

// reach remembers when a job's change was first seen at its end. Only in memory: the job record is written by the
// job's own pod, and this server writes nothing (docs/iteration-2-plan.md §23, step 1).
func (s *consoleServer) reach(job pages.Job) {
	s.factsMu.Lock()
	defer s.factsMu.Unlock()
	if _, known := s.reached[job.ID]; !known {
		keep(s.reached, job.ID, s.now().UTC().Format(time.RFC3339Nano))
	}
}

// withReached gives each job the end this server saw it reach when its record has none.
func (s *consoleServer) withReached(jobs []pages.Job) []pages.Job {
	s.factsMu.Lock()
	defer s.factsMu.Unlock()
	for i := range jobs {
		if seen, known := s.reached[jobs[i].ID]; known && jobs[i].Reached == "" {
			jobs[i].Reached = seen
		}
	}
	return jobs
}
