package apps

import (
	"encoding/json"

	deliveryrules "github.com/samclement/swhurl-platform/v2/delivery/rules"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// statusDeployment translates the evidence already validated by Explain for delivery's health rule. It never
// reads more objects, and preserves absence as nil rather than manufacturing a release or workload.
func statusDeployment(target observe.Target, evidence observe.Evidence) (deliveryrules.Deployment, error) {
	unit, err := statusObjectOf(target.Unit, evidence.Objects)
	if err != nil {
		return deliveryrules.Deployment{}, err
	}
	release, err := statusObjectOf(target.Release, evidence.Objects)
	if err != nil {
		return deliveryrules.Deployment{}, err
	}
	workload, err := statusObjectOf(target.Workload, evidence.Objects)
	if err != nil {
		return deliveryrules.Deployment{}, err
	}
	pods, err := statusPodsOf(target, evidence.Pods)
	if err != nil {
		return deliveryrules.Deployment{}, err
	}
	return deliveryrules.Deployment{Namespace: target.Namespace, Name: target.Release.Name,
		Unit: unit, Release: release, Workloads: []map[string]any{workload}, Pods: pods}, nil
}

func statusObjectOf(ref observe.Ref, reads []observe.Read) (map[string]any, error) {
	for _, read := range reads {
		if read.Ref == ref && read.Status == observe.Present {
			var object map[string]any
			err := json.Unmarshal(read.Object, &object)
			return object, err
		}
	}
	return nil, nil
}

func statusPodsOf(target observe.Target, lists []observe.List) ([]map[string]any, error) {
	var pods []map[string]any
	for _, list := range lists {
		if list.Ref != target.Pods || list.Selector != target.Selector || list.Status != observe.Present {
			continue
		}
		for _, item := range list.Items {
			var pod map[string]any
			if err := json.Unmarshal(item, &pod); err != nil {
				return nil, err
			}
			pods = append(pods, pod)
		}
	}
	return pods, nil
}

// statusState is the single instance state: delivery owns deployment health and revision currentness. Apps
// preserves failed/unattempted reads as unknown and distinguishes active progress from a stalled request. Routes
// and backups remain separate. A healthy rollback is not the requested version and never becomes Updating just
// because its unit has not applied the failed upgrade.
func statusState(report observe.Report, deployment deliveryrules.Deployment) observe.State {
	if state, unknown := unreadStatus(report.Live); unknown {
		return state
	}
	verdict := deliveryrules.DeploymentHealthOf(deployment)
	state := observe.State{Field: "deployment", Reason: verdict.Detail}
	switch verdict.Health {
	case deliveryrules.DeploymentUnjudged:
		state.Name = observe.StateUnknown
	case deliveryrules.DeploymentStopped:
		state.Name = observe.StateStopped
	case deliveryrules.DeploymentSuspended:
		state.Name = observe.StateSuspended
	case deliveryrules.DeploymentUnhealthy, deliveryrules.DeploymentStalled:
		state.Name = observe.StateStalled
		if verdict.Health == deliveryrules.DeploymentUnhealthy {
			state.Name = observe.StateFailing
		}
		if releaseProgressing(report.Live.Release) {
			state.Name = observe.StateUpdating
		}
	case deliveryrules.DeploymentHealthy:
		if verdict.RolledBack {
			state.Name = observe.StateRolledBack
			return state
		}
		return revisionState(report)
	}
	return state
}

// unreadStatus keeps an unavailable API fact distinct from an absent object or a known unhealthy workload.
func unreadStatus(live observe.LiveFacts) (observe.State, bool) {
	for _, fact := range []struct{ field, status, reason string }{
		{"unit", live.Unit.Status, live.Unit.Reason},
		{"release", live.Release.Status, live.Release.Reason},
		{"workload", live.Workload.Status, live.Workload.Reason},
		{"image", live.Running.Status, live.Running.Reason},
	} {
		if fact.status == observe.Unavailable || fact.status == observe.NotRead {
			reason := fact.reason
			if reason == "" {
				reason = fact.status
			}
			return observe.State{Name: observe.StateUnknown, Field: fact.field, Reason: reason}, true
		}
	}

	return observe.State{}, false
}

func releaseProgressing(release observe.FluxFacts) bool {
	for _, condition := range release.Conditions {
		if condition.Type == "Reconciling" && condition.Status == "True" && condition.Reason == "Progressing" {
			return true
		}
	}
	return false
}

// revisionState applies only to a healthy requested deployment; revision proof never waives unhealthy facts.
func revisionState(report observe.Report) observe.State {
	if report.Currentness.State == "applied" || report.Currentness.State == "unaffected" {
		return observe.State{Name: observe.StateHealthy}
	}
	state := observe.State{Name: observe.StateUnknown, Field: "unit.revision", Reason: report.Currentness.Reason}
	for _, finding := range report.Findings {
		if finding.Field == "unit.revision" && (finding.Outcome == observe.Different || finding.Outcome == observe.NotApplied) {
			state.Name = observe.StateUpdating
		}
	}
	return state
}
