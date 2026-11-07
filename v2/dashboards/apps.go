// Package dashboards decides what ClickStack should hold for the platform's apps: one dashboard per app, its
// default alerts, the two reserved webhooks and the console's record of which dashboard is whose. It compares that
// with what HyperDX lists and plans the changes; it performs none. Apps come from the cluster's HelmReleases or
// from the v2 records, both supplied by the caller.
package dashboards

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Kinds an app runs as. A scheduled job is not one v2 runs, so it has no dashboard here.
const (
	Web    = "web"
	Worker = "worker"
)

// AlertsAnnotation is where an app on the cluster states its own alert thresholds, on its namespaces.
const AlertsAnnotation = "platform.swhurl.com/alerts"

// staging is the environment whose facts an app's dashboard follows when its environments disagree.
const staging = "staging"

var environments = []string{staging, "prod"}

// appName is an app's name: a DNS label of at most 40 characters starting with a letter.
var appName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// App is one app as its dashboard needs it: its name, what it runs as, its environments (staging first) and its
// own alert settings ("" for the defaults).
type App struct {
	Name         string
	Kind         string
	Environments []string
	Alerts       string
}

// DashboardName is the name of the app's dashboard, which is also how a dashboard is known to be an app's.
func (a App) DashboardName() string { return "App: " + a.Name }

// namespaces is the app's namespaces as an SQL list: 'web-staging', 'web-prod'.
func (a App) namespaces() string {
	quoted := make([]string, 0, len(a.Environments))
	for _, environment := range a.Environments {
		quoted = append(quoted, "'"+a.Name+"-"+environment+"'")
	}
	return strings.Join(quoted, ", ")
}

// Instance is one environment of an app as a source states it: the cluster's HelmRelease or the v2 records.
type Instance struct {
	Application, Environment, Kind, Alerts string
}

// ErrNoRecords is a records tree that names no instance: never read as "every app is gone".
var ErrNoRecords = errors.New("the records name no app instance; no change made")

// FromRecords is the apps the v2 records name. Every instance must be a known environment of a valid app name that
// runs as web or worker; anything else, or no instance at all, is a failure rather than an app left out.
func FromRecords(instances []Instance) ([]App, error) {
	if len(instances) == 0 {
		return nil, ErrNoRecords
	}
	for _, instance := range instances {
		if err := instance.valid(); err != nil {
			return nil, err
		}
	}
	return grouped(instances), nil
}

func (i Instance) valid() error {
	if !appName.MatchString(i.Application) || !slices.Contains(environments, i.Environment) {
		return errors.New("an instance record names no valid app and environment; no change made")
	}
	if i.Kind != Web && i.Kind != Worker {
		return errors.New("app " + i.Application + "/" + i.Environment + " runs as neither web nor worker; no change made")
	}
	return nil
}

// grouped is one App per application, sorted by name, each with its environments staging first. Its kind and alert
// settings are staging's when it has staging, so a production still on another release does not decide them.
func grouped(instances []Instance) []App {
	byApp := map[string][]Instance{}
	for _, instance := range instances {
		byApp[instance.Application] = append(byApp[instance.Application], instance)
	}
	apps := make([]App, 0, len(byApp))
	for name, found := range byApp {
		sort.SliceStable(found, func(i, j int) bool {
			return environmentOrder(found[i].Environment) < environmentOrder(found[j].Environment)
		})
		app := App{Name: name, Kind: found[0].Kind}
		for _, instance := range found {
			if !slices.Contains(app.Environments, instance.Environment) {
				app.Environments = append(app.Environments, instance.Environment)
			}
			if app.Alerts == "" {
				app.Alerts = instance.Alerts
			}
		}
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps
}

func environmentOrder(environment string) int { return slices.Index(environments, environment) }

// release is what discovery reads of a HelmRelease: its identity first, and its values only once it is an app's.
type release struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Values map[string]json.RawMessage `json:"values"`
	} `json:"spec"`
}

// namespace is what discovery reads of a Namespace: its name and annotations.
type namespace struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
}

// Discover is the apps whose environments the cluster runs: each HelmRelease that the Flux unit
// app-<app>-<env> in flux-system applied, named for its app, in the app's namespace <app>-<env>. Shared services and
// releases installed by hand never match. An app's alert settings are the annotation on its namespaces. A document
// that does not decode, or an app release that runs as a scheduled job, stops discovery: neither is an absent app.
func Discover(releases, namespaces [][]byte) ([]App, error) {
	settings, err := alertSettings(namespaces)
	if err != nil {
		return nil, err
	}
	instances := []Instance{}
	for _, raw := range releases {
		var found release
		if json.Unmarshal(raw, &found) != nil {
			return nil, errors.New("a HelmRelease does not decode; no change made")
		}
		instance, ok := found.instance()
		if !ok {
			continue
		}
		if instance.Kind, err = found.kind(); err != nil {
			return nil, errors.New("app " + instance.Application + "/" + instance.Environment + " " + err.Error() + "; no change made")
		}
		instance.Alerts = settings[instance.Application+"-"+instance.Environment]
		instances = append(instances, instance)
	}
	return grouped(instances), nil
}

// instance is the app environment a release is, when its unit, name and namespace all say the same one.
func (r release) instance() (Instance, bool) {
	labels := r.Metadata.Labels
	unit, ok := strings.CutPrefix(labels["kustomize.toolkit.fluxcd.io/name"], "app-")
	if !ok || labels["kustomize.toolkit.fluxcd.io/namespace"] != "flux-system" {
		return Instance{}, false
	}
	cut := strings.LastIndex(unit, "-")
	if cut < 0 {
		return Instance{}, false
	}
	app, environment := unit[:cut], unit[cut+1:]
	if !appName.MatchString(app) || !slices.Contains(environments, environment) ||
		r.Metadata.Name != app || r.Metadata.Namespace != unit {
		return Instance{}, false
	}
	return Instance{Application: app, Environment: environment}, true
}

// kind is what an app release runs as: web when it states a Service at all, else a worker, unless its controller is
// a CronJob, which v2 does not run and which has no dashboard.
func (r release) kind() (string, error) {
	if _, ok := r.Spec.Values["service"]; ok {
		return Web, nil
	}
	var controllers struct {
		Main struct {
			Type string `json:"type"`
		} `json:"main"`
	}
	if raw, ok := r.Spec.Values["controllers"]; ok && json.Unmarshal(raw, &controllers) != nil {
		return "", errors.New("states controllers that do not decode")
	}
	if controllers.Main.Type == "cronjob" {
		return "", errors.New("runs as a scheduled job, which has no dashboard")
	}
	return Worker, nil
}

func alertSettings(namespaces [][]byte) (map[string]string, error) {
	settings := map[string]string{}
	for _, raw := range namespaces {
		var found namespace
		if json.Unmarshal(raw, &found) != nil {
			return nil, errors.New("a Namespace does not decode; no change made")
		}
		settings[found.Metadata.Name] = found.Metadata.Annotations[AlertsAnnotation]
	}
	return settings, nil
}
