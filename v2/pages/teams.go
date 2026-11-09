package pages

import (
	"slices"
	"strconv"
	"strings"
)

// TeamsPage is the Teams page: every team with the apps it owns, the changes that keep the registry and the apps in
// step, and the apps whose team is not registered at all.
type TeamsPage struct {
	Teams         []TeamView
	AppCount      int // how many apps the teams own, each counted once
	Unregistered  []Unregistered
	RegistryError string
	Error         string
	Github        bool
}

// TeamInfo is what the registry says about one team: its description and, when it has one, its notification channel.
type TeamInfo struct {
	Description string
	Channel     string
	Transport   string
}

// TeamView is one team's card.
type TeamView struct {
	Name, Description string
	Channel           string // its notification channel's name, empty when it has none
	Transport         string
	Count             int
	Names             string // its apps' names, comma-separated, for the hints that list them
	Apps              []TeamApp
	Job               *TeamJob
	RenameHint        string
}

// TeamApp is one app of a team: its language, its two environments and where to read its logs.
type TeamApp struct {
	App, Stack, StackLabel string
	Staging, Prod          *AppRow
	DashboardURL, LogsURL  string
}

// TeamJob is the last change to a team.
type TeamJob struct {
	ID     int
	Action string
	State  string
	Link   string
}

// Unregistered is one team name no app record can name, and the apps that name it anyway.
type Unregistered struct {
	Team string
	Apps []UnregisteredApp
}

// UnregisteredApp is one app of an unregistered team: its environments, in order.
type UnregisteredApp struct {
	App  string
	Envs []string
}

// TeamsPageOf computes the page from the registry and the rows: the apps of each team, and the teams the apps name
// that the registry does not hold. registered is false when the registry could not be read at all.
func TeamsPageOf(registry map[string]TeamInfo, registered bool, rows []AppRow, site Site,
	stacks, dashboards map[string]string) TeamsPage {
	owned := ownedByTeam(rows)
	page := TeamsPage{Github: true}
	if !registered {
		// The registry is unknown, so nothing can be called unregistered: the teams the apps name stand in for it.
		registry = map[string]TeamInfo{}
		for team := range owned {
			registry[team] = TeamInfo{}
		}
	}
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		page.Teams = append(page.Teams, teamViewOf(name, registry[name], owned[name], site, stacks, dashboards))
		page.AppCount += len(owned[name])
	}
	unregistered := make([]string, 0, len(owned))
	for name := range owned {
		if _, known := registry[name]; !known {
			unregistered = append(unregistered, name)
		}
	}
	slices.Sort(unregistered)
	for _, name := range unregistered {
		page.Unregistered = append(page.Unregistered, Unregistered{Team: name, Apps: unregisteredApps(owned[name])})
	}
	return page
}

// ownedByTeam is each team's apps, in the order the rows came, each app with its environments.
func ownedByTeam(rows []AppRow) map[string]map[string]map[string]*AppRow {
	owned := map[string]map[string]map[string]*AppRow{}
	for i := range rows {
		row := &rows[i]
		team, app := row.Team(), row.Instance.App
		if owned[team] == nil {
			owned[team] = map[string]map[string]*AppRow{}
		}
		if owned[team][app] == nil {
			owned[team][app] = map[string]*AppRow{}
		}
		owned[team][app][row.Instance.Env] = row
	}
	return owned
}

// teamViewOf is one team's card: its apps, and the hint that says what a rename would meet.
func teamViewOf(name string, info TeamInfo, apps map[string]map[string]*AppRow, site Site,
	stacks, dashboards map[string]string) TeamView {
	view := TeamView{Name: name, Description: info.Description, Channel: info.Channel, Transport: info.Transport,
		Count: len(apps)}
	names := make([]string, 0, len(apps))
	for app := range apps {
		names = append(names, app)
	}
	slices.Sort(names)
	view.Names, view.RenameHint = strings.Join(names, ", "), renameHint(len(apps), names)
	for _, app := range names {
		view.Apps = append(view.Apps, TeamApp{App: app, Stack: stacks[app], StackLabel: stackLabel(stacks[app]),
			Staging: apps[app]["staging"], Prod: apps[app]["prod"],
			DashboardURL: dashboards[app], LogsURL: site.LogsURL(app)})
	}
	return view
}

// renameHint is what a rename would meet, in the edit form's words.
func renameHint(count int, apps []string) string {
	owns := "It owns no app, so nothing else changes"
	if count > 0 {
		suffix := "s"
		if count == 1 {
			suffix = ""
		}
		owns = "It owns " + strconv.Itoa(count) + " app" + suffix + " (" + strings.Join(apps, ", ") +
			"), so the command refuses until each names another team: move each with Change team on its page first"
	}
	return "Changing it renames the team. " + owns + ". Stored logs and traces keep the old name."
}

// unregisteredApps is one unregistered team's apps, each with its environments.
func unregisteredApps(apps map[string]map[string]*AppRow) []UnregisteredApp {
	names := make([]string, 0, len(apps))
	for app := range apps {
		names = append(names, app)
	}
	slices.Sort(names)
	found := make([]UnregisteredApp, 0, len(names))
	for _, app := range names {
		envs := make([]string, 0, len(apps[app]))
		for env := range apps[app] {
			envs = append(envs, env)
		}
		slices.Sort(envs)
		found = append(found, UnregisteredApp{App: app, Envs: envs})
	}
	return found
}

// stackLabel is a stack as the page names it, or the stack's own name when it has no label.
func stackLabel(stack string) string {
	if label := StackNames[stack]; label != "" {
		return label
	}
	return stack
}
