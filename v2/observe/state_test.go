package observe_test

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// fluxCondition is a Flux object whose Ready condition carries a reason, and optionally a Reconciling condition:
// what tells a failure from work in progress.
func fluxCondition(t *testing.T, status, reason string, reconciling bool, applied string) []byte {
	t.Helper()
	conditions := []any{map[string]any{"type": "Ready", "status": status, "reason": reason, "observedGeneration": 3}}
	if reconciling {
		conditions = append(conditions, map[string]any{"type": "Reconciling", "status": "True", "observedGeneration": 3})
	}
	return encode(t, map[string]any{
		"metadata": map[string]any{"generation": 3},
		"status":   map[string]any{"lastAppliedRevision": applied, "conditions": conditions},
	})
}

// containerPod is a pod whose main container runs the intended image and has the given ready flag and state.
func containerPod(t *testing.T, ready bool, state map[string]any) []byte {
	t.Helper()
	image := "docker.io/nginxinc/nginx-unprivileged@" + digest
	return encode(t, map[string]any{
		"metadata": map[string]any{"name": "hello-main-7c8f9d6b5-x2k4l"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "main", "image": image}}},
		"status": map[string]any{"containerStatuses": []any{map[string]any{"name": "main",
			"imageID": "docker-pullable://" + image, "ready": ready, "state": state}}},
	})
}

// The state table, carried from v1's StateTests (tests/test_console.py: the verdict cases and the unit-state
// vocabulary) with the same evidence expressed as API documents. Cases marked CHANGED are deliberate: v2's
// records are the truth (the expected revision is the records' commit and the wanted replica count is the
// record's), a container that runs but is not ready yet is not failing, and v2 has no job kind, so v1's job
// verdict has no case here.
func TestStateTable(t *testing.T) {
	base := target()
	stopped := target()
	stopped.Replicas = 0
	waiting := map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "back-off restarting failed container"}}
	for _, tt := range []struct {
		name   string
		target observe.Target
		change func(observe.Evidence) observe.Evidence
		want   observe.State
	}{
		// v1 test_health_requires_release_revision_workloads_and_running_image.
		{"v1 healthy", base, same, observe.State{Name: observe.StateHealthy}},
		{"v1 no release is unknown", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, absent(base.Release))
		}, observe.State{observe.StateUnknown, "release", observe.ReasonNotFound}},
		// CHANGED: v1 had no revision to compare and said unknown; the records' commit is always the expected
		// revision, so a unit that has applied none yet is updating.
		{"v1 no applied revision", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Unit, fluxDocument(t, flux{generation: 3})))
		}, observe.State{observe.StateUpdating, "unit.revision", observe.ReasonNotAppliedRev}},
		{"v1 no workload is unknown", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, absent(base.Workload))
		}, observe.State{observe.StateUnknown, "workload", observe.ReasonNotFound}},
		// CHANGED: v1 said unknown when no running image was observed; replicas are wanted and none runs the
		// record's image, so it is updating.
		{"v1 no running image", base, func(e observe.Evidence) observe.Evidence {
			return withPods(e, podList(t, base))
		}, observe.State{observe.StateUpdating, "image", observe.ReasonNoPods}},
		// CHANGED: v1's missing wanted count cannot happen, because the wanted count is the record's; the
		// nearest case is a workload that cannot be read, which is unknown and never absent.
		{"v1 wanted count missing", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, unavailable(base.Workload, observe.ReasonForbidden))
		}, observe.State{observe.StateUnknown, "workload", observe.ReasonForbidden}},
		{"v1 fewer replicas ready", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Workload, deploymentDocument(t, deployment{generation: 4, available: 0})))
		}, observe.State{observe.StateUpdating, "replicas", observe.ReasonReplicas}},
		{"v1 scaled to zero is stopped", stopped, func(e observe.Evidence) observe.Evidence {
			e = withRead(e, present(base.Workload, deploymentDocument(t, deployment{generation: 4, available: 0})))
			return withPods(e, podList(t, base))
		}, observe.State{observe.StateStopped, "replicas", observe.ReasonNoReplicas}},
		{"v1 unit suspended", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Unit, fluxDocument(t, flux{generation: 3, suspended: true, applied: "main@sha1:" + commit})))
		}, observe.State{observe.StateSuspended, "unit", observe.ReasonSuspended}},
		{"v1 release suspended", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Release, fluxDocument(t, flux{generation: 2, suspended: true})))
		}, observe.State{observe.StateSuspended, "release", observe.ReasonSuspended}},
		// v1 test_one_vocabulary_and_waiting_is_updating_not_failing: waiting is updating, not failing.
		{"v1 waiting for a dependency", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Unit, fluxCondition(t, "False", "DependencyNotReady", false, "main@sha1:"+commit)))
		}, observe.State{observe.StateUpdating, "unit", observe.ReasonNotTrue}},
		{"v1 progressing", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Unit, fluxCondition(t, "False", "Progressing", false, "main@sha1:"+commit)))
		}, observe.State{observe.StateUpdating, "unit", observe.ReasonNotTrue}},
		{"v1 running health checks", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Unit, fluxCondition(t, "Unknown", "Progressing", true, "main@sha1:"+commit)))
		}, observe.State{observe.StateUpdating, "unit", observe.ReasonNotTrue}},
		{"v1 health check failed", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Unit, fluxCondition(t, "False", "HealthCheckFailed", false, "main@sha1:"+commit)))
		}, observe.State{observe.StateFailing, "unit", observe.ReasonFailed}},
		// v1 ops.problems: a container that is not ready and says why is failing.
		{"v1 a crashing container", base, func(e observe.Evidence) observe.Evidence {
			return withPods(e, podList(t, base, containerPod(t, false, waiting)))
		}, observe.State{observe.StateFailing, "containers", observe.ReasonContainers}},
		// CHANGED: v1 counted a running container that is not ready yet as a failing pod; it says no waiting or
		// terminated reason, so it is the workload's readiness that is not there yet.
		{"a container not ready yet", base, func(e observe.Evidence) observe.Evidence {
			e = withRead(e, present(base.Workload, deploymentDocument(t, deployment{generation: 4, available: 0, status: "False"})))
			return withPods(e, podList(t, base, containerPod(t, false, map[string]any{"running": map[string]any{}})))
		}, observe.State{observe.StateUpdating, "ready", observe.ReasonNotAvailable}},
		// CHANGED: v1 called an instance behind only by other apps' commits healthy; the records' commit is the
		// expected revision, so every instance is updating until Flux applies it.
		{"a commit elsewhere", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Unit, fluxDocument(t, flux{generation: 3, applied: "main@sha1:" + other})))
		}, observe.State{observe.StateUpdating, "unit.revision", observe.Different}},
		// CHANGED: v1 refused a missing unit as an error; it is a fact, and the instance is failing.
		{"the unit is absent", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, absent(base.Unit))
		}, observe.State{observe.StateFailing, "unit", observe.ReasonNotFound}},
		{"the release failed", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Release, fluxCondition(t, "False", "UpgradeFailed", false, "")))
		}, observe.State{observe.StateFailing, "release", observe.ReasonFailed}},
		{"a failure Flux is retrying", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, present(base.Release, fluxCondition(t, "False", "UpgradeFailed", true, "")))
		}, observe.State{observe.StateUpdating, "release", observe.ReasonNotTrue}},
		{"another image", base, func(e observe.Evidence) observe.Evidence {
			return withPods(e, podList(t, base, podDocument(t, pod{image: "docker.io/x@sha256:" + other + other[:24], started: true})))
		}, observe.State{observe.StateUpdating, "image", observe.ReasonDigest}},
		{"a failed read is unknown, never absent", base, func(e observe.Evidence) observe.Evidence {
			return withRead(e, unavailable(base.Release, observe.ReasonTimedOut))
		}, observe.State{observe.StateUnknown, "release", observe.ReasonTimedOut}},
		{"a read not made is unknown", base, func(e observe.Evidence) observe.Evidence {
			return without(e, base.Release)
		}, observe.State{observe.StateUnknown, "release", observe.NotRead}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report, err := observe.Explain(tt.target, tt.change(healthy(t)))
			if err != nil {
				t.Fatal(err)
			}
			if report.State != tt.want {
				t.Errorf("state %+v, want %+v (findings %+v)", report.State, tt.want, report.Findings)
			}
		})
	}
}

