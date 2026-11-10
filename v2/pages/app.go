package pages

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
)

// The app page, the page an app that is not deployed yet shows, a promotion's review and the New app form: what they
// show, computed from the rows, the instance's own records and the documents a page reads.

// NamedValue is one name beside its text, in the order its document states them.
type NamedValue struct{ Name, Value string }

// Choice is one radio choice: the value it submits, the title, and the text under it.
type Choice struct{ Value, Title, Text string }

// IncludedAnswer is one template answer the New app form names, and the line saying what it includes.
type IncludedAnswer struct{ Key, Label, Text string }

// EveryApp are the answers every app includes, whatever its type: the template asks nothing for them.
var EveryApp = map[string]bool{"events": true, "flags": true}

// Included are the answers the New app form lists, in the order it lists them, with what each includes.
var Included = []IncludedAnswer{
	{Key: "frontend", Label: "Browser frontend", Text: "a page built with Vite+ that the app serves at /, calling its own /api"},
	{Key: "database", Label: "SQLite", Text: "a database file the platform keeps on a volume and backs up nightly"},
	{Key: "api", Label: "API description", Text: "an OpenAPI document of its routes, so other apps can find and call it"},
	{Key: "events", Label: "Events", Text: "an AsyncAPI document of the events it publishes, with an example event"},
	{Key: "flags", Label: "Feature flags", Text: "an example feature flag, set per environment from the console"},
}

// exposureLabels are how the console words an instance's exposure on its own page.
var exposureLabels = map[string]string{"private": "private (no route)",
	"authenticated-web": "signed-in (Google sign-in)", "public": "public (no sign-in)"}

// ExposureWord is an exposure as an app page words it, its first letter capitalised.
func ExposureWord(exposure string) string {
	word := exposureLabels[exposure]
	if word == "" {
		word = exposure
	}
	if word == "" {
		return ""
	}
	return strings.ToUpper(word[:1]) + word[1:]
}

// ExposureChoices are how a form offers who can reach an app, with the platform's domain in each line.
func ExposureChoices(domain string) []Choice {
	return []Choice{
		{Value: "authenticated-web", Title: "Signed in",
			Text: "A web address under " + domain + ", behind Google sign-in: only the accounts on the sign-in list."},
		{Value: "private", Title: "Private",
			Text: "No web address: only the apps granted a call to it can reach it. For APIs, workers and jobs."},
	}
}

// StackCatalogue is v2/contract/stacks.json as the New app form shows it: the stacks in the order the file states
// them, each with its template repository and description, and the app types the templates can build.
type StackCatalogue struct {
	Stacks []StackInfo
	Types  []AppType
}

// StackInfo is one stack: its name, its template repository, one line about it and the app types it builds.
type StackInfo struct {
	Name, Label, Repository, Description, Types string
}

// AppType is one kind of app the console creates: what it is, the stacks that build it, the template answers that
// make it, the exposure staging starts with and the included answers its pills name.
type AppType struct {
	Key, Title, Text string
	Stacks           []string
	Answers          []NamedValue
	Exposure         string
	Kind             string // the template answer that says web or worker
	Included         []IncludedAnswer
}

// Named is the stack's display name, or its own name when it has none.
func (s StackInfo) Named() string { return StackLabelOf(s.Name) }

// StackLabelOf is a stack as a page names it, or the stack's own name when it has no label.
func StackLabelOf(stack string) string {
	if label := StackNames[stack]; label != "" {
		return label
	}
	return stack
}

// IncludedOf is the answers an app type includes, in the console's order: every app's, then the type's own.
func IncludedOf(answers []NamedValue) []IncludedAnswer {
	own := map[string]bool{}
	for _, answer := range answers {
		if answer.Value != "none" {
			own[answer.Name] = true
		}
	}
	found := make([]IncludedAnswer, 0, len(Included))
	for _, included := range Included {
		if EveryApp[included.Key] || own[included.Key] {
			found = append(found, included)
		}
	}
	return found
}

