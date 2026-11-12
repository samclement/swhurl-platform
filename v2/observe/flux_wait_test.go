package observe

import (
	"encoding/json"
	"testing"
)

const waitRevision = "main@sha1:new"

// waitUnit is one Kustomization as the API server returns it; each option changes one fact of a ready unit.
func waitUnit(name string, change func(spec, status map[string]any, ready map[string]any)) []byte {
	ready := map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded", "message": ""}
	spec := map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": FluxSource}, "suspend": false}
	status := map[string]any{"observedGeneration": 1, "lastAppliedRevision": waitRevision,
		"lastAttemptedRevision": waitRevision}
	if change != nil {
		change(spec, status, ready)
	}
	conditions := []any{ready}
	if status["reconciling"] == true {
		delete(status, "reconciling")
		conditions = append(conditions, map[string]any{"type": "Reconciling", "status": "True"})
	}
	status["conditions"] = conditions
	encoded, err := json.Marshal(map[string]any{"metadata": map[string]any{"name": name, "generation": 1},
		"spec": spec, "status": status})
	if err != nil {
		panic(err)
	}
	return encoded
}

func TestFluxUnitAtStatesWhereAUnitStands(t *testing.T) {
	failedAtRevision := func(_, status, ready map[string]any) {
		ready["status"], ready["reason"], ready["message"] = "False", "HealthCheckFailed", "timeout"
		status["lastAppliedRevision"] = "main@sha1:old"
	}
	cases := []struct {
		name   string
		unit   []byte
		expect FluxProgress
	}{
		{"ready at the revision", waitUnit("a", nil), FluxReady},
		{"ready at the previous revision", waitUnit("a", func(_, status, _ map[string]any) {
			status["lastAppliedRevision"] = "main@sha1:old"
		}), FluxWaiting},
		{"suspended, whatever its status", waitUnit("a", func(spec, _, ready map[string]any) {
			spec["suspend"], ready["status"] = true, "False"
		}), FluxSuspended},
		{"failed at the revision", waitUnit("a", failedAtRevision), FluxFailed},
		{"a dependency not ready is not yet", waitUnit("a", func(spec, status, ready map[string]any) {
			failedAtRevision(spec, status, ready)
			ready["reason"] = "DependencyNotReady"
		}), FluxWaiting},
		{"still reconciling is not yet", waitUnit("a", func(spec, status, ready map[string]any) {
			failedAtRevision(spec, status, ready)
			status["reconciling"] = true
		}), FluxWaiting},
		{"failed at an older revision is not yet", waitUnit("a", func(spec, status, ready map[string]any) {
			failedAtRevision(spec, status, ready)
			status["lastAttemptedRevision"] = "main@sha1:old"
		}), FluxWaiting},
		{"a generation not yet observed", waitUnit("a", func(_, status, _ map[string]any) {
			status["observedGeneration"] = 0
		}), FluxWaiting},
		{"another source is ready at any revision", waitUnit("a", func(spec, status, _ map[string]any) {
			spec["sourceRef"] = map[string]any{"kind": "OCIRepository", "name": "other"}
			status["lastAppliedRevision"] = "v1@sha256:x"
		}), FluxReady},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, err := FluxUnitAt(c.unit, waitRevision)
			if err != nil || state.Progress != c.expect || state.Name != "a" {
				t.Fatalf("FluxUnitAt = %+v, %v; want %s", state, err, c.expect)
			}
		})
	}
	state, _ := FluxUnitAt(waitUnit("a", failedAtRevision), waitRevision)
	if state.Message != "timeout" {
		t.Fatalf("a failed unit's message is its Ready message, got %q", state.Message)
	}
}

func TestFluxUnitAtRefusesAMalformedDocument(t *testing.T) {
	for _, document := range []string{`{`, `{"metadata":{}}`, `[]`} {
		if _, err := FluxUnitAt([]byte(document), waitRevision); err == nil {
			t.Fatalf("FluxUnitAt(%s) accepted it", document)
		}
	}
}
