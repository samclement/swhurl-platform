package observe

// The two platform checks whose evidence comes from the host the operator runs on, not from the cluster alone: the
// Flux controllers, judged against the settings the checkout installs them with, and the host heartbeat timer, whose
// systemctl output the caller supplies. Deciding is pure here; reading the file, running systemctl and the clock are
// the caller's.

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CheckoutFluxSettings is the one file that states the Flux release and the controller arguments: make flux-install
// installs from it and this check judges the cluster against it, so neither can drift from the other.
const CheckoutFluxSettings = "clusters/home/flux-system/install/flux.json"

// PlatformFluxController is where a Flux controller's Deployment is read; the settings name which controllers.
var PlatformFluxController = Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "flux-system"}

// fluxDefaultComponents are flux install's own controllers; the settings name the extra ones this cluster runs.
var fluxDefaultComponents = []string{"source-controller", "kustomize-controller", "helm-controller", "notification-controller"}

// FluxSettings is what flux.json states.
type FluxSettings struct {
	Version           string              `json:"version"`
	ExtraComponents   []string            `json:"extraComponents"`
	RemovedComponents []string            `json:"removedComponents"`
	Args              map[string][]string `json:"args"`
}

// DecodeFluxSettings reads flux.json. Settings that state no version are refused: the check never guesses one.
func DecodeFluxSettings(data []byte) (FluxSettings, error) {
	var settings FluxSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return FluxSettings{}, err
	}
	if settings.Version == "" {
		return FluxSettings{}, errors.New("it states no version")
	}
	return settings, nil
}

// Controllers are the Deployments flux-system must run, in install order: flux install's own and the extra ones.
func (s FluxSettings) Controllers() []string {
	return append(slices.Clone(fluxDefaultComponents), s.ExtraComponents...)
}

// FluxEvidence is the checkout's settings — or why they could not be read — and each controller's live Deployment.
type FluxEvidence struct {
	Settings    FluxSettings
	Problem     string
	Deployments map[string]Read
}

func platformFluxControllers(e FluxEvidence) []PlatformEntry {
	if e.Problem != "" {
		return []PlatformEntry{bad(fmt.Sprintf("cannot read %s (%s), so the Flux version and settings are unchecked",
			CheckoutFluxSettings, e.Problem))}
	}
	entries := []PlatformEntry{}
	for _, name := range e.Settings.Controllers() {
		entries = append(entries, fluxController(name, e.Settings.Version, e.Settings.Args[name], e.Deployments[name]))
	}
	return entries
}

// fluxController judges one controller: a plain `flux install` leaves it without the arguments Git states, and an
// upgrade that did not happen leaves it at another version. Both warn; only a Deployment that cannot be read is bad.
func fluxController(name, version string, args []string, read Read) PlatformEntry {
	switch read.Status {
	case Present:
	case Absent:
		return warn(name + " is not installed (run: make flux-install)")
	default:
		return bad(fmt.Sprintf("cannot read flux-system/%s (kubectl -n flux-system get deploy %s)", name, name))
	}
	live, running, readable := fluxDeploymentFacts(read.Object)
	if !readable {
		return bad(fmt.Sprintf("cannot read flux-system/%s (kubectl -n flux-system get deploy %s)", name, name))
	}
	missing := 0
	for _, arg := range args {
		if !slices.Contains(running, arg) {
			missing++
		}
	}
	if missing > 0 || live != "v"+version {
		return warn(fluxControllerDrift(name, version, live, args, missing))
	}
	return ok(name + " " + live + settingsSuffix(args, " with the settings in Git"))
}

// fluxControllerDrift says what the controller is and what Git states it should be.
func fluxControllerDrift(name, version, live string, args []string, missing int) string {
	if live == "" {
		live = "unknown"
	}
	expected := "v" + version + settingsSuffix(args, " with "+strings.Join(args, ", "))
	found := fmt.Sprintf(" with %d/%d Git settings", len(args)-missing, len(args))
	return name + " is " + live + settingsSuffix(args, found) + "; expected " + expected + " (run: make flux-install)"
}

// settingsSuffix is the suffix only a controller with required arguments carries.
func settingsSuffix(args []string, suffix string) string {
	if len(args) == 0 {
		return ""
	}
	return suffix
}