// TeamChoice is one team the console offers: its description, and whether it has a notification channel.
type TeamChoice struct {
	Name, Description string
	Channel           bool
}

// TeamChoices is every registered team in name order, as a form offers them.
func TeamChoices(registry map[string]TeamInfo) []TeamChoice {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	slices.Sort(names)
	found := make([]TeamChoice, 0, len(names))
	for _, name := range names {
		found = append(found, TeamChoice{Name: name, Description: registry[name].Description,
			Channel: registry[name].Channel != ""})
	}
	return found
}

// ConnectableApp is one web app another app could call, with what its own API document says about it.
type ConnectableApp struct{ App, Team, Says string }

// ConnectableEvent is one event another app could receive, with what its own document says about it.
type ConnectableEvent struct{ Name, App, Team, Says string }

// Connectable is what an app can connect to in one environment: the web apps it could call and the events it could
// receive, or why neither could be read.
type Connectable struct {
	Apps    []ConnectableApp
	Events  []ConnectableEvent
	Problem string
}

// ConnectableTo is what an app may connect to in one environment: every other web app, and every event those apps
// publish, leaving out what an existing instance already calls or receives.
func ConnectableTo(rows []AppRow, offers map[string]Offers, env, app string) Connectable {
	here, called, received := connectableHere(rows, env, app)
	found := Connectable{}
	for _, row := range here {
		if row.Instance.App != app {
			connectableRow(&found, row, offers[row.Instance.App], called, received)
		}
	}
	return found
}

// connectableHere is one environment's rows and what the instance already calls and receives, so the form leaves
// those out.
func connectableHere(rows []AppRow, env, app string) ([]AppRow, map[string]bool, map[string]bool) {
	here := make([]AppRow, 0, len(rows))
	for _, row := range rows {
		if row.Instance.Env == env {
			here = append(here, row)
		}
	}
	mine := Connections{}
	for _, row := range here {
		if row.Instance.App == app {
			mine = row.Connections()
		}
	}
	called, received := map[string]bool{}, map[string]bool{}
	for _, name := range mine.Calls {
		called[name] = true
	}
	for _, name := range mine.Subscribes {
		received[name] = true
	}
	return here, called, received
}

// connectableRow adds one other app's call and each of its events the instance does not already have.
func connectableRow(found *Connectable, row AppRow, said Offers, called, received map[string]bool) {
	team, connections := row.Team(), row.Connections()
	if connections.Address != "" && !called[row.Instance.App] {
		found.Apps = append(found.Apps, ConnectableApp{App: row.Instance.App, Team: team, Says: said.APIText()})
	}
	for _, event := range connections.Publishes {
		name := row.Instance.App + "." + event
		if !received[name] {
			found.Events = append(found.Events, ConnectableEvent{Name: name, App: row.Instance.App, Team: team,
				Says: said.EventText(event)})
		}
	}
}

// APIText is what an app's OpenAPI document says about its API, empty when it describes none.
func (o Offers) APIText() string {
	if o.API == nil {
		return ""
	}
	return o.API.Description
}

// EventText is what an app's AsyncAPI document says about one of its events, empty when it says nothing.
func (o Offers) EventText(name string) string {
	for _, event := range o.Events {
		if event.Name == name {
			return event.Description
		}
	}
	return ""
}

// FlagLine is one line of an instance's Feature flags card, as the template states it: the declaration, the override
// and what the app runs with.
type FlagLine struct {
	Name         string
	Description  string
	Type         string
	Declared     string // the declared value, as JSON
	Min, Max     string // a bound as the declaration states it, empty when it states none
	Enum         []string
	Undeclared   bool
	Overridden   bool
	OverrideText string // the override as JSON, empty when it is not JSON the app can read
	AgeKnown     bool
	AgeWord      string // "today", "3 days ago"
	Stale        bool
	Effective    string // what the app runs with, as JSON
	EffectiveRaw string // the same value as a form field or an enumeration writes it
}

