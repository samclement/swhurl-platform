package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// A restore's own outcome words. "restored" says the data was replaced; it never says the app runs — that is
// the app block's own fact.
const (
	RestoreRestored = "restored"
	RestoreFailed   = "failed"

	StepDone    = "done"
	StepFailed  = "failed"
	StepNotRead = "not_read"

	JobComplete = "complete"
	JobFailed   = "failed"
	JobRunning  = "running"
	JobUnknown  = "unknown"

	AppRunning = "running"
	AppStopped = "stopped"
	AppUnknown = "unknown"
)

// The restore's own bounds. Every wait is bounded and every poll is the same interval, so a test's clock
// decides how long a run takes.
const (
	pollInterval    = 2 * time.Second
	podsGoneTimeout = 3 * time.Minute
	rolloutTimeout  = 3 * time.Minute
	logTailLines    = 100
	// jobTimeout is the Job's own deadline plus a minute: the pod is bounded first, and this wait only has
	// to outlive it.
	jobTimeout = kernel.RestoreDeadline + time.Minute
	// undoTimeout bounds the undo on a context of its own: a run that has used up its own must still be able
	// to put the app back.
	undoTimeout = 2 * time.Minute
	// maxReadFailures is how many consecutive failed reads a wait tolerates before the step fails: one
	// refused or timed-out request during a fifteen-minute wait is not evidence that the work stopped.
	maxReadFailures = 3
)

// Cluster is what a restore reads and changes: both halves of the kube adapter, because a restore that could
// not read the Deployment it scales would have to guess what it is returning the app to.
type Cluster struct {
	Reader kube.Reader
	Writer kube.Writer
}

// RestoreInputs is one restore: the instance's documents, the plan that decided its archive, the two
// credentials its pod holds for this run, the target the documents were compiled to, and the cluster it acts
// on. The archive, its checksums and its table count are taken from the plan, so a caller cannot restore a
// different archive from the one the plan verified.
type RestoreInputs struct {
	Instance, Release, Profile, Egress []byte
	Plan                               observe.RestorePlan
	Target                             observe.Target
	AgeIdentity, ReadURL               string
	Cluster                            Cluster
	Sleep                              func(time.Duration)
	Now                                func() time.Time
}

// RestoreOutcome is what a restore did: every step in the order it ran, including the undo steps, the Job's
// log as evidence, and the app's state afterwards. It claims nothing about the data being the point in time
// the operator wanted.
type RestoreOutcome struct {
	State  string
	Reason string // the step that failed; empty when the restore completed
	Steps  []RestoreStep
	Job    RestoreJobOutcome
	App    RestoreApp
	// BackupSuspended is whether the run ended with the instance's backup schedule suspended: true only when
	// a restore pod may still hold the claim and the undo stopped before the schedule was put back.
	BackupSuspended bool
	Started         time.Time
	Finished        time.Time
}

// RestoreStep is one thing the run did, or failed to do.
type RestoreStep struct {
	Name    string
	Outcome string
	Detail  string
}

// RestoreJobOutcome is the Job's own answer and its evidence: the log is read whatever the Job's state was,
// and a log it could not read is `not_read` with a fixed reason rather than a failed restore.
type RestoreJobOutcome struct {
	Name      string
	State     string
	Log       string
	LogReason string
}

// RestoreApp is the app's state when the run ended: the replica count it had before (the count the run
// returns it to), whether its release is still suspended, and whether it was observed available again.
type RestoreApp struct {
	Replicas  int32
	Suspended bool
	Available bool
	State     string
}

// Refusal is the use case's own refusal: it will not start, and nothing was changed. A failure
// after the first change is never a refusal, so a caller can tell "not started" from "stopped part-way" by
// the type alone.
type Refusal struct{ Subject, Reason string }

func (r *Refusal) Error() string { return "refused: " + r.Subject + ": " + r.Reason }

