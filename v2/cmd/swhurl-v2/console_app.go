package main

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The app page, the page an app that is not deployed yet shows, a promotion's review, and the New app form.

// appData is one instance's page: its facts, its flags and the changes that made it.
type appData struct {
	shell
	pages.AppPage
	Changes []changeView
}

// pendingData is what an instance not deployed yet shows.
type pendingData struct {
	shell
	pages.Pending
}

// promotionData is a promotion's review.
type promotionData struct {
	shell
	pages.PromotionPage
}

// newAppData is the New app form.
type newAppData struct {
	shell
	pages.NewAppPage
}

// exposuresView is a form's exposure choices with the one marked as chosen.
type exposuresView struct {
	Choices      []pages.Choice
	Marked, Mark string
}

// connectionsTableView is an instance's connections as its page lists them.
type connectionsTableView struct {
	Rows           []pages.ConnectionRow
	App, Env       string
	Remove, Github bool
}

// connectionChoicesView is what a form offers for connecting to other apps.
type connectionChoicesView struct {
	Apps         []pages.ConnectableApp
	Events       []pages.ConnectableEvent
	ChosenCalls  map[string]bool
	ChosenEvents map[string]bool
	Env, Who     string
	Problem      string
}

// app serves one instance's page, or the page that waits for it while it is not deployed yet.
func (s *consoleServer) app(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	instance, known := pages.Named(r.PathValue("app"), r.PathValue("env"))
	if !known {
		s.failure(w, r, http.StatusNotFound, "No such app instance.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	tree, rows, row, err := s.appRow(ctx, instance)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	jobs := s.jobsOf(ctx)
	if row == nil || row.Report == nil {
		s.pending(w, r, instance, row != nil, false, jobs)
		return
	}
	if row.Report.Live.Unit.Status == "absent" {
		page := pendingData{shell: s.pageWith(r, false, jobs),
			Pending: pages.PendingOf(instance, false, true, nil, nil, "")}
		s.render(w, r, http.StatusOK, "pending.html", page)
		return
	}
	data, err := s.readApp(ctx, instance, tree, rows, *row)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	data.shell = s.pageWith(r, false, jobs)
	s.render(w, r, http.StatusOK, "app.html", data)
}

// appRow is the pinned tree, every row, and the instance's row (nil when the cluster has no such instance).
func (s *consoleServer) appRow(ctx context.Context, instance pages.Instance) (map[string][]byte, []pages.AppRow, *pages.AppRow, error) {
	tree, rows, err := s.readRows(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range rows {
		if rows[i].Instance == instance {
			return tree, rows, &rows[i], nil
		}
	}
	return tree, rows, nil, nil
}

// pending shows why an instance is not deployed: its job and pull request, or that Flux has not applied it.
func (s *consoleServer) pending(w http.ResponseWriter, r *http.Request, instance pages.Instance, removing, inGit bool,
	jobs []pages.Job) {
	pulls, prsError := s.consolePulls(r.Context())
	prs := pages.PullsFor(pulls, instance)
	pending := pages.PendingOf(instance, removing, inGit, jobs, prs, prsError)
	if !removing && len(pending.Jobs) == 0 && len(pending.PRs) == 0 {
		s.failure(w, r, http.StatusNotFound, "No such app instance.")
		return
	}
	page := pendingData{shell: s.pageWith(r, false, jobs), Pending: pending}
	s.render(w, r, http.StatusOK, "pending.html", page)
}

// readApp is one instance's page: the pinned tree, the rows and the documents the page shows.
func (s *consoleServer) readApp(ctx context.Context, instance pages.Instance, tree map[string][]byte,
	rows []pages.AppRow, row pages.AppRow) (appData, error) {
	site, err := orchestration.ConsoleSite(tree, s.repository)
	if err != nil {
		return appData{}, err
	}
	registry, registryErr := orchestration.ConsoleTeams(tree)
	teamsError := ""
	if registryErr != nil {
		registry, teamsError = map[string]pages.TeamInfo{}, registryErr.Error()
	}
	declaration, problem := orchestration.FlagDeclaration(tree[flagDocument(instance)])
	store := s.flagStore(ctx)
	card := orchestration.Flags(ctx, store, instance.Env, instance.App, declaration, problem, s.now())
	flags := orchestration.ConsoleFlagView(instance.Env, card)
	variables, variablesKnown := orchestration.ConsoleDesiredVariables(tree, instance.App, instance.Env)
	connectable := pages.ConnectableTo(rows, orchestration.ConsoleOffers(tree, rows, instance.Env), instance.Env, instance.App)
	jobs := s.jobsOf(ctx)
	page := pages.AppPageOf(pages.AppInput{Instance: instance, Rows: rows, Site: site,
		Stacks: s.stacksOf(ctx, []string{instance.App}), Dashboards: s.dashboardsOf(ctx), Jobs: jobs,
		Flags: flags, FlagChanged: "", FlagsWritable: store != nil, Teams: registry, TeamsError: teamsError,
		Settings: orchestration.ConsoleSettings(tree, instance.App, instance.Env), Variables: variables,
		VariablesKnown: variablesKnown, Connectable: connectable, Github: s.hasGitHub})
	reads := s.changeReads()
	own := []pages.Job{}
	for _, job := range jobs {
		if job.Unit == instance.Unit() || job.Unit == instance.Name() {
			own = append(own, job)
		}
	}
	rows_, _ := orchestration.ConsoleChanges(ctx, own, reads, false)
	changes := make([]changeView, 0, len(rows_))
	for _, change := range rows_ {
		changes = append(changes, changeViewOf(change, reads.Now))
	}
	return appData{AppPage: page, Changes: changes}, nil
}

// flagDocument is the path an instance's offered flags document takes in the pinned tree.
func flagDocument(instance pages.Instance) string {
	return "v2-records/" + instance.App + "/" + instance.Env + "/offers/" + kernel.DocumentRoles["flags"]
}

// promotion is staging's image reviewed against production's, or the promotion pull request already open.
func (s *consoleServer) promotion(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	app := r.PathValue("app")
	if !s.hasGitHub {
		s.failure(w, r, http.StatusConflict, "Staging and a GitHub token are required.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	pulls, prsError := s.consolePulls(ctx)
	if prsError != "" {
		s.failure(w, r, http.StatusConflict, prsError)
		return
	}
	existing := pages.PullsFor(pulls, pages.Instance{App: app, Env: "prod"})
	if len(existing) > 0 {
		page := promotionData{shell: s.page(r, false), PromotionPage: pages.PromotionPage{App: app, PRs: existing}}
		s.render(w, r, http.StatusOK, "promotion.html", page)
		return
	}
	_, rows, err := s.readRows(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	review, problem := promotionReview(app, rows)
	if problem != "" {
		s.failure(w, r, http.StatusConflict, problem)
		return
	}
	page := promotionData{shell: s.page(r, false), PromotionPage: pages.PromotionPage{App: app, Review: &review}}
	s.render(w, r, http.StatusOK, "promotion.html", page)
}

// promotionReview is staging's image against production's, or why the promotion cannot be reviewed.
func promotionReview(app string, rows []pages.AppRow) (pages.PromotionReview, string) {
	staging, prod := promotionRows(app, rows)
	if staging == nil {
		return pages.PromotionReview{}, "its records are not in Git, so what it declares is unknown"
	}
	if prod == nil {
		return pages.PromotionReview{}, "v2 has no first promotion: production has no records yet"
	}
	return pages.PromotionOf(app, staging, prod), ""
}

// promotionRows is one app's staging and production rows, each only when its records declare it.
func promotionRows(app string, rows []pages.AppRow) (*pages.AppRow, *pages.AppRow) {
	var staging, prod *pages.AppRow
	for i := range rows {
		row := &rows[i]
		if row.Instance.App != app || row.Inventory == nil || row.Removed != "" {
			continue
		}
		switch row.Instance.Env {
		case "staging":
			staging = row
		case "prod":
			prod = row
		}
	}
	return staging, prod
}

// newApp shows the form that creates an app's repository and opens the pull request that adds it to staging.
func (s *consoleServer) newApp(w http.ResponseWriter, r *http.Request) {
	if !readable(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	tree, rows, err := s.readRows(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	catalogue, err := orchestration.ConsoleStacks(tree)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	site, err := orchestration.ConsoleSite(tree, s.repository)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	registry, registryErr := orchestration.ConsoleTeams(tree)
	teamsError := ""
	if registryErr != nil {
		registry, teamsError = map[string]pages.TeamInfo{}, registryErr.Error()
	}
	read := newAppRead{tree: tree, catalogue: catalogue, teams: registry, teamsError: teamsError,
		connectable: pages.ConnectableTo(rows, orchestration.ConsoleOffers(tree, rows, "staging"), "staging", ""),
		hookHost:    "flux-webhook." + site.Domain}
	s.renderNewApp(w, r, read, pages.NewAppForm{}, nil, nil, "", http.StatusOK)
}

// replicas is a report's ready replica count, "unknown" when the cluster did not state one.
func replicas(value *int32) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *value)
}

// spaced is a report's underscored reason in words.
func spaced(text string) string { return strings.ReplaceAll(text, "_", " ") }

// routeReason is why a desired route is not observed, with the console's words for no reason given.
func routeReason(reason string) string {
	if reason == "" {
		return "no address observed"
	}
	return spaced(reason)
}

// exposuresOf is a form's exposure choices with the chosen one marked.
func exposuresOf(choices []pages.Choice, marked, mark string) exposuresView {
	return exposuresView{Choices: choices, Marked: marked, Mark: mark}
}

// printPlaceholder is a text field's example: a team grant naming the app's own team, or the first one.
func printPlaceholder(prefix, team string) string {
	if team == "" {
		team = "demo"
	}
	return prefix + team
}

// allowHint is what the Allow callers field says it takes.
func allowHint(env, app, team string) string {
	return "An app name, or team:<team> for every app of that team (its own team too: nothing is implicit); several " +
		"comma-separated. Allowed in " + env + " only. An app that calls " + app + " is connected once it is allowed."
}

// choicesOf is what an existing instance's Connect form offers.
func choicesOf(connectable pages.Connectable, calls, events []string, env, who string) connectionChoicesView {
	return connectionChoicesView{Apps: connectable.Apps, Events: connectable.Events, ChosenCalls: chosen(calls),
		ChosenEvents: chosen(events), Env: env, Who: who, Problem: connectable.Problem}
}

// newAppChoices is what the New app form offers to connect to in staging.
func newAppChoices(page pages.NewAppPage) connectionChoicesView {
	return connectionChoicesView{Apps: page.CallableApps, Events: page.OfferedEvents,
		ChosenCalls: chosen(page.ChosenCalls), ChosenEvents: chosen(page.ChosenEvents), Env: "staging",
		Who: "The new app", Problem: page.ConnectionsError}
}

// chosen is a list of names as the set a checkbox reads.
func chosen(names []string) map[string]bool {
	found := map[string]bool{}
	for _, name := range names {
		found[name] = true
	}
	return found
}

// connectionTableOf is an instance's connections as its page lists them.
func connectionTableOf(connections pages.Connections, app, env string, remove, github bool) connectionsTableView {
	return connectionsTableView{Rows: connections.Rows, App: app, Env: env, Remove: remove, Github: github}
}

// bools are the two values a boolean flag's form offers.
func bools() []string { return []string{"true", "false"} }

// prose is a contract's own prose escaped for HTML as Jinja escapes it: html/template would also write "+" as
// "&#43;", so the New app form would not say "Vite+" as the page it replaces did.
func prose(text string) template.HTML { return template.HTML(html.EscapeString(text)) }

// jsString is a value as JavaScript's own JSON text, which Jinja's tojson writes too.
func jsString(value string) template.JS { return template.JS(pages.JinjaJSON(value)) }

// flagsStore opens the console's NATS store once, and not again for a minute after a failure.
func (s *consoleServer) flagStore(ctx context.Context) nats.KV {
	s.flagsMu.Lock()
	defer s.flagsMu.Unlock()
	if s.flagsStore != nil {
		return s.flagsStore
	}
	if s.natsSeed == "" || (!s.flagsTriedAt.IsZero() && time.Since(s.flagsTriedAt) < time.Minute) {
		return nil
	}
	s.flagsTriedAt = time.Now()
	url := s.natsURL
	if url == "" {
		url = defaultNATSURL
	}
	open := s.natsOpen
	if open == nil {
		open = nats.Open
	}
	store, err := open(ctx, url, s.natsSeed, kernel.ConsoleInbox)
	if err != nil {
		fmt.Fprintf(os.Stderr, "console: feature flags: %v\n", err)
		return nil
	}
	s.flagsStore = store
	return store
}