// FlagView is the card: its rows and why it cannot show all of them.
type FlagView struct {
	Rows    []FlagLine
	Problem string
}

// FlagAge is how old an override is, in the card's words.
func FlagAge(days int) string {
	if days == 0 {
		return "today"
	}
	if days == 1 {
		return "1 day ago"
	}
	return strconv.Itoa(days) + " days ago"
}

// ScaleField is one field of the app page's Scale form: the form's name, the setting it changes, its label and the
// value it shows as a placeholder.
type ScaleField struct{ Field, Setting, Label, Placeholder string }

// ScaleSettings are the two settings the Scale form changes, in its order.
var ScaleSettings = []struct{ Field, Setting, Label string }{
	{"replicas", "replicas", "Replicas"},
	{"memory", "memoryMiB", "Memory (MiB)"},
}

// ScaleFieldsOf is the Scale form's fields, each with its current value as a placeholder.
func ScaleFieldsOf(settings map[string]string) []ScaleField {
	found := make([]ScaleField, 0, len(ScaleSettings))
	for _, field := range ScaleSettings {
		found = append(found, ScaleField{Field: field.Field, Setting: field.Setting, Label: field.Label,
			Placeholder: settings[field.Setting]})
	}
	return found
}

// Has is whether the app has an instance in one environment.
func (s Summary) Has(env string) bool { return s.Envs[env] != nil }

// HasHosts is whether the instance has a web address to open.
func (p AppPage) HasHosts() bool { return len(p.Hosts) > 0 }

// AppPage is one instance's page: what it is, what it runs, who reaches it and what it connects to.
type AppPage struct {
	Instance         Instance
	Stack            string
	StackLabel       string
	Tabs             []Link
	Row              *AppRow
	Summary          *Summary
	State            State
	Team             string
	DesiredImage     string
	Database         string
	Hosts            []string
	Exposure         string
	ExposureWord     string
	Problems         []ProblemRow
	Routes           []Route
	Certificates     []Certificate
	Connections      Connections
	Owner            string
	Allows           []string
	Writable         bool
	V2               bool
	Github           bool
	Domain           string
	Connectable      Connectable
	Exposures        []Choice
	Teams            []TeamChoice
	TeamsError       string
	Scale            []ScaleField
	Variables        []NamedValue
	VariablesKnown   bool
	Flags            FlagView
	FlagChanged      string
	FlagsWritable    bool
	LogsURL          string
	DashboardURL     string
	RepositoryURL    string
	PromotionProblem string
	RunningJob       *Job
	UnitSuspended    bool
	RunningDigests   string
	DigestsKnown     bool
}

// AppInput is what an instance's page reads: its rows, the documents of the commit the page shows, its flags and the
// facts it links to.
type AppInput struct {
	Instance       Instance
	Rows           []AppRow
	Site           Site
	Stacks         map[string]string
	Dashboards     map[string]string
	Jobs           []Job
	Flags          FlagView
	FlagChanged    string
	FlagsWritable  bool
	Teams          map[string]TeamInfo
	TeamsError     string
	Settings       map[string]string
	Variables      []NamedValue
	VariablesKnown bool
	Connectable    Connectable
	Github         bool
}

