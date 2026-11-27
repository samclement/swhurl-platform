package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/console"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// A new app is one command, `swhurl-v2 app-repo`: it renders the stack's template into a repository of its own, creates
// that repository with its first commit and image webhook, waits for the first image and opens the pull request that
// creates the app's staging instance. The console decides no answer, image or webhook — it reads the form as v2's own
// flags and keeps them as the job's record, so a job that stopped after creating its repository can be finished without
// a second one.

// newRepo starts the job that creates a repository and opens its staging pull request, or shows the form again with
// the sentence that refused it.
func (s *consoleServer) newRepo(w http.ResponseWriter, r *http.Request) {
	fields, err := formFields(r)
	if err != nil {
		s.newAppRefusal(w, r, pages.NewAppForm{}, nil, nil, http.StatusBadRequest, "The form could not be read: "+err.Error())
		return
	}
	form := newAppFormOf(fields)
	if !s.newAppReady(w, r, form, fields) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()
	read, err := s.reads().NewApp(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	calls, events := console.SplitNames(fields["calls"]), console.SplitNames(fields["subscribe"])
	if problem := read.Connectable.Problem; problem != "" && (len(calls) > 0 || len(events) > 0) {
		s.newAppRefusal(w, r, form, calls, events, http.StatusBadRequest,
			"cannot check the apps and events chosen: "+problem)
		return
	}
	chosen, err := pages.NewAppChoose(fields, read.Catalogue, read.Teams, read.Callable(), read.Offered())
	if err != nil {
		s.newAppRefusal(w, r, form, calls, events, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.jobs.Start(ctx, pages.NewAppJob, chosen.Name+"/staging", identityOf(r), chosen.Record,
		console.NewAppWork(chosen, false, identityOf(r), read, s.openNewApp))
	if err != nil {
		s.failure(w, r, http.StatusConflict, err.Error())
		return
	}
	http.Redirect(w, r, "/jobs/"+strconv.Itoa(job.ID), http.StatusSeeOther)
}

// newAppReady answers what a new app needs before one is started: both tokens, and a registry to run the job in.
func (s *consoleServer) newAppReady(w http.ResponseWriter, r *http.Request, form pages.NewAppForm,
	fields map[string][]string) bool {
	if !s.hasGitHub || !s.appRepos {
		s.newAppRefusal(w, r, form, console.SplitNames(fields["calls"]), console.SplitNames(fields["subscribe"]),
			http.StatusConflict, "Both GitHub tokens must be configured (console-github Secret).")
		return false
	}
	if s.jobs == nil {
		s.failure(w, r, http.StatusConflict, console.NoJobsProblem)
		return false
	}
	return true
}

// resumeJob finishes a new-app job that stopped after creating its repository: the same choices, no second repository.
func (s *consoleServer) resumeJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || s.jobs == nil {
		s.failure(w, r, http.StatusNotFound, "No such job, or nothing of it is left to finish.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()
	found, known := s.jobs.Get(ctx, id)
	if !known || !console.Resumable(found, s.jobs.Recent(ctx)) {
		s.failure(w, r, http.StatusNotFound, "No such job, or nothing of it is left to finish.")
		return
	}
	if !s.hasGitHub || !s.appRepos {
		s.failure(w, r, http.StatusConflict, "Both GitHub tokens must be configured (console-github Secret).")
		return
	}
	read, err := s.reads().NewApp(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	chosen, err := pages.NewAppFromRecord(found.Resume, read.Catalogue)
	if err != nil {
		s.failure(w, r, http.StatusConflict, err.Error())
		return
	}
	job, err := s.jobs.Start(ctx, pages.FinishAppJob, found.Unit, identityOf(r), found.Resume,
		console.NewAppWork(chosen, true, identityOf(r), read, s.openNewApp))
	if err != nil {
		s.failure(w, r, http.StatusConflict, err.Error())
		return
	}
	http.Redirect(w, r, "/jobs/"+strconv.Itoa(job.ID), http.StatusSeeOther)
}

// openNewApp is the CLI's execution boundary: its scratch file holds only the pinned
// catalogue supplied by the console; the job decides the choices and whether to resume.
func (s *consoleServer) openNewApp(ctx context.Context, job console.NewAppRequest, line func(string)) (string, error) {
	contract, err := contractFile(job.Contract)
	if err != nil {
		return "", err
	}
	defer os.Remove(contract)
	var refusal strings.Builder
	c := s.command(&refusal, line)
	one := request{command: "app-repo", contractPath: contract, appType: job.Choices.Type,
		stack: job.Choices.Stack, resume: job.Finishing, hookHost: job.HookHost,
		spec: &changeSpec{app: job.Choices.Name, description: job.Choices.Description, settings: job.Choices.Flags,
			requestedBy: job.Identity, repository: s.repository}}
	opened, status := c.openAppRepo(one)
	if status != 0 {
		return "", errors.New(strings.TrimSpace(refusal.String()))
	}
	return opened, nil
}

// contractFile writes the catalogue for the command that takes its path, removing a
// partial file on failure. The job's caller removes a complete one when it ends.
func contractFile(data []byte) (string, error) {
	file, err := os.CreateTemp("", "stacks-*.json")
	if err != nil {
		return "", fmt.Errorf("io: working folder: cannot create one")
	}
	name := file.Name()
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(name)
		return "", fmt.Errorf("io: working folder: cannot write the catalogue")
	}
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("io: working folder: cannot write the catalogue")
	}
	return name, nil
}

// newAppFormOf is the form's own fields as the page shows them again after a refusal.
func newAppFormOf(fields map[string][]string) pages.NewAppForm {
	return pages.NewAppForm{Name: strings.TrimSpace(first(fields, "name")), Type: first(fields, "type"),
		Stack: first(fields, "stack"), Description: first(fields, "description"), Team: first(fields, "team"),
		Exposure: first(fields, "exposure")}
}

// newAppRefusal shows the New app form again with the sentence that refused it, keeping what was typed.
func (s *consoleServer) newAppRefusal(w http.ResponseWriter, r *http.Request, form pages.NewAppForm,
	calls, events []string, status int, problem string) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	read, err := s.reads().NewApp(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.renderNewApp(w, r, read, form, calls, events, problem, status)
}

// renderNewApp is the New app page with a form's answers and, on a refusal, its sentence.
func (s *consoleServer) renderNewApp(w http.ResponseWriter, r *http.Request, read console.NewAppRead, form pages.NewAppForm,
	calls, events []string, problem string, status int) {
	page := pages.NewAppPageOf(pages.NewAppInput{Catalogue: read.Catalogue, Teams: read.Teams,
		TeamsError: read.TeamsError, Form: form, Connectable: read.Connectable, Error: problem,
		Github: s.hasGitHub, AppRepos: s.appRepos})
	page.ChosenCalls, page.ChosenEvents = calls, events
	s.render(w, r, status, "new_repo.html", newAppData{shell: s.page(r, false), NewAppPage: page})
}
