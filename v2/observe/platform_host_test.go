package observe

// The two host checks, case by case: the cases verify-platform's Python checks (flux.check_controllers and
// notifications.check_notification_heartbeat) were tested with, kept here when the checks moved.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

const fluxVersion = "2.8.1"

// fluxDeployment is one controller's live Deployment, at version with the arguments it runs.
func fluxDeployment(version string, args ...string) Read {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, `"`+arg+`"`)
	}
	object := `{"metadata":{"labels":{"app.kubernetes.io/version":"` + version + `"}},"spec":{"template":{"spec":` +
		`{"containers":[{"args":[` + strings.Join(quoted, ",") + `]}]}}}}`
	return Read{Status: Present, Object: []byte(object)}
}

func healthyFluxSettings() FluxSettings {
	return FluxSettings{Version: fluxVersion, ExtraComponents: []string{"image-reflector-controller"},
		RemovedComponents: []string{"image-automation-controller"},
		Args:              map[string][]string{"kustomize-controller": {"--requeue-dependency=5s"}}}
}

// healthyFlux is every controller installed at the pinned version, the one with required arguments running them.
func healthyFlux() FluxEvidence {
	settings := healthyFluxSettings()
	deployments := map[string]Read{}
	for _, name := range settings.Controllers() {
		deployments[name] = fluxDeployment("v"+fluxVersion, settings.Args[name]...)
	}
	return FluxEvidence{Settings: settings, Deployments: deployments}
}

// healthyHeartbeat is an active timer and a service that succeeded five minutes before the report's time.
func healthyHeartbeat() HeartbeatEvidence {
	return HeartbeatEvidence{Supplied: true, Text: heartbeatOutput("active", "success", "0",
		platformNow.Add(-5*time.Minute).Unix())}
}

// heartbeatOutput is systemctl show --timestamp=unix's two blocks: the timer states only its Id and ActiveState.
func heartbeatOutput(active, result, status string, exited int64) string {
	timer := "Id=" + heartbeatTimer + "\nActiveState=" + active + "\n\n"
	service := "Id=" + heartbeatService + "\nResult=" + result + "\nExecMainStatus=" + status + "\n"
	if exited != 0 {
		service += "ExecMainExitTimestamp=@" + strconv.FormatInt(exited, 10) + "\n"
	}
	return timer + service
}

func TestFluxControllers(t *testing.T) {
	settings := healthyFluxSettings()
	for _, test := range []struct {
		name   string
		change func(*FluxEvidence)
		level  CheckLevel
		want   string
	}{
		{"every controller matches", func(*FluxEvidence) {}, CheckOK, "kustomize-controller v2.8.1 with the settings in Git"},
		{"a controller without required arguments states only its version", func(*FluxEvidence) {}, CheckOK, "helm-controller v2.8.1"},
		{"a missing deployment warns", func(e *FluxEvidence) { e.Deployments["helm-controller"] = Read{Status: Absent} },
			CheckWarn, "helm-controller is not installed (run: make flux-install)"},
		{"another version warns", func(e *FluxEvidence) { e.Deployments["source-controller"] = fluxDeployment("v0.0.1") },
			CheckWarn, "source-controller is v0.0.1; expected v2.8.1 (run: make flux-install)"},
		{"a missing argument warns with what Git states", func(e *FluxEvidence) {
			e.Deployments["kustomize-controller"] = fluxDeployment("v"+fluxVersion, "--log-level=info")
		}, CheckWarn, "kustomize-controller is v2.8.1 with 0/1 Git settings; expected v2.8.1 with --requeue-dependency=5s " +
			"(run: make flux-install)"},
		{"an unknown version warns", func(e *FluxEvidence) { e.Deployments["source-controller"] = fluxDeployment("") },
			CheckWarn, "source-controller is unknown; expected v2.8.1"},
		{"an unreadable deployment is bad", func(e *FluxEvidence) {
			e.Deployments["source-controller"] = Read{Status: Present, Object: []byte(`{"metadata":{}}`)}
		}, CheckBad, "cannot read flux-system/source-controller (kubectl -n flux-system get deploy source-controller)"},
		{"a failed read is bad, never a warning", func(e *FluxEvidence) {
			e.Deployments["source-controller"] = Read{Status: Unavailable, Reason: ReasonForbidden}
		}, CheckBad, "cannot read flux-system/source-controller"},
		{"malformed settings leave the version unchecked", func(e *FluxEvidence) {
			*e = FluxEvidence{Problem: "it states no version"}
		}, CheckBad, "cannot read clusters/home/flux-system/install/flux.json (it states no version), so the Flux version " +
			"and settings are unchecked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := healthyFlux()
			test.change(&evidence)
			entries := platformFluxControllers(evidence)
			for _, entry := range entries {
				if entry.Level == test.level && strings.Contains(entry.Message, test.want) {
					return
				}
			}
			t.Fatalf("no %s entry containing %q in %+v", test.level, test.want, entries)
		})
	}
	if names := settings.Controllers(); strings.Join(names, " ") != "source-controller kustomize-controller "+
		"helm-controller notification-controller image-reflector-controller" {
		t.Fatalf("controllers %v", names)
	}
}

// Settings that cannot be decoded, or state no version, are refused: no check may guess a version.
func TestDecodeFluxSettings(t *testing.T) {
	settings, err := DecodeFluxSettings([]byte(`{"version":"2.8.1","args":{"kustomize-controller":["--requeue-dependency=5s"]}}`))
	if err != nil || settings.Version != "2.8.1" || settings.Args["kustomize-controller"][0] != "--requeue-dependency=5s" {
		t.Fatalf("%+v %v", settings, err)
	}
	for _, document := range []string{"", "{", "{}", `{"version":""}`, `[]`} {
		if _, err := DecodeFluxSettings([]byte(document)); err == nil {
			t.Fatalf("%q was accepted", document)
		}
	}
}