// Restore performs one restore from the plan it is given: it suspends the release, stops the app, runs the
// backup image's restore job on the app's own claim, reads the job's log, then puts the app back. Every
// failure from the first change onward unwinds in the order the restore took the app apart — job, secret,
// replicas, release — and each undo is attempted even when an earlier one failed.
func Restore(ctx context.Context, inputs RestoreInputs) (RestoreOutcome, error) {
	if !inputs.Plan.Ready() {
		return RestoreOutcome{}, &Refusal{"restore.plan", "not_ready"}
	}
	spec, release, profile, err := decodeRestoreDocuments(inputs)
	if err != nil {
		return RestoreOutcome{}, err
	}
	egress, err := egressOf(inputs.Egress, spec.Application, spec.Environment)
	if err != nil {
		return RestoreOutcome{}, err
	}
	job := kernel.RestoreJob{
		Name:          kernel.RestoreJobName(spec.Application, inputs.Now()),
		Archive:       inputs.Plan.Archive.Newest,
		ArchiveSHA256: inputs.Plan.Metadata.SHA256, PlainSHA256: inputs.Plan.Metadata.PlainSHA256,
		Tables: inputs.Plan.Metadata.Tables, AgeIdentity: inputs.AgeIdentity, ReadURL: inputs.ReadURL,
	}
	objects, err := kernel.RenderRestore(spec, release, profile, egress, job)
	if err != nil {
		return RestoreOutcome{}, err
	}
	replicas, err := preflight(ctx, inputs)
	if err != nil {
		return RestoreOutcome{}, err
	}
	r := &restorer{ctx: ctx, cluster: inputs.Cluster, target: inputs.Target, sleep: inputs.Sleep,
		now: inputs.Now, replicas: replicas,
		out: RestoreOutcome{Job: RestoreJobOutcome{Name: job.Name, State: JobUnknown}, Started: inputs.Now()}}
	return r.run(objects)
}

// decodeRestoreDocuments reads the three records a restore renders from.
func decodeRestoreDocuments(inputs RestoreInputs) (kernel.InstanceSpec, kernel.ReleaseBundle, kernel.PlatformProfile, error) {
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, kernel.PlatformProfile{}, err
	}
	release, err := kernel.DecodeRelease(inputs.Release)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, kernel.PlatformProfile{}, err
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, kernel.PlatformProfile{}, err
	}
	return spec, release, profile, nil
}

// preflight is what a restore reads before it changes anything: no restore of this instance is already
// running, and the app's live replica count, which is what it is returned to. A failure here is a refusal:
// nothing has been changed.
func preflight(ctx context.Context, inputs RestoreInputs) (int32, error) {
	if inputs.Target.Backup == nil {
		// An archive to restore comes from a backup job; an instance that renders none cannot have one.
		return 0, &Refusal{"restore.backup", "no_backup"}
	}
	listed, err := inputs.Cluster.Reader.List(ctx, inputs.Target.Pods, kernel.RestoreLabel)
	if err != nil {
		return 0, &Refusal{"restore.pods", failureReason(err)}
	}
	if listed.Status != observe.Present {
		return 0, &Refusal{"restore.pods", listed.Reason}
	}
	if len(listed.Items) > 0 {
		return 0, &Refusal{"restore", "in_progress"}
	}
	return appReplicas(ctx, inputs.Cluster, inputs.Target)
}

