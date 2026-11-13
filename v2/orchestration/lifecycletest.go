package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/datadestroy"
	"github.com/samclement/swhurl-platform/v2/lifecycletest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// The lifecycle test's waits: make live-test-lifecycle's own bounds. kubectl delete --wait had none, so a deleted unit
// or reader pod is given the unit's own timeout.
const (
	lifecycleReady     = 6 * time.Minute
	lifecycleRollout   = 3 * time.Minute
	lifecycleResumed   = 3 * time.Minute
	lifecycleGone      = 2 * time.Minute
	lifecycleNamespace = 3 * time.Minute
	lifecycleDeleted   = 5 * time.Minute
	lifecycleSettle    = 10 * time.Second
	lifecyclePoll      = 2 * time.Second
	// lifecycleLogLines bounds the reader pod's log: the marker is one line.
	lifecycleLogLines = 100
)

// LifecycleTestInputs is the cluster the test changes, the exec port the workload's marker is read through, the
// clock its waits use, and where each line goes as it is decided.
type LifecycleTestInputs struct {
	Cluster  Cluster
	Secrets  kube.SecretNamer
	Executor kube.Executor
	Now      func() time.Time
	Sleep    func(time.Duration)
	Say      func(lifecycletest.Line)
}

// LifecycleTest proves the lifecycle contract on the throwaway fixture: install, suspend and resume, uninstall,
// destroy-data and an Orphan unit, in that order. Leftovers from an earlier run, or a cluster it cannot read, are a
// *lifecycletest.Preflight and nothing is changed. Otherwise a step that cannot go on is one failed line and the steps
// after it do not run; the passed line follows when nothing failed, and the cleanup always runs last, even when the
// caller's context is cancelled. The lines are returned as well as said.
func LifecycleTest(ctx context.Context, inputs LifecycleTestInputs) ([]lifecycletest.Line, error) {
	run := &lifecycleRun{ctx: ctx, inputs: inputs}
	if err := run.preflight(); err != nil {
		return nil, err
	}
	for _, step := range run.steps() {
		run.say(lifecycletest.Line{Level: lifecycletest.Section, Text: step.title})
		if err := step.do(); err != nil {
			run.say(lifecycletest.Failed(err.Error()))
			break
		}
	}
	for _, line := range lifecycletest.Conclusion(run.lines) {
		run.say(line)
	}
	run.cleanup()
	run.say(lifecycletest.CleanedUp())
	return run.lines, nil
}

// lifecycleRun is one run: the lines said so far, the marker the workload wrote, and every volume the test's claim was
// seen bound to, which the cleanup destroys.
type lifecycleRun struct {
	ctx      context.Context
	inputs   LifecycleTestInputs
	lines    []lifecycletest.Line
	original string
	volume   string
	volumes  []string
}

type lifecycleStep struct {
	title string
	do    func() error
}

func (r *lifecycleRun) steps() []lifecycleStep {
	return []lifecycleStep{
		{lifecycletest.InstallStep, r.install},
		{lifecycletest.SuspendStep, r.suspendAndResume},
		{lifecycletest.UninstallStep, r.uninstall},
		{lifecycletest.DestroyStep, r.destroy},
		{lifecycletest.OrphanStep, r.orphan},
	}
}

func (r *lifecycleRun) say(line lifecycletest.Line) {
	r.lines = append(r.lines, line)
	if r.inputs.Say != nil {
		r.inputs.Say(line)
	}
}

// preflight refuses when the namespace or either unit already exists, or when it cannot tell.
func (r *lifecycleRun) preflight() error {
	for _, ref := range []observe.Ref{lifecycleNamespaceRef(), lifecycleUnitRef(lifecycletest.Unit), lifecycleUnitRef(lifecycletest.OrphanUnit)} {
		present, err := r.present(ref)
		if err != nil {
			return &lifecycletest.Preflight{Reason: "cannot check for leftovers: " + err.Error()}
		}
		if present {
			return lifecycletest.Leftovers()
		}
	}
	return nil
}

// install applies the fixture and reads the marker its workload wrote, and the volume its claim is bound to.
func (r *lifecycleRun) install() error {
	if err := r.createUnit(lifecycletest.Unit, lifecycletest.MirrorPrune); err != nil {
		return err
	}
	if err := r.awaitAvailable(); err != nil {
		return err
	}
	marker, err := r.execMarker()
	if err != nil {
		return err
	}
	claim, err := r.read(lifecycleClaimRef())
	if err != nil {
		return err
	}
	if claim == nil {
		return fmt.Errorf("PersistentVolumeClaim %s/%s not found", lifecycletest.Namespace, lifecycletest.Claim)
	}
	if r.volume, err = lifecycletest.VolumeOf(claim); err != nil {
		return err
	}
	r.original = marker
	r.remember(r.volume)
	r.say(lifecycletest.Wrote(marker, r.volume))
	return nil
}

// suspendAndResume suspends the unit with v2's own unit action, checks the workload keeps running, then resumes it.
func (r *lifecycleRun) suspendAndResume() error {
	if err := r.flux(FluxSuspend); err != nil {
		return err
	}
	unit, err := r.read(lifecycleUnitRef(lifecycletest.Unit))
	if err != nil {
		return err
	}
	suspended := false
	if unit != nil {
		if suspended, err = lifecycletest.Suspended(unit); err != nil {
			return err
		}
	}
	r.say(lifecycletest.UnitSuspended(suspended))
	available, err := r.available()
	if err != nil {
		return err
	}
	r.say(lifecycletest.RunningWhileSuspended(available))
	if err := r.flux(FluxResume); err != nil {
		return err
	}
	r.say(lifecycletest.ResumedReady(r.wait(lifecycleResumed, "", r.ready(lifecycletest.Unit)) == nil))
	return nil
}

