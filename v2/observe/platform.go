package observe

// The platform checks that only read the cluster: the Flux units, Traefik's HTTPS redirect, the Flux alerts, the
// notify service and the console's two CronJobs. This file states the objects they need and judges what was read;
// reading them, and the time, are the caller's. A failed read is a failed check, never absence.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Level is one entry's verdict: ok passes, warn passes but asks for attention, bad fails the run.
type CheckLevel string

const (
	CheckOK   CheckLevel = "ok"
	CheckWarn CheckLevel = "warn"
	CheckBad  CheckLevel = "bad"
)

// PlatformEntry is one result of a check.
type PlatformEntry struct {
	Level   CheckLevel `json:"level"`
	Message string     `json:"message"`
}

// PlatformCheck is one named group of results under its section title.
type PlatformCheck struct {
	Name    string          `json:"name"`
	Section string          `json:"section"`
	Entries []PlatformEntry `json:"entries"`
}

// PlatformReport is every check, in the order they run, with where and when the evidence was read.
type PlatformReport struct {
	Source     string
	ObservedAt string
	Checks     []PlatformCheck
}

// Passed is whether no entry is bad: a warning passes.
func (r PlatformReport) Passed() bool {
	for _, check := range r.Checks {
		for _, entry := range check.Entries {
			if entry.Level == CheckBad {
				return false
			}
		}
	}
	return true
}

// The objects the checks read. The units, alerts and providers are whole namespace collections: the root units carry
// no label to select them by.
var (
	PlatformUnits         = Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization", Namespace: "flux-system"}
	PlatformAlerts        = Ref{APIVersion: "notification.toolkit.fluxcd.io/v1beta3", Kind: "Alert", Namespace: "flux-system"}
	PlatformProviders     = Ref{APIVersion: "notification.toolkit.fluxcd.io/v1beta3", Kind: "Provider", Namespace: "flux-system"}
	PlatformTraefik       = Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "kube-system", Name: "traefik"}
	PlatformNotify        = Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "notify", Name: "notify"}
	PlatformConsole       = Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease", Namespace: "console", Name: "console"}
	PlatformDashboards    = Ref{APIVersion: "batch/v1", Kind: "CronJob", Namespace: "console", Name: "console-dashboards"}
	PlatformNotifications = Ref{APIVersion: "batch/v1", Kind: "CronJob", Namespace: "console", Name: "console-notifications"}
)

// platformAlertNames are the Flux alerts platform/alerts/alerts.yaml declares.
var platformAlertNames = []string{"failures"}

// platformFresh is how recently a CronJob must have succeeded: both run every minute.
const platformFresh = 5 * time.Minute

// PlatformEvidence is what the caller read for the checks, and the time it read it at.
type PlatformEvidence struct {
	Units, Alerts, Providers                            List
	Traefik, Notify, Console, Dashboards, Notifications Read
	Now                                                 time.Time
}

// PlatformChecks judges the evidence, in the order the checks run. Each group's section comes from the catalogue.
func PlatformChecks(e PlatformEvidence) []PlatformCheck {
	return []PlatformCheck{
		platformCheck("flux-units", platformUnits(e.Units)),
		platformCheck("ingress", platformIngress(e.Traefik)),
		platformCheck("alerts", platformAlerts(e.Alerts, e.Providers)),
		platformCheck("notify-service", platformNotifyService(e.Notify, e.Console)),
		platformCheck("dashboards", platformDashboards(e.Dashboards, e.Now)),
		platformCheck("notifications", platformNotifications(e.Notifications, e.Now)),
	}
}

func ok(message string) PlatformEntry   { return PlatformEntry{CheckOK, message} }
func warn(message string) PlatformEntry { return PlatformEntry{CheckWarn, message} }
func bad(message string) PlatformEntry  { return PlatformEntry{CheckBad, message} }

type platformUnit struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Status struct {
		Conditions []struct {
			Type, Status, Message string
		} `json:"conditions"`
	} `json:"status"`
}

