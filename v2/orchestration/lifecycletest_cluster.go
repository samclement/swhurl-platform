package orchestration

import (
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/lifecycletest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// The objects the lifecycle test reads and changes, each a fixed kind the adapter states.
func lifecycleUnitRef(name string) observe.Ref {
	ref, _ := kube.WritableRefOf(fluxUnitGroup, "Kustomization", lifecycletest.FluxNamespace, name)
	return ref
}

func lifecycleNamespaceRef() observe.Ref {
	ref, _ := kube.RefOf("", "Namespace", "", lifecycletest.Namespace)
	return ref
}

func lifecycleClaimRef() observe.Ref {
	ref, _ := kube.RefOf("", "PersistentVolumeClaim", lifecycletest.Namespace, lifecycletest.Claim)
	return ref
}

func lifecycleDeploymentRef() observe.Ref {
	ref, _ := kube.RefOf("apps", "Deployment", lifecycletest.Namespace, lifecycletest.Workload)
	return ref
}

func lifecyclePodsRef() observe.Ref {
	ref, _ := kube.RefOf("", "Pod", lifecycletest.Namespace, "")
	return ref
}

func lifecycleReaderRef() observe.Ref {
	ref, _ := kube.WritableRefOf("", "Pod", lifecycletest.Namespace, lifecycletest.ReaderPod)
	return ref
}

func lifecycleVolumeRef(name string) observe.Ref {
	ref, _ := kube.RefOf("", "PersistentVolume", "", name)
	return ref
}

// read is one object's document, nil when the server says it is absent, or an error naming why it could not be read:
// a failed read is never taken for absence.
func (r *lifecycleRun) read(ref observe.Ref) ([]byte, error) {
	return readObject(r.ctx, r.inputs.Cluster.Reader, ref, ref.Kind+" "+ref.Name)
}

// present is whether the server says the object exists.
func (r *lifecycleRun) present(ref observe.Ref) (bool, error) {
	document, err := r.read(ref)
	return document != nil, err
}

// wait polls met until it holds or limit passes. A read that fails is retried, and named when the wait times out;
// what is the failure's words.
func (r *lifecycleRun) wait(limit time.Duration, what string, met func() (bool, error)) error {
	started := r.inputs.Now()
	last := ""
	for {
		done, err := met()
		if err == nil && done {
			return nil
		}
		last = ""
		if err != nil {
			last = err.Error()
		}
		if r.ctx.Err() != nil {
			return fmt.Errorf("%s: wait stopped: %w", what, r.ctx.Err())
		}
		if r.inputs.Now().Sub(started) >= limit {
			if last != "" {
				return fmt.Errorf("%s after %s (last read: %s)", what, limit, last)
			}
			return fmt.Errorf("%s after %s", what, limit)
		}
		r.inputs.Sleep(lifecyclePoll)
	}
}

// gone is whether the server says the object is absent.
func (r *lifecycleRun) gone(ref observe.Ref) func() (bool, error) {
	return func() (bool, error) {
		present, err := r.present(ref)
		return !present, err
	}
}

// ready is whether a unit's Ready condition is True.
func (r *lifecycleRun) ready(unit string) func() (bool, error) {
	return func() (bool, error) {
		document, err := r.read(lifecycleUnitRef(unit))
		if err != nil || document == nil {
			return false, err
		}
		return lifecycletest.Ready(document)
	}
}

// available is whether the workload has its one replica available; false when it is gone.
func (r *lifecycleRun) available() (bool, error) {
	document, err := r.read(lifecycleDeploymentRef())
	if err != nil || document == nil {
		return false, err
	}
	return lifecycletest.Available(document)
}

// podsGone is whether no workload pod is left.
func (r *lifecycleRun) podsGone() (bool, error) {
	pods, err := r.pods()
	return len(pods) == 0, err
}

// pods is the workload's pods.
func (r *lifecycleRun) pods() ([][]byte, error) {
	pods, err := r.inputs.Cluster.Reader.List(r.ctx, lifecyclePodsRef(), lifecycletest.Selector)
	if err != nil {
		return nil, err
	}
	if pods.Status != observe.Present {
		return nil, fmt.Errorf("failed: pods: %s", pods.Reason)
	}
	return pods.Items, nil
}

// createUnit creates one unit for the fixture and waits for it to be Ready.
func (r *lifecycleRun) createUnit(name string, policy lifecycletest.DeletionPolicy) error {
	if err := r.inputs.Cluster.Writer.Create(r.ctx, lifecycleUnitRef(name), lifecycletest.UnitDocument(name, policy)); err != nil {
		return fmt.Errorf("create Kustomization %s: %w", name, err)
	}
	return r.wait(lifecycleReady, "Kustomization "+name+" not Ready", r.ready(name))
}

// deleteUnit deletes one unit and waits for it to go: Flux's finalizer prunes or orphans its objects first.
func (r *lifecycleRun) deleteUnit(name string) error {
	ref := lifecycleUnitRef(name)
	if err := r.inputs.Cluster.Writer.Delete(r.ctx, ref); err != nil {
		return fmt.Errorf("delete Kustomization %s: %w", name, err)
	}
	return r.wait(lifecycleDeleted, "Kustomization "+name+" still present", r.gone(ref))
}

// awaitAvailable waits for the workload's replica, as a rollout status does.
func (r *lifecycleRun) awaitAvailable() error {
	what := fmt.Sprintf("deploy/%s in %s not available", lifecycletest.Workload, lifecycletest.Namespace)
	return r.wait(lifecycleRollout, what, r.available)
}

// flux runs one of v2's unit actions on the test's unit, in process: the lines it reports are not the test's.
func (r *lifecycleRun) flux(action FluxAction) error {
	err := Flux(r.ctx, FluxInputs{Action: action, Unit: lifecycletest.Unit, Cluster: r.inputs.Cluster,
		Now: r.inputs.Now, Sleep: r.inputs.Sleep})
	if err != nil {
		return fmt.Errorf("%s Kustomization %s: %w", action, lifecycletest.Unit, err)
	}
	return nil
}

// execMarker reads the marker inside the workload's running pod, through the API server.
func (r *lifecycleRun) execMarker() (string, error) {
	pods, err := r.pods()
	if err != nil {
		return "", err
	}
	pod, err := lifecycletest.RunningPod(pods)
	if err != nil {
		return "", err
	}
	result, err := r.inputs.Executor.Exec(r.ctx, kube.ExecRequest{Namespace: lifecycletest.Namespace, Pod: pod,
		Container: lifecycletest.Container, Command: []string{"cat", lifecycletest.Marker}})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("cat %s in pod %s exited %d", lifecycletest.Marker, pod, result.ExitCode)
	}
	return string(result.Stdout), nil
}

