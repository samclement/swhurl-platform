package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// fluxCluster answers a unit action's reads and records its patches, as a Flux controller and the API server do.
// The unit's own document states its spec; every other read states a generation and a status, and reports the
// annotating request handled once the cluster has seen that object read handled times before.
type fluxCluster struct {
	staleGeneration bool   // Ready and the handled token may still describe a previous spec
	unhandled       int    // token-bearing reads that still report the request unhandled
	spec            string // the unit's spec, as the cluster holds it
	ready           string
	message         string
	absent          bool
	readErr         error
	writeErr        error

	reads   []observe.Ref
	writes  []observe.Ref
	patches [][]byte
	since   map[observe.Ref]int
	tokens  map[observe.Ref]string
}

func newFluxCluster() *fluxCluster {
	return &fluxCluster{spec: `{"suspend": false, "sourceRef": {"kind": "GitRepository", "name": "swhurl-platform"}}`,
		ready: "True", since: map[observe.Ref]int{}, tokens: map[observe.Ref]string{}}
}

func (f *fluxCluster) Read(_ context.Context, ref observe.Ref) (observe.Read, error) {
	f.reads = append(f.reads, ref)
	if f.readErr != nil {
		return observe.Read{}, f.readErr
	}
	if f.absent {
		return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
	}
	return observe.Read{Ref: ref, Status: observe.Present, Object: f.document(ref)}, nil
}

// document is one object as the cluster holds it: the unit's spec, and a status that reports this object's request
// handled once it has been read unhandled times since the annotation.
func (f *fluxCluster) document(ref observe.Ref) []byte {
	status := map[string]any{"observedGeneration": 3, "lastAppliedRevision": revision, "lastAttemptedRevision": "1.2.3",
		"artifact":   map[string]any{"revision": revision},
		"conditions": []any{map[string]any{"type": "Ready", "status": "Unknown", "message": ""}}}
	if token := f.tokens[ref]; token != "" {
		f.since[ref]++
		if f.since[ref] > f.unhandled {
			status["lastHandledReconcileAt"] = token
			status["conditions"] = []any{map[string]any{"type": "Ready", "status": f.ready, "message": f.message}}
		}
	}
	if f.staleGeneration {
		status["observedGeneration"] = 2
	}
	document := map[string]any{"metadata": map[string]any{"generation": 3}, "status": status}
	if ref.Kind == "Kustomization" {
		document["spec"] = json.RawMessage(f.spec)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err) // a test's own document
	}
	return encoded
}

// The rest of the read and write ports are not a unit action's: a call that reaches one is a wiring mistake.
var errNotAUnitAction = errors.New("a unit action reads and patches one object at a time")

func (f *fluxCluster) List(context.Context, observe.Ref, string) (observe.List, error) {
	return observe.List{}, errNotAUnitAction
}

func (f *fluxCluster) ListAllNamespaces(context.Context, observe.Ref, string) (observe.List, error) {
	return observe.List{}, errNotAUnitAction
}

func (f *fluxCluster) ListNamespace(context.Context, observe.Ref) (observe.List, error) {
	return observe.List{}, errNotAUnitAction
}

func (f *fluxCluster) Create(context.Context, observe.Ref, []byte) error { return errNotAUnitAction }

func (f *fluxCluster) Delete(context.Context, observe.Ref) error { return errNotAUnitAction }

func (f *fluxCluster) Logs(context.Context, observe.Ref, int64) (string, error) {
	return "", errNotAUnitAction
}

func (f *fluxCluster) Patch(_ context.Context, ref observe.Ref, patch []byte) error {
	f.writes = append(f.writes, ref)
	f.patches = append(f.patches, patch)
	if f.writeErr != nil {
		return f.writeErr
	}
	var change struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(patch, &change); err != nil {
		return err
	}
	if token := change.Metadata.Annotations[fluxRequestedAt]; token != "" {
		f.tokens[ref] = token
	}
	return nil
}

// fluxResult is one action's run: the lines it reported, the pauses it took, and its error.
type fluxResult struct {
	lines  []string
	sleeps []time.Duration
	err    error
}

// runFlux performs one action against a scripted cluster, with a clock its own pauses advance.
func runFlux(t *testing.T, cluster *fluxCluster, unit string, action FluxAction) fluxResult {
	t.Helper()
	return runFluxOn(t, cluster, unit, nil, action)
}

