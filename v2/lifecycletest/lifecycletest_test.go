package lifecycletest

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Each verdict is Python's pair of words: ok when the check passed, bad otherwise.
func TestVerdicts(t *testing.T) {
	for _, c := range []struct {
		pass, fail Line
		ok, bad    string
	}{
		{Wrote("Fri Nov 13\n", "pv-1"), Wrote("", "pv-1"), "workload wrote data to pv-1", "no marker written"},
		{UnitSuspended(true), UnitSuspended(false), "unit suspended", "unit not suspended"},
		{RunningWhileSuspended(true), RunningWhileSuspended(false), "workload still running while suspended", "workload stopped while suspended"},
		{ResumedReady(true), ResumedReady(false), "unit resumed and Ready", "unit not Ready after resume"},
		{Pruned(true), Pruned(false), "workload pruned", "workload not pruned"},
		{NamespaceKept(true), NamespaceKept(false), "prune-protected namespace kept", "namespace deleted"},
		{ClaimKept("Bound"), ClaimKept(""), "prune-protected claim kept", "claim deleted"},
		{DataIntact("a\n", "a\n"), DataIntact("a\n", ""), "data intact after uninstall", "data changed or missing after uninstall"},
		{DestroyRefused(true), DestroyRefused(false), "destroy-data refuses without CONFIRM", "destroy-data ran without CONFIRM"},
		{ClaimUntouched(true), ClaimUntouched(false), "claim untouched by refused destroy", "claim removed by refused destroy"},
		{ClaimDestroyed(false), ClaimDestroyed(true), "claim destroyed", "claim still exists"},
		{VolumeDestroyed("pv-1", false), VolumeDestroyed("pv-1", true), "PV pv-1 and its data destroyed", "PV pv-1 still exists"},
		{OrphanKept(true), OrphanKept(false), "workload kept running after its Orphan unit was deleted", "Orphan unit deletion removed the workload"},
	} {
		if c.pass != (Line{OK, c.ok}) || c.fail != (Line{Bad, c.bad}) {
			t.Errorf("%+v %+v", c.pass, c.fail)
		}
	}
	// A claim that is there but not Bound is not kept, and a marker that changed is not intact.
	if ClaimKept("Pending").Level != Bad || DataIntact("a\n", "b\n").Level != Bad {
		t.Fatal("a Pending claim or a changed marker passed")
	}
}

// The lines read as Python printed them: a section and the passed line each after an empty line, the passed line
// only when nothing failed, and the cleanup's line last.
func TestTextAndConclusion(t *testing.T) {
	steps := []Line{{Section, InstallStep}, Wrote("x", "pv-1")}
	lines := append(append(append([]Line{}, steps...), Conclusion(steps)...), CleanedUp())
	want := "\n== Install disposable app ==\n[OK] workload wrote data to pv-1\n\nLifecycle test passed.\n[INFO] Cleaned up lifecycle-test test resources"
	if text := Text(lines); text != want || !Passes(lines) {
		t.Fatalf("%q", text)
	}
	failed := append(steps, Failed("Kustomization lifecycle-test not Ready after 6m0s"))
	if Conclusion(failed) != nil || Passes(failed) {
		t.Fatal("a failed run concluded")
	}
	if Format(failed[2]) != "[BAD] Kustomization lifecycle-test not Ready after 6m0s" {
		t.Fatalf("%q", Format(failed[2]))
	}
	if Leftovers().Error() != "lifecycle-test namespace or lifecycle-test Kustomizations already exist; clean up first" {
		t.Fatal(Leftovers())
	}
}

