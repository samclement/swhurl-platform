package console

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The console's read-only pages, as their models: each is read once per request through a Server, and the command
// puts the page's shell around it and renders it. A failed read is the page's error in Go's own words, never a
// fallback.

// Overview is the Overview page's reading: what needs attention across the platform's checks and units.
type Overview struct {
	Checks   pages.Checks
	AppCount int
	Units    []pages.Unit
	Problems []pages.Problem
	Updating []pages.Problem
}

// Overview reads the pinned tree and the cluster once, and counts the apps the cluster runs or is still removing.
func (s Server) Overview(ctx context.Context) (Overview, error) {
	_, collection, units, err := s.status(ctx)
	if err != nil {
		return Overview{}, err
	}
	checks, err := s.Checks(ctx)
	if err != nil {
		return Overview{}, err
	}
	listed, err := pages.Units(units)
	if err != nil {
		return Overview{}, fmt.Errorf("Flux units unavailable: %w", err)
	}
	return Overview{Checks: checks, AppCount: len(collection.Instances) + len(collection.Removing), Units: listed,
		Problems: pages.Problems(checks, listed), Updating: pages.Updating(listed)}, nil
}

// Platform is the Platform page: the units in the order they build on each other, and the checks beside them.
type Platform struct {
	Checks    pages.Checks
	Units     []pages.Unit
	Problems  []pages.Problem
	Updating  []pages.Problem
	Groups    []pages.Group
	Unhealthy []pages.Unit
	Elsewhere []pages.Unit
	Layers    []Layer
	Revision  string
}

// Layer is one title of the units list and the units under it.
type Layer struct {
	Title, Text string
	Members     []UnitRow
}

// UnitRow is one row of the units list: why it offers no actions when it is refused them, and the job running on it
// when there is one.
type UnitRow struct {
	pages.UnitRow
	Refused    string
	Job        *pages.Job
	RefreshURL string
}

// Platform reads the units and the platform checks.
func (s Server) Platform(ctx context.Context) (Platform, error) {
	listed, err := s.Units(ctx)
	if err != nil {
		return Platform{}, err
	}
	checks, err := s.Checks(ctx)
	if err != nil {
		return Platform{}, err
	}
	return PlatformOf(checks, listed), nil
}

