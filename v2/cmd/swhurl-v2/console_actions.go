package main

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// A console form's answer. Every change the console makes is one job: the browser gets the job where its button was,
// and a plain form the job's page, so the same handler serves both. A refusal before any work starts is the console's
// own words on an error page; a refusal the work meets is the job's last line, in v2's words.

// inPlace is whether a button in a list sent this request: its answer replaces the button, so the operator stays on
// the list they pressed it on.
func inPlace(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// started answers a started job: in a list a link to it where the button was, which re-reads the page shortly; from a
// plain form the job's own page.
func (s *consoleServer) started(w http.ResponseWriter, r *http.Request, job pages.Job) {
	if !inPlace(r) {
		http.Redirect(w, r, "/jobs/"+strconv.Itoa(job.ID), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := `<a class="started" href="/jobs/` + strconv.Itoa(job.ID) + `" ` + string(follow(backURL(r))) +
		`><span class="mark updating" aria-hidden="true"></span> ` + html.EscapeString(job.Action) +
		` started · job #` + strconv.Itoa(job.ID) + `</a>`
	w.Write([]byte(body))
}

// refusalAnswer says why a button in a list did nothing, where the button was: htmx swaps only a successful answer, so
// a refusal that stays on the page is one.
func (s *consoleServer) refusalAnswer(w http.ResponseWriter, r *http.Request, problem string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<span class="bad" role="alert">` + html.EscapeString(problem) + `</span>`))
}

// backURL is the page the button was on, as the browser tells it: a path of this console, or the overview.
func backURL(r *http.Request) string {
	here, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil || !strings.HasPrefix(here.Path, "/") {
		return "/"
	}
	if here.RawQuery != "" {
		return here.Path + "?" + here.RawQuery
	}
	return here.Path
}

// startUnit performs one Flux unit action as a job. The action's refusal — a root unit, a suspended unit's reconcile —
// is the job's own last line, as it always was: the console only decides that a job may start.
func (s *consoleServer) startUnit(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		s.failure(w, r, http.StatusConflict, noJobsProblem)
		return
	}
	action, unit := r.PathValue("action"), r.PathValue("unit")
	job, err := s.jobs.Start(r.Context(), action, unit, identityOf(r), nil, s.unitWork(action, unit))
	if err != nil {
		if inPlace(r) {
			s.refusalAnswer(w, r, err.Error())
			return
		}
		s.failure(w, r, http.StatusConflict, err.Error())
		return
	}
	s.started(w, r, job)
}

// unitWork is one unit action as a job's work: Flux's own lines, and the refusal it ends with when there is one.
func (s *consoleServer) unitWork(action, unit string) orchestration.JobWork {
	return func(ctx context.Context, line func(string)) (string, error) {
		return "", orchestration.Flux(ctx, orchestration.FluxInputs{Action: orchestration.FluxAction(action), Unit: unit,
			Cluster: s.cluster(), Now: s.now, Sleep: time.Sleep, Progress: line})
	}
}

// cluster is the API server a unit action reads and changes: the one credential the console was given, and nothing
// else. A console without the write port runs no unit action.
func (s *consoleServer) cluster() orchestration.Cluster {
	return orchestration.Cluster{Reader: s.reader, Writer: s.writer}
}

// noJobsProblem says why a form did nothing: this console would have no record of the job it started.
const noJobsProblem = "This console runs no jobs: it has no cluster record, so nothing would remember them. Start it " +
	"with --in-cluster or --kubeconfig."
