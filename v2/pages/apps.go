package pages

import (
	"net/url"
	"slices"
	"strings"
)

// StackNames is a stack as the language it is.
var StackNames = map[string]string{"typescript": "TypeScript", "kotlin": "Kotlin"}

// Tally counts environments by state for the Deployments list.
type Tally struct {
	Healthy, Updating, Apps, Attention int
}

// Busy is how many environments need attention or are updating.
func (t Tally) Busy() int { return t.Attention + t.Updating }

// EnvView is one environment in an app's row: its state and image in the list, and its facts in the opened row.
type EnvView struct {
	App, Env, Label string
	Row             *AppRow
	Href            string // the instance's page
	Unit, Action    string // the Flux unit a list's button acts on, and which action
	RefreshURL      string // the page a running job's link re-reads
	Suspended       bool
	Database        string
	DatabaseKnown   bool
	Job             *Job // the job running on it, if one is
	Address         string
}

// AppView is one app as the Deployments list shows it.
type AppView struct {
	App, Team, Comparison string
	Staging, Prod         *EnvView
	Open                  bool
	Stack, StackLabel     string
	Href                  string // the staging or production page, by environment
	TeamHref              string
	PromotionHref         string
	LogsURL               string
	RepositoryURL         string
	DashboardURL          string
}

// Link is one entry of a filter bar: its name, where it goes, and whether it is the current one.
type Link struct {
	Name, Href string
	Here       bool
}

// AppsPage is the Deployments page: every app as a list or, with a map, one environment as who calls whom.
type AppsPage struct {
	Apps          []AppView
	Teams         []Link
	AllTeams      Link
	ShowAll       Link
	ShowAttention Link
	ListView      Link
	MapView       Link
	Envs          []Link
	Tally         Tally
	Attention     bool
	Team, View    string
	Env           string
	StackNames    map[string]string
	Map           *AppMap
	Site          Site
}

// AppsInput is what the Deployments page reads: the rows, the filters, the facts it links to, and the platform it
// belongs to.
type AppsInput struct {
	Rows       []AppRow
	View       string
	Team       string
	Env        string
	Attention  bool
	RefreshURL string
	Stacks     map[string]string
	Dashboards map[string]string
	Running    []Job
	Site       Site
}

// appsHref is one filter's address; a blank team stays in it, in the order the console's own macro wrote it.
func appsHref(view, team, env, show string) string {
	href := "/apps?view=" + url.QueryEscape(view) + "&team=" + url.QueryEscape(team) + "&env=" + url.QueryEscape(env)
	if show != "" {
		href += "&show=" + url.QueryEscape(show)
	}
	return href
}

// AppsPageOf computes the Deployments list from its rows and filters.
func AppsPageOf(input AppsInput) AppsPage {
	view, env := appsView(input)
	summaries := Summaries(input.Rows)
	shown := appsShown(summaries, input.Team)
	tally := appsTally(shown)
	if input.Attention {
		shown = appsAttention(shown)
	}
	page := appsFilters(input, view, env, teamsOf(summaries))
	page.Tally = tally
	if view == "map" {
		found := AppMapOf(rowsOfTeam(input.Rows, input.Team), env)
		page.Map = &found
		return page
	}
	for _, summary := range shown {
		page.Apps = append(page.Apps, appViewOf(summary, input, env))
	}
	return page
}

// appsView is the list or the map, and the environment a map shows, as the query named them.
func appsView(input AppsInput) (string, string) {
	view, env := "list", "staging"
	if input.View == "map" {
		view = "map"
	}
	if environments[input.Env] {
		env = input.Env
	}
	return view, env
}

// appsShown is the apps of one team, those that need attention first and then by name.
func appsShown(summaries []Summary, team string) []Summary {
	shown := []Summary{}
	for _, summary := range summaries {
		if team == "" || summary.Team() == team {
			shown = append(shown, summary)
		}
	}
	slices.SortStableFunc(shown, func(a, b Summary) int {
		healthyA, healthyB := a.allHealthy(), b.allHealthy()
		if healthyA != healthyB {
			if healthyA {
				return 1
			}
			return -1
		}
		return strings.Compare(a.App, b.App)
	})
	return shown
}

// appsTally counts the environments the list shows by state.
func appsTally(shown []Summary) Tally {
	tally := Tally{Apps: len(shown)}
	for _, summary := range shown {
		for _, row := range summary.Envs {
			switch row.State.Key {
			case StateHealthy:
				tally.Healthy++
			case StateUpdating:
				tally.Updating++
			default:
				tally.Attention++
			}
		}
	}
	return tally
}

