package console

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/dashboards"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// rowsOf decodes a bulk status collection into the rows the console's pages show: one row per instance it reports,
// and one per instance only the cluster still has, so an app removed from Git stays visible until its namespace goes.
func rowsOf(collection observe.Collection) ([]pages.AppRow, error) {
	teams := map[string]string{}
	rows := make([]pages.AppRow, 0, len(collection.Instances))
	for _, instance := range collection.Instances {
		row, err := rowOf(instance, teams)
		if err != nil {
			return nil, err
		}
		rows = append(rows, *row)
	}
	seen := map[pages.Instance]bool{}
	for _, row := range rows {
		seen[row.Instance] = true
	}
	for _, gone := range collection.Removing {
		if row, known := pages.RemovedRow(gone.Application, gone.Environment, gone.Reason); known && !seen[row.Instance] {
			row.Teams = teams
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b pages.AppRow) int {
		if order := strings.Compare(a.Instance.App, b.Instance.App); order != 0 {
			return order
		}
		return strings.Compare(a.Instance.Env, b.Instance.Env)
	})
	return rows, nil
}

var (
	dashboardApp = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
	dashboardID  = regexp.MustCompile(`^[0-9a-f]{24}$`)
)

// rowOf is one instance as a row, remembering its team for the rows after it.
func rowOf(instance observe.InstanceReport, teams map[string]string) (*pages.AppRow, error) {
	named, known := pages.Named(instance.Application, instance.Environment)
	if !known {
		return nil, fmt.Errorf("Go status unavailable: invalid instance identity")
	}
	var inventory pages.Inventory
	if len(instance.Inventory) > 0 {
		if err := json.Unmarshal(instance.Inventory, &inventory); err != nil {
			return nil, fmt.Errorf("Go status unavailable: invalid inventory")
		}
		if inventory.Team != "" {
			teams[named.App] = inventory.Team
		}
	}
	encoded, err := observe.EncodeReport(instance.Report)
	if err != nil {
		return nil, fmt.Errorf("Go status unavailable: invalid report")
	}
	report, err := pages.DecodeReport(encoded)
	if err != nil {
		return nil, fmt.Errorf("Go status unavailable: invalid report")
	}
	suspended := report.Live.Unit.Suspended != nil && *report.Live.Unit.Suspended
	return &pages.AppRow{Instance: named, State: pages.StateOf(instance.Report.State.Name, instance.Report.State.Reason),
		Inventory: &inventory, Report: &report, Teams: teams, Suspended: suspended}, nil
}

// TeamRegistry is the team registry as a page shows it: each team's description and its channel's name and
// transport, read from the pinned tree's v2-records/teams.json.
func TeamRegistry(tree map[string][]byte) (map[string]pages.TeamInfo, error) {
	data, known := tree[kernel.RecordsRoot+"/"+kernel.TeamsRecordName]
	if !known {
		return nil, fmt.Errorf("cannot read v2-records/teams.json: not in the repository")
	}
	registry, err := kernel.DecodeTeams(data)
	if err != nil {
		return nil, fmt.Errorf("cannot read v2-records/teams.json: %w", err)
	}
	found := map[string]pages.TeamInfo{}
	for name, team := range registry.Teams {
		info := pages.TeamInfo{Description: team.Description}
		if team.Channel != nil {
			info.Channel, info.Transport = team.Channel.Name, team.Channel.Transport
		}
		found[name] = info
	}
	return found, nil
}

// dashboardsOf is each app's HyperDX dashboard, from the record the dashboard job keeps. Empty when it cannot be
// read, so a page leaves the link out; only an app name and a dashboard id become a link.
func dashboardsOf(ctx context.Context, data kube.Data) map[string]string {
	record, err := data.GetConfigMap(ctx, dashboards.RecordNamespace, dashboards.RecordName)
	found := map[string]string{}
	if err != nil || record == nil {
		return found
	}
	ids := map[string]string{}
	if err := json.Unmarshal([]byte(record.Data[dashboards.RecordKey]), &ids); err != nil {
		return found
	}
	for app, id := range ids {
		if dashboardApp.MatchString(app) && dashboardID.MatchString(id) {
			found[app] = id
		}
	}
	return found
}

// AppStack reads one app's swhurl.yaml from its repository's default branch and returns the stack it names.
// reachable is false when GitHub could not be asked at all: a page then stops asking it for the rest of the apps
// rather than waiting once per app. A file that is absent or unusable names no stack, which is a fact, not a failure.
func AppStack(ctx context.Context, console github.Console, owner, app string) (stack string, reachable bool) {
	body, found, err := console.AppFile(ctx, owner+"/"+app, "", "swhurl.yaml")
	if err != nil {
		return "", false
	}
	if !found {
		return "", true
	}
	declaration, err := appsource.Parse(body, "swhurl.yaml")
	if err != nil {
		return "", true
	}
	if version, named := number(declaration.Get("version")); !named || version != 1 {
		return "", true
	}
	stack, _ = declaration.Get("stack").(string)
	return stack, true
}

// number is a declared version as the YAML reader states it.
func number(value any) (int64, bool) {
	switch found := value.(type) {
	case int64:
		return found, true
	case int:
		return int64(found), true
	}
	return 0, false
}

// Site is the platform a page links into: the domain its profile's routing shares, and the owner of its
// repositories.
func Site(files map[string][]byte, repository string) (pages.Site, error) {
	profile, err := kernel.DecodeProfile(files[profilePath])
	if err != nil {
		return pages.Site{}, fmt.Errorf("platform profile unavailable: %w", err)
	}
	owner, _, _ := strings.Cut(repository, "/")
	return pages.Site{Domain: profile.Routing.BaseDomain, Owner: owner}, nil
}
