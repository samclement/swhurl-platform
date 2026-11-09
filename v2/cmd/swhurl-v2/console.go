package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The console's templates and static assets travel inside the binary: the image carries no checkout.
//
//go:embed console/templates/*.html console/static
var consoleFiles embed.FS

const (
	identityHeader = "X-Auth-Request-Email"
	devIdentity    = "operator@localhost (dev)"
	consoleRepo    = "samclement/swhurl-platform"
	consoleToken   = "GITHUB_TOKEN"
	appReposToken  = "APP_REPOS_TOKEN" // creates app repositories: only whether it is set is read here
)

// realToken is a token from the environment, or empty when it is unset or still the placeholder a Secret starts as.
func realToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "REPLACE_ME" {
		return ""
	}
	return value
}

// console is the read-only server: it writes nothing, and every read carries the request's deadline.
func (c cli) console(r request) int {
	port, err := strconv.Atoi(r.port)
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprintf(c.diagnostic, "invalid: port: expected_a_port\n")
		return 2
	}
	connection, source, status := c.connection(r)
	if status != 0 {
		return status
	}
	reader, err := connection.Reader()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	collector, ok := reader.(kube.Collector)
	if !ok {
		return fail(c.diagnostic, errors.New("invalid: kube: collector_unavailable"))
	}
	data, _ := reader.(kube.Data)
	server, err := newConsoleServer(consoleConfig{Reader: reader, Collector: collector, Data: data, Dev: r.dev,
		Git: c.git, Repository: os.Getenv("CONSOLE_REPO"), Token: realToken(os.Getenv(consoleToken)),
		AppRepos: realToken(os.Getenv(appReposToken)) != ""})
	if err != nil {
		return fail(c.diagnostic, err)
	}
	address := "0.0.0.0:" + r.port
	if r.dev {
		address = "127.0.0.1:" + r.port
	}
	fmt.Fprintf(c.out, "serving the console on http://%s (reads as %s)\n", address, source)
	return server.serve(c.out, address)
}

// consoleConfig is what the server needs to read: the cluster, the Git source it pins, and the identity it serves.
type consoleConfig struct {
	Reader     kube.Reader
	Collector  kube.Collector
	Data       kube.Data // the console's own records (jobs, dashboards); nil reads none
	Dev        bool
	Git        git.Reader
	Snapshots  git.Snapshots
	Repository string
	Token      string
	AppRepos   bool // whether a token that creates app repositories is configured (a new app can be finished)
	Now        func() time.Time
}

// consoleServer is one running console: the pinned tree, the cluster reader and the templates.
type consoleServer struct {
	bundle     *orchestration.ConsoleBundle
	reader     kube.Reader
	collector  kube.Collector
	data       kube.Data
	github     github.Console
	appConsole github.Console // anonymous reads of app repositories (their swhurl.yaml)
	repository string
	token      string
	appRepos   bool
	templates  map[string]*template.Template
	static     fs.FS
	assets     string
	identity   string
	hasGitHub  bool
	now        func() time.Time

	stackMu sync.Mutex
	stacks  map[string]stackEntry

	factsMu sync.Mutex
	merged  map[int]pages.Pull // merged pull requests, by number
	reached map[int]string     // when this server first saw a job's change at its end, by job
}

func newConsoleServer(config consoleConfig) (*consoleServer, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	server := &consoleServer{reader: config.Reader, collector: config.Collector, data: config.Data, now: config.Now,
		appConsole: github.NewConsole(nil), repository: config.Repository, token: config.Token, appRepos: config.AppRepos,
		stacks: map[string]stackEntry{}, merged: map[int]pages.Pull{}, reached: map[int]string{}}
	if server.repository == "" {
		server.repository = consoleRepo
	}
	// A token reads pull requests either way, as the Python console's --dev does; --dev pins the checkout instead.
	if config.Token != "" {
		server.github = github.NewConsole(nil)
		server.hasGitHub = true
	}
	if config.Dev {
		snapshots := config.Snapshots
		if snapshots == nil {
			snapshots = git.New()
		}
		server.bundle = &orchestration.ConsoleBundle{Source: localConsole{snapshots: snapshots, root: "."}}
		server.identity = devIdentity
	} else {
		server.bundle = &orchestration.ConsoleBundle{Source: orchestration.ConsoleSourceOf{
			Console: server.github, Repository: config.Repository, Token: config.Token}}
	}
	static, err := fs.Sub(consoleFiles, "console/static")
	if err != nil {
		return nil, err
	}
	server.static = static
	if server.assets, err = assetHash(static); err != nil {
		return nil, err
	}
	if server.templates, err = consoleTemplates(); err != nil {
		return nil, err
	}
	return server, nil
}

