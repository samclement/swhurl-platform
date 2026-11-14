package main

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/orchestration"
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
	data, err := s.readCatalogue(ctx, query.Get("env"), query.Get("kind"), query.Get("team"))
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	data.shell = s.page(r, false)
	s.render(w, r, http.StatusOK, "catalogue.html", data)
}

// readCatalogue reads the cluster, the pinned tree and the documents of the environment the page shows, then computes
// it: only the environment a page shows has its documents decoded.
func (s *consoleServer) readCatalogue(ctx context.Context, env, kind, team string) (catalogueData, error) {
	tree, rows, err := s.readRows(ctx)
	if err != nil {
		return catalogueData{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.repository)
	if err != nil {
		return catalogueData{}, err
	}
	input := pages.CatalogueInput{Rows: rows, Env: env, Kind: kind, Team: team, Site: site, Github: s.hasGitHub}
	input.Offers = orchestration.ConsoleOffers(tree, rows, pages.EnvironmentNamed(env))
	return catalogueData{CataloguePage: pages.CataloguePageOf(input)}, nil
}

// unitData is one Flux unit's page.
type unitData struct {
	shell
	Unit     pages.Unit
	Refused  string
	App      *pages.Instance
	Detail   string
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
	data, err := s.readApps(ctx, input)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	data.shell = s.page(r, true)
	s.render(w, r, http.StatusOK, "apps.html", data)
}

// readApps reads the cluster once and the two remote facts the list links to, then computes the page.
func (s *consoleServer) readApps(ctx context.Context, input pages.AppsInput) (appsData, error) {
	tree, rows, err := s.readRows(ctx)
	if err != nil {
		return appsData{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.repository)
	if err != nil {
		return appsData{}, err
	}
	input.Rows, input.Site, input.Running = rows, site, s.jobsOf(ctx)
	if input.View != "map" {
		// What the list shows decides what it reads: only an app it names has its stack asked for.
		first := pages.AppsPageOf(input)
		apps := make([]string, 0, len(first.Apps))
		for _, app := range first.Apps {
			apps = append(apps, app.App)
		}
		input.Stacks = s.stacksOf(ctx, apps)
		if s.data != nil {
			input.Dashboards = orchestration.ConsoleDashboards(ctx, s.data)
		}
	}
	return appsData{AppsPage: pages.AppsPageOf(input)}, nil
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
			stack, reachable = orchestration.ConsoleStack(ctx, s.appConsole, owner, app)
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
	data, err := s.readEgress(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	data.shell = s.page(r, false)
	s.render(w, r, http.StatusOK, "egress.html", data)
}

func (s *consoleServer) readEgress(ctx context.Context) (egressData, error) {
	tree, rows, err := s.readRows(ctx)
	if err != nil {
		return egressData{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.repository)
	if err != nil {
		return egressData{}, err
	}
	return egressData{EgressPage: pages.EgressPageOf(rows, site)}, nil
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

// readTeams reads the cluster, the registry and the two remote facts the cards link to.
func (s *consoleServer) readTeams(ctx context.Context) (teamsData, error) {
	tree, rows, err := s.readRows(ctx)
	if err != nil {
		return teamsData{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.repository)
	if err != nil {
		return teamsData{}, err
	}
	registry, registryErr := orchestration.ConsoleTeams(tree)
	data := teamsData{TeamsPage: pages.TeamsPageOf(registry, registryErr == nil, rows, site,
		s.stacksOf(ctx, appsOf(rows)), s.dashboardsOf(ctx))}
	if registryErr != nil {
		data.RegistryError = registryErr.Error()
	}
	data.Github = s.hasGitHub
	for _, job := range s.jobsOf(ctx) {
		team, isTeam := strings.CutPrefix(job.Unit, "team:")
		if !isTeam {
			continue
		}
		for i := range data.Teams {
			if data.Teams[i].Name == team && data.Teams[i].Job == nil {
				data.Teams[i].Job = &pages.TeamJob{ID: job.ID, Action: job.Action, State: job.State, Link: job.Link}
			}
		}
	}
	return data, nil
}

// appsOf is every app the rows name, once each, in name order.
func appsOf(rows []pages.AppRow) []string {
	apps := make([]string, 0, len(rows))
	for _, row := range rows {
		apps = append(apps, row.Instance.App)
	}
	slices.Sort(apps)
	return slices.Compact(apps)
}

// dashboardsOf is each app's HyperDX dashboard, empty when the record cannot be read.
func (s *consoleServer) dashboardsOf(ctx context.Context) map[string]string {
	if s.data == nil {
		return nil
	}
	return orchestration.ConsoleDashboards(ctx, s.data)
}

// unit serves one Flux unit: what it is, what needs it, its actions and the jobs that ran on it.
func (s *consoleServer) unit(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	name, _ := strings.CutPrefix(r.URL.Path, "/units/")
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	unit, found, err := s.findUnit(ctx, name)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if !found {
		s.failure(w, r, http.StatusNotFound, "No Flux unit by that name.")
		return
	}
	data := unitData{Unit: unit, Refused: pages.RefusedUnits[unit.Name], Detail: unit.Ready.Detail,
		App: unitApp(unit.Name), JobViews: s.jobsOn(ctx, unit.Name)}
	if data.Detail == "" {
		data.Detail = "applied " + unit.Revision
	}
	data.shell = s.page(r, false)
	s.render(w, r, http.StatusOK, "unit.html", data)
}

// findUnit is one unit of the cluster's units, and whether it is one of them.
func (s *consoleServer) findUnit(ctx context.Context, name string) (pages.Unit, bool, error) {
	items, err := orchestration.ConsoleUnits(ctx, s.collector)
	if err != nil {
		return pages.Unit{}, false, err
	}
	listed, err := pages.Units(items)
	if err != nil {
		return pages.Unit{}, false, fmt.Errorf("Flux units unavailable: %w", err)
	}
	for _, unit := range listed {
		if unit.Name == name {
			return unit, true, nil
		}
	}
	return pages.Unit{}, false, nil
}

// unitApp is the app instance a unit applies, when it applies one.
func unitApp(name string) *pages.Instance {
	if instance, ok := pages.InstanceOf(name); ok {
		return &instance
	}
	return nil
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
	revision, err := s.bundle.Revision(ctx)
	if err != nil {
		return nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	return s.bundle.Tree(ctx, revision)
}

// readRows is the cluster and the pinned tree one page shows: resolved once, read once.
func (s *consoleServer) readRows(ctx context.Context) (map[string][]byte, []pages.AppRow, error) {
	revision, err := s.bundle.Revision(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	tree, err := s.bundle.Tree(ctx, revision)
	if err != nil {
		return nil, nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	records, err := orchestration.Records(tree)
	if err != nil {
		return nil, nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	units, err := orchestration.ConsoleUnits(ctx, s.collector)
	if err != nil {
		return nil, nil, err
	}
	collection, err := orchestration.ConsoleStatus(ctx, records, revision, s.reader, units, s.compare, "console", s.now)
	if err != nil {
		return nil, nil, err
	}
	rows, err := orchestration.ConsoleRows(collection)
	if err != nil {
		return nil, nil, err
	}
	return tree, rows, nil
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
	return s.withReached(orchestration.ConsoleJobs(ctx, s.data, s.reader, s.now()))
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