// uninstall deletes the app-style unit and checks what it prunes and what it keeps.
func (r *lifecycleRun) uninstall() error {
	if err := r.deleteUnit(lifecycletest.Unit); err != nil {
		return err
	}
	r.say(lifecycletest.Pruned(r.wait(lifecycleGone, "", r.gone(lifecycleDeploymentRef())) == nil))
	_ = r.wait(lifecycleGone, "", r.podsGone) // as make live-test-lifecycle always has, its outcome is not a check
	namespace, err := r.present(lifecycleNamespaceRef())
	if err != nil {
		return err
	}
	r.say(lifecycletest.NamespaceKept(namespace))
	phase := ""
	claim, err := r.read(lifecycleClaimRef())
	if err == nil && claim != nil {
		phase, err = lifecycletest.PhaseOf(claim)
	}
	if err != nil {
		return err
	}
	r.say(lifecycletest.ClaimKept(phase))
	marker, err := r.readMarker()
	if err != nil {
		return err
	}
	r.say(lifecycletest.DataIntact(r.original, marker))
	return nil
}

// destroy asks v2's destroy-data to delete the claim without a confirmation, which must be refused, then with one.
func (r *lifecycleRun) destroy() error {
	target, err := datadestroy.ParseTarget(lifecycletest.Target)
	if err != nil {
		return err
	}
	var refusal *datadestroy.Refusal
	r.say(lifecycletest.DestroyRefused(errors.As(r.destroyData(target, ""), &refusal)))
	claim, err := r.present(lifecycleClaimRef())
	if err != nil {
		return err
	}
	r.say(lifecycletest.ClaimUntouched(claim))
	if err := r.destroyData(target, target.Text); err != nil {
		return fmt.Errorf("destroy-data %s: %w", target.Text, err)
	}
	if claim, err = r.present(lifecycleClaimRef()); err != nil {
		return err
	}
	r.say(lifecycletest.ClaimDestroyed(claim))
	volume, err := r.present(lifecycleVolumeRef(r.volume))
	if err != nil {
		return err
	}
	r.say(lifecycletest.VolumeDestroyed(r.volume, volume))
	return nil
}

// destroyData is make destroy-data's path: the confirmation first, then the guarded deletion.
func (r *lifecycleRun) destroyData(target datadestroy.Target, confirm string) error {
	if err := datadestroy.Confirm(target, confirm); err != nil {
		return err
	}
	_, err := DestroyData(r.ctx, DestroyInputs{Reader: r.inputs.Cluster.Reader, Writer: r.inputs.Cluster.Writer,
		Secrets: r.inputs.Secrets, Target: target, Now: r.inputs.Now, Sleep: r.inputs.Sleep})
	return err
}

// orphan applies the fixture through a unit with deletionPolicy Orphan, deletes the unit, and checks the workload
// is still there after a pause.
func (r *lifecycleRun) orphan() error {
	if err := r.createUnit(lifecycletest.OrphanUnit, lifecycletest.Orphan); err != nil {
		return err
	}
	if err := r.awaitAvailable(); err != nil {
		return err
	}
	if err := r.deleteUnit(lifecycletest.OrphanUnit); err != nil {
		return err
	}
	r.inputs.Sleep(lifecycleSettle)
	available, err := r.available()
	if err != nil {
		return err
	}
	r.say(lifecycletest.OrphanKept(available))
	return nil
}

// cleanup deletes both units, the namespace when it carries the test's label, and every volume the test's claim was
// seen bound to whose claim lived in the namespace. Nothing it does fails the run, and it runs even when the caller's
// context is cancelled.
func (r *lifecycleRun) cleanup() {
	c := &lifecycleRun{ctx: context.WithoutCancel(r.ctx), inputs: r.inputs}
	for _, unit := range []string{lifecycletest.Unit, lifecycletest.OrphanUnit} {
		_ = c.deleteUnit(unit)
	}
	if claim, err := c.read(lifecycleClaimRef()); err == nil && claim != nil {
		if volume, err := lifecycletest.VolumeOf(claim); err == nil {
			r.remember(volume)
		}
	}
	namespace := lifecycleNamespaceRef()
	if document, err := c.read(namespace); err == nil && document != nil && lifecycletest.Labelled(document) {
		if c.inputs.Cluster.Writer.Delete(c.ctx, namespace) == nil {
			_ = c.wait(lifecycleNamespace, "", c.gone(namespace))
		}
	}
	for _, volume := range r.volumes {
		c.destroyVolume(volume)
	}
}

// destroyVolume lets the provisioner delete one volume, only when its claim lived in the test's namespace.
func (r *lifecycleRun) destroyVolume(name string) {
	ref := lifecycleVolumeRef(name)
	volume, err := r.read(ref)
	if err != nil || volume == nil || !lifecycletest.Claimed(volume) {
		return
	}
	_ = r.inputs.Cluster.Writer.Patch(r.ctx, ref, []byte(lifecycletest.ReclaimDelete))
	_ = r.wait(lifecycleGone, "", r.gone(ref))
}

// remember adds a volume the cleanup destroys, once.
func (r *lifecycleRun) remember(volume string) {
	for _, known := range r.volumes {
		if known == volume {
			return
		}
	}
	if volume != "" {
		r.volumes = append(r.volumes, volume)
	}
}