// localConsole reads the checkout the operator started the console in: the same filtered bytes GitHub serves.
type localConsole struct {
	snapshots git.Snapshots
	root      string
}

func (l localConsole) Revision(context.Context) (string, error) {
	return l.snapshots.Head(l.root)
}

func (l localConsole) Tree(_ context.Context, revision string) (map[string][]byte, error) {
	archive, err := l.snapshots.Archive(l.root, revision)
	if err != nil {
		return nil, err
	}
	return orchestration.FilterTarball(archive, false)
}

// assetHash names the static files, so a new console image is never served with an old stylesheet from a cache.
func assetHash(static fs.FS) (string, error) {
	digest := sha256.New()
	var files []string
	err := fs.WalkDir(static, ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, name)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	slices.Sort(files)
	for _, name := range files {
		body, err := fs.ReadFile(static, name)
		if err != nil {
			return "", err
		}
		digest.Write(body)
	}
	return hex.EncodeToString(digest.Sum(nil))[:12], nil
}

var shortenedHash = regexp.MustCompile(`\b([0-9a-f]{12})[0-9a-f]{8,}\b`)

// short escapes a value and shortens each commit id or digest in it to twelve characters, the full value on hover.
func shortValue(value any) template.HTML {
	text := html.EscapeString(fmt.Sprint(value))
	return template.HTML(shortenedHash.ReplaceAllString(text, `<span class="hash" title="$0">$1…</span>`))
}

func mark(key string) template.HTML {
	return template.HTML(`<span class="mark ` + html.EscapeString(key) + `" aria-hidden="true"></span>`)
}

func jobMark(state string) string {
	switch state {
	case "running":
		return "updating"
	case "succeeded":
		return "healthy"
	case "failed", "interrupted":
		return "failing"
	}
	return "updating"
}

func checkMark(level string) string {
	if level == "ok" {
		return "healthy"
	}
	if level == "bad" {
		return "failing"
	}
	return "warn"
}

// consoleTemplates parses one set per page: a page's definitions may not change another page's, so base, the
// shared macros and the page are parsed together each time.
func consoleTemplates() (map[string]*template.Template, error) {
	funcs := template.FuncMap{"short": shortValue, "mark": mark, "jobMark": jobMark, "checkMark": checkMark,
		"stateOf": stateOf, "runningOf": runningOf, "follow": follow, "hasPrefix": strings.HasPrefix,
		"join": strings.Join, "fieldOf": fieldOf, "submitOf": submitOf, "addressOf": addressOf,
		"channelSubmit": channelSubmit, "teamEnvOf": teamEnvOf,
		// href is one attribute holding a link's address, escaped once: in an attribute html/template would escape "+"
		// as well, which a query string uses for a space and a browser reads as one, and a query string has "&".
		"href": func(address string) template.HTMLAttr {
			return template.HTMLAttr(`href="` + html.EscapeString(address) + `"`)
		},
		// filterHref is a filter bar's link as the console's Jinja macro wrote it: the macro's own text was never
		// escaped, so the "&" between parameters stays, and the values interpolated into it were, so a team name's
		// markup becomes entities. A value that itself holds "&" is the one case this writes differently.
		"filterHref": func(address string) template.HTMLAttr {
			return template.HTMLAttr(`href="` + macroEscaped(address) + `"`)
		},
		// notLast separates a list's entries but the last, as Jinja's loop.last does.
		"notLast": func(i, count int) bool { return i < count-1 },
		// raw is a page's own text, written as HTML the way Jinja's caller() block is Markup.
		"raw": func(text string) template.HTML { return template.HTML(text) }}
	pages, err := fs.Glob(consoleFiles, "console/templates/*.html")
	if err != nil {
		return nil, err
	}
	found := map[string]*template.Template{}
	for _, name := range pages {
		base := path.Base(name)
		if strings.HasPrefix(base, "_") || base == "base.html" {
			continue
		}
		set := template.New("console").Funcs(funcs)
		for _, part := range []string{"console/templates/base.html", "console/templates/_status.html",
			"console/templates/_form.html", name} {
			body, err := consoleFiles.ReadFile(part)
			if err != nil {
				return nil, err
			}
			if _, err := set.Parse(string(body)); err != nil {
				return nil, fmt.Errorf("template %s: %w", path.Base(part), err)
			}
		}
		found[base] = set
	}
	return found, nil
}