// runFluxOn is runFlux on a unit or, when release is not nil, that HelmRelease.
func runFluxOn(t *testing.T, cluster *fluxCluster, unit string, release *FluxRelease, action FluxAction) fluxResult {
	t.Helper()
	clock := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	found := fluxResult{}
	err := Flux(context.Background(), FluxInputs{Action: action, Unit: unit, Release: release,
		Cluster: Cluster{Reader: cluster, Writer: cluster},
		Now:     func() time.Time { return clock },
		Sleep:   func(pause time.Duration) { clock = clock.Add(pause); found.sleeps = append(found.sleeps, pause) },
		Progress: func(line string) {
			found.lines = append(found.lines, line)
		}})
	found.err = err
	return found
}

func unitRef() observe.Ref {
	ref, _ := fluxRefOf("Kustomization", fluxNamespace, "gw")
	return ref
}

func sourceRef() observe.Ref {
	ref, _ := fluxRefOf("GitRepository", fluxNamespace, "swhurl-platform")
	return ref
}

// A reconcile requests the source first, then the unit, and reports what each fetched or applied.
func TestFluxReconcileRequestsTheSourceThenTheUnit(t *testing.T) {
	cluster := newFluxCluster()
	cluster.unhandled = 1
	found := runFlux(t, cluster, "gw", FluxReconcile)
	if found.err != nil {
		t.Fatal(found.err)
	}
	if len(cluster.writes) != 2 || cluster.writes[0] != sourceRef() || cluster.writes[1] != unitRef() {
		t.Fatalf("wrote %v, want the source then the unit", cluster.writes)
	}
	for _, patch := range cluster.patches {
		var change map[string]map[string]map[string]string
		if err := json.Unmarshal(patch, &change); err != nil {
			t.Fatalf("patch %s: %v", patch, err)
		}
		token := change["metadata"]["annotations"][fluxRequestedAt]
		if _, err := time.Parse(time.RFC3339Nano, token); err != nil {
			t.Fatalf("the annotation %q is not a time: %v", token, err)
		}
	}
	want := []string{
		"► annotating GitRepository swhurl-platform in flux-system namespace",
		"◎ waiting for GitRepository reconciliation",
		"✔ fetched revision " + revision,
		"► annotating Kustomization gw in flux-system namespace",
		"◎ waiting for Kustomization reconciliation",
		"✔ applied revision " + revision,
	}
	if strings.Join(found.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines %q, want %q", found.lines, want)
	}
	if len(found.sleeps) != 2 || found.sleeps[0] != fluxPoll || found.sleeps[1] != fluxPoll {
		t.Fatalf("pauses %v, want one two-second poll for each object", found.sleeps)
	}
}

// A handled request that is not Ready has failed, and the controller's own message is the reason.
func TestFluxHandledButNotReadyFailsTheAction(t *testing.T) {
	cluster := newFluxCluster()
	cluster.ready, cluster.message = "False", "health check failed after 5m"
	found := runFlux(t, cluster, "gw", FluxResume)
	if found.err == nil || found.err.Error() != "Kustomization gw reconciliation failed: health check failed after 5m" {
		t.Fatalf("error %v", found.err)
	}
	if len(cluster.writes) != 2 {
		t.Fatalf("wrote %v: resume patches the unit and then requests it", cluster.writes)
	}
}

// The console's three sentences for what it refuses: a root unit, a suspended unit, and a source it cannot request.
func TestFluxRefusesBeforeWritingAnything(t *testing.T) {
	for _, tt := range []struct {
		name    string
		unit    string
		spec    string
		action  FluxAction
		message string
	}{
		{"a root unit", "cluster-stack", "", FluxReconcile,
			"cluster-stack is applied by make flux-bootstrap, not by Flux"},
		{"a suspended unit", "gw", `{"suspend": true}`, FluxReconcile,
			"gw is suspended; resume it instead (resume also reconciles)"},
		{"a source it cannot request", "gw", `{"suspend": false, "sourceRef": {"kind": "Chart", "name": "x"}}`, FluxReconcile,
			`gw has an unsupported source kind 'Chart'`},
		{"a unit with no source", "gw", `{"suspend": false}`, FluxReconcile,
			"gw has an unsupported source kind None"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newFluxCluster()
			if tt.spec != "" {
				cluster.spec = tt.spec
			}
			found := runFlux(t, cluster, tt.unit, tt.action)
			if found.err == nil || found.err.Error() != tt.message {
				t.Fatalf("error %v, want %q", found.err, tt.message)
			}
			if len(cluster.writes) != 0 {
				t.Fatalf("a refusal wrote %v", cluster.writes)
			}
		})
	}
}