// The unit is Python's unit(), field for field, and the reader pod is kubectl run's pod with Python's overrides.
func TestDocuments(t *testing.T) {
	var unit, reader map[string]any
	if err := json.Unmarshal(UnitDocument(OrphanUnit, Orphan), &unit); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "lifecycle-test-orphan", "namespace": "flux-system"},
		"spec": map[string]any{"interval": "10m", "sourceRef": map[string]any{"kind": "GitRepository", "name": "swhurl-platform"},
			"path": "./tests/fixtures/lifecycle-app", "prune": true, "deletionPolicy": "Orphan", "wait": true, "timeout": "5m"}}
	if !reflect.DeepEqual(unit, want) {
		t.Fatalf("%v", unit)
	}
	if err := json.Unmarshal(ReaderDocument(), &reader); err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "reader", "namespace": "lifecycle-test"},
		"spec": map[string]any{"automountServiceAccountToken": false, "restartPolicy": "Never",
			"volumes": []any{map[string]any{"name": "d", "persistentVolumeClaim": map[string]any{"claimName": "data"}}},
			"containers": []any{map[string]any{"name": "reader", "image": "busybox:1.37", "command": []any{"cat", "/data/marker"},
				"volumeMounts": []any{map[string]any{"name": "d", "mountPath": "/data"}}}}}}
	if !reflect.DeepEqual(reader, want) {
		t.Fatalf("%v", reader)
	}
}

// What each read object says: Ready only when the Ready condition is True, the claim's volume and phase, the
// namespace's label, a volume's claim namespace, and the first Running pod not being deleted.
func TestFacts(t *testing.T) {
	ready, err := Ready([]byte(`{"status":{"conditions":[{"type":"Reconciling","status":"True"},{"type":"Ready","status":"True"}]}}`))
	if !ready || err != nil {
		t.Fatal("a Ready unit is not ready")
	}
	if ready, _ := Ready([]byte(`{"status":{"conditions":[{"type":"Ready","status":"False"}]}}`)); ready {
		t.Fatal("a failed unit is ready")
	}
	if _, err := Ready([]byte(`[]`)); err == nil {
		t.Fatal("a list was read as a unit")
	}
	if suspended, _ := Suspended([]byte(`{"spec":{"suspend":true}}`)); !suspended {
		t.Fatal("not suspended")
	}
	if available, _ := Available([]byte(`{"status":{"availableReplicas":1}}`)); !available {
		t.Fatal("not available")
	}
	if available, _ := Available([]byte(`{"status":{}}`)); available {
		t.Fatal("no replica is available")
	}
	if volume, _ := VolumeOf([]byte(`{"spec":{"volumeName":"pv-1"}}`)); volume != "pv-1" {
		t.Fatal(volume)
	}
	if phase, _ := PhaseOf([]byte(`{"status":{"phase":"Bound"}}`)); phase != "Bound" {
		t.Fatal(phase)
	}
	if !Labelled([]byte(`{"metadata":{"labels":{"platform.swhurl.com/lifecycle-test":"true"}}}`)) ||
		Labelled([]byte(`{"metadata":{"labels":{"platform.swhurl.com/lifecycle-test":"false"}}}`)) || Labelled([]byte(`{}`)) {
		t.Fatal("the label guard")
	}
	if !Claimed([]byte(`{"spec":{"claimRef":{"namespace":"lifecycle-test"}}}`)) ||
		Claimed([]byte(`{"spec":{"claimRef":{"namespace":"apps"}}}`)) || Claimed([]byte(`{"spec":{}}`)) {
		t.Fatal("the volume guard")
	}
	pods := [][]byte{
		[]byte(`{"metadata":{"name":"writer-a"},"status":{"phase":"Pending"}}`),
		[]byte(`{"metadata":{"name":"writer-b","deletionTimestamp":"2026-11-13T09:00:00Z"},"status":{"phase":"Running"}}`),
		[]byte(`{"metadata":{"name":"writer-c"},"status":{"phase":"Running"}}`),
	}
	if pod, err := RunningPod(pods); pod != "writer-c" || err != nil {
		t.Fatalf("%q %v", pod, err)
	}
	if _, err := RunningPod(pods[:2]); err == nil || err.Error() != "no Running pod of deploy/writer in lifecycle-test" {
		t.Fatalf("%v", err)
	}
}