// AppPageOf computes an instance's page from its rows and the facts a page reads.
func AppPageOf(input AppInput) AppPage {
	page := AppPage{Instance: input.Instance, Stack: input.Stacks[input.Instance.App],
		Flags: input.Flags, FlagChanged: input.FlagChanged, FlagsWritable: input.FlagsWritable,
		Teams: TeamChoices(input.Teams), TeamsError: input.TeamsError, Scale: ScaleFieldsOf(input.Settings),
		Variables: input.Variables, VariablesKnown: input.VariablesKnown, Connectable: input.Connectable,
		Github: input.Github, Domain: input.Site.Domain, Owner: input.Site.Owner,
		Exposures: ExposureChoices(input.Site.Domain),
		LogsURL:   input.Site.LogsURL(input.Instance.App), RepositoryURL: input.Site.RepositoryURL(input.Instance.App)}
	page.StackLabel = StackLabelOf(page.Stack)
	if dashboard := input.Dashboards[input.Instance.App]; dashboard != "" {
		page.DashboardURL = input.Site.DashboardURL(dashboard)
	}
	page.summaryOf(input.Rows)
	if page.Row == nil {
		return page
	}
	page.rowFacts()
	page.reportFacts()
	page.RunningJob = runningJobOf(input.Jobs, input.Instance)
	return page
}

// summaryOf is the app's environments and this instance's row among them, with the tabs to switch between them.
func (p *AppPage) summaryOf(rows []AppRow) {
	summaries := Summaries(rows)
	for i := range summaries {
		if summaries[i].App != p.Instance.App {
			continue
		}
		p.Summary = &summaries[i]
		p.Row = summaries[i].Envs[p.Instance.Env]
	}
	for _, env := range []string{"staging", "prod"} {
		if p.Summary != nil && p.Summary.Has(env) {
			p.Tabs = append(p.Tabs, Link{Name: env, Href: "/apps/" + p.Instance.App + "/" + env,
				Here: env == p.Instance.Env})
		}
	}
}

// rowFacts is what the instance's own records declare: what it is, who owns it, and what it connects to.
func (p *AppPage) rowFacts() {
	p.State = p.Row.State
	p.Team = p.Row.Team()
	p.DesiredImage = p.Row.ImageReference()
	p.Writable = p.Row.Inventory != nil && p.Row.Removed == ""
	p.V2 = p.Row.Inventory != nil
	if p.Row.Inventory != nil {
		p.Database, p.Exposure, p.Allows = p.Row.Inventory.Database.Kind, p.Row.Inventory.Exposure, p.Row.Inventory.Allows
	}
	p.ExposureWord = ExposureWord(p.Exposure)
	p.Connections = p.Row.Connections()
}

// reportFacts is what the cluster observed: its problems, routes, certificates and running image.
func (p *AppPage) reportFacts() {
	report := p.Row.Report
	if report == nil {
		return
	}
	p.Problems, p.Routes, p.Certificates = report.Problems, report.Routes, report.Certificates
	for _, route := range report.Routes {
		p.Hosts = append(p.Hosts, route.Hosts...)
	}
	if p.Instance.Env == "staging" && !report.Promotion.Ready {
		p.PromotionProblem = strings.ReplaceAll(report.Promotion.Reason, "_", " ")
	}
	p.UnitSuspended = report.Live.Unit.Suspended != nil && *report.Live.Unit.Suspended
	p.DigestsKnown = report.Live.Running.Digests != nil
	if report.Live.Running.Digests != nil {
		p.RunningDigests = strings.Join(*report.Live.Running.Digests, ", ")
	}
}

// runningJobOf is the job still changing this instance, if one is.
func runningJobOf(jobs []Job, instance Instance) *Job {
	for i := range jobs {
		if jobs[i].Running() && (jobs[i].Unit == instance.Unit() || jobs[i].Unit == instance.Name()) {
			return &jobs[i]
		}
	}
	return nil
}

// PendingPull is one open pull request as the page that waits for it shows it.
type PendingPull struct {
	Number    int
	Title     string
	URL       string
	Opened    string
	ChecksURL string
	Checks    string
	Merging   string
}

// PendingPullOf is one open pull request as the page shows it: the checks on it, and how it merges.
func PendingPullOf(open OpenPull) PendingPull {
	found := PullOf(open)
	checksURL := found.ChecksURL
	if open.ChecksURL == "" {
		checksURL = found.URL + "/checks"
	}
	return PendingPull{Number: found.Number, Title: open.Title, URL: found.URL, Opened: open.Opened,
		ChecksURL: checksURL, Checks: found.Checks, Merging: MergeMode(open.Body)}
}

