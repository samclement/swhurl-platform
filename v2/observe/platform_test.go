package observe

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var platformNow = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func presentRead(ref Ref, object string) Read {
	return Read{Ref: ref, Status: Present, Object: []byte(object)}
}

func collectionOf(ref Ref, items ...string) List {
	list := List{Ref: ref, Status: Present, Items: [][]byte{}}
	for _, item := range items {
		list.Items = append(list.Items, []byte(item))
	}
	return list
}

func cronObject(suspend bool, last string) string {
	status := `{}`
	if last != "" {
		status = `{"lastSuccessfulTime":"` + last + `"}`
	}
	return `{"spec":{"suspend":` + map[bool]string{true: "true", false: "false"}[suspend] + `},"status":` + status + `}`
}

const traefikRedirects = `{"spec":{"template":{"spec":{"containers":[{"args":["--entryPoints.web.http.redirections.entryPoint.scheme=https"]}]}}}}`
const notifyRunning = `{"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/samclement/swhurl-console:src-abc@sha256:00"}]}}},"status":{"readyReplicas":2}}`
const consoleAt = `{"spec":{"values":{"controllers":{"main":{"containers":{"main":{"image":{"tag":"src-abc"}}}}}}}}`

// healthy is evidence every check passes on.
func healthy() PlatformEvidence {
	recent := platformNow.Add(-time.Minute).Format(time.RFC3339)
	return PlatformEvidence{
		Units:         collectionOf(PlatformUnits, `{"metadata":{"name":"infra"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`),
		Alerts:        collectionOf(PlatformAlerts, `{"metadata":{"name":"failures"},"spec":{"providerRef":{"name":"ntfy"}}}`),
		Providers:     collectionOf(PlatformProviders, `{"metadata":{"name":"ntfy"}}`),
		Traefik:       presentRead(PlatformTraefik, traefikRedirects),
		Notify:        presentRead(PlatformNotify, notifyRunning),
		Console:       presentRead(PlatformConsole, consoleAt),
		Dashboards:    presentRead(PlatformDashboards, cronObject(false, recent)),
		Notifications: presentRead(PlatformNotifications, cronObject(false, recent)),
		Now:           platformNow,
	}
}

// checkEntries is one check's results, by name.
func checkEntries(t *testing.T, e PlatformEvidence, name string) []PlatformEntry {
	t.Helper()
	for _, check := range PlatformChecks(e) {
		if check.Name == name {
			return check.Entries
		}
	}
	t.Fatalf("no check %s", name)
	return nil
}

func TestPlatformChecksPassInOrder(t *testing.T) {
	checks := PlatformChecks(healthy())
	names := []string{}
	for _, check := range checks {
		names = append(names, check.Name)
		for _, entry := range check.Entries {
			if entry.Level != CheckOK {
				t.Errorf("%s: %+v", check.Name, entry)
			}
		}
	}
	if strings.Join(names, " ") != "flux-units ingress alerts notify-service dashboards notifications" {
		t.Fatalf("order %v", names)
	}
	if !(PlatformReport{Checks: checks}).Passed() {
		t.Fatal("a healthy platform does not pass")
	}
}