func platformUnits(list List) []PlatformEntry {
	if list.Status != Present {
		return []PlatformEntry{bad("cannot read Flux kustomizations (kubectl -n flux-system get kustomizations)")}
	}
	if len(list.Items) == 0 {
		return []PlatformEntry{bad("no Flux kustomizations in flux-system, so nothing applies Git: make flux-bootstrap")}
	}
	found := make([]platformUnit, 0, len(list.Items))
	for _, item := range list.Items {
		var u platformUnit
		_ = json.Unmarshal(item, &u) // the adapter checked each item's identity; a status it cannot read is no Ready condition
		found = append(found, u)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Metadata.Name < found[j].Metadata.Name })
	entries := make([]PlatformEntry, 0, len(found))
	for _, u := range found {
		entries = append(entries, platformUnitEntry(u))
	}
	return entries
}

func platformUnitEntry(u platformUnit) PlatformEntry {
	status, message := "Unknown", "no Ready condition"
	for _, condition := range u.Status.Conditions {
		if condition.Type == "Ready" {
			status, message = condition.Status, condition.Message
			break
		}
	}
	if status == "True" {
		return ok(u.Metadata.Name)
	}
	if message != "" {
		return bad(u.Metadata.Name + " is not Ready: " + message)
	}
	return bad(u.Metadata.Name + " is not Ready")
}

// platformRedirect is the argument k3s's Traefik chart renders for ports.web.redirections.entryPoint.
const platformRedirect = "entryPoints.web.http.redirections.entryPoint.scheme=https"