// appReplicas is the live replica count a restore returns the app to: the fact, not the record's, so a
// manually scaled instance comes back as it was.
func appReplicas(ctx context.Context, cluster Cluster, target observe.Target) (int32, error) {
	read, err := cluster.Reader.Read(ctx, target.Workload)
	if err != nil {
		return 0, &Refusal{"restore.deployment", failureReason(err)}
	}
	if read.Status != observe.Present {
		return 0, &Refusal{"restore.deployment", reasonOf(read)}
	}
	var deployment struct {
		Spec struct {
			Replicas *int32 `json:"replicas"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(read.Object, &deployment); err != nil {
		return 0, &Refusal{"restore.deployment", observe.ReasonMalformed}
	}
	if deployment.Spec.Replicas == nil {
		return 1, nil // the API server's own default when the Deployment states none
	}
	return *deployment.Spec.Replicas, nil
}

// reasonOf is the fixed reason a read states, or the malformed document it could not be read from.
func reasonOf(read observe.Read) string {
	if read.Reason != "" {
		return read.Reason
	}
	return observe.ReasonNotFound
}

// restorer is one restore's run: what it has changed so far, the steps it has taken, and the cluster it takes
// them on.
type restorer struct {
	ctx     context.Context
	cluster Cluster
	target  observe.Target
	sleep   func(time.Duration)
	now     func() time.Time

	replicas   int32
	suspended  bool
	scaled     bool
	secretName string
	jobName    string
	// backupSuspended is whether this run suspended the backup schedule and has not put it back;
	// backupWasSuspended is what it found, so a schedule the operator had already suspended stays that way.
	backupSuspended    bool
	backupWasSuspended bool

	steps []RestoreStep
	out   RestoreOutcome
}

// run is the restore in three acts. Each step records what it did; a failure stops the sequence and unwinds.
func (r *restorer) run(objects kernel.RestoreObjects) (RestoreOutcome, error) {
	for _, act := range []func() error{
		func() error { return r.takeDown(objects) }, r.restore, r.putBack,
	} {
		if err := act(); err != nil {
			return r.failed(err)
		}
	}
	r.out.State = RestoreRestored
	r.out.BackupSuspended = r.backupSuspended
	r.out.App = RestoreApp{Replicas: r.replicas, Available: true, State: AppRunning}
	if r.replicas == 0 {
		// An app that was stopped before the run stays stopped: the restore returns it to what it was, and
		// that is not running.
		r.out.App.Available, r.out.App.State = false, AppStopped
	}
	r.out.Finished = r.now()
	return r.out, nil
}

// takeDown stops the app before anything touches its database: Flux first, then the replicas, then the pods
// themselves, and only then the two transient objects.
func (r *restorer) takeDown(objects kernel.RestoreObjects) error {
	steps := []struct {
		name   string
		action func() error
	}{
		{"suspend", r.suspend}, {"backup-suspend", r.suspendBackup}, {"scale-down", r.scaleDown},
		{"pods-gone", r.podsGone},
		{"secret", func() error { return r.create("", "Secret", objects.Secret) }},
		{"job", func() error { return r.create("batch", "Job", objects.Job) }},
	}
	return r.attemptAll(steps)
}

// restore waits for the Job, reads its log beside its own answer, and fails the run when the Job did. The log
// is read even when the wait failed: a Job that is still running has a log, and it is the evidence a failed
// wait leaves.
func (r *restorer) restore() error {
	err := r.attempt("job-finished", r.waitForJob)
	r.readLog()
	if err != nil {
		return err
	}
	return r.attempt("job-result", func() error {
		if r.out.Job.State != JobComplete {
			return fmt.Errorf("failed: restore job: %s", r.out.Job.State)
		}
		return nil
	})
}

// putBack removes the Job and its Secret, returns the app's replica count, hands the release back to Flux,
// and only then waits for the app to be available.
func (r *restorer) putBack() error {
	steps := []struct {
		name   string
		action func() error
	}{
		{"delete-job", r.deleteJob}, {"delete-secret", r.deleteSecret}, {"backup-resume", r.resumeBackup},
		{"scale-up", r.scaleUp}, {"resume", r.resume}, {"available", r.waitForRollout},
	}
	return r.attemptAll(steps)
}

// attemptAll runs steps in order and stops at the first failure.
func (r *restorer) attemptAll(steps []struct {
	name   string
	action func() error
}) error {
	for _, step := range steps {
		if err := r.attempt(step.name, step.action); err != nil {
			return err
		}
	}
	return nil
}

// failed records the failure, unwinds the app, and states what the app's own state now is. The failing error
// is returned unchanged: the outcome carries the steps and the app's state beside it.
func (r *restorer) failed(err error) (RestoreOutcome, error) {
	r.unwind()
	r.out.State = RestoreFailed
	r.out.BackupSuspended = r.backupSuspended
	r.out.App = RestoreApp{Replicas: r.replicas, Suspended: r.suspended, State: r.appState()}
	r.out.Finished = r.now()
	return r.out, err
}

// unwind puts the app back in the order the restore took it apart, on a context of its own with a short
// bound: the run's own context may have been what failed. Each undo is attempted even when an earlier one
// failed, and each is a recorded step — a failed undo here is what leaves the app stopped, and the report
// names it rather than hiding it.
func (r *restorer) unwind() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), undoTimeout)
	defer cancel()
	previous := r.ctx
	r.ctx = ctx
	defer func() { r.ctx = previous }()
	if r.jobName != "" {
		_ = r.attempt("delete-job", r.deleteJob)
	}
	if r.secretName != "" {
		_ = r.attempt("delete-secret", r.deleteSecret)
	}
	if r.jobName != "" {
		// The Job's pod may still hold the claim: restarting the app over it is worse than leaving the app
		// stopped, so the undo stops here and the app block says so. The next restore's preflight refuses
		// until that pod is gone.
		return
	}
	if r.backupSuspended {
		_ = r.attempt("backup-resume", r.resumeBackup)
	}
	if r.scaled {
		_ = r.attempt("scale-up", r.scaleUp)
	}
	if r.suspended {
		_ = r.attempt("resume", r.resume)
	}
}

// appState is what the run can state about the app: running only if it observed it so, stopped if it left it
// scaled down or suspended, and unknown when it never changed it.
func (r *restorer) appState() string {
	switch {
	case r.out.App.Available:
		return AppRunning
	case r.scaled || r.suspended:
		return AppStopped
	}
	return AppUnknown
}

// attempt runs one step and records its outcome. The first failing step names the outcome's reason.
func (r *restorer) attempt(name string, action func() error) error {
	err := action()
	step := RestoreStep{Name: name, Outcome: StepDone}
	if err != nil {
		step.Outcome = StepFailed
		if r.out.Reason == "" {
			r.out.Reason = name
		}
	}
	if err != nil {
		step.Detail = err.Error()
	}
	r.steps = append(r.steps, step)
	r.out.Steps = r.steps
	return err
}

// suspend stops Flux reconciling the release, so no upgrade starts and the scale-down is not reverted. The
// run records the intent before the request: a request that timed out may still have been applied.
func (r *restorer) suspend() error {
	r.suspended = true
	return r.patch(r.target.Release, `{"spec":{"suspend":true}}`)
}

// suspendBackup stops the nightly run for the window: a backup that copied the database while the restore swaps
// its files would publish a stale archive. The schedule's own state is read first, so one the operator had
// already suspended is left suspended.
func (r *restorer) suspendBackup() error {
	read, err := r.cluster.Reader.Read(r.ctx, r.target.Backup.CronJob)
	if err != nil {
		return err
	}
	if read.Status != observe.Present {
		return unavailable("backup", reasonOf(read))
	}
	suspended, err := cronJobSuspended(read.Object)
	if err != nil {
		return err
	}
	r.backupWasSuspended = suspended
	if err := r.patch(r.target.Backup.CronJob, `{"spec":{"suspend":true}}`); err != nil {
		return err
	}
	r.backupSuspended = true
	return nil
}

// resumeBackup puts the schedule back as it was. A CronJob that was already suspended stays so, and the run no
// longer owns it: nothing to put back.
func (r *restorer) resumeBackup() error {
	if !r.backupWasSuspended {
		if err := r.patch(r.target.Backup.CronJob, `{"spec":{"suspend":false}}`); err != nil {
			return err
		}
	}
	r.backupSuspended = false
	return nil
}

// cronJobSuspended is a CronJob's own suspension fact: absent means not suspended.
func cronJobSuspended(document []byte) (bool, error) {
	var cronJob struct {
		Spec struct {
			Suspend *bool `json:"suspend"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(document, &cronJob); err != nil {
		return false, unavailable("backup", observe.ReasonMalformed)
	}
	return cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend, nil
}

// scaleDown stops the app: the restore replaces the files under it, so nothing may be writing. The intent is
// recorded before the request, as the suspend's is.
func (r *restorer) scaleDown() error {
	r.scaled = true
	return r.replicasPatch(0)
}

// podsGone waits until the app's own pods are gone, bounded: a pod that outlives the bound is a failure, not
// a reason to restore anyway.
func (r *restorer) podsGone() error {
	return r.wait(podsGoneTimeout, func() (bool, error) {
		listed, err := r.cluster.Reader.List(r.ctx, r.target.Pods, r.target.Selector)
		if err != nil {
			return false, err
		}
		if listed.Status != observe.Present {
			return false, unavailable("pods", listed.Reason)
		}
		return len(listed.Items) == 0, nil
	})
}

// create makes one of the restore's two transient objects under the kind the write table states.
func (r *restorer) create(group, kind string, object kernel.RestoreObject) error {
	ref, err := kube.WritableRefOf(group, kind, r.target.Namespace, object.Name)
	if err != nil {
		return err
	}
	// Recorded before the request: a create that timed out may still have been accepted, and a Secret left
	// behind holds the age identity.
	if kind == "Secret" {
		r.secretName = object.Name
	} else {
		r.jobName = object.Name
	}
	return r.cluster.Writer.Create(r.ctx, ref, object.Document)
}

// waitForJob waits until the Job reports Complete or Failed, and states which. Running out of time is a
// failure of this step: the pod's own deadline is shorter, so a Job that is still going was not bounded.
func (r *restorer) waitForJob() error {
	return r.wait(jobTimeout, func() (bool, error) {
		read, err := r.cluster.Reader.Read(r.ctx, r.jobRef())
		if err != nil {
			return false, err
		}
		if read.Status != observe.Present {
			return false, unavailable("job", reasonOf(read))
		}
		state, err := jobState(read.Object)
		if err != nil {
			return false, err
		}
		r.out.Job.State = state
		return state == JobComplete || state == JobFailed, nil
	})
}

// readLog is the Job's evidence. A log it cannot read is `not_read` with the reason, never a failed restore:
// what the job did is its own answer, and the log is what the operator reads afterwards.
func (r *restorer) readLog() {
	step := RestoreStep{Name: "log", Outcome: StepDone}
	if step.Detail = r.log(); step.Detail != "" {
		step.Outcome = StepNotRead
		r.out.Job.LogReason = step.Detail
	}
	r.steps = append(r.steps, step)
	r.out.Steps = r.steps
}

// log reads the newest pod's log tail for this Job's name, and returns why it could not.
func (r *restorer) log() string {
	selector := kernel.RestoreLabel + "=" + r.out.Job.Name
	listed, err := r.cluster.Reader.List(r.ctx, r.target.Pods, selector)
	if err != nil {
		return observe.ReasonStatus
	}
	if listed.Status != observe.Present || len(listed.Items) == 0 {
		return reasonOf(observe.Read{Status: listed.Status, Reason: listed.Reason})
	}
	name, err := podName(listed.Items[0])
	if err != nil {
		return observe.ReasonMalformed
	}
	ref := observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: r.target.Namespace, Name: name}
	tail, err := r.cluster.Writer.Logs(r.ctx, ref, logTailLines)
	if err != nil {
		return failureReason(err)
	}
	r.out.Job.Log = tail
	return ""
}

// deleteJob removes the Job and its pod, and waits until that pod is gone: the next step scales the app back
// up over the claim, and nothing may still be writing to it. The Job is deleted with the API's background
// propagation, so this wait is for the cluster, not a second delete.
func (r *restorer) deleteJob() error {
	if err := r.cluster.Writer.Delete(r.ctx, r.jobRef()); err != nil {
		return err
	}
	if err := r.wait(podsGoneTimeout, r.restorePodGone); err != nil {
		return err
	}
	r.jobName = ""
	return nil
}

// restorePodGone is whether this run's own pod has left the namespace.
func (r *restorer) restorePodGone() (bool, error) {
	listed, err := r.cluster.Reader.List(r.ctx, r.target.Pods, kernel.RestoreLabel+"="+r.jobName)
	if err != nil {
		return false, err
	}
	if listed.Status != observe.Present {
		return false, unavailable("pods", listed.Reason)
	}
	return len(listed.Items) == 0, nil
}

// deleteSecret removes the two credentials the pod held. It is deleted even when the Job failed: neither is
// valid beyond the run.
func (r *restorer) deleteSecret() error {
	ref, err := kube.WritableRefOf("", "Secret", r.target.Namespace, r.secretName)
	if err != nil {
		return err
	}
	if err := r.cluster.Writer.Delete(r.ctx, ref); err != nil {
		return err
	}
	r.secretName = ""
	return nil
}

// scaleUp returns the app to the replica count it had, and stops scaling on failure so a later step cannot
// pretend it came back.
func (r *restorer) scaleUp() error {
	if err := r.replicasPatch(r.replicas); err != nil {
		return err
	}
	r.scaled = false
	return nil
}

// resume hands the release back to Flux, after the app is scaled: a resume first would restart the app while
// the database was still being replaced.
func (r *restorer) resume() error {
	if err := r.patch(r.target.Release, `{"spec":{"suspend":false}}`); err != nil {
		return err
	}
	r.suspended = false
	return nil
}

// waitForRollout waits until the app is available at the replica count it had.
func (r *restorer) waitForRollout() error {
	return r.wait(rolloutTimeout, func() (bool, error) {
		read, err := r.cluster.Reader.Read(r.ctx, r.target.Workload)
		if err != nil {
			return false, err
		}
		if read.Status != observe.Present {
			return false, unavailable("deployment", reasonOf(read))
		}
		return deploymentAvailable(read.Object, r.replicas)
	})
}

// wait polls one condition until it holds or the timeout passes. A read that fails is retried a few times
// before it fails the step; a condition that is never true is a timeout. The clock and the sleep are the
// caller's, so the use case itself has no notion of wall time.
func (r *restorer) wait(timeout time.Duration, ready func() (bool, error)) error {
	started := r.now()
	failures := 0
	for {
		done, err := ready()
		if err != nil {
			failures++
			if failures >= maxReadFailures {
				return err
			}
		} else if failures = 0; done {
			return nil
		}
		if r.now().Sub(started) >= timeout {
			return errors.New("failed: wait: " + observe.ReasonTimedOut)
		}
		r.sleep(pollInterval)
	}
}

// patch applies one merge patch to an object the reader can name.
func (r *restorer) patch(ref observe.Ref, patch string) error {
	return r.cluster.Writer.Patch(r.ctx, ref, []byte(patch))
}

// replicasPatch sets the Deployment's replica count.
func (r *restorer) replicasPatch(count int32) error {
	return r.patch(r.target.Workload, fmt.Sprintf(`{"spec":{"replicas":%d}}`, count))
}

// jobRef is the Job this run created.
func (r *restorer) jobRef() observe.Ref {
	return observe.Ref{APIVersion: "batch/v1", Kind: "Job", Namespace: r.target.Namespace, Name: r.jobName}
}

// jobState is a Job's own answer: a true Complete or Failed condition, or running until one appears.
func jobState(document []byte) (string, error) {
	var job struct {
		Status struct {
			Conditions []struct{ Type, Status string } `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(document, &job); err != nil {
		return JobUnknown, unavailable("job", observe.ReasonMalformed)
	}
	for _, condition := range job.Status.Conditions {
		if condition.Status != "True" {
			continue
		}
		switch condition.Type {
		case "Complete":
			return JobComplete, nil
		case "Failed":
			return JobFailed, nil
		}
	}
	return JobRunning, nil
}

// deploymentAvailable is the chart's Deployment being available at the intended count: the generation it
// reports having observed is the one it has now, and enough replicas are available.
func deploymentAvailable(document []byte, want int32) (bool, error) {
	var deployment struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Replicas *int32 `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ObservedGeneration int64 `json:"observedGeneration"`
			AvailableReplicas  int32 `json:"availableReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal(document, &deployment); err != nil {
		return false, unavailable("deployment", observe.ReasonMalformed)
	}
	if deployment.Spec.Replicas != nil {
		want = *deployment.Spec.Replicas
	}
	if want == 0 {
		return true, nil
	}
	return deployment.Status.ObservedGeneration >= deployment.Metadata.Generation &&
		deployment.Status.AvailableReplicas >= want, nil
}

// podName is one listed pod's name.
func podName(document []byte) (string, error) {
	var pod struct {
		Metadata struct{ Name string } `json:"metadata"`
	}
	if err := json.Unmarshal(document, &pod); err != nil || pod.Metadata.Name == "" {
		return "", unavailable("pod", observe.ReasonMalformed)
	}
	return pod.Metadata.Name, nil
}

// unavailable is a read that failed part-way through a restore: a failure of this run, never a refusal,
// because the run has already changed something.
func unavailable(subject, reason string) error {
	if reason == "" {
		reason = observe.ReasonNotFound
	}
	return errors.New("unavailable: " + subject + ": " + reason)
}

// failureReason is the fixed reason an adapter error states.
func failureReason(err error) string {
	var failure *kube.Error
	if errors.As(err, &failure) && failure.Reason != "" {
		return failure.Reason
	}
	return observe.ReasonStatus
}