// Pending is what an instance not deployed yet shows: why it is not there, the jobs that create it and the pull
// requests that add it.
type Pending struct {
	Label    string
	App      string
	Removing bool
	InGit    bool
	Jobs     []Job
	PRs      []PendingPull
	PRsError string
}

// PendingOf computes the page: removing says the app was removed from Git and its live resources are going, in_git
// says Flux has not applied it yet, and neither says its pull request is still waiting.
func PendingOf(instance Instance, removing, inGit bool, jobs []Job, pulls []PendingPull, prsError string) Pending {
	found := []Job{}
	for _, job := range jobs {
		if job.Unit == instance.Name() && strings.HasPrefix(job.Action, "new app") {
			found = append(found, job)
		}
	}
	return Pending{Label: instance.Name(), App: instance.App, Removing: removing, InGit: inGit,
		Jobs: found, PRs: pulls, PRsError: prsError}
}

// PullsFor are the open pull requests that add or promote one instance.
func PullsFor(pulls []OpenPull, instance Instance) []PendingPull {
	found := []PendingPull{}
	for _, pull := range pulls {
		action, target := BranchTarget(pull.Branch)
		if (action == "new" || action == "v2-promote") && target == instance.Name() {
			found = append(found, PendingPullOf(pull))
		}
	}
	return found
}

// PromotionPage is the promotion review: a staging instance's image and what production runs now.
type PromotionPage struct {
	App    string
	PRs    []PendingPull
	Review *PromotionReview
}

// PromotionReview is what a promotion would change, and why it cannot be opened.
type PromotionReview struct {
	App      string
	Problem  string
	Outcome  string // same or update
	Image    string
	Previous string
}

// PromotionOf computes the review of staging's image against production's.
func PromotionOf(app string, staging, prod *AppRow) PromotionReview {
	review := PromotionReview{App: app, Image: staging.ImageReference(), Outcome: "update"}
	if prod != nil {
		review.Previous = prod.ImageReference()
	}
	if review.Image == review.Previous {
		review.Outcome = "same"
	}
	if staging.Report != nil && !staging.Report.Promotion.Ready {
		review.Problem = strings.ReplaceAll(staging.Report.Promotion.Reason, "_", " ")
	}
	return review
}

// NewAppForm is the New app form's answer as it is shown again after a refusal.
type NewAppForm struct{ Name, Type, Stack, Description, Team, Exposure string }

// NewAppPage is the New app form: the types and stacks a repository can be made from, the teams and the apps it can
// connect to.
type NewAppPage struct {
	Types            []AppType
	ChosenType       string
	StackTypes       map[string]string
	Stacks           []StackInfo
	Owner            string
	Domain           string
	Teams            []TeamChoice
	TeamsError       string
	CallableApps     []ConnectableApp
	OfferedEvents    []ConnectableEvent
	ConnectionsError string
	Exposures        []Choice
	ChosenExposure   string
	ChosenStack      string
	IncludedAll      []IncludedAnswer
	Form             NewAppForm
	Error            string
	Github           bool
	AppRepos         bool
	// ChosenCalls and ChosenEvents are the boxes a refused form left ticked.
	ChosenCalls, ChosenEvents []string
}

// NewAppInput is what the form reads: the catalogue at the pinned commit, the teams and what staging offers.
type NewAppInput struct {
	Catalogue   StackCatalogue
	Site        Site
	Teams       map[string]TeamInfo
	TeamsError  string
	Connectable Connectable
	Form        NewAppForm
	Error       string
	Github      bool
	AppRepos    bool
}

