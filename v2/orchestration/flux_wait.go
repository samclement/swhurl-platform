package orchestration

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// The wait make flux-reconcile ends with. cluster-stack does not wait for the units it defines, so a slow or failing
// unit never holds it (or a new app's unit) back; this gives the operator the wait instead. It polls until every unit
// reports Ready at the revision the GitRepository fetched, reporting each unit as it finishes, and stops at the first
// that fails at that revision with the unit's own message. Suspended units are reported and skipped. It only reads.

// FluxWaitLevel is a line's verdict: a heading names the revision waited for, ok a unit at it, info the source
// moving on, warn a skipped unit, and bad where the wait stopped.
type FluxWaitLevel string

const (
	FluxWaitHeading FluxWaitLevel = "heading"
	FluxWaitOK      FluxWaitLevel = "ok"
	FluxWaitInfo    FluxWaitLevel = "info"
	FluxWaitWarn    FluxWaitLevel = "warn"
	FluxWaitBad     FluxWaitLevel = "bad"
)

// FluxWaitLine is one line of the wait, reported as it happens.
type FluxWaitLine struct {
	Level FluxWaitLevel
	Text  string
}

// FluxWaitInputs is one wait: the cluster it reads, how long it may take, and how time and lines arrive.
type FluxWaitInputs struct {
	Reader   kube.Reader
	Timeout  time.Duration
	Poll     time.Duration
	Now      func() time.Time
	Sleep    func(time.Duration)
	Progress func(FluxWaitLine)
}

// FluxWait waits for every unit at the source's newest revision. It returns false when it stopped at a bad line (a
// source with no artifact, a failed unit or the timeout), and an error when a read failed, so the state is unknown.
func FluxWait(ctx context.Context, inputs FluxWaitInputs) (bool, error) {
	run := &fluxWaitRun{inputs: inputs, seen: map[string]bool{}}
	revision, err := fluxSourceRevision(ctx, inputs.Reader)
	if err != nil {
		return false, err
	}
	if revision == "" {
		run.say(FluxWaitBad, "GitRepository %s has no artifact yet", observe.FluxSource)
		return false, nil
	}
	run.say(FluxWaitHeading, "Flux units at %s", revision)
	run.start = inputs.Now()
	for {
		if revision, err = run.follow(ctx, revision); err != nil {
			return false, err
		}
		units, err := fluxUnitsAt(ctx, inputs.Reader, revision)
		if err != nil {
			return false, err
		}
		passed, waiting := run.report(units)
		if !passed || len(waiting) == 0 {
			return passed, nil
		}
		if inputs.Now().Sub(run.start) >= inputs.Timeout {
			run.say(FluxWaitBad, "timed out after %.0fs waiting for: %s", inputs.Timeout.Seconds(), strings.Join(waiting, ", "))
			return false, nil
		}
		inputs.Sleep(inputs.Poll)
	}
}

// fluxWaitRun is one wait's run: when it started, the units it has already reported at this revision, and its lines.
type fluxWaitRun struct {
	inputs FluxWaitInputs
	start  time.Time
	seen   map[string]bool
}

func (r *fluxWaitRun) say(level FluxWaitLevel, format string, args ...any) {
	r.inputs.Progress(FluxWaitLine{level, fmt.Sprintf(format, args...)})
}

// follow is the revision to wait for now. A commit can land while this waits (the console's deploy bot pushes after
// every tools/ change); units then move on to it and never report the old revision, so every unit must reach the
// newest one, and those already reported are reported again at it.
func (r *fluxWaitRun) follow(ctx context.Context, revision string) (string, error) {
	latest, err := fluxSourceRevision(ctx, r.inputs.Reader)
	if err != nil || latest == "" || latest == revision {
		return revision, err
	}
	r.say(FluxWaitInfo, "source moved to %s; waiting for every unit at that", latest)
	r.seen = map[string]bool{}
	return latest, nil
}

// report reports each unit that has finished since the last poll, once, and returns whether none failed and the
// names of those still waiting.
func (r *fluxWaitRun) report(units []observe.FluxUnitState) (bool, []string) {
	passed, waiting := true, []string{}
	for _, unit := range units {
		if unit.Progress == observe.FluxWaiting {
			waiting = append(waiting, unit.Name)
			continue
		}
		if r.seen[unit.Name] {
			continue
		}
		r.seen[unit.Name] = true
		switch unit.Progress {
		case observe.FluxReady:
			r.say(FluxWaitOK, "%s (%.0fs)", unit.Name, r.inputs.Now().Sub(r.start).Seconds())
		case observe.FluxSuspended:
			r.say(FluxWaitWarn, "%s is suspended; skipped", unit.Name)
		default:
			r.say(FluxWaitBad, "%s failed: %s", unit.Name, cmp.Or(unit.Message, "no message"))
			passed = false
		}
	}
	return passed, waiting
}

// fluxSourceRevision is the revision the platform's GitRepository last fetched, or "" before its first artifact.
func fluxSourceRevision(ctx context.Context, reader kube.Reader) (string, error) {
	ref, err := fluxRefOf("GitRepository", fluxNamespace, observe.FluxSource)
	if err != nil {
		return "", err
	}
	read, err := reader.Read(ctx, ref)
	if err != nil {
		return "", err
	}
	if read.Status != observe.Present {
		return "", absentOrUnavailable("GitRepository", observe.FluxSource, read)
	}
	var source fluxState
	if err := json.Unmarshal(read.Object, &source); err != nil {
		return "", unavailable("GitRepository "+observe.FluxSource, observe.ReasonMalformed)
	}
	return source.Status.Artifact.Revision, nil
}

// fluxUnitsAt is every unit in flux-system and where it stands at revision, by name.
func fluxUnitsAt(ctx context.Context, reader kube.Reader, revision string) ([]observe.FluxUnitState, error) {
	list, err := reader.ListNamespace(ctx, observe.PlatformUnits)
	if err != nil {
		return nil, err
	}
	if list.Status != observe.Present {
		return nil, unavailable("Flux units", list.Reason)
	}
	units := make([]observe.FluxUnitState, 0, len(list.Items))
	for _, item := range list.Items {
		unit, err := observe.FluxUnitAt(item, revision)
		if err != nil {
			return nil, unavailable("Flux units", observe.ReasonMalformed)
		}
		units = append(units, unit)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
	return units, nil
}
