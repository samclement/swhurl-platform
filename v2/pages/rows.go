package pages

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"
)

// An instance is always named <app>/<env>; its Flux unit and namespace are derived from that name and never guessed
// from a label.
type Instance struct{ App, Env string }

// Named is an instance of the two environments a v2 record pair can name.
func Named(app, env string) (Instance, bool) {
	if app == "" || !environments[env] {
		return Instance{}, false
	}
	return Instance{App: app, Env: env}, true
}

// InstanceOf is the instance a unit names (app-<app>-<env>), whether or not it is a known one.
func InstanceOf(unit string) (Instance, bool) {
	if app, env, found := instanceOfUnit(unit); found {
		return Instance{App: app, Env: env}, true
	}
	return Instance{}, false
}

// Unit is the Flux unit that applies the instance.
func (i Instance) Unit() string { return "app-" + i.App + "-" + i.Env }

// Namespace is the namespace the instance runs in.
func (i Instance) Namespace() string { return i.App + "-" + i.Env }

// Name is how every page names the instance.
func (i Instance) Name() string { return i.App + "/" + i.Env }

// Connection is one relationship as the records state it: the role, the app, event or host on the other side, and the
// resolution's outcome and reason.
type Connection struct {
	Role    string `json:"role"`
	App     string `json:"app,omitempty"`
	Event   string `json:"event,omitempty"`
	Host    string `json:"host,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Artifact is the image an instance runs.
type Artifact struct {
	Image  string `json:"image"`
	Digest string `json:"digest"`
}

// Database is the state an instance keeps itself.
type Database struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// Allocation is an instance's place on the egress proxy.
type Allocation struct {
	Port   int32    `json:"port"`
	Grants []string `json:"grants"`
}

// Inventory is what an instance's records state, as the Go report's inventory block carries it: the facts a page
// shows beside the report, never a second verdict.
type Inventory struct {
	Team          string       `json:"team"`
	Kind          string       `json:"kind"`
	Exposure      string       `json:"exposure"`
	Port          int32        `json:"port"`
	Hosts         []string     `json:"hosts"`
	Address       string       `json:"address"`
	API           string       `json:"api"`
	Artifact      Artifact     `json:"artifact"`
	Database      Database     `json:"database"`
	Offers        string       `json:"offers"`
	Egress        Allocation   `json:"egress"`
	Calls         []string     `json:"calls"`
	Allows        []string     `json:"allows"`
	Callers       []string     `json:"callers"`
	Publishes     []string     `json:"publishes"`
	Subscriptions []string     `json:"subscriptions"`
	Connections   []Connection `json:"connections"`
}

// ConnectionRow is one connection as a page lists it, with the other app's team and the words for a waiting reason.
type ConnectionRow struct {
	Role, App, Event, Team, Status string
}

// Kind is what travels over the connection.
func (r ConnectionRow) Kind() string {
	switch r.Role {
	case "calls", "called-by":
		return "call"
	case "reaches":
		return "host"
	}
	return "event"
}

// Direction is out when this app declared the connection and may remove it, in when the other app did.
func (r ConnectionRow) Direction() string {
	if r.Role == "called-by" || r.Role == "subscriber" {
		return "in"
	}
	return "out"
}

// What is what happens over the connection, in words, with a waiting or refused reason.
func (r ConnectionRow) What() string {
	what := map[string]string{"calls": "its API", "called-by": "calls this app", "publishes": "publishes",
		"subscribes": "receives", "subscriber": "receives", "reaches": "outside the cluster"}[r.Role]
	if r.Status != "" {
		return what + ", " + r.Status
	}
	return what
}

// waiting are the resolver's reasons as the connection table words them.
var waiting = map[string]string{
	"callee_absent":       "waiting for {app}/{env} to exist",
	"no_consent":          "waiting for {app} to allow it",
	"callee_not_web":      "a worker: it has no address to call",
	"self_call":           "it calls itself",
	"publisher_not_ready": "waiting for {app} to publish {event}",
}

// Connections is an instance's declared connections, derived from its inventory and the teams of its environment.
type Connections struct {
	Address, API                                  string
	Calls, Callers, Publishes, Subscribes, Egress []string
	Subscribers                                   [][2]string // (this app's event, the app receiving it)
	Rows                                          []ConnectionRow
	Offers                                        string
	ProxyPort                                     int32
}

// ConnectionsOf derives an instance's connection block from its inventory, in its own environment.
func ConnectionsOf(inventory *Inventory, teams map[string]string, env string) Connections {
	if inventory == nil {
		return Connections{}
	}
	found := Connections{Address: inventory.Address, API: inventory.API, Calls: inventory.Calls,
		Callers: inventory.Callers, Publishes: inventory.Publishes, Subscribes: inventory.Subscriptions,
		Offers: inventory.Offers, Egress: inventory.Egress.Grants, ProxyPort: inventory.Egress.Port}
	for _, row := range inventory.Connections {
		if row.Role == "subscriber" {
			found.Subscribers = append(found.Subscribers, [2]string{row.Event, row.App})
		}
		status := ""
		if wording, known := waiting[row.Reason]; known {
			status = strings.NewReplacer("{app}", row.App, "{env}", env, "{event}", row.Event).Replace(wording)
		}
		event := row.Event
		if event == "" {
			event = row.Host
		}
		found.Rows = append(found.Rows, ConnectionRow{Role: row.Role, App: row.App, Event: event,
			Team: teams[row.App], Status: status})
	}
	return found
}

// Report is the part of a Go observation's v5 report a page shows: its state, the evidence beside it and how far Git
// has got. Decoded by the caller, which owns the report's version and validation.
type Report struct {
	State        ReportState   `json:"state"`
	Verdict      string        `json:"verdict"`
	Currentness  Currentness   `json:"currentness"`
	Promotion    Promotion     `json:"promotion"`
	Live         Live          `json:"live"`
	Findings     []Finding     `json:"findings"`
	Problems     []ProblemRow  `json:"problems"`
	Routes       []Route       `json:"routes"`
	Certificates []Certificate `json:"certificates"`
	Target       ReportTarget  `json:"target"`
}

// ReportState is an instance's state as the report decided it.
type ReportState struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Currentness is how the running revision compares with the one Git wants.
type Currentness struct {
	State           string `json:"state"`
	Reason          string `json:"reason"`
	AppliedRevision string `json:"appliedRevision"`
}

// Promotion is Go's decision on whether the staging image may be promoted.
type Promotion struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

// Live is what the cluster shows: the unit, the release, the workload and its running digests.
type Live struct {
	Unit     Evidence `json:"unit"`
	Release  Evidence `json:"release"`
	Workload Workload `json:"workload"`
	Running  Running  `json:"running"`
}

// Evidence is one piece of live evidence: its status, and why when it could not be read.
type Evidence struct {
	Status     string      `json:"status"`
	Reason     string      `json:"reason"`
	Suspended  *bool       `json:"suspended"`
	Conditions []Condition `json:"conditions"`
}

// Condition is one Kubernetes condition as the report states it.
type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	ObservedGeneration *int32 `json:"observedGeneration"`
}

// Workload is whether the instance's pods are ready.
type Workload struct {
	Status        string `json:"status"`
	ReadyReplicas *int32 `json:"readyReplicas"`
}

// Running is the image the pods run.
type Running struct {
	Status  string    `json:"status"`
	Digests *[]string `json:"digests"`
}

// Finding is one field the observation assessed.
type Finding struct {
	Field   string `json:"field"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// ProblemRow is one pod problem.
type ProblemRow struct {
	Pod, Container, Reason, Message string
}

// Route is one desired route's live evidence.
type Route struct {
	Name    string   `json:"name"`
	Outcome string   `json:"outcome"`
	Hosts   []string `json:"hosts"`
	Reason  string   `json:"reason"`
}

// Certificate is one desired certificate's live evidence.
type Certificate struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// ReportTarget is the instance as the report found it: the names it looked for and what Git wants.
type ReportTarget struct {
	Namespace string `json:"namespace"`
	Replicas  int32  `json:"replicas"`
	Revision  string `json:"revision"`
}

// DecodeReport reads one report as a page shows it.
func DecodeReport(data []byte) (Report, error) {
	var found Report
	err := json.Unmarshal(data, &found)
	return found, err
}

// AppRow is one deployed instance as the pages show it: the Go report, and the block its records state.
type AppRow struct {
	Instance  Instance
	State     State
	Suspended bool
	Inventory *Inventory
	Report    *Report
	Teams     map[string]string
	Removed   string // why a removed instance is still shown (its namespace is still there), empty otherwise
}

// Team is the team the instance's records name.
func (r AppRow) Team() string {
	if r.Inventory == nil {
		return ""
	}
	return r.Inventory.Team
}

// Image is the image its release requests.
func (r AppRow) Image() string {
	if r.Inventory == nil {
		return ""
	}
	return r.Inventory.Artifact.Image
}

// Digest is the digest that identifies its image.
func (r AppRow) Digest() string {
	if r.Inventory == nil {
		return ""
	}
	return r.Inventory.Artifact.Digest
}

// Hosts are its web addresses, as its records state them.
func (r AppRow) Hosts() []string {
	if r.Inventory == nil {
		return nil
	}
	return r.Inventory.Hosts
}

// DatabasePath is the declared path; Known is false when nothing is known, for no record or no HelmRelease yet.
func (r AppRow) DatabasePath() (path string, known bool) {
	if r.Inventory == nil {
		return "", false
	}
	if r.Report != nil && r.Report.Live.Release.Status == "absent" {
		return "", false
	}
	return r.Inventory.Database.Path, true
}

// Connections is its connections, with each other app's team.
func (r AppRow) Connections() Connections {
	return ConnectionsOf(r.Inventory, r.Teams, r.Instance.Env)
}

// Summary is one app across its environments, for the Deployments list.
type Summary struct {
	App     string
	Envs    map[string]*AppRow
	Order   []string // the environments in the order the rows came
	Compare string   // same, differs, or empty with one environment
}

// Team is the team its instances name.
func (s Summary) Team() string {
	for _, env := range s.Order {
		if team := s.Envs[env].Team(); team != "" {
			return team
		}
	}
	return ""
}

// SameName is whether staging and production run the same image.
func Compare(staging, prod *AppRow) string {
	if (staging.Digest() != "" || prod.Digest() != "") && staging.Digest() == prod.Digest() {
		return "same"
	}
	if staging.Image() == prod.Image() && staging.Digest() == "" && prod.Digest() == "" {
		return "same"
	}
	return "differs"
}

// Summaries groups rows by app, in app order; an app with both environments compares them by digest.
func Summaries(rows []AppRow) []Summary {
	byApp := map[string]*Summary{}
	order := []string{}
	for i := range rows {
		row := &rows[i]
		found, known := byApp[row.Instance.App]
		if !known {
			found = &Summary{App: row.Instance.App, Envs: map[string]*AppRow{}}
			byApp[row.Instance.App] = found
			order = append(order, row.Instance.App)
		}
		found.Envs[row.Instance.Env] = row
		found.Order = append(found.Order, row.Instance.Env)
	}
	slices.Sort(order)
	found := make([]Summary, 0, len(order))
	for _, app := range order {
		summary := byApp[app]
		if staging, prod := summary.Envs["staging"], summary.Envs["prod"]; staging != nil && prod != nil {
			summary.Compare = Compare(staging, prod)
		}
		found = append(found, *summary)
	}
	return found
}

// removed names why an instance the cluster still has and the records do not is shown, in the same words as the
// console always has: its cleanup has not finished.
var removed = map[string]string{
	"unit_and_release": "removed from Git; live cleanup in progress",
	"unit":             "removed from Git; live cleanup in progress, its unit is still there",
	"release":          "removed from Git; live cleanup in progress, its HelmRelease is still there",
}

// RemovedRow is one instance only the cluster still has: shown until its namespace goes.
func RemovedRow(app, env, reason string) (AppRow, bool) {
	instance, known := Named(app, env)
	if !known {
		return AppRow{}, false
	}
	return AppRow{Instance: instance, State: State{Key: StateUnknown, Detail: removed[reason]}, Removed: reason}, true
}

// Site is the platform a page links into: the domain its web addresses share and the owner of its repositories.
type Site struct{ Domain, Owner string }

// LogsURL is HyperDX, filtered to one app's logs and traces.
func (s Site) LogsURL(app string) string {
	return "https://clickstack." + s.Domain + "/search?" + url.Values{
		"where": {`ServiceName:"` + app + `"`}, "whereLanguage": {"lucene"}}.Encode()
}

// DashboardURL is an app's HyperDX dashboard.
func (s Site) DashboardURL(id string) string {
	return "https://clickstack." + s.Domain + "/dashboards/" + id
}

// UngrantedURL is HyperDX filtered to the egress proxy's lines for one instance's requests that no grant covers.
func (s Site) UngrantedURL(instance Instance) string {
	return "https://clickstack." + s.Domain + "/search?" + url.Values{
		"where":         {`ServiceName:"egress" AND "instance=` + instance.Namespace() + ` granted=-"`},
		"whereLanguage": {"lucene"}}.Encode()
}

// RepositoryURL is an app's repository on GitHub.
func (s Site) RepositoryURL(app string) string { return "https://github.com/" + s.Owner + "/" + app }

// Job is one console job as its record states it: its output, where it ended and what a later job needs to finish it.
type Job struct {
	ID       int
	Action   string
	Unit     string // the target: a Flux unit, or <app>/<env> for a pull request
	Identity string
	Started  time.Time
	Finished *time.Time
	State    string // running, succeeded, failed, interrupted
	Link     string
	Owner    string
	Resume   map[string]any
	Reached  string // when its change was first seen at its end, ISO 8601
	Lines    []string
	Live     bool // false for a record read from the store: another pod's job, or one from before a restart
}

// Running is whether the job has not ended.
func (j Job) Running() bool { return j.State == "running" }

// Interrupted marks a job whose pod went away before it ended: what it had done stays above it.
func (j Job) Interrupted(finished time.Time) Job {
	j.State, j.Finished = "interrupted", &finished
	j.Lines = append(j.Lines, interruptedLine)
	return j
}

const interruptedLine = "[ERROR] interrupted: the console restarted before this job finished. " +
	"What it had done by then is above; nothing later happened"