// readMarker reads the marker through a short-lived pod that mounts the claim, once the workload is gone.
func (r *lifecycleRun) readMarker() (string, error) {
	ref := lifecycleReaderRef()
	what := fmt.Sprintf("pod %s/%s", lifecycletest.Namespace, lifecycletest.ReaderPod)
	if r.inputs.Cluster.Writer.Delete(r.ctx, ref) == nil {
		_ = r.wait(lifecycleDeleted, what+" still present", r.gone(ref))
	}
	if err := r.inputs.Cluster.Writer.Create(r.ctx, ref, lifecycletest.ReaderDocument()); err != nil {
		return "", fmt.Errorf("create %s: %w", what, err)
	}
	succeeded := func() (bool, error) {
		document, err := r.read(ref)
		if err != nil || document == nil {
			return false, err
		}
		phase, err := lifecycletest.PhaseOf(document)
		return phase == "Succeeded", err
	}
	if err := r.wait(lifecycleGone, what+" not Succeeded", succeeded); err != nil {
		return "", err
	}
	marker, err := r.inputs.Cluster.Writer.Logs(r.ctx, ref, lifecycleLogLines)
	if err != nil {
		return "", fmt.Errorf("logs of %s: %w", what, err)
	}
	if err := r.inputs.Cluster.Writer.Delete(r.ctx, ref); err != nil {
		return "", fmt.Errorf("delete %s: %w", what, err)
	}
	return marker, r.wait(lifecycleDeleted, what+" still present", r.gone(ref))
}