// Suspend only patches; resume patches and then waits, because a resumed unit has to be applied.
func TestFluxSuspendPatchesAndResumeAlsoWaits(t *testing.T) {
	cluster := newFluxCluster()
	suspended := runFlux(t, cluster, "gw", FluxSuspend)
	if suspended.err != nil {
		t.Fatal(suspended.err)
	}
	if strings.Join(suspended.lines, "\n") != "► suspending Kustomization gw\n✔ Kustomization gw suspended" {
		t.Fatalf("lines %q", suspended.lines)
	}
	if len(cluster.writes) != 1 || string(cluster.patches[0]) != `{"spec":{"suspend":true}}` {
		t.Fatalf("wrote %v %s", cluster.writes, cluster.patches)
	}
	resumed := runFlux(t, cluster, "gw", FluxResume)
	if resumed.err != nil {
		t.Fatal(resumed.err)
	}
	if len(cluster.writes) != 3 || string(cluster.patches[1]) != `{"spec":{"suspend":false}}` {
		t.Fatalf("wrote %v %s", cluster.writes[1:], cluster.patches[1])
	}
	if resumed.lines[0] != "► resuming Kustomization gw" || resumed.lines[len(resumed.lines)-1] != "✔ applied revision "+revision {
		t.Fatalf("lines %q", resumed.lines)
	}
}

// A unit that is not there, and a read that failed, say which they were.
func TestFluxMissingUnitsAndUnreadableClusters(t *testing.T) {
	absent := newFluxCluster()
	absent.absent = true
	found := runFlux(t, absent, "gw", FluxReconcile)
	if found.err == nil || found.err.Error() != "no Kustomization named 'gw'" {
		t.Fatalf("absent: %v", found.err)
	}
	broken := newFluxCluster()
	broken.readErr = errors.New("failed: read: status")
	if found := runFlux(t, broken, "gw", FluxReconcile); found.err == nil || found.err.Error() != broken.readErr.Error() {
		t.Fatalf("unreadable: %v", found.err)
	}
	refused := newFluxCluster()
	refused.writeErr = &Refusal{"resource", "forbidden"}
	if found := runFlux(t, refused, "gw", FluxSuspend); found.err == nil || found.err.Error() != "refused: resource: forbidden" {
		t.Fatalf("a refused patch: %v", found.err)
	}
}

// The console waits ten minutes, as the flux command's --timeout=10m does, and says the unit may still finish.
func TestFluxTimesOutAfterTenMinutes(t *testing.T) {
	cluster := newFluxCluster()
	cluster.unhandled = 10_000
	found := runFlux(t, cluster, "gw", FluxReconcile)
	if found.err == nil || found.err.Error() !=
		"timed out after 10m waiting for GitRepository swhurl-platform; it may still finish (check the unit page)" {
		t.Fatalf("error %v", found.err)
	}
	if len(found.sleeps) != 300 {
		t.Fatalf("%d pauses, want 300 two-second polls in ten minutes", len(found.sleeps))
	}
}

// An action that is none of the three is refused rather than guessed.
func TestFluxRefusesAnUnknownAction(t *testing.T) {
	found := runFlux(t, newFluxCluster(), "gw", FluxAction("delete"))
	if found.err == nil || found.err.Error() != "refused: unit.action: not_reconcile_suspend_or_resume" {
		t.Fatalf("error %v", found.err)
	}
}

// An ambiguous or absent target is refused before any cluster call.
func TestFluxNeedsExactlyOneTarget(t *testing.T) {
	for _, unit := range []string{"", "gw"} {
		cluster := newFluxCluster()
		var release *FluxRelease
		if unit != "" {
			release = &FluxRelease{Namespace: "ns", Name: "r"}
		}
		found := runFluxOn(t, cluster, unit, release, FluxSuspend)
		if found.err == nil || found.err.Error() != "invalid: target: name exactly one unit or release" ||
			len(cluster.writes)+len(cluster.reads) != 0 {
			t.Fatalf("target %q %v: %v", unit, release, found.err)
		}
	}
}

