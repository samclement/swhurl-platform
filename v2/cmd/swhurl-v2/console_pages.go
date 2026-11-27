package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/console"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// stackEntry is one app's remembered stack read: a stack does not change language, so a success is kept for an hour
// and a failure for five minutes, after which a page asks GitHub again.
type stackEntry struct {
	at    time.Time
	stack string
}

// appsData is the Deployments page.
type appsData struct {
	shell
	pages.AppsPage
}

// egressData is the Egress page.
type egressData struct {
	shell
	pages.EgressPage
}

// catalogueData is the Catalogue page: what one environment's apps offer each other.
type catalogueData struct {
	shell
	pages.CataloguePage
}

// catalogue serves every API and event of one environment, with what each app's own documents say about it.
func (s *consoleServer) catalogue(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	query := r.URL.Query()
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	page, err := s.reads().Catalogue(ctx, query.Get("env"), query.Get("kind"), query.Get("team"))
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.render(w, r, http.StatusOK, "catalogue.html", catalogueData{shell: s.page(r, false), CataloguePage: page})
}

// unitData is one Flux unit's page.
type unitData struct {
	shell
	console.UnitPage
	JobViews []jobView
}

// apps serves the Deployments list, or one environment's map of who calls whom.
func (s *consoleServer) apps(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	query := r.URL.Query()
	input := pages.AppsInput{View: query.Get("view"), Team: query.Get("team"), Env: query.Get("env"),
		Attention: query.Get("show") == "attention", RefreshURL: r.URL.RequestURI()}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	page, err := s.reads().Apps(ctx, input)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.render(w, r, http.StatusOK, "apps.html", appsData{shell: s.page(r, true), AppsPage: page})
}

// stacksOf is each app's stack (its language), from the swhurl.yaml on its repository's default branch. A failure is
// kept for five minutes; after one, the rest are not asked for either, so GitHub being away costs one wait.
func (s *consoleServer) stacksOf(ctx context.Context, apps []string) map[string]string {
	found := map[string]string{}
	reachable := true
	owner, _, _ := strings.Cut(s.repository, "/")
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	for _, app := range apps {
		cached, known := s.stacks[app]
		keep := time.Hour
		if cached.stack == "" {
			keep = 5 * time.Minute
		}
		if known && time.Since(cached.at) <= keep {
			found[app] = cached.stack
			continue
		}
		stack := ""
		if reachable {
			stack, reachable = console.AppStack(ctx, s.appConsole, owner, app)
		}
		s.stacks[app] = stackEntry{at: time.Now(), stack: stack}
		found[app] = stack
	}
	return found
}

// egress serves every instance's port on the egress proxy and the hosts outside the cluster it is granted.
func (s *consoleServer) egress(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	page, err := s.reads().Egress(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.render(w, r, http.StatusOK, "egress.html", egressData{shell: s.page(r, false), EgressPage: page})
}

// teamsData is the Teams page.
type teamsData struct {
	shell
	pages.TeamsPage
}

// teams serves every team with the apps that belong to it and the changes that keep the two in step.
func (s *consoleServer) teams(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	data, err := s.readTeams(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	data.shell = s.page(r, false)
	s.render(w, r, http.StatusOK, "teams.html", data)
}

// readTeams is the Teams page without its shell, which a team change's refusal shows as well.
func (s *consoleServer) readTeams(ctx context.Context) (teamsData, error) {
	page, err := s.reads().Teams(ctx)
	return teamsData{TeamsPage: page}, err
}

// dashboardsOf is each app's HyperDX dashboard, empty when the record cannot be read.
func (s *consoleServer) dashboardsOf(ctx context.Context) map[string]string {
	return s.reads().Dashboards(ctx)
}

// unit serves one Flux unit: what it is, what needs it, its actions and the jobs that ran on it.
func (s *consoleServer) unit(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	name, _ := strings.CutPrefix(r.URL.Path, "/units/")
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	page, found, err := s.reads().UnitPage(ctx, name)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if !found {
		s.failure(w, r, http.StatusNotFound, "No Flux unit by that name.")
		return
	}
	data := unitData{UnitPage: page, JobViews: s.jobsOn(ctx, name)}
	data.shell = s.page(r, false)
	s.render(w, r, http.StatusOK, "unit.html", data)
}

// jobsOn is the console's jobs for one unit, newest first.
func (s *consoleServer) jobsOn(ctx context.Context, name string) []jobView {
	views := []jobView{}
	for _, job := range s.jobsOf(ctx) {
		if job.Unit == name {
			label, _ := pages.Target(job.Unit)
			views = append(views, jobViewOf(job, label))
		}
	}
	return views
}

// pinnedTree is the selected files of the commit this request reads: one resolution, one download, cached by SHA.
func (s *consoleServer) pinnedTree(ctx context.Context) (map[string][]byte, error) {
	return s.reads().PinnedTree(ctx)
}

// readRows is the cluster and the pinned tree one page shows: resolved once, read once.
func (s *consoleServer) readRows(ctx context.Context) (map[string][]byte, []pages.AppRow, error) {
	return s.reads().Rows(ctx)
}

// jobsOf is the console's jobs, newest first: none when its own records cannot be read. The registry this server runs
// holds the records it keeps and the jobs it is running; a server with no registry reads the records alone.
func (s *consoleServer) jobsOf(ctx context.Context) []pages.Job {
	if s.jobs != nil {
		return s.withReached(s.jobs.Recent(ctx))
	}
	if s.data == nil {
		return nil
	}
	return s.withReached(console.ConsoleJobs(ctx, s.data, s.reader, s.now()))
}

// jobViewsOf is every job as a row shows it.
func jobViewsOf(jobs []pages.Job) []jobView {
	views := make([]jobView, 0, len(jobs))
	for _, job := range jobs {
		label, _ := pages.Target(job.Unit)
		views = append(views, jobViewOf(job, label))
	}
	return views
}

// jobViewOf is one job as a row shows it.
func jobViewOf(job pages.Job, label string) jobView {
	finished := ""
	if job.Finished != nil {
		finished = job.Finished.Format("15:04")
	}
	return jobView{ID: job.ID, Action: job.Action, TargetLabel: label, Identity: job.Identity, State: job.State,
		Link: job.Link, StartedClock: job.Started.Format("15:04"), FinishedClock: finished}
}

// runningJobs is the jobs still running, which the header counts and a row replaces its button with.
func runningJobs(views []jobView) []jobView {
	found := []jobView{}
	for _, view := range views {
		if view.State == "running" {
			found = append(found, view)
		}
	}
	return found
}

// readable is whether a page's method may read it: GET or HEAD, with 405 otherwise.
func readable(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}