func same(e observe.Evidence) observe.Evidence { return e }

// The first matching rule wins, in the table's order: suspended, failing, updating, stopped, unknown.
func TestStateRuleOrder(t *testing.T) {
	base := target()
	crashing := podList(t, base, containerPod(t, false, map[string]any{"terminated": map[string]any{"reason": "OOMKilled"}}))
	stopped := target()
	stopped.Replicas = 0
	for _, tt := range []struct {
		name     string
		target   observe.Target
		evidence observe.Evidence
		want     string
	}{
		{"suspended before failing", base, withPods(withRead(healthy(t), present(base.Release,
			fluxDocument(t, flux{generation: 2, suspended: true}))), crashing), observe.StateSuspended},
		{"failing before updating", base, withPods(withRead(healthy(t), present(base.Unit,
			fluxDocument(t, flux{generation: 3, applied: "main@sha1:" + other}))), crashing), observe.StateFailing},
		{"updating before stopped", stopped, healthy(t), observe.StateUpdating},
		{"stopped before unknown", stopped, withPods(withRead(withRead(healthy(t),
			present(base.Workload, deploymentDocument(t, deployment{generation: 4, available: 0}))),
			unavailable(base.Release, observe.ReasonForbidden)), podList(t, base)), observe.StateStopped},
		{"updating before unknown", base, withRead(withRead(healthy(t), unavailable(base.Release, observe.ReasonForbidden)),
			present(base.Workload, deploymentDocument(t, deployment{generation: 4, available: 0}))), observe.StateUpdating},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report, err := observe.Explain(tt.target, tt.evidence)
			if err != nil {
				t.Fatal(err)
			}
			if report.State.Name != tt.want {
				t.Errorf("state %+v, want %s", report.State, tt.want)
			}
		})
	}
}