// A release is suspended by its own spec, and resumed and waited for as a unit is: make suspend freezes Helm itself.
func TestFluxSuspendsAndResumesARelease(t *testing.T) {
	cluster := newFluxCluster()
	release := &FluxRelease{Namespace: "ns", Name: "r"}
	ref, _ := fluxRefOf("HelmRelease", "ns", "r")
	if ref.APIVersion != "helm.toolkit.fluxcd.io/v2" {
		t.Fatalf("ref %v", ref)
	}
	suspended := runFluxOn(t, cluster, "", release, FluxSuspend)
	if suspended.err != nil {
		t.Fatal(suspended.err)
	}
	if strings.Join(suspended.lines, "\n") != "► suspending HelmRelease r in ns namespace\n✔ HelmRelease r suspended" {
		t.Fatalf("lines %q", suspended.lines)
	}
	if len(cluster.writes) != 1 || cluster.writes[0] != ref || string(cluster.patches[0]) != `{"spec":{"suspend":true}}` {
		t.Fatalf("wrote %v %s", cluster.writes, cluster.patches)
	}
	resumed := runFluxOn(t, cluster, "", release, FluxResume)
	if resumed.err != nil {
		t.Fatal(resumed.err)
	}
	if strings.Join(resumed.lines, "\n") != strings.Join([]string{"► resuming HelmRelease r in ns namespace",
		"► annotating HelmRelease r in ns namespace", "◎ waiting for HelmRelease reconciliation",
		"✔ applied revision 1.2.3"}, "\n") {
		t.Fatalf("lines %q", resumed.lines)
	}
	if len(cluster.writes) != 3 || cluster.writes[1] != ref || cluster.writes[2] != ref ||
		string(cluster.patches[1]) != `{"spec":{"suspend":false}}` {
		t.Fatalf("wrote %v %s", cluster.writes, cluster.patches)
	}
	for _, read := range cluster.reads {
		if read != ref {
			t.Fatalf("a release's resume read %v", read)
		}
	}
}

// A handled request with Ready true is not current if the controller has not observed the resumed spec.
func TestFluxReleaseResumeRequiresTheCurrentGeneration(t *testing.T) {
	cluster := newFluxCluster()
	cluster.staleGeneration = true
	found := runFluxOn(t, cluster, "", &FluxRelease{Namespace: "ns", Name: "r"}, FluxResume)
	if found.err == nil || !strings.Contains(found.err.Error(), "timed out after 10m waiting for HelmRelease r") ||
		len(found.sleeps) != 300 {
		t.Fatalf("stale generation: %v, pauses %d", found.err, len(found.sleeps))
	}
}

// A release is reconciled with its unit, and a missing one says so: neither leaves a change behind.
func TestFluxRefusesAReleaseReconcileAndNamesAMissingRelease(t *testing.T) {
	cluster := newFluxCluster()
	found := runFluxOn(t, cluster, "", &FluxRelease{Namespace: "ns", Name: "r"}, FluxReconcile)
	if found.err == nil || found.err.Error() != "a HelmRelease is reconciled with the unit that applies it: reconcile the unit" ||
		len(cluster.writes)+len(cluster.reads) != 0 {
		t.Fatalf("reconcile: %v, %v %v", found.err, cluster.writes, cluster.reads)
	}
	missing := newFluxCluster()
	missing.writeErr = &kube.Error{Code: kube.Failed, Subject: "write", Reason: observe.ReasonNotFound}
	found = runFluxOn(t, missing, "", &FluxRelease{Namespace: "ns", Name: "r"}, FluxSuspend)
	if found.err == nil || found.err.Error() != "no HelmRelease named 'r' in ns namespace" {
		t.Fatalf("missing release: %v", found.err)
	}
	found = runFluxOn(t, missing, "gw", nil, FluxResume)
	if found.err == nil || found.err.Error() != "no Kustomization named 'gw'" {
		t.Fatalf("missing unit: %v", found.err)
	}
}