// serve runs the server until it is stopped, with the reads bounded and the HTTP timeouts set.
func (s *consoleServer) serve(out io.Writer, address string) int {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.Handle("/static/", http.StripPrefix("/static/", staticHandler(s.static)))
	mux.HandleFunc("/platform", s.platform)
	mux.HandleFunc("/apps", s.apps)
	mux.HandleFunc("/catalogue", s.catalogue)
	mux.HandleFunc("/egress", s.egress)
	mux.HandleFunc("/units/", s.unit)
	mux.HandleFunc("/teams", s.teams)
	mux.HandleFunc("/activity", s.activity)
	mux.HandleFunc("/jobs/{id}", s.job)
	mux.HandleFunc("/jobs/{id}/events", s.jobEvents)
	// The addresses the console moved: the old page names still lead to the page that replaced them.
	mux.HandleFunc("/services", redirect("/apps?view=map"))
	mux.HandleFunc("/units", redirect("/platform#units"))
	mux.HandleFunc("/jobs", redirect("/activity"))
	mux.HandleFunc("/", s.overview)
	server := &http.Server{Addr: address, Handler: s.identityMiddleware(mux), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	// A SIGTERM stops the process as the pod's grace period expects; the job drain, which must catch it, comes with
	// the jobs this server does not yet run. The module names no process signals (restore.go states the rule).
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(out, "console: %v\n", err)
			return 1
		}
		return 0
	case <-stop.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		return 0
	}
}

// identityMiddleware refuses a request without oauth2-proxy's identity, and one a page of another site sent.
func (s *consoleServer) identityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, strings.TrimSuffix(r.URL.Path, "/"), http.StatusPermanentRedirect)
			return
		}
		identity := s.identity
		if identity == "" {
			identity = strings.TrimSpace(r.Header.Get(identityHeader))
		}
		if identity == "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, "sign-in required: no identity from oauth2-proxy\n")
			return
		}
		next.ServeHTTP(w, withIdentity(r, identity))
	})
}

// redirect is one of the console's moved addresses.
func redirect(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusMovedPermanently) }
}

// consoleIdentity is the signed-in operator's email, carried from the middleware to the page.
type consoleIdentity struct{ Email string }

type identityKey struct{}

func withIdentity(r *http.Request, email string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), identityKey{}, consoleIdentity{Email: email}))
}

func identityOf(r *http.Request) string {
	found, _ := r.Context().Value(identityKey{}).(consoleIdentity)
	return found.Email
}

// macroEscaped is text as html escaping writes it, except that "&" stays: a console macro's own address text was
// never escaped, and only the values inside it were.
func macroEscaped(text string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(text)
}

// staticHandler serves the embedded assets with their content-hashed URLs cached for ever.
func staticHandler(static fs.FS) http.Handler {
	fileServer := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fileServer.ServeHTTP(w, r)
	})
}

// shell is what every page's template shows around its content.
type shell struct {
	Title       string
	Path        string
	Identity    string
	ReadAt      string
	RefreshURL  string
	Assets      string
	AutoRefresh bool
	HasGithub   bool
	Running     []jobView
}

// stateView is a state as a template prints it: the mark and word, optionally without "Healthy", optionally with
// the reason.
type stateView struct {
	State         pages.State
	Detail, Quiet bool
}

func stateOf(state pages.State, detail, quiet bool) stateView {
	return stateView{State: state, Detail: detail, Quiet: quiet}
}

// runningView is a running job where a row's button was.
type runningView struct {
	Job        any // a pages.Job or a jobView: both carry the id and action a row shows
	RefreshURL string
}

func runningOf(job any, refresh string) runningView {
	return runningView{Job: job, RefreshURL: refresh}
}

// follow re-reads the page a running job is on shortly, so the row shows what the job did without a refresh.
func follow(url string) template.HTMLAttr {
	return template.HTMLAttr(`hx-get="` + html.EscapeString(url) +
		`" hx-trigger="load delay:10s" hx-select="main" hx-target="main" hx-swap="outerHTML"`)
}

// textFieldView is one text field of a form.
type textFieldView struct{ ID, Label, Value, Placeholder, Hint, Name string }

func fieldOf(id, label, value, placeholder, hint, name string) textFieldView {
	return textFieldView{ID: id, Label: label, Value: value, Placeholder: placeholder, Hint: hint, Name: name}
}