func TestNotificationHeartbeat(t *testing.T) {
	now := platformNow
	fresh := now.Add(-5 * time.Minute).Unix()
	for _, test := range []struct {
		name     string
		evidence HeartbeatEvidence
		level    CheckLevel
		want     string
	}{
		{"an active timer and a recent success pass", healthyHeartbeat(), CheckOK,
			"host notification heartbeat timer is active and its service succeeded within 15 minutes"},
		{"an inactive timer is bad", HeartbeatEvidence{Supplied: true, Text: heartbeatOutput("inactive", "success", "0", fresh)},
			CheckBad, "swhurl-notification-heartbeat.timer is not active, so nothing on this host watches the notification " +
				"checker: make host-heartbeat in your own terminal"},
		{"a failed result is bad", HeartbeatEvidence{Supplied: true, Text: heartbeatOutput("active", "exit-code", "0", fresh)},
			CheckBad, "swhurl-notification-heartbeat.service last failed (journalctl -u swhurl-notification-heartbeat.service; " +
				"tail /var/log/swhurl-platform/swhurl-notification-heartbeat.log)"},
		{"a nonzero exit status is bad", HeartbeatEvidence{Supplied: true, Text: heartbeatOutput("active", "success", "1", fresh)},
			CheckBad, "swhurl-notification-heartbeat.service last failed"},
		{"no readable timestamp is bad", HeartbeatEvidence{Supplied: true, Text: heartbeatOutput("active", "success", "0", 0)},
			CheckBad, "swhurl-notification-heartbeat.service has no readable time of its last success " +
				"(systemctl show swhurl-notification-heartbeat.service)"},
		{"an unreadable timestamp is bad", HeartbeatEvidence{Supplied: true,
			Text: "Id=" + heartbeatTimer + "\nActiveState=active\n\nId=" + heartbeatService +
				"\nResult=success\nExecMainStatus=0\nExecMainExitTimestamp=Thu 2026-11-06 12:00:00 GMT\n"},
			CheckBad, "has no readable time of its last success"},
		{"a run older than fifteen minutes is bad", HeartbeatEvidence{Supplied: true,
			Text: heartbeatOutput("active", "success", "0", now.Add(-16*time.Minute).Unix())}, CheckBad,
			"swhurl-notification-heartbeat.service last succeeded 16.0 minutes ago (systemctl status " +
				"swhurl-notification-heartbeat.timer; tail /var/log/swhurl-platform/swhurl-notification-heartbeat.log)"},
		{"clock skew under a minute still passes", HeartbeatEvidence{Supplied: true,
			Text: heartbeatOutput("active", "success", "0", now.Add(30*time.Second).Unix())}, CheckOK,
			"host notification heartbeat timer is active"},
		{"clock skew over a minute is bad", HeartbeatEvidence{Supplied: true,
			Text: heartbeatOutput("active", "success", "0", now.Add(2*time.Minute).Unix())}, CheckBad,
			"last succeeded -2.0 minutes ago"},
		{"no facts at all is bad, never skipped", HeartbeatEvidence{}, CheckBad, "host heartbeat facts not supplied"},
		{"an empty file is bad", HeartbeatEvidence{Supplied: true}, CheckBad,
			"cannot read the host notification heartbeat (systemctl status swhurl-notification-heartbeat.timer)"},
		{"malformed text is bad", HeartbeatEvidence{Supplied: true, Text: "systemctl: command not found\n"}, CheckBad,
			"cannot read the host notification heartbeat"},
		{"output missing the service is bad", HeartbeatEvidence{Supplied: true, Text: "Id=" + heartbeatTimer + "\nActiveState=active\n"},
			CheckBad, "cannot read the host notification heartbeat"},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := platformHeartbeat(test.evidence, now)
			for _, entry := range entries {
				if entry.Level == test.level && strings.Contains(entry.Message, test.want) {
					return
				}
			}
			t.Fatalf("no %s entry containing %q in %+v", test.level, test.want, entries)
		})
	}
}

// An inactive timer and a failed service are both reported; a sound run under an inactive timer reports only the timer.
func TestNotificationHeartbeatReportsEveryFault(t *testing.T) {
	stale := platformNow.Add(-20 * time.Minute).Unix()
	entries := platformHeartbeat(HeartbeatEvidence{Supplied: true, Text: heartbeatOutput("inactive", "exit-code", "1", stale)}, platformNow)
	if len(entries) != 2 || entries[0].Level != CheckBad || entries[1].Level != CheckBad {
		t.Fatalf("%+v", entries)
	}
	entries = platformHeartbeat(HeartbeatEvidence{Supplied: true,
		Text: heartbeatOutput("inactive", "success", "0", platformNow.Add(-time.Minute).Unix())}, platformNow)
	if len(entries) != 1 || !strings.Contains(entries[0].Message, "is not active") {
		t.Fatalf("%+v", entries)
	}
}

// Properties are kept with the unit whose Id introduced them; properties before any Id belong to no unit.
func TestHeartbeatUnitsParsesBlocksByID(t *testing.T) {
	units := heartbeatUnits("Result=success\n\nId=a.timer\nActiveState=active\n\nId=b.service\nResult=success\nExecMainStatus=0\n")
	if len(units) != 2 || units["a.timer"]["ActiveState"] != "active" || units["a.timer"]["Result"] != "" ||
		units["b.service"]["ExecMainStatus"] != "0" {
		t.Fatalf("%+v", units)
	}
	if units := heartbeatUnits(""); len(units) != 0 {
		t.Fatalf("%+v", units)
	}
}
