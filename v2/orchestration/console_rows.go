package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/dashboards"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// ConsoleRows decodes a bulk status collection into the rows the console's pages show: one row per instance it
// reports, and one per instance only the cluster still has, so an app removed from Git stays visible until its
// namespace goes.
func ConsoleRows(collection observe.Collection) ([]pages.AppRow, error) {
	teams := map[string]string{}
	rows := make([]pages.AppRow, 0, len(collection.Instances))
	for _, instance := range collection.Instances {
		row, err := consoleRowOf(instance, teams)
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

// consoleRowOf is one instance as a row, remembering its team for the rows after it.
func consoleRowOf(instance observe.InstanceReport, teams map[string]string) (*pages.AppRow, error) {
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

// ConsoleTeams is the team registry as a page shows it: each team's description and its channel's name and
// transport, read from the pinned tree's v2-records/teams.json.
func ConsoleTeams(tree map[string][]byte) (map[string]pages.TeamInfo, error) {
	data, known := tree[consoleRecords+"/teams.json"]
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

// ConsoleDashboards is each app's HyperDX dashboard, from the record the dashboard job keeps. Empty when it cannot be
// read, so a page leaves the link out; only an app name and a dashboard id become a link.
func ConsoleDashboards(ctx context.Context, data kube.Data) map[string]string {
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

// ConsoleStack reads one app's swhurl.yaml from its repository's default branch and returns the stack it names.
// reachable is false when GitHub could not be asked at all: a page then stops asking it for the rest of the apps
// rather than waiting once per app. A file that is absent or unusable names no stack, which is a fact, not a failure.
func ConsoleStack(ctx context.Context, console github.Console, owner, app string) (stack string, reachable bool) {
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

// ConsoleSite is the platform a page links into: the domain its profile's routing shares, and the owner of its
// repositories.
func ConsoleSite(files map[string][]byte, repository string) (pages.Site, error) {
	profile, err := kernel.DecodeProfile(files[consoleProfile])
	if err != nil {
		return pages.Site{}, fmt.Errorf("platform profile unavailable: %w", err)
	}
	owner, _, _ := strings.Cut(repository, "/")
	return pages.Site{Domain: profile.Routing.BaseDomain, Owner: owner}, nil
}

// The console keeps its jobs outside the pod: one key per job in this ConfigMap, written as each starts and as it
// ends (docs/console.md#jobs-and-restarts). A page reads them; step 2's writer owns what is written.
const (
	consoleJobsNamespace = "console"
	consoleJobsName      = "console-jobs"
	consolePodKind       = "Pod"
)

// ConsoleJobs reads the jobs the console keeps outside its pod, newest first. A record that cannot be read is left
// out rather than shown wrong, and an unreadable store is no jobs (the page still renders).
func ConsoleJobs(ctx context.Context, data kube.Data, reader kube.Reader, now time.Time) []pages.Job {
	record, err := data.GetConfigMap(ctx, consoleJobsNamespace, consoleJobsName)
	if err != nil || record == nil {
		return nil
	}
	found := make([]pages.Job, 0, len(record.Data))
	for _, raw := range record.Data {
		if job, ok := decodeJob(raw); ok {
			found = append(found, job)
		}
	}
	slices.SortFunc(found, func(a, b pages.Job) int { return b.ID - a.ID })
	alive := map[string]bool{}
	for i := range found {
		if !found[i].Running() {
			continue
		}
		owner := found[i].Owner
		known, ok := alive[owner]
		if !ok {
			known = owner != "" && podAlive(ctx, reader, owner)
			alive[owner] = known
		}
		if !known {
			found[i] = found[i].Interrupted(now)
		}
	}
	return found
}

// podAlive is whether the pod that ran a job still exists; an unreadable answer counts as alive, so a job is called
// interrupted only when its pod is known to be gone.
func podAlive(ctx context.Context, reader kube.Reader, owner string) bool {
	read, err := reader.Read(ctx, observe.Ref{APIVersion: "v1", Kind: consolePodKind, Namespace: consoleJobsNamespace, Name: owner})
	if err != nil {
		return true
	}
	return read.Status != observe.Absent
}

// decodeJob is one stored record as a job; anything malformed is not a job.
func decodeJob(raw string) (pages.Job, bool) {
	var record struct {
		ID       int            `json:"id"`
		Action   string         `json:"action"`
		Unit     string         `json:"unit"`
		Identity string         `json:"identity"`
		Started  string         `json:"started"`
		Finished *string        `json:"finished"`
		State    string         `json:"state"`
		Link     string         `json:"link"`
		Owner    string         `json:"owner"`
		Resume   map[string]any `json:"resume"`
		Reached  string         `json:"reached"`
		Lines    []string       `json:"lines"`
	}
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return pages.Job{}, false
	}
	started, err := time.Parse(time.RFC3339Nano, record.Started)
	if err != nil {
		return pages.Job{}, false
	}
	var finished *time.Time
	if record.Finished != nil {
		at, err := time.Parse(time.RFC3339Nano, *record.Finished)
		if err != nil {
			return pages.Job{}, false
		}
		finished = &at
	}
	if !slices.Contains([]string{"running", "succeeded", "failed", "interrupted"}, record.State) {
		return pages.Job{}, false
	}
	return pages.Job{ID: record.ID, Action: record.Action, Unit: record.Unit, Identity: record.Identity,
		Started: started, Finished: finished, State: record.State, Link: record.Link, Owner: record.Owner,
		Resume: record.Resume, Reached: record.Reached, Lines: record.Lines}, true
}
