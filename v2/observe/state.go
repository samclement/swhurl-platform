package observe

// The states an instance can be in: the one vocabulary the console shows.
const (
	StateSuspended = "suspended"
	StateFailing   = "failing"
	StateUpdating  = "updating"
	StateStopped   = "stopped"
	StateUnknown   = "unknown"
	StateHealthy   = "healthy"
)

// State is an instance's state and the one fact that decided it: the finding's field and its reason. A healthy
// state names nothing.
type State struct {
	Name, Field, Reason string
}

// StateOf decides an instance's state from one observation. The rules run in this order and the first that
// matches wins:
//
//	suspended  the unit or the release is suspended
//	failing    a container is not ready with a waiting or terminated reason, the unit is absent, or Flux states
//	           a failure
//	updating   the revision is not applied yet, the release is reconciling, the image differs from the record,
//	           or fewer replicas are ready than wanted
//	stopped    the record wants no replicas
//	unknown    any fact not read or not established, named
//	healthy    otherwise
//
// The expected revision and digest are the records', so a commit the unit has not applied yet is updating
// whatever it touched. Routes, certificates and the backup are reported beside the state, never decide it.
func StateOf(report Report) State {
	for _, rule := range []func(Report) (State, bool){suspendedState, failingState, updatingState, stoppedState,
		unknownState} {
		if state, matched := rule(report); matched {
			return state
		}
	}
	return State{Name: StateHealthy}
}

func suspendedState(report Report) (State, bool) {
	for _, finding := range report.Findings {
		if (finding.Field == "unit" || finding.Field == "release") && finding.Reason == ReasonSuspended {
			return State{StateSuspended, finding.Field, ReasonSuspended}, true
		}
	}
	return State{}, false
}

func failingState(report Report) (State, bool) {
	if len(report.Problems) > 0 {
		return State{StateFailing, "containers", ReasonContainers}, true
	}
	for _, finding := range report.Findings {
		switch {
		case finding.Field == "unit" && finding.Outcome == Absent:
			return State{StateFailing, finding.Field, ReasonNotFound}, true
		case (finding.Field == "unit" || finding.Field == "release") && finding.Reason == ReasonFailed:
			return State{StateFailing, finding.Field, ReasonFailed}, true
		}
	}
	return State{}, false
}

// updatingState is the first fact established as not yet what the records want: the unit not reconciled or
// behind the records' commit, the release not settled, another image or none running, or the replicas not
// available. An absent release or workload is not work in progress, so it is left to the unknown rule.
func updatingState(report Report) (State, bool) {
	for _, finding := range report.Findings {
		if outcomeSign(finding.Outcome) == negative && finding.Outcome != Absent {
			return State{StateUpdating, finding.Field, stateReason(finding)}, true
		}
	}
	return State{}, false
}

func stoppedState(report Report) (State, bool) {
	if report.Target.Replicas == 0 {
		return State{StateStopped, "replicas", ReasonNoReplicas}, true
	}
	return State{}, false
}

// unknownState names the first fact that is not established as what the records want: one not read, a read
// that failed, or an object that is absent although nothing is applying it.
func unknownState(report Report) (State, bool) {
	for _, finding := range report.Findings {
		if !IsPositive(finding.Outcome) {
			return State{StateUnknown, finding.Field, stateReason(finding)}, true
		}
	}
	return State{}, false
}

// stateReason is a finding's reason, or its outcome when the outcome is the whole answer.
func stateReason(finding Finding) string {
	if finding.Reason != "" {
		return finding.Reason
	}
	return finding.Outcome
}
