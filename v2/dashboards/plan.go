package dashboards

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

// Action is what a step does to one object in ClickStack.
type Action string

const (
	Keep   Action = "keep"   // already as wanted
	Create Action = "create" // missing
	Update Action = "update" // there, but different: replaced in place, keeping its id
	Delete Action = "delete" // the platform's, and no longer wanted
	Leave  Action = "leave"  // tagged as the platform's but not named for an app: never deleted
)

// DashboardStep is one change to the app dashboards, or none (Keep, Leave).
type DashboardStep struct {
	Action    Action
	App       string    // the app it is for; "" for Delete and Leave
	ID        string    // the live dashboard's, for Keep, Update, Delete and Leave
	Dashboard Dashboard // the definition, for Create, Update and Keep; the live one for Delete and Leave
}

// Sources is the ids of the trace and log sources the tiles read.
type Sources struct{ Traces, Logs string }

// Source is one HyperDX data source: its id, its kind and the table it reads.
type Source struct{ ID, Kind, Table string }

// PickSources is the first trace source on otel_traces and the first log source on otel_logs; without both, the
// tiles would read nothing.
func PickSources(sources []Source) (Sources, error) {
	var picked Sources
	for _, source := range sources {
		switch {
		case source.Kind == "trace" && source.Table == "otel_traces" && picked.Traces == "":
			picked.Traces = source.ID
		case source.Kind == "log" && source.Table == "otel_logs" && picked.Logs == "":
			picked.Logs = source.ID
		}
	}
	if picked.Traces == "" || picked.Logs == "" {
		return Sources{}, errors.New("ClickStack has no trace source on otel_traces and log source on otel_logs")
	}
	return picked, nil
}

// PlanDashboards is how to bring the tagged dashboards to one per app: keep those that are their definition,
// update those that differ, create the missing ones and, with prune, delete each other tagged dashboard named
// exactly App: <app name>. A tagged dashboard named otherwise is left. Untagged dashboards are never considered.
// Two tagged dashboards of one name, or one without an id, stop the plan: which to change would be a guess.
func PlanDashboards(apps []App, live []Dashboard, sources Sources, prune bool) ([]DashboardStep, error) {
	wanted := make([]Dashboard, 0, len(apps))
	for _, app := range apps {
		wanted = append(wanted, Definition(app, sources.Traces, sources.Logs))
	}
	deletable := func(name string) bool {
		app, ok := strings.CutPrefix(name, "App: ")
		return ok && appName.MatchString(app)
	}
	return planFamily(Tag, wanted, live, prune, deletable)
}

// ErrNoServices is an empty platform catalogue: never read as "every platform service is gone".
var ErrNoServices = errors.New("the platform catalogue names no service; no change made")

// PlanPlatformDashboards is the same plan for the platform's own services, which carry PlatformTag: one dashboard
// per catalogue entry named Platform: <service>, and with prune, delete each other tagged dashboard named
// Platform: <x>. A tagged dashboard named otherwise is left, and no App: dashboard is ever read or deleted here.
func PlanPlatformDashboards(services []Service, live []Dashboard, sources Sources, prune bool) ([]DashboardStep, error) {
	if len(services) == 0 {
		return nil, ErrNoServices
	}
	wanted := make([]Dashboard, 0, len(services))
	for _, service := range services {
		wanted = append(wanted, PlatformDefinition(service, sources.Traces, sources.Logs))
	}
	deletable := func(name string) bool {
		service, ok := strings.CutPrefix(name, PlatformPrefix)
		return ok && service != ""
	}
	return planFamily(PlatformTag, wanted, live, prune, deletable)
}

// planFamily brings the dashboards carrying one tag to one per wanted definition. deletable says which leftover
// names the family may delete; every other tagged dashboard is left as it is.
func planFamily(tag string, wanted []Dashboard, live []Dashboard, prune bool, deletable func(string) bool) ([]DashboardStep, error) {
	tagged, err := taggedDashboards(live, tag)
	if err != nil {
		return nil, err
	}
	steps := []DashboardStep{}
	for _, definition := range wanted {
		current, ok := tagged[definition.Name]
		delete(tagged, definition.Name)
		switch {
		case !ok:
			steps = append(steps, DashboardStep{Action: Create, App: ownerOf(definition.Name), Dashboard: definition})
		case Same(current, definition):
			steps = append(steps, DashboardStep{Action: Keep, App: ownerOf(definition.Name), ID: current.ID, Dashboard: definition})
		default:
			steps = append(steps, DashboardStep{Action: Update, App: ownerOf(definition.Name), ID: current.ID, Dashboard: definition})
		}
	}
	if !prune {
		return steps, nil
	}
	return append(steps, staleDashboards(tagged, deletable)...), nil
}

// ownerOf is the dashboard's subject: the app or platform service its name states.
func ownerOf(name string) string {
	for _, prefix := range []string{"App: ", PlatformPrefix} {
		if owner, ok := strings.CutPrefix(name, prefix); ok {
			return owner
		}
	}
	return ""
}

// Tagged is the dashboards that carry a tag: the app pass's or the platform pass's.
func Tagged(live []Dashboard, tag string) []Dashboard {
	tagged := []Dashboard{}
	for _, board := range live {
		for _, found := range board.Tags {
			if found == tag {
				tagged = append(tagged, board)
				break
			}
		}
	}
	return tagged
}

func taggedDashboards(live []Dashboard, tag string) (map[string]Dashboard, error) {
	tagged := map[string]Dashboard{}
	for _, board := range Tagged(live, tag) {
		if _, twice := tagged[board.Name]; twice || board.ID == "" {
			return nil, errors.New("ClickStack lists dashboard " + board.Name + " twice or without an id; no change made")
		}
		tagged[board.Name] = board
	}
	return tagged, nil
}

func staleDashboards(tagged map[string]Dashboard, deletable func(string) bool) []DashboardStep {
	steps := []DashboardStep{}
	for name, board := range tagged {
		action := Leave
		if deletable(name) {
			action = Delete
		}
		steps = append(steps, DashboardStep{Action: action, ID: board.ID, Dashboard: board})
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Dashboard.Name < steps[j].Dashboard.Name })
	return steps
}

// RecordName, RecordNamespace and RecordKey are where the console reads which dashboard is whose.
const (
	RecordNamespace = "console"
	RecordName      = "app-dashboards"
	RecordKey       = "dashboards.json"
	// RecordFieldManager is the field manager the record is written under.
	RecordFieldManager = "dashboard-sync"
)

// dashboardID is a MongoDB ObjectId: the only thing that may become part of the console's link.
var dashboardID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// Recordable is the entries the record may hold: an app name and an ObjectId.
func Recordable(ids map[string]string) map[string]string {
	kept := map[string]string{}
	for app, id := range ids {
		if appName.MatchString(app) && dashboardID.MatchString(id) {
			kept[app] = id
		}
	}
	return kept
}

// Record is the console's record of each app's dashboard id, {"<app>": "<id>"} sorted by app, written exactly as
// the Python job wrote it (", " and ": " separators) so an unchanged record is never rewritten. Only an app name
// and an ObjectId are recorded: a dashboard created without an id in its answer waits for the next pass.
func Record(ids map[string]string) string {
	kept := Recordable(ids)
	apps := make([]string, 0, len(kept))
	for app := range kept {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	entries := make([]string, 0, len(apps))
	for _, app := range apps {
		entries = append(entries, `"`+app+`": "`+ids[app]+`"`)
	}
	return "{" + strings.Join(entries, ", ") + "}"
}