// fluxDeploymentFacts are the version label and the arguments every container runs with; false when the object is
// not a Deployment this check can read.
func fluxDeploymentFacts(object []byte) (string, []string, bool) {
	var deployment struct {
		Metadata *struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec *struct {
			Template *struct {
				Spec *struct {
					Containers *[]struct {
						Args []string `json:"args"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if json.Unmarshal(object, &deployment) != nil || deployment.Metadata == nil || deployment.Spec == nil ||
		deployment.Spec.Template == nil || deployment.Spec.Template.Spec == nil || deployment.Spec.Template.Spec.Containers == nil {
		return "", nil, false
	}
	running := []string{}
	for _, container := range *deployment.Spec.Template.Spec.Containers {
		running = append(running, container.Args...)
	}
	return deployment.Metadata.Labels["app.kubernetes.io/version"], running, true
}

// The host units that keep the notification checker watched, how recently the service must have succeeded, and how
// far ahead of the caller's clock a success may be stamped before it is a fault rather than clock skew.
const (
	heartbeatTimer   = "swhurl-notification-heartbeat.timer"
	heartbeatService = "swhurl-notification-heartbeat.service"
	heartbeatFresh   = 15 * time.Minute
	heartbeatSkew    = -time.Minute
	heartbeatLog     = "tail /var/log/swhurl-platform/swhurl-notification-heartbeat.log"
)

// HeartbeatEvidence is the `systemctl show --timestamp=unix` output for the heartbeat timer and service that the
// caller supplied. Supplied is false when the run named no such facts, which is bad, never a skipped check.
type HeartbeatEvidence struct {
	Supplied bool
	Text     string
}

func platformHeartbeat(e HeartbeatEvidence, now time.Time) []PlatformEntry {
	if !e.Supplied {
		return []PlatformEntry{bad("host heartbeat facts not supplied")}
	}
	units := heartbeatUnits(e.Text)
	timer, timerRead := units[heartbeatTimer]
	service, serviceRead := units[heartbeatService]
	if !timerRead || !serviceRead {
		return []PlatformEntry{bad("cannot read the host notification heartbeat (systemctl status " + heartbeatTimer + ")")}
	}
	entries, active := []PlatformEntry{}, timer["ActiveState"] == "active"
	if !active {
		entries = append(entries, bad(heartbeatTimer+" is not active, so nothing on this host watches the "+
			"notification checker: make host-heartbeat in your own terminal"))
	}
	return append(entries, heartbeatRun(service, active, now)...)
}

// heartbeatRun judges the service's last run: whether it succeeded, and how long ago.
func heartbeatRun(service map[string]string, active bool, now time.Time) []PlatformEntry {
	if service["Result"] != "success" || service["ExecMainStatus"] != "0" {
		return []PlatformEntry{bad(fmt.Sprintf("%s last failed (journalctl -u %s; %s)", heartbeatService, heartbeatService, heartbeatLog))}
	}
	taken, readable := heartbeatTime(service["ExecMainExitTimestamp"])
	if !readable {
		return []PlatformEntry{bad(heartbeatService + " has no readable time of its last success (systemctl show " + heartbeatService + ")")}
	}
	switch age := now.Sub(taken); {
	case age < heartbeatSkew || age > heartbeatFresh:
		return []PlatformEntry{bad(fmt.Sprintf("%s last succeeded %.1f minutes ago (systemctl status %s; %s)",
			heartbeatService, age.Minutes(), heartbeatTimer, heartbeatLog))}
	case !active:
		return nil // the inactive timer is already reported; the run itself is sound
	}
	return []PlatformEntry{ok("host notification heartbeat timer is active and its service succeeded within 15 minutes")}
}

// heartbeatUnits are the properties systemctl printed, by the Id of the unit they belong to: each block starts with
// its Id, and a line that states no property ends the block. Properties before any Id belong to no unit.
func heartbeatUnits(text string) map[string]map[string]string {
	units, current := map[string]map[string]string{}, map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		key, value, stated := strings.Cut(strings.TrimRight(line, "\r"), "=")
		switch {
		case !stated:
			current = map[string]string{}
		case key == "Id":
			current = map[string]string{}
			units[value] = current
		default:
			current[key] = value
		}
	}
	return units
}

// heartbeatTime is the moment `--timestamp=unix` states (@<seconds>); false when the service states none or states
// one this check cannot read, which is reported rather than taken for a fresh run.
func heartbeatTime(stated string) (time.Time, bool) {
	rest, unix := strings.CutPrefix(strings.TrimSpace(stated), "@")
	seconds, err := strconv.ParseInt(rest, 10, 64)
	if !unix || err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}