// submitView is a form's submit row: its button, and the note saying what happens next.
type submitView struct {
	Label, Note string
	Disabled    bool
	Github      bool
}

func submitOf(github bool, label, note string, disabled bool) submitView {
	return submitView{Label: label, Note: note, Disabled: disabled, Github: github}
}

// addressView is the notification channel's address field.
type addressView struct{ ID, Label string }

func addressOf(id, channel string) addressView {
	if channel != "" {
		return addressView{ID: id, Label: "New address"}
	}
	return addressView{ID: id, Label: "Address"}
}

func channelSubmit(channel string) string {
	if channel != "" {
		return "Open new address PR"
	}
	return "Open channel PR"
}

// teamEnvView is one environment of a team's app, as the table shows it.
type teamEnvView struct {
	App, Env string
	Row      *pages.AppRow
}

func teamEnvOf(app, env string, row *pages.AppRow) teamEnvView {
	return teamEnvView{App: app, Env: env, Row: row}
}

// jobView is one console job as a row shows it.
type jobView struct {
	ID                                         int
	Action, TargetLabel, Identity, State, Link string
	StartedClock, FinishedClock                string
}

// pullView is one open pull request as the list of waiting requests shows it.
type pullView struct {
	Number int
	Title  string
	URL    string
	Opened string
}

// overviewData is the Overview page: what needs attention, what is waiting, and what ran.
type overviewData struct {
	shell
	Checks   pages.Checks
	AppCount int
	Units    []pages.Unit
	Problems []pages.Problem
	Updating []pages.Problem
	PRs      []pullView
	PRsError string
	Recent   []jobView
}

// platformData is the Platform page: the units in the order they build on each other, and the checks beside them.
type platformData struct {
	shell
	Checks    pages.Checks
	Units     []pages.Unit
	Problems  []pages.Problem
	Updating  []pages.Problem
	Groups    []pages.Group
	Unhealthy []pages.Unit
	Elsewhere []pages.Unit
	Layers    []layerView
	Revision  string
}

// layerView is one title of the units list and the units under it.
type layerView struct {
	Title, Text string
	Members     []unitRowView
}

// unitRowView is one row of the units list, with the job running on it when there is one.
type unitRowView struct {
	pages.UnitRow
	Refused    string
	Job        *jobView
	RefreshURL string
}

// errorData is the error page.
type errorData struct {
	shell
	Error string
}

// page is the shell around a page, with the jobs running now read for the header.
func (s *consoleServer) page(r *http.Request, autoRefresh bool) shell {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	return s.pageWith(r, autoRefresh, s.jobsOf(ctx))
}

// pageWith is the shell around a page that has read the jobs already.
func (s *consoleServer) pageWith(r *http.Request, autoRefresh bool, jobs []pages.Job) shell {
	readAt := s.now().Format("15:04")
	return shell{Path: r.URL.Path, Identity: identityOf(r), ReadAt: readAt, Assets: s.assets,
		AutoRefresh: autoRefresh, HasGithub: s.hasGitHub, RefreshURL: r.URL.RequestURI(), Running: runningJobs(jobViewsOf(jobs))}
}

func (s *consoleServer) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	set, known := s.templates[name]
	if !known {
		fmt.Fprintf(os.Stderr, "console: no template %s\n", name)
		return
	}
	if err := set.ExecuteTemplate(w, "base", data); err != nil {
		// The status line is already sent: what is left is a truncated page, logged rather than hidden.
		fmt.Fprintf(os.Stderr, "console: template %s: %v\n", name, err)
	}
}

func (s *consoleServer) failure(w http.ResponseWriter, r *http.Request, status int, problem string) {
	page := errorData{shell: s.page(r, false), Error: problem}
	s.render(w, status, "error.html", page)
}

// overview reads the pinned tree and the cluster once, and shows what needs attention.
func (s *consoleServer) overview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	data, err := s.readOverview(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	jobs := s.jobsOf(ctx)
	data.shell = s.pageWith(r, true, jobs)
	for _, job := range jobViewsOf(jobs) {
		if job.State != "running" && len(data.Recent) < recentJobs {
			data.Recent = append(data.Recent, job)
		}
	}
	s.render(w, http.StatusOK, "overview.html", data)
}