// PlatformOf is the Platform page of the checks and units read: the units grouped by layer against the revision
// most of them applied, and the ones that are unhealthy or elsewhere.
func PlatformOf(checks pages.Checks, units []pages.Unit) Platform {
	revision := pages.CommonRevision(units)
	return Platform{Checks: checks, Units: units, Problems: pages.Problems(checks, units),
		Updating: pages.Updating(units), Groups: pages.CheckGroups(checks), Revision: revision,
		Unhealthy: unusual(units, func(unit pages.Unit) bool { return !unit.Healthy() }),
		Elsewhere: unusual(units, func(unit pages.Unit) bool { return unit.Revision != revision }),
		Layers:    layersOf(units, revision)}
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

// layersOf is the units list's layers, each with its rows: a layer nothing sits in is left out.
func layersOf(units []pages.Unit, revision string) []Layer {
	var found []Layer
	for _, layer := range append(slices.Clone(pages.Layers), struct{ Prefix, Title, Text string }{Title: pages.OtherLayer}) {
		view := Layer{Title: layer.Title, Text: layer.Text}
		for _, unit := range units {
			if unit.Layer() == layer.Title {
				view.Members = append(view.Members, UnitRow{UnitRow: pages.UnitRowOf(unit, revision),
					Refused: pages.RefusedUnits[unit.Name]})
			}
		}
		if len(view.Members) > 0 {
			found = append(found, view)
		}
	}
	return found
}

// UnitPage is one Flux unit's page: what it is, why it offers no actions when it is refused them, the app instance
// it applies, and what its readiness says.
type UnitPage struct {
	Unit    pages.Unit
	Refused string
	App     *pages.Instance
	Detail  string
}

// UnitPage reads one unit of the cluster's units, and whether it is one of them.
func (s Server) UnitPage(ctx context.Context, name string) (UnitPage, bool, error) {
	listed, err := s.Units(ctx)
	if err != nil {
		return UnitPage{}, false, err
	}
	for _, unit := range listed {
		if unit.Name == name {
			return UnitPageOf(unit), true, nil
		}
	}
	return UnitPage{}, false, nil
}

// UnitPageOf is one unit's page: its readiness's detail, or the revision it applied when that says nothing.
func UnitPageOf(unit pages.Unit) UnitPage {
	page := UnitPage{Unit: unit, Refused: pages.RefusedUnits[unit.Name], Detail: unit.Ready.Detail}
	if instance, ok := pages.InstanceOf(unit.Name); ok {
		page.App = &instance
	}
	if page.Detail == "" {
		page.Detail = "applied " + unit.Revision
	}
	return page
}

// Catalogue reads the cluster, the pinned tree and the documents of the environment the page shows, then computes
// it: only the environment a page shows has its documents decoded.
func (s Server) Catalogue(ctx context.Context, env, kind, team string) (pages.CataloguePage, error) {
	tree, rows, err := s.Rows(ctx)
	if err != nil {
		return pages.CataloguePage{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.Repository)
	if err != nil {
		return pages.CataloguePage{}, err
	}
	input := pages.CatalogueInput{Rows: rows, Env: env, Kind: kind, Team: team, Site: site, Github: s.GitHub}
	input.Offers = orchestration.ConsoleOffers(tree, rows, pages.EnvironmentNamed(env))
	return pages.CataloguePageOf(input), nil
}

// Apps reads the cluster once and the two remote facts the list links to, then computes the Deployments list, or one
// environment's map of who calls whom.
func (s Server) Apps(ctx context.Context, input pages.AppsInput) (pages.AppsPage, error) {
	tree, rows, err := s.Rows(ctx)
	if err != nil {
		return pages.AppsPage{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.Repository)
	if err != nil {
		return pages.AppsPage{}, err
	}
	input.Rows, input.Site, input.Running = rows, site, s.Jobs(ctx)
	if input.View != "map" {
		// What the list shows decides what it reads: only an app it names has its stack asked for.
		first := pages.AppsPageOf(input)
		apps := make([]string, 0, len(first.Apps))
		for _, app := range first.Apps {
			apps = append(apps, app.App)
		}
		input.Stacks = s.Stacks(ctx, apps)
		input.Dashboards = s.Dashboards(ctx)
	}
	return pages.AppsPageOf(input), nil
}

// Egress reads every instance's port on the egress proxy and the hosts outside the cluster it is granted.
func (s Server) Egress(ctx context.Context) (pages.EgressPage, error) {
	tree, rows, err := s.Rows(ctx)
	if err != nil {
		return pages.EgressPage{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.Repository)
	if err != nil {
		return pages.EgressPage{}, err
	}
	return pages.EgressPageOf(rows, site), nil
}

// Teams reads the cluster, the registry and the two remote facts the cards link to, and puts each team's latest job
// on its card. A registry that cannot be read is the page's error, beside the apps it can still show.
func (s Server) Teams(ctx context.Context) (pages.TeamsPage, error) {
	tree, rows, err := s.Rows(ctx)
	if err != nil {
		return pages.TeamsPage{}, err
	}
	site, err := orchestration.ConsoleSite(tree, s.Repository)
	if err != nil {
		return pages.TeamsPage{}, err
	}
	registry, registryErr := orchestration.ConsoleTeams(tree)
	page := pages.TeamsPageOf(registry, registryErr == nil, rows, site, s.Stacks(ctx, appsOf(rows)), s.Dashboards(ctx))
	if registryErr != nil {
		page.RegistryError = registryErr.Error()
	}
	page.Github = s.GitHub
	for _, job := range s.Jobs(ctx) {
		team, isTeam := strings.CutPrefix(job.Unit, "team:")
		if !isTeam {
			continue
		}
		for i := range page.Teams {
			if page.Teams[i].Name == team && page.Teams[i].Job == nil {
				page.Teams[i].Job = &pages.TeamJob{ID: job.ID, Action: job.Action, State: job.State, Link: job.Link}
			}
		}
	}
	return page, nil
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