func platformIngress(read Read) []PlatformEntry {
	if problem, failed := platformUnread(read, "Traefik (kube-system/traefik)", "its HTTP redirect is unknown"); failed {
		return []PlatformEntry{problem}
	}
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Args []string `json:"args"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	_ = json.Unmarshal(read.Object, &deployment) // an unreadable spec has no redirect, which is the failure reported
	if containers := deployment.Spec.Template.Spec.Containers; len(containers) > 0 {
		for _, arg := range containers[0].Args {
			if strings.Contains(arg, platformRedirect) {
				return []PlatformEntry{ok("Traefik redirects HTTP to HTTPS")}
			}
		}
	}
	return []PlatformEntry{bad("Traefik does not redirect HTTP to HTTPS (plain-HTTP sign-in fails with 403); " +
		"check infra/traefik/helmchartconfig.yaml")}
}

// platformUnread is the failed check for an object that was not read: unavailable says why, absent says it is missing.
func platformUnread(read Read, what, consequence string) (PlatformEntry, bool) {
	switch read.Status {
	case Present:
		return PlatformEntry{}, false
	case Absent:
		return bad(fmt.Sprintf("%s is missing, so %s", what, consequence)), true
	}
	return bad(fmt.Sprintf("cannot read %s (%s), so %s", what, read.Reason, consequence)), true
}

type platformAlert struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		ProviderRef struct {
			Name string `json:"name"`
		} `json:"providerRef"`
	} `json:"spec"`
}

func platformAlerts(found, providers List) []PlatformEntry {
	if found.Status != Present || providers.Status != Present {
		return []PlatformEntry{bad("cannot read Flux alerts and providers (make reconcile UNIT=platform-alerts)")}
	}
	named := map[string]platformAlert{}
	for _, item := range found.Items {
		var a platformAlert
		_ = json.Unmarshal(item, &a) // the adapter checked each item's identity
		named[a.Metadata.Name] = a
	}
	existing := map[string]bool{}
	for _, item := range providers.Items {
		var p platformAlert
		_ = json.Unmarshal(item, &p)
		existing[p.Metadata.Name] = true
	}
	entries := make([]PlatformEntry, 0, len(platformAlertNames))
	for _, name := range platformAlertNames {
		entries = append(entries, platformAlertEntry(name, named, existing))
	}
	return entries
}

func platformAlertEntry(name string, named map[string]platformAlert, providers map[string]bool) PlatformEntry {
	a, found := named[name]
	provider := a.Spec.ProviderRef.Name
	switch {
	case !found:
		return warn("alert " + name + " is missing: no push notifications for it (make reconcile UNIT=platform-alerts)")
	case !providers[provider]:
		return bad("alert " + name + " points at provider " + provider + ", which does not exist, so its notifications are " +
			"lost (make reconcile UNIT=platform-alerts)")
	}
	return ok("alert " + name + " sends to " + provider)
}

const platformNotifyLost = "teams get no copies of their apps' messages (make reconcile UNIT=platform-notify)"

func platformNotifyService(deployment, console Read) []PlatformEntry {
	if problem, failed := platformUnread(deployment, "the notify service (notify/notify)", platformNotifyLost); failed {
		return []PlatformEntry{problem}
	}
	if problem, failed := platformUnread(console, "the console's HelmRelease (console/console)", "the notify service's image is unchecked"); failed {
		return []PlatformEntry{problem}
	}
	running, ready, readable := platformNotifyImage(deployment.Object)
	if readable && ready == 0 {
		return []PlatformEntry{bad("the notify service is not running: teams get no copies of their apps' messages " +
			"(kubectl -n notify get pods; make reconcile UNIT=platform-notify)")}
	}
	tag := platformConsoleTag(console.Object)
	if !readable || tag == "" {
		return []PlatformEntry{bad("cannot read the notify service's image or the console's tag (notify/notify, console/console): " + platformNotifyLost)}
	}
	if !strings.Contains(running, ":"+tag) {
		name, _, _ := strings.Cut(running, "@")
		return []PlatformEntry{warn("the notify service runs " + name[strings.LastIndex(name, ":")+1:] + ", not the console's " +
			tag + ": make console-image pins both, then commit and push")}
	}
	return []PlatformEntry{ok("the notify service is running " + tag + ", as the console")}
}

// platformNotifyImage is the notify Deployment's first container image and its ready replicas; false when it names none.
func platformNotifyImage(object []byte) (string, int64, bool) {
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int64 `json:"readyReplicas"`
		} `json:"status"`
	}
	if json.Unmarshal(object, &deployment) != nil || len(deployment.Spec.Template.Spec.Containers) == 0 {
		return "", 0, false
	}
	image := deployment.Spec.Template.Spec.Containers[0].Image
	return image, deployment.Status.ReadyReplicas, image != ""
}

// platformConsoleTag is the image tag the console's HelmRelease pins; empty when it names none.
func platformConsoleTag(object []byte) string {
	var release struct {
		Spec struct {
			Values struct {
				Controllers struct {
					Main struct {
						Containers struct {
							Main struct {
								Image struct {
									Tag string `json:"tag"`
								} `json:"image"`
							} `json:"main"`
						} `json:"containers"`
					} `json:"main"`
				} `json:"controllers"`
			} `json:"values"`
		} `json:"spec"`
	}
	_ = json.Unmarshal(object, &release) // a release it cannot read names no tag
	return release.Spec.Values.Controllers.Main.Containers.Main.Image.Tag
}

// cronState is a CronJob's last success as the check sees it.
type cronState int

const (
	cronUnknown cronState = iota
	cronSuspended
	cronStale
	cronRecent
)

// platformCronRun is whether a CronJob is suspended, has not succeeded within fresh of now, or has.
func platformCronRun(object []byte, now time.Time) cronState {
	var job struct {
		Spec *struct {
			Suspend bool `json:"suspend"`
		} `json:"spec"`
		Status struct {
			LastSuccessfulTime string `json:"lastSuccessfulTime"`
		} `json:"status"`
	}
	if json.Unmarshal(object, &job) != nil || job.Spec == nil {
		return cronUnknown
	}
	if job.Spec.Suspend {
		return cronSuspended
	}
	if job.Status.LastSuccessfulTime == "" {
		return cronStale
	}
	taken, err := time.Parse(time.RFC3339, job.Status.LastSuccessfulTime)
	if err != nil {
		return cronUnknown
	}
	if now.Sub(taken) > platformFresh {
		return cronStale
	}
	return cronRecent
}

// platformCronJob is the one entry for a CronJob that must keep succeeding: what each state means is the caller's words.
type platformCronJob struct {
	what, consequence, unknown, suspended, stale, recent string
}

func (c platformCronJob) entry(read Read, now time.Time) []PlatformEntry {
	if problem, failed := platformUnread(read, c.what, c.consequence); failed {
		return []PlatformEntry{problem}
	}
	switch platformCronRun(read.Object, now) {
	case cronSuspended:
		return []PlatformEntry{bad(c.suspended)}
	case cronStale:
		return []PlatformEntry{bad(c.stale)}
	case cronRecent:
		return []PlatformEntry{ok(c.recent)}
	}
	return []PlatformEntry{bad(c.unknown)}
}

func platformDashboards(read Read, now time.Time) []PlatformEntry {
	return platformCronJob{
		what:        "dashboard sync status (console/console-dashboards)",
		consequence: "app dashboards and alerts may not follow Git (make reconcile UNIT=platform-console)",
		unknown:     "cannot read dashboard sync status (console/console-dashboards; make reconcile UNIT=platform-console)",
		suspended: `dashboard and alert sync is suspended, so app dashboards and alerts do not follow Git: kubectl -n ` +
			`console patch cronjob console-dashboards -p '{"spec":{"suspend":false}}'`,
		stale: "dashboard and alert sync has not succeeded in 5 minutes (kubectl -n console get jobs; kubectl -n " +
			"console logs job/<newest console-dashboards-...>). To repair platform webhooks, dashboards and alerts: make " +
			"clickstack-dashboards; no new jobs at all: docs/operations.md#troubleshooting",
		recent: "dashboard and alert sync succeeded in the last 5 minutes",
	}.entry(read, now)
}

func platformNotifications(read Read, now time.Time) []PlatformEntry {
	return platformCronJob{
		what:        "notification checker status (console/console-notifications)",
		consequence: "notifications may be late or lost (make reconcile UNIT=platform-console)",
		unknown:     "cannot read notification checker status (console/console-notifications; make reconcile UNIT=platform-console)",
		suspended: "notification checker is suspended, so no notifications are sent: kubectl -n " +
			`console patch cronjob console-notifications -p '{"spec":{"suspend":false}}'`,
		stale: "notification checker has not succeeded in 5 minutes, so notifications may be late or lost " +
			"(kubectl -n console get jobs; kubectl -n console logs job/<newest console-notifications-...>); " +
			"no new jobs at all: docs/operations.md#troubleshooting",
		recent: "notification checker succeeded in the last 5 minutes",
	}.entry(read, now)
}

// PlatformKind and PlatformVersion name the JSON document Encode writes.
const (
	PlatformKind    = "v2-platform-checks"
	PlatformVersion = 1
)

type platformDocument struct {
	Kind       string          `json:"kind"`
	Version    int             `json:"version"`
	Source     string          `json:"source"`
	ObservedAt string          `json:"observedAt"`
	Passed     bool            `json:"passed"`
	Checks     []PlatformCheck `json:"checks"`
}

// EncodePlatformReport is the report as the v2-platform-checks document. A check with no entries is refused: every check says
// something, so an empty one is a defect, not a pass.
func EncodePlatformReport(r PlatformReport) ([]byte, error) {
	for _, check := range r.Checks {
		if len(check.Entries) == 0 {
			return nil, errors.New("invalid: platform checks: check " + check.Name + " has no entries")
		}
	}
	return json.Marshal(platformDocument{PlatformKind, PlatformVersion, r.Source, r.ObservedAt, r.Passed(), r.Checks})
}

// PlatformText is the report as verify-platform prints it: a heading per section and one line per entry.
func PlatformText(r PlatformReport) string {
	var out bytes.Buffer
	for _, check := range r.Checks {
		fmt.Fprintf(&out, "\n== %s ==\n", check.Section)
		for _, entry := range check.Entries {
			fmt.Fprintf(&out, "[%s] %s\n", strings.ToUpper(string(entry.Level)), entry.Message)
		}
	}
	if r.Passed() {
		out.WriteString("\nValidation passed.\n")
	}
	return out.String()
}