// appsAttention keeps the apps with an environment that is not healthy.
func appsAttention(shown []Summary) []Summary {
	kept := []Summary{}
	for _, summary := range shown {
		if !summary.allHealthy() {
			kept = append(kept, summary)
		}
	}
	return kept
}

// teamsOf is every team the rows name, once each, in name order.
func teamsOf(summaries []Summary) []string {
	teams := []string{}
	for _, summary := range summaries {
		if team := summary.Team(); team != "" {
			teams = append(teams, team)
		}
	}
	slices.Sort(teams)
	return slices.Compact(teams)
}

// rowsOfTeam is the rows of one team, or all of them.
func rowsOfTeam(rows []AppRow, team string) []AppRow {
	if team == "" {
		return rows
	}
	kept := []AppRow{}
	for _, row := range rows {
		if row.Team() == team {
			kept = append(kept, row)
		}
	}
	return kept
}

// appsFilters is the page's filter bar and the facts every row and the map link to.
func appsFilters(input AppsInput, view, env string, teams []string) AppsPage {
	page := AppsPage{Attention: input.Attention, Team: input.Team, View: view, Env: env,
		StackNames: StackNames, Site: input.Site,
		AllTeams:      Link{Href: appsHref(view, "", env, ""), Here: input.Team == ""},
		ShowAll:       Link{Href: appsHref(view, input.Team, env, ""), Here: !input.Attention},
		ShowAttention: Link{Href: appsHref(view, input.Team, env, "attention"), Here: input.Attention},
		ListView:      Link{Href: appsHref("list", input.Team, env, ""), Here: view == "list"},
		MapView:       Link{Href: appsHref("map", input.Team, env, ""), Here: view == "map"},
		Envs: []Link{{Name: "staging", Href: appsHref("map", input.Team, "staging", ""), Here: env == "staging"},
			{Name: "prod", Href: appsHref("map", input.Team, "prod", ""), Here: env == "prod"}},
	}
	for _, team := range teams {
		page.Teams = append(page.Teams, Link{Name: team, Href: appsHref(view, team, env, ""), Here: input.Team == team})
	}
	return page
}

// appViewOf is one app as a row of the list.
func appViewOf(summary Summary, input AppsInput, env string) AppView {
	view := AppView{App: summary.App, Team: summary.Team(), Comparison: summary.Compare,
		Open: summary.opensInList(), Stack: input.Stacks[summary.App], Href: "/apps/" + summary.App + "/staging",
		TeamHref:      appsHref("list", summary.Team(), env, ""),
		PromotionHref: "/apps/" + summary.App + "/staging/promotion",
		LogsURL:       input.Site.LogsURL(summary.App), RepositoryURL: input.Site.RepositoryURL(summary.App)}
	if view.Stack != "" {
		view.StackLabel = StackNames[view.Stack]
		if view.StackLabel == "" {
			view.StackLabel = view.Stack
		}
	}
	if id := input.Dashboards[summary.App]; id != "" {
		view.DashboardURL = input.Site.DashboardURL(id)
	}
	view.Staging = envViewOf(summary.App, "staging", summary.Envs["staging"], input)
	view.Prod = envViewOf(summary.App, "prod", summary.Envs["prod"], input)
	return view
}

// envViewOf is one environment of one app as the list shows it, present or not.
func envViewOf(app, env string, row *AppRow, input AppsInput) *EnvView {
	view := &EnvView{App: app, Env: env, Label: strings.ToUpper(env[:1]) + env[1:], Href: "/apps/" + app + "/" + env,
		Unit: "app-" + app + "-" + env, Action: "reconcile", RefreshURL: input.RefreshURL}
	if row == nil {
		return view
	}
	view.Row, view.Suspended = row, row.Suspended
	if row.Suspended {
		view.Action = "resume"
	}
	view.Database, view.DatabaseKnown = row.DatabasePath()
	view.Address = row.Connections().Address
	for i := range input.Running {
		if job := &input.Running[i]; job.Unit == view.Unit || job.Unit == row.Instance.Name() {
			view.Job = job
			break
		}
	}
	return view
}

// opensInList is whether the row opens itself: an environment that is not healthy, updating or stopped.
func (s Summary) opensInList() bool {
	for _, row := range s.Envs {
		switch row.State.Key {
		case StateHealthy, StateUpdating, "stopped":
		default:
			return true
		}
	}
	return false
}

// ActionLabel is the button's word for the action the environment takes.
func (v EnvView) ActionLabel() string { return strings.ToUpper(v.Action[:1]) + v.Action[1:] }

// allHealthy is whether every environment of the app is healthy.
func (s Summary) allHealthy() bool {
	for _, row := range s.Envs {
		if row.State.Key != StateHealthy {
			return false
		}
	}
	return true
}
