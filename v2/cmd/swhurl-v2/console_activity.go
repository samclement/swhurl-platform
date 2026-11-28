package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/samclement/swhurl-platform/v2/console"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// recentJobs is how many finished jobs Overview lists.
const recentJobs = 5

type activityData struct {
	shell
	console.Activity
}

type jobData struct {
	shell
	console.JobPage
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
	s.render(w, r, http.StatusOK, "activity.html", data)
}

func (s *consoleServer) readActivity(ctx context.Context, jobs []pages.Job, reads console.ChangeReads) activityData {
	return activityData{Activity: console.ActivityOf(ctx, jobs, reads)}
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
		s.render(w, r, http.StatusNotFound, "error.html", page)
		return
	}
	data.shell = s.pageWith(r, false, jobs)
	s.render(w, r, http.StatusOK, "job.html", data)
}

func (s *consoleServer) readJob(ctx context.Context, jobs []pages.Job, id int, reads console.ChangeReads) (jobData, bool) {
	page, found := console.JobPageOf(ctx, jobs, id, reads, s.hasGitHub && s.appRepos)
	return jobData{JobPage: page}, found
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

// changeReads are the reads a change's row takes through this server: GitHub with its token, the pinned bundle for
// main, the merge commits' evidence and the cluster. Without a token there are no pull request reads, so a merged change's progress is not known.
func (s *consoleServer) changeReads() console.ChangeReads {
	reads := console.ChangeReads{OpenPulls: s.consolePulls, Renders: s.evidence.Renders, Main: s.mainRenders,
		Cached: s.evidence.Cached, Rows: s.rowsByInstance, Reach: s.reach, Now: s.now()}
	if s.github != nil {
		reads.Pull = s.pull
	}
	return reads
}

func (s *consoleServer) consolePulls(ctx context.Context) ([]pages.OpenPull, string) {
	return console.OpenPulls(ctx, s.github, s.repository, s.token)
}

func (s *consoleServer) pull(ctx context.Context, number int) (pages.Pull, error) {
	return s.facts.Pull(ctx, s.github, s.repository, s.token, number)
}

func (s *consoleServer) mainRenders(ctx context.Context) (map[string]pages.Render, error) {
	return s.reads().MainRenders(ctx)
}

func (s *consoleServer) rowsByInstance(ctx context.Context) (map[string]*pages.AppRow, error) {
	return s.reads().RowsByInstance(ctx)
}

// reach writes into a job's record that its change was seen at its end; a server that runs no registry writes nothing.
// The save outlives the page that saw it: ChangeReads.Reach carries no context.
func (s *consoleServer) reach(job pages.Job) {
	if s.jobs != nil {
		s.jobs.Reach(context.Background(), job.ID)
	}
}