// NewAppPageOf computes the New app form from the catalogue and the environment it would deploy to.
func NewAppPageOf(input NewAppInput) NewAppPage {
	page := NewAppPage{Types: input.Catalogue.Types, Stacks: input.Catalogue.Stacks, Owner: input.Site.Owner,
		Domain: input.Site.Domain, Teams: TeamChoices(input.Teams), TeamsError: input.TeamsError,
		CallableApps: input.Connectable.Apps, OfferedEvents: input.Connectable.Events,
		ConnectionsError: input.Connectable.Problem, Form: input.Form, Error: input.Error,
		Github: input.Github, AppRepos: input.AppRepos, Exposures: ExposureChoices(input.Site.Domain),
		StackTypes: map[string]string{}, IncludedAll: Included}
	for _, stack := range input.Catalogue.Stacks {
		page.StackTypes[stack.Name] = stack.Types
	}
	page.ChosenType = "web-app"
	for _, appType := range page.Types {
		if appType.Key == page.Form.Type {
			page.ChosenType = appType.Key
		}
	}
	page.ChosenStack = page.Form.Stack
	if page.ChosenStack == "" {
		page.ChosenStack = "typescript"
	}
	page.ChosenExposure = page.Form.Exposure
	if page.ChosenExposure == "" {
		for _, appType := range page.Types {
			if appType.Key == page.ChosenType {
				page.ChosenExposure = appType.Exposure
			}
		}
	}
	return page
}

// JinjaJSON is a value as the console's `tojson` writes it: JSON with an object's keys sorted, ", " and ": "
// between them, and the characters that matter inside HTML escaped. Values are the plain ones a JSON document gives:
// nil, bool, string, float64, json.Number, []any and map[string]any.
func JinjaJSON(value any) string {
	var out strings.Builder
	writeJinjaJSON(&out, value)
	return out.String()
}

func writeJinjaJSON(out *strings.Builder, value any) {
	switch found := value.(type) {
	case []any:
		writeJinjaList(out, found)
	case []string:
		items := make([]any, 0, len(found))
		for _, item := range found {
			items = append(items, item)
		}
		writeJinjaList(out, items)
	case map[string]any:
		writeJinjaObject(out, found)
	default:
		out.WriteString(jinjaScalar(value))
	}
}

// writeJinjaList is one list, each item in turn.
func writeJinjaList(out *strings.Builder, items []any) {
	out.WriteString("[")
	for i, item := range items {
		if i > 0 {
			out.WriteString(", ")
		}
		writeJinjaJSON(out, item)
	}
	out.WriteString("]")
}

// writeJinjaObject is one object, its keys in name order.
func writeJinjaObject(out *strings.Builder, fields map[string]any) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	out.WriteString("{")
	for i, name := range names {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(jinjaQuoted(name))
		out.WriteString(": ")
		writeJinjaJSON(out, fields[name])
	}
	out.WriteString("}")
}

// jinjaScalar is one value that is not a list or an object.
func jinjaScalar(value any) string {
	switch found := value.(type) {
	case nil:
		return "null"
	case bool:
		if found {
			return "true"
		}
		return "false"
	case string:
		return jinjaQuoted(found)
	case float64:
		return pythonNumber(found)
	case int64:
		return strconv.FormatInt(found, 10)
	case int:
		return strconv.Itoa(found)
	case json.Number:
		return found.String()
	}
	quoted, _ := json.Marshal(value)
	return htmlSafeJSON(string(quoted))
}

// jinjaQuoted is a string as JSON writes it, with the characters that matter inside HTML escaped.
func jinjaQuoted(text string) string {
	quoted, _ := json.Marshal(text)
	return htmlSafeJSON(string(quoted))
}

// pythonNumber is a JSON number as Python's json module writes it, which keeps a whole float's ".0".
func pythonNumber(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e16 {
		return strconv.FormatFloat(value, 'f', 1, 64)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// htmlSafeJSON is a JSON text with the characters that matter inside HTML escaped, as Jinja's tojson does.
func htmlSafeJSON(text string) string {
	return strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`, "'", `\u0027`).Replace(text)
}
