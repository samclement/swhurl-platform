package observe

import (
	"encoding/json"
	"errors"
)

// FluxSource is the GitRepository the platform's units apply from: a unit waits for the revision it fetched. A unit
// with another source is ready once Ready, whatever revision it applied.
const FluxSource = "swhurl-platform"

// FluxProgress is where one Flux unit stands against the revision its source fetched.
type FluxProgress string

const (
	FluxWaiting   FluxProgress = "waiting"
	FluxReady     FluxProgress = "ready"
	FluxFailed    FluxProgress = "failed"
	FluxSuspended FluxProgress = "suspended"
)

// fluxNotYet are the Ready=False reasons that mean "not yet", not "failed at this revision".
var fluxNotYet = map[string]bool{"DependencyNotReady": true, "Progressing": true, "ProgressingWithRetry": true}

// FluxUnitState is one unit's name, its progress at a revision, and its Ready message (the reason a failed one gives).
type FluxUnitState struct {
	Name     string
	Progress FluxProgress
	Message  string
}

type fluxWaitUnit struct {
	Metadata struct {
		Name       string `json:"name"`
		Generation int64  `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Suspend   bool `json:"suspend"`
		SourceRef struct {
			Name string `json:"name"`
		} `json:"sourceRef"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration    int64  `json:"observedGeneration"`
		LastAppliedRevision   string `json:"lastAppliedRevision"`
		LastAttemptedRevision string `json:"lastAttemptedRevision"`
		Conditions            []struct {
			Type, Status, Reason, Message string
		} `json:"conditions"`
	} `json:"status"`
}

// FluxUnitAt is where one Kustomization stands at revision. A suspended unit is suspended whatever its status. A unit
// is ready when Ready at its current generation and, if it applies from FluxSource, at revision; it has failed when
// it is not Ready after attempting revision at its current generation, for a reason other than not yet, and is no
// longer reconciling. Anything else is waiting. A document without a name is malformed, not a unit.
func FluxUnitAt(object []byte, revision string) (FluxUnitState, error) {
	var unit fluxWaitUnit
	if err := json.Unmarshal(object, &unit); err != nil || unit.Metadata.Name == "" {
		return FluxUnitState{}, errors.New("malformed Kustomization")
	}
	ready, reconciling := unit.conditions()
	return FluxUnitState{Name: unit.Metadata.Name, Progress: unit.progress(revision, ready, reconciling),
		Message: ready.Message}, nil
}

// progress is FluxUnitAt's decision, from the unit's spec, its status and its two conditions.
func (u fluxWaitUnit) progress(revision string, ready, reconciling fluxWaitCondition) FluxProgress {
	if u.Spec.Suspend {
		return FluxSuspended
	}
	if u.Status.ObservedGeneration != u.Metadata.Generation {
		return FluxWaiting
	}
	if ready.Status == "True" && u.at(u.Status.LastAppliedRevision, revision) {
		return FluxReady
	}
	finished := !fluxNotYet[ready.Reason] && reconciling.Status != "True"
	if ready.Status == "False" && u.at(u.Status.LastAttemptedRevision, revision) && finished {
		return FluxFailed
	}
	return FluxWaiting
}

// at is whether a revision the unit reports is the one waited for: always, for a unit with another source.
func (u fluxWaitUnit) at(reported, revision string) bool {
	return u.Spec.SourceRef.Name != FluxSource || reported == revision
}

type fluxWaitCondition struct{ Status, Reason, Message string }

// conditions is the unit's first Ready condition and its first Reconciling one; a missing one is the zero value.
func (u fluxWaitUnit) conditions() (ready, reconciling fluxWaitCondition) {
	var seenReady, seenReconciling bool
	for _, c := range u.Status.Conditions {
		if c.Type == "Ready" && !seenReady {
			ready, seenReady = fluxWaitCondition{c.Status, c.Reason, c.Message}, true
		}
		if c.Type == "Reconciling" && !seenReconciling {
			reconciling, seenReconciling = fluxWaitCondition{c.Status, c.Reason, c.Message}, true
		}
	}
	return ready, reconciling
}