func TestPlatformChecks(t *testing.T) {
	stale := platformNow.Add(-6 * time.Minute).Format(time.RFC3339)
	unavailable := func(ref Ref) Read { return Read{Ref: ref, Status: Unavailable, Reason: ReasonForbidden} }
	absent := func(ref Ref) Read { return Read{Ref: ref, Status: Absent, Reason: ReasonNotFound} }
	for _, test := range []struct {
		name, check string
		change      func(*PlatformEvidence)
		level       CheckLevel
		message     string
	}{
		{"units unreadable", "flux-units", func(e *PlatformEvidence) { e.Units = List{Ref: PlatformUnits, Status: Unavailable} },
			CheckBad, "cannot read Flux kustomizations"},
		{"no units", "flux-units", func(e *PlatformEvidence) { e.Units = collectionOf(PlatformUnits) }, CheckBad, "no Flux kustomizations"},
		{"unit not ready", "flux-units", func(e *PlatformEvidence) {
			e.Units = collectionOf(PlatformUnits, `{"metadata":{"name":"apps"},"status":{"conditions":[{"type":"Ready","status":"False","message":"health check failed"}]}}`)
		}, CheckBad, "apps is not Ready: health check failed"},
		{"unit without condition", "flux-units", func(e *PlatformEvidence) { e.Units = collectionOf(PlatformUnits, `{"metadata":{"name":"apps"}}`) },
			CheckBad, "apps is not Ready: no Ready condition"},
		{"no redirect", "ingress", func(e *PlatformEvidence) { e.Traefik = presentRead(PlatformTraefik, `{"spec":{}}`) },
			CheckBad, "Traefik does not redirect HTTP to HTTPS"},
		{"traefik unreadable", "ingress", func(e *PlatformEvidence) { e.Traefik = unavailable(PlatformTraefik) },
			CheckBad, "cannot read Traefik (kube-system/traefik) (forbidden)"},
		{"traefik absent", "ingress", func(e *PlatformEvidence) { e.Traefik = absent(PlatformTraefik) }, CheckBad, "Traefik (kube-system/traefik) is missing"},
		{"alert missing", "alerts", func(e *PlatformEvidence) { e.Alerts = collectionOf(PlatformAlerts) }, CheckWarn, "alert failures is missing"},
		{"provider missing", "alerts", func(e *PlatformEvidence) { e.Providers = collectionOf(PlatformProviders) },
			CheckBad, "alert failures points at provider ntfy, which does not exist"},
		{"alerts unreadable", "alerts", func(e *PlatformEvidence) { e.Providers = List{Ref: PlatformProviders, Status: Unavailable} },
			CheckBad, "cannot read Flux alerts and providers"},
		{"notify not running", "notify-service", func(e *PlatformEvidence) {
			e.Notify = presentRead(PlatformNotify, strings.Replace(notifyRunning, `"readyReplicas":2`, `"readyReplicas":0`, 1))
		}, CheckBad, "the notify service is not running"},
		{"notify not running and console unreadable", "notify-service", func(e *PlatformEvidence) {
			e.Notify = presentRead(PlatformNotify, strings.Replace(notifyRunning, `"readyReplicas":2`, `"readyReplicas":0`, 1))
			e.Console = presentRead(PlatformConsole, `{}`)
		}, CheckBad, "the notify service is not running"},
		{"notify on another image", "notify-service", func(e *PlatformEvidence) {
			e.Console = presentRead(PlatformConsole, strings.Replace(consoleAt, "src-abc", "src-def", 1))
		}, CheckWarn, "the notify service runs src-abc, not the console's src-def"},
		{"notify absent", "notify-service", func(e *PlatformEvidence) { e.Notify = absent(PlatformNotify) },
			CheckBad, "the notify service (notify/notify) is missing"},
		{"console unreadable", "notify-service", func(e *PlatformEvidence) { e.Console = unavailable(PlatformConsole) },
			CheckBad, "cannot read the console's HelmRelease"},
		{"console without tag", "notify-service", func(e *PlatformEvidence) { e.Console = presentRead(PlatformConsole, `{}`) },
			CheckBad, "cannot read the notify service's image or the console's tag"},
		{"dashboards suspended", "dashboards", func(e *PlatformEvidence) { e.Dashboards = presentRead(PlatformDashboards, cronObject(true, "")) },
			CheckBad, "dashboard and alert sync is suspended"},
		{"dashboards stale", "dashboards", func(e *PlatformEvidence) { e.Dashboards = presentRead(PlatformDashboards, cronObject(false, stale)) },
			CheckBad, "dashboard and alert sync has not succeeded in 5 minutes"},
		{"dashboards never ran", "dashboards", func(e *PlatformEvidence) { e.Dashboards = presentRead(PlatformDashboards, cronObject(false, "")) },
			CheckBad, "dashboard and alert sync has not succeeded in 5 minutes"},
		{"dashboards unreadable", "dashboards", func(e *PlatformEvidence) { e.Dashboards = unavailable(PlatformDashboards) },
			CheckBad, "cannot read dashboard sync status"},
		{"dashboards without spec", "dashboards", func(e *PlatformEvidence) { e.Dashboards = presentRead(PlatformDashboards, `{}`) },
			CheckBad, "cannot read dashboard sync status"},
		{"checker suspended", "notifications", func(e *PlatformEvidence) {
			e.Notifications = presentRead(PlatformNotifications, cronObject(true, stale))
		},
			CheckBad, "notification checker is suspended"},
		{"checker stale", "notifications", func(e *PlatformEvidence) {
			e.Notifications = presentRead(PlatformNotifications, cronObject(false, stale))
		},
			CheckBad, "notification checker has not succeeded in 5 minutes"},
		{"checker absent", "notifications", func(e *PlatformEvidence) { e.Notifications = absent(PlatformNotifications) },
			CheckBad, "notification checker status (console/console-notifications) is missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := healthy()
			test.change(&evidence)
			found := checkEntries(t, evidence, test.check)
			if len(found) != 1 || found[0].Level != test.level || !strings.Contains(found[0].Message, test.message) {
				t.Fatalf("%+v", found)
			}
		})
	}
}

func TestUnitsAreSortedAndEachReported(t *testing.T) {
	evidence := healthy()
	evidence.Units = collectionOf(PlatformUnits, `{"metadata":{"name":"b"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`,
		`{"metadata":{"name":"a"},"status":{"conditions":[{"type":"Ready","status":"Unknown"}]}}`)
	found := checkEntries(t, evidence, "flux-units")
	if len(found) != 2 || found[0] != (PlatformEntry{CheckBad, "a is not Ready"}) || found[1] != (PlatformEntry{CheckOK, "b"}) {
		t.Fatalf("%+v", found)
	}
}

func TestPlatformReportEncoding(t *testing.T) {
	report := PlatformReport{Source: "serviceaccount in console", ObservedAt: "2026-11-06T12:00:00Z", Checks: PlatformChecks(healthy())}
	report.Checks[1].Entries = []PlatformEntry{{CheckWarn, "w"}}
	data, err := EncodePlatformReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
		Source  string `json:"source"`
		Passed  bool   `json:"passed"`
		Checks  []PlatformCheck
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Kind != PlatformKind || document.Version != 1 || !document.Passed || len(document.Checks) != 6 ||
		document.Checks[1].Entries[0].Level != CheckWarn || document.Source != report.Source {
		t.Fatalf("%s", data)
	}
	report.Checks[0].Entries = []PlatformEntry{{CheckBad, "x"}}
	if data, _ := EncodePlatformReport(report); !strings.Contains(string(data), `"passed":false`) {
		t.Fatalf("a bad entry passed: %s", data)
	}
	report.Checks[0].Entries = nil
	if _, err := EncodePlatformReport(report); err == nil {
		t.Fatal("a check with no entries was encoded")
	}
}

func TestPlatformText(t *testing.T) {
	text := PlatformText(PlatformReport{Checks: []PlatformCheck{{"ingress", "Ingress", []PlatformEntry{{CheckBad, "no"}, {CheckOK, "yes"}}}}})
	if text != "\n== Ingress ==\n[BAD] no\n[OK] yes\n" {
		t.Fatalf("%q", text)
	}
}