// readOverview is what a page shows to a failed read: Go's own words, never a fallback.
func (s *consoleServer) readOverview(ctx context.Context) (overviewData, error) {
	revision, err := s.bundle.Revision(ctx)
	if err != nil {
		return overviewData{}, fmt.Errorf("Go status unavailable: %w", err)
	}
	tree, err := s.bundle.Tree(ctx, revision)
	if err != nil {
		return overviewData{}, fmt.Errorf("Go status unavailable: %w", err)
	}
	records, err := orchestration.Records(tree)
	if err != nil {
		return overviewData{}, fmt.Errorf("Go status unavailable: %w", err)
	}
	units, err := orchestration.ConsoleUnits(ctx, s.collector)
	if err != nil {
		return overviewData{}, err
	}
	collection, err := orchestration.ConsoleStatus(ctx, records, revision, s.reader, units, s.compare, "console", s.now)
	if err != nil {
		return overviewData{}, err
	}
	checks, err := orchestration.ConsoleChecks(ctx, s.reader, "console", s.now)
	if err != nil {
		return overviewData{}, err
	}
	listed, err := pages.Units(units)
	if err != nil {
		return overviewData{}, fmt.Errorf("Flux units unavailable: %w", err)
	}
	prs, prsErr := s.openPulls(ctx)
	return overviewData{Checks: checks, AppCount: len(collection.Instances) + len(collection.Removing), Units: listed,
		Problems: pages.Problems(checks, listed), Updating: pages.Updating(listed), PRs: prs, PRsError: prsErr}, nil
}

// platform shows the units in the order they build on each other and the checks beside them.
func (s *consoleServer) platform(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	data, err := s.readPlatform(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	data.shell = s.page(r, true)
	s.render(w, http.StatusOK, "platform.html", data)
}

func (s *consoleServer) readPlatform(ctx context.Context) (platformData, error) {
	units, err := orchestration.ConsoleUnits(ctx, s.collector)
	if err != nil {
		return platformData{}, err
	}
	listed, err := pages.Units(units)
	if err != nil {
		return platformData{}, fmt.Errorf("Flux units unavailable: %w", err)
	}
	checks, err := orchestration.ConsoleChecks(ctx, s.reader, "console", s.now)
	if err != nil {
		return platformData{}, err
	}
	revision := pages.CommonRevision(listed)
	return platformData{Checks: checks, Units: listed, Problems: pages.Problems(checks, listed),
		Updating: pages.Updating(listed), Groups: pages.CheckGroups(checks), Revision: revision,
		Unhealthy: unusual(listed, func(unit pages.Unit) bool { return !unit.Healthy() }),
		Elsewhere: unusual(listed, func(unit pages.Unit) bool { return unit.Revision != revision }),
		Layers:    platformLayers(listed, revision)}, nil
}

// unusual is the units a condition selects, for the two counts the units list's summary states.
func unusual(units []pages.Unit, condition func(pages.Unit) bool) []pages.Unit {
	var found []pages.Unit
	for _, unit := range units {
		if condition(unit) {
			found = append(found, unit)
		}
	}
	return found
}

// platformLayers is the units list's layers, each with its rows: a layer nothing sits in is left out.
func platformLayers(units []pages.Unit, revision string) []layerView {
	var found []layerView
	for _, layer := range append(slices.Clone(pages.Layers), struct{ Prefix, Title, Text string }{Title: pages.OtherLayer}) {
		view := layerView{Title: layer.Title, Text: layer.Text}
		for _, unit := range units {
			if unit.Layer() == layer.Title {
				view.Members = append(view.Members, unitRowView{UnitRow: pages.UnitRowOf(unit, revision),
					Refused: pages.RefusedUnits[unit.Name]})
			}
		}
		if len(view.Members) > 0 {
			found = append(found, view)
		}
	}
	return found
}

// compare is the GitHub comparison the status reads: nil when this console has no token, which leaves every
// revision unproved rather than assuming it unaffected.
func (s *consoleServer) compare(from, to string) ([]string, bool, error) {
	if s.github == nil {
		return nil, false, errors.New("no GitHub token")
	}
	return s.github.Compare(context.Background(), s.repository, s.token, from, to)
}

// openPulls lists the console's open pull requests, or says why it could not.
func (s *consoleServer) openPulls(ctx context.Context) ([]pullView, string) {
	pulls, problem := s.consolePulls(ctx)
	if problem != "" {
		return nil, problem
	}
	found := make([]pullView, 0, len(pulls))
	for _, pull := range pulls {
		found = append(found, pullView{Number: pull.Number, Title: pull.Title, URL: pull.URL, Opened: pull.Opened})
	}
	return found, ""
}
