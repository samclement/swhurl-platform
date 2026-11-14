package orchestration

import (
	"context"
	"errors"
	"strconv"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/dashboards"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// DashboardPorts is the dashboard pass's only effects: the cluster's app releases and namespaces (discovery), the
// Secrets it reads by name and the console's record, and ClickStack.
type DashboardPorts struct {
	Reader     kube.Reader
	Data       kube.Data
	ClickStack clickstack.Opener
}

// DashboardOptions says where the apps come from: with FromRecords, Records, the v2 records' instances (none is a
// failure); otherwise the cluster's HelmReleases. DryRun reads and plans and writes nothing: not ClickStack, not the
// console's record.
type DashboardOptions struct {
	FromRecords bool
	Records     []RecordedInstance
	DryRun      bool
}

// RecordedInstance is one instance folder of the v2 records: its app, its environment and its release record.
type RecordedInstance struct {
	Application, Environment string
	Release                  []byte
}

// DashboardReport is what a pass did or, in a dry run, would do, line by line in order; it holds no credential.
type DashboardReport struct{ Lines []DashboardLine }

// DashboardLine is one outcome: Changed for a change made or planned, otherwise a fact (up to date, left alone).
type DashboardLine struct {
	Changed bool
	Text    string
}

func (r *DashboardReport) add(changed bool, text string) {
	r.Lines = append(r.Lines, DashboardLine{Changed: changed, Text: text})
}

// Where the pass reads its credentials, each by name and afresh on every pass.
const (
	clickstackInputs = "clickstack-runtime-inputs"
	adminEmailKey    = "CLICKSTACK_ADMIN_EMAIL"
	mongoURIKey      = "connectionString.standard"
	ntfyNamespace    = "console"
	ntfySecret       = "notification-ntfy"
	ntfyPlatformKey  = "NTFY_PLATFORM_URL"
	notifyNamespace  = "notify"
	notifyAlerts     = "notify-alerts"
	notifyAlertsKey  = "secret"
)

// dashboardPass is one pass's inputs, the apps it found, and its report as it goes.
type dashboardPass struct {
	ports   DashboardPorts
	options DashboardOptions
	apps    []dashboards.App
	sources dashboards.Sources
	prune   bool
	reason  string
	report  DashboardReport
}

// SyncDashboards runs one pass: find the apps, read the credentials, open ClickStack, repair both reserved webhooks,
// bring each app's dashboard to its definition, record the dashboards for the console, delete those of apps that are
// gone, then bring each app's default alerts to its dashboard as it now is. The first failure stops the pass, and
// the report says how far it got. No app found on the cluster prunes nothing but still repairs the webhooks.
func SyncDashboards(ctx context.Context, ports DashboardPorts, options DashboardOptions) (DashboardReport, error) {
	pass := &dashboardPass{ports: ports, options: options}
	err := pass.run(ctx)
	return pass.report, err
}

func (p *dashboardPass) run(ctx context.Context) error {
	if err := p.discover(ctx); err != nil {
		return err
	}
	api, wanted, err := p.open(ctx)
	if err != nil {
		return err
	}
	notify, err := p.webhooks(ctx, api, wanted)
	if err != nil {
		return err
	}
	if err := p.dashboards(ctx, api); err != nil {
		return err
	}
	if err := p.alerts(ctx, api, notify); err != nil {
		return err
	}
	return p.platformDashboards(ctx, api)
}

// discover finds the apps: from the records, every one or a failure; from the cluster, the app releases, which may
// be none (a cluster being restored), and then nothing is deleted.
func (p *dashboardPass) discover(ctx context.Context) error {
	if p.options.FromRecords {
		apps, err := appsOfRecords(p.options.Records)
		p.apps, p.prune, p.reason = apps, true, "its app has no records in Git"
		return err
	}
	apps, err := appsOfCluster(ctx, p.ports.Reader)
	p.apps, p.prune, p.reason = apps, len(apps) > 0, "its app has no HelmRelease on the cluster"
	return err
}

func appsOfRecords(records []RecordedInstance) ([]dashboards.App, error) {
	instances := make([]dashboards.Instance, 0, len(records))
	for _, record := range records {
		release, err := kernel.DecodeRelease(record.Release)
		if err != nil || release.Application != record.Application {
			return nil, errors.New("the release record of " + record.Application + "/" + record.Environment + " is not valid; no change made")
		}
		instances = append(instances, dashboards.Instance{Application: record.Application,
			Environment: record.Environment, Kind: release.Runtime.Kind})
	}
	return dashboards.FromRecords(instances)
}

// appsOfCluster lists the HelmReleases Flux applied and the app namespaces, whose annotations hold alert settings.
func appsOfCluster(ctx context.Context, reader kube.Reader) ([]dashboards.App, error) {
	releases, err := reader.ListAllNamespaces(ctx, observe.Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease"},
		"kustomize.toolkit.fluxcd.io/namespace=flux-system")
	if err := present(releases, err); err != nil {
		return nil, err
	}
	namespaces, err := reader.List(ctx, observe.Ref{APIVersion: "v1", Kind: "Namespace"}, observe.AppLabel)
	if err := present(namespaces, err); err != nil {
		return nil, err
	}
	return dashboards.Discover(releases.Items, namespaces.Items)
}

func present(list observe.List, err error) error {
	if err != nil {
		return err
	}
	if list.Status != observe.Present {
		return errors.New("cannot list " + list.Ref.Kind + " (" + list.Reason + "); no change made")
	}
	return nil
}

// open reads every credential, builds the reserved webhooks from them, and only then authenticates to ClickStack.
func (p *dashboardPass) open(ctx context.Context) (clickstack.API, []dashboards.Webhook, error) {
	email, err := secretText(ctx, p.ports.Data, clickstackNamespace, clickstackInputs, adminEmailKey)
	if err != nil {
		return nil, nil, bootstrapFirst(err)
	}
	uri, err := secretText(ctx, p.ports.Data, clickstackNamespace, mongoURISecret, mongoURIKey)
	if err != nil {
		return nil, nil, bootstrapFirst(err)
	}
	address, err := secretText(ctx, p.ports.Data, ntfyNamespace, ntfySecret, ntfyPlatformKey)
	if err != nil {
		return nil, nil, err
	}
	secret, err := secretText(ctx, p.ports.Data, notifyNamespace, notifyAlerts, notifyAlertsKey)
	if err != nil {
		return nil, nil, err
	}
	wanted, err := dashboards.Destinations(address, []byte(secret))
	if err != nil {
		return nil, nil, err
	}
	api, err := p.ports.ClickStack.Open(ctx, uri, email)
	return api, wanted, err
}

func bootstrapFirst(err error) error {
	return errors.New(err.Error() + "; run make clickstack-bootstrap first")
}

// secretText is one key of one Secret read by name; absent, empty or unreadable is an error naming only where.
func secretText(ctx context.Context, data kube.Data, namespace, name, key string) (string, error) {
	secret, err := data.GetSecret(ctx, namespace, name)
	if err != nil {
		return "", errors.New("cannot read " + namespace + "/" + name + ": " + err.Error())
	}
	if secret == nil || len(secret.Data[key]) == 0 {
		return "", errors.New(namespace + "/" + name + " has no " + key)
	}
	return string(secret.Data[key]), nil
}

// webhooks repairs both reserved webhooks, both checked before either is written, and returns the notify
// webhook's id, which every app alert names ("" only when a dry run would create it).
func (p *dashboardPass) webhooks(ctx context.Context, api clickstack.API, wanted []dashboards.Webhook) (string, error) {
	live, err := api.Webhooks(ctx)
	if err != nil {
		return "", err
	}
	steps, err := dashboards.PlanWebhooks(live, wanted)
	if err != nil {
		return "", err
	}
	notify := ""
	for _, step := range steps {
		id, err := p.webhook(ctx, api, step)
		if err != nil {
			return "", err
		}
		if step.Webhook.Name == dashboards.NotifyWebhook {
			notify = id
		}
	}
	return notify, nil
}

func (p *dashboardPass) webhook(ctx context.Context, api clickstack.API, step dashboards.WebhookStep) (string, error) {
	name := step.Webhook.Name
	switch {
	case step.Action == dashboards.Keep:
		p.report.add(false, name+" is up to date")
		return step.ID, nil
	case p.options.DryRun:
		p.report.add(true, "would "+string(step.Action)+" "+name)
		return step.ID, nil
	case step.Action == dashboards.Update:
		if err := api.UpdateWebhook(ctx, step.ID, step.Webhook); err != nil {
			return "", err
		}
		p.report.add(true, "updated "+name)
		return step.ID, nil
	}
	id, err := api.CreateWebhook(ctx, step.Webhook)
	if err == nil {
		p.report.add(true, "created "+name)
	}
	return id, err
}

// dashboards brings each app's dashboard to its definition, records them for the console, then deletes those of
// apps that are gone.
func (p *dashboardPass) dashboards(ctx context.Context, api clickstack.API) error {
	listed, err := api.Sources(ctx)
	if err != nil {
		return err
	}
	sources, err := dashboards.PickSources(listed)
	if err != nil {
		return err
	}
	p.sources = sources
	live, err := api.Dashboards(ctx)
	if err != nil {
		return err
	}
	steps, err := dashboards.PlanDashboards(p.apps, live, sources, p.prune)
	if err != nil {
		return err
	}
	ids, err := p.applyDashboards(ctx, api, steps)
	if err != nil {
		return err
	}
	if left := len(dashboards.Tagged(live, dashboards.Tag)); !p.prune && left > 0 { // no prune: no app was found, so every one is left
		p.report.add(false, "no app releases found; not deleting dashboards ("+strconv.Itoa(left)+" left)")
	}
	if err := p.record(ctx, ids); err != nil {
		return err
	}
	return p.pruneDashboards(ctx, api, steps, p.reason)
}

// applyDashboards creates and updates, and returns each app's dashboard id ("" for one created without an id in
// its answer, or in a dry run).
func (p *dashboardPass) applyDashboards(ctx context.Context, api clickstack.API, steps []dashboards.DashboardStep) (map[string]string, error) {
	ids := map[string]string{}
	for _, step := range steps {
		if step.App == "" {
			continue
		}
		ids[step.App] = step.ID
		name := step.Dashboard.Name
		switch {
		case step.Action == dashboards.Keep:
			p.report.add(false, name+" is up to date")
		case p.options.DryRun:
			p.report.add(true, "would "+string(step.Action)+" "+name)
		case step.Action == dashboards.Update:
			if err := api.UpdateDashboard(ctx, step.ID, step.Dashboard); err != nil {
				return nil, err
			}
			p.report.add(true, "updated "+name)
		default:
			id, err := api.CreateDashboard(ctx, step.Dashboard)
			if err != nil {
				return nil, err
			}
			ids[step.App] = id
			p.report.add(true, "created "+name)
		}
	}
	return ids, nil
}

// record keeps the console's record in step, writing only when it changed. With no app found on the cluster it is
// left as it is, like the dashboards. A record that cannot be read or written stops the pass: the console would
// link to dashboards that are gone.
func (p *dashboardPass) record(ctx context.Context, ids map[string]string) error {
	if p.options.DryRun || !p.prune && len(p.apps) == 0 {
		return nil
	}
	ids = dashboards.Recordable(ids)
	raw := dashboards.Record(ids)
	where := dashboards.RecordNamespace + "/" + dashboards.RecordName
	current, err := p.ports.Data.GetConfigMap(ctx, dashboards.RecordNamespace, dashboards.RecordName)
	if err != nil {
		return errors.New("could not read the dashboard record in " + where + ": " + err.Error())
	}
	if current != nil && current.Data[dashboards.RecordKey] == raw {
		return nil
	}
	err = p.ports.Data.SetConfigMapKey(ctx, dashboards.RecordNamespace, dashboards.RecordName, dashboards.RecordKey, raw,
		dashboards.RecordFieldManager)
	if err != nil {
		return errors.New("could not record dashboard addresses in " + where + ": " + err.Error())
	}
	counted := strconv.Itoa(len(ids)) + " dashboard addresses"
	if len(ids) == 1 {
		counted = "1 dashboard address"
	}
	p.report.add(true, "recorded "+counted+" for the console")
	return nil
}

// pruneDashboards deletes the family's leftovers, each with the reason it is gone, and leaves what is tagged but not
// named for the family.
func (p *dashboardPass) pruneDashboards(ctx context.Context, api clickstack.API, steps []dashboards.DashboardStep, reason string) error {
	for _, step := range steps {
		name := step.Dashboard.Name
		switch {
		case step.Action == dashboards.Leave:
			p.report.add(false, "left "+name+": tagged "+dashboards.Tag+" but not named for an app")
		case step.Action != dashboards.Delete:
		case p.options.DryRun:
			p.report.add(true, "would delete "+name+" ("+reason+")")
		default:
			if err := api.DeleteDashboard(ctx, step.ID); err != nil {
				return err
			}
			p.report.add(true, "deleted "+name+" ("+reason+")")
		}
	}
	return nil
}

// platformDashboards brings the platform services' own dashboards to the fixed catalogue: no record and no alerts,
// which belong to apps. A dashboard tagged for the platform but named otherwise is left, and the app family's
// dashboards are never read or deleted here.
func (p *dashboardPass) platformDashboards(ctx context.Context, api clickstack.API) error {
	live, err := api.Dashboards(ctx)
	if err != nil {
		return err
	}
	steps, err := dashboards.PlanPlatformDashboards(dashboards.PlatformServices(), live, p.sources, true)
	if err != nil {
		return err
	}
	if _, err := p.applyDashboards(ctx, api, steps); err != nil {
		return err
	}
	return p.pruneDashboards(ctx, api, steps, "its service is not in the platform catalogue")
}

// alerts reads the dashboards again, since a created or updated one has new tile ids, and brings each app's
// default alerts to them.
func (p *dashboardPass) alerts(ctx context.Context, api clickstack.API, notify string) error {
	live, err := api.Dashboards(ctx)
	if err != nil {
		return err
	}
	existing, err := api.Alerts(ctx)
	if err != nil {
		return err
	}
	steps, err := dashboards.PlanAlerts(p.apps, dashboards.Tagged(live, dashboards.Tag), existing, notify, p.prune)
	if err != nil {
		return err
	}
	changed := 0
	for _, step := range steps {
		if step.Action == dashboards.Keep {
			continue
		}
		changed++
		if err := p.alert(ctx, api, step); err != nil {
			return err
		}
	}
	if changed == 0 {
		p.report.add(false, strconv.Itoa(appsWithDashboards(p.apps, live))+" app(s) have their default alerts")
	}
	return nil
}

func appsWithDashboards(apps []dashboards.App, live []dashboards.Dashboard) int {
	names := map[string]bool{}
	for _, board := range dashboards.Tagged(live, dashboards.Tag) {
		names[board.Name] = true
	}
	count := 0
	for _, app := range apps {
		if names[app.DashboardName()] {
			count++
		}
	}
	return count
}

func (p *dashboardPass) alert(ctx context.Context, api clickstack.API, step dashboards.AlertStep) error {
	name := step.Alert.Name
	if p.options.DryRun {
		p.report.add(true, "would "+string(step.Action)+" "+name)
		return nil
	}
	var err error
	switch step.Action {
	case dashboards.Create:
		err = api.CreateAlert(ctx, step.Alert)
	case dashboards.Update:
		err = api.UpdateAlert(ctx, step.ID, step.Alert)
	default:
		err = api.DeleteAlert(ctx, step.ID)
	}
	if err != nil {
		return err
	}
	text := map[dashboards.Action]string{dashboards.Create: "created " + name, dashboards.Update: "updated " + name,
		dashboards.Delete: "deleted " + name + ": its app or tile is gone, or the app switched it off"}[step.Action]
	p.report.add(true, text)
	return nil
}

// ClickStackRunners is how the adapter's programs reach ClickStack: mongosh beside its MongoDB, and Node in its
// app, whose pod is found from the Deployment once, on the first request. Neither error carries the program or
// the output.
func ClickStackRunners(reader kube.Reader, executor kube.Executor) (mongo, app clickstack.Runner) {
	mongo = func(ctx context.Context, program []byte) ([]byte, error) {
		return execProgram(ctx, executor, kube.ExecRequest{Namespace: clickstackNamespace, Pod: "clickstack-mongodb-0",
			Container: "mongod", Command: []string{"mongosh", "--quiet", "--nodb", "--norc", "--file", "/dev/stdin"}, Stdin: program})
	}
	pod := ""
	app = func(ctx context.Context, program []byte) ([]byte, error) {
		if pod == "" {
			found, err := clickstackAppPod(ctx, reader)
			if err != nil {
				return nil, err
			}
			pod = found
		}
		return execProgram(ctx, executor, kube.ExecRequest{Namespace: clickstackNamespace, Pod: pod, Container: "app",
			Command: []string{"node", "--input-type=module", "-"}, Stdin: program})
	}
	return mongo, app
}

func clickstackAppPod(ctx context.Context, reader kube.Reader) (string, error) {
	deployment, err := reader.Read(ctx, clickstackRef("Deployment", "clickstack-app"))
	if err != nil {
		return "", err
	}
	selector := observe.ClickStackSelector(deployment)
	if selector == "" {
		return "", errors.New("cannot read the ClickStack app Deployment's selector")
	}
	pods, err := reader.List(ctx, clickstackRef("Pod", ""), selector)
	if err != nil {
		return "", err
	}
	if pod := observe.ClickStackPod(pods); pod != "" {
		return pod, nil
	}
	return "", errors.New("no ready ClickStack app pod")
}

func execProgram(ctx context.Context, executor kube.Executor, request kube.ExecRequest) ([]byte, error) {
	result, err := executor.Exec(ctx, request)
	if err != nil {
		return nil, errors.New("exec in " + request.Pod + " failed")
	}
	if result.ExitCode != 0 {
		return nil, errors.New("the program in " + request.Pod + " exited " + strconv.Itoa(result.ExitCode))
	}
	return result.Stdout, nil
}
