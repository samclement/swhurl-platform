package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/orchestration"
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
	read, err := s.newAppRead(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	calls, events := splitNames(fields["calls"]), splitNames(fields["subscribe"])
	if problem := read.connectable.Problem; problem != "" && (len(calls) > 0 || len(events) > 0) {
		s.newAppRefusal(w, r, form, calls, events, http.StatusBadRequest,
			"cannot check the apps and events chosen: "+problem)
		return
	}
	chosen, err := pages.NewAppChoose(fields, read.catalogue, read.teams, read.callable(), read.offered())
	if err != nil {
		s.newAppRefusal(w, r, form, calls, events, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.jobs.Start(ctx, pages.NewAppJob, chosen.Name+"/staging", identityOf(r), chosen.Record,
		s.newAppWork(chosen, false, identityOf(r), read))
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
		s.newAppRefusal(w, r, form, splitNames(fields["calls"]), splitNames(fields["subscribe"]),
			http.StatusConflict, "Both GitHub tokens must be configured (console-github Secret).")
		return false
	}
	if s.jobs == nil {
		s.failure(w, r, http.StatusConflict, noJobsProblem)
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
	if !known || !resumable(found, s.jobs.Recent(ctx)) {
		s.failure(w, r, http.StatusNotFound, "No such job, or nothing of it is left to finish.")
		return
	}
	if !s.hasGitHub || !s.appRepos {
		s.failure(w, r, http.StatusConflict, "Both GitHub tokens must be configured (console-github Secret).")
		return
	}
	read, err := s.newAppRead(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	chosen, err := pages.NewAppFromRecord(found.Resume, read.catalogue)
	if err != nil {
		s.failure(w, r, http.StatusConflict, err.Error())
		return
	}
	job, err := s.jobs.Start(ctx, pages.FinishAppJob, found.Unit, identityOf(r), found.Resume,
		s.newAppWork(chosen, true, identityOf(r), read))
	if err != nil {
		s.failure(w, r, http.StatusConflict, err.Error())
		return
	}
	http.Redirect(w, r, "/jobs/"+strconv.Itoa(job.ID), http.StatusSeeOther)
}

// newAppWork creates the repository, or (finishing) finds the one an earlier job created.
func (s *consoleServer) newAppWork(chosen pages.NewAppChoices, finishing bool, identity string,
	read newAppRead) orchestration.JobWork {
	return func(ctx context.Context, line func(string)) (string, error) {
		opened := ""
		var refusal strings.Builder
		contract, err := read.contractFile()
		if err != nil {
			return "", err
		}
		defer os.Remove(contract)
		c := s.command(&refusal, func(text string) {
			if url, isPull := strings.CutPrefix(text, "Opened "); isPull {
				opened = strings.TrimSpace(url)
			}
			line(text)
		})
		one := request{command: "app-repo", contractPath: contract, appType: chosen.Type,
			stack: chosen.Stack, resume: finishing, hookHost: read.hookHost,
			spec: &changeSpec{app: chosen.Name, description: chosen.Description, settings: chosen.Flags,
				requestedBy: identity, repository: s.repository}}
		if status := c.appRepo(one); status != 0 {
			return "", errors.New(strings.TrimSpace(refusal.String()))
		}
		return opened, nil
	}
}

// newAppRead is what the New app form and its job read from one pinned commit: the catalogue, the teams, the apps
// staging offers, the site and the host the repository's webhook reaches.
type newAppRead struct {
	tree        map[string][]byte
	catalogue   pages.StackCatalogue
	teams       map[string]pages.TeamInfo
	connectable pages.Connectable
	teamsError  string
	hookHost    string
}

// contractFile writes the pinned commit's stack catalogue to a file of its own: the command that renders a template
// reads the catalogue the pages show, not the one the image happens to carry, and reads its other files from disk.
func (read newAppRead) contractFile() (string, error) {
	data, known := read.tree["v2/contract/stacks.json"]
	if !known {
		return "", errors.New("v2/contract/stacks.json: not in the pinned commit")
	}
	file, err := os.CreateTemp("", "stacks-*.json")
	if err != nil {
		return "", fmt.Errorf("io: working folder: cannot create one")
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return "", fmt.Errorf("io: working folder: cannot write the catalogue")
	}
	return file.Name(), nil
}

// newAppRead reads the pinned commit once for one form.
func (s *consoleServer) newAppRead(ctx context.Context) (newAppRead, error) {
	tree, rows, err := s.readRows(ctx)
	if err != nil {
		return newAppRead{}, err
	}
	catalogue, err := orchestration.ConsoleStacks(tree)
	if err != nil {
		return newAppRead{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.repository)
	if err != nil {
		return newAppRead{}, err
	}
	teams, _ := orchestration.ConsoleTeams(tree)
	return newAppRead{tree: tree, catalogue: catalogue, teams: teams, hookHost: "flux-webhook." + site.Domain,
		connectable: pages.ConnectableTo(rows, orchestration.ConsoleOffers(tree, rows, "staging"), "staging", "")}, nil
}

// callable is the apps staging offers a call to, as the form's checkboxes name them; nil when the lists could not be
// read, so a form that chose nothing is not refused for a catalogue it did not use.
func (read newAppRead) callable() map[string]bool {
	if read.connectable.Problem != "" {
		return nil
	}
	found := make(map[string]bool, len(read.connectable.Apps))
	for _, one := range read.connectable.Apps {
		found[one.App] = true
	}
	return found
}

// offered is the events staging publishes, as the form's checkboxes name them.
func (read newAppRead) offered() map[string]bool {
	if read.connectable.Problem != "" {
		return nil
	}
	found := make(map[string]bool, len(read.connectable.Events))
	for _, one := range read.connectable.Events {
		found[one.Name] = true
	}
	return found
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
	read, err := s.newAppRead(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.renderNewApp(w, r, read, form, calls, events, problem, status)
}

// renderNewApp is the New app page with a form's answers and, on a refusal, its sentence.
func (s *consoleServer) renderNewApp(w http.ResponseWriter, r *http.Request, read newAppRead, form pages.NewAppForm,
	calls, events []string, problem string, status int) {
	page := pages.NewAppPageOf(pages.NewAppInput{Catalogue: read.catalogue, Teams: read.teams,
		TeamsError: read.teamsError, Form: form, Connectable: read.connectable, Error: problem,
		Github: s.hasGitHub, AppRepos: s.appRepos})
	page.ChosenCalls, page.ChosenEvents = calls, events
	s.render(w, r, status, "new_repo.html", newAppData{shell: s.page(r, false), NewAppPage: page})
}
