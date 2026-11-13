package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// A unit action is one of the three changes the console makes to a Flux unit: reconcile it, suspend it, or resume it
// (which also reconciles). make suspend and make resume also suspend and resume one HelmRelease. The console keeps
// the lines this prints as the job's output and reads the unit's page afterwards. Nothing here runs kubectl or the
// flux command: the unit, its source and the selected release are read and patched through the API server.

// FluxAction is one of the three changes a unit action makes.
type FluxAction string

const (
	FluxReconcile FluxAction = "reconcile"
	FluxSuspend   FluxAction = "suspend"
	FluxResume    FluxAction = "resume"
)

const (
	fluxNamespace   = "flux-system"
	fluxTimeout     = 10 * time.Minute
	fluxPoll        = 2 * time.Second
	fluxRequestedAt = "reconcile.fluxcd.io/requestedAt"
	fluxUnitGroup   = "kustomize.toolkit.fluxcd.io"
	fluxHelmGroup   = "helm.toolkit.fluxcd.io"
	fluxSourceGroup = "source.toolkit.fluxcd.io"
)

// fluxSources is the kinds of source a unit may name: a reconcile requests the source before the unit, because the
// unit applies what the source fetched.
var fluxSources = map[string]bool{"GitRepository": true, "OCIRepository": true, "Bucket": true}

// fluxRoots are the units make flux-bootstrap applies: Flux does not reconcile them, so no action here may.
var fluxRoots = map[string]bool{"cluster-sources": true, "cluster-stack": true}

// FluxInputs is one unit action: what to do, to which unit or release, on which cluster, and how time and lines
// arrive. Every port is supplied by the caller, so the command prints and a test waits without sleeping.
type FluxInputs struct {
	Action FluxAction
	Unit   string
	// Release is the HelmRelease a suspend or resume changes instead of a unit; nil is the unit.
	Release  *FluxRelease
	Cluster  Cluster
	Now      func() time.Time
	Sleep    func(time.Duration)
	Progress func(string)
}

// FluxRelease is one HelmRelease a suspend or resume changes.
type FluxRelease struct {
	Namespace string
	Name      string
}

// Flux performs one unit action. It refuses a root unit before touching the cluster, and a reconcile of a suspended
// unit before annotating anything: a refused action makes no write.
func Flux(ctx context.Context, inputs FluxInputs) error {
	if (inputs.Unit == "") == (inputs.Release == nil) {
		return errors.New("invalid: target: name exactly one unit or release")
	}
	run := &fluxRun{ctx: ctx, inputs: inputs}
	if inputs.Release != nil {
		switch inputs.Action {
		case FluxSuspend, FluxResume:
			return run.setSuspended("HelmRelease", inputs.Release.Namespace, inputs.Release.Name,
				inputs.Action == FluxSuspend)
		case FluxReconcile:
			return errors.New("a HelmRelease is reconciled with the unit that applies it: reconcile the unit")
		}
		return &Refusal{"unit.action", "not_reconcile_suspend_or_resume"}
	}
	if fluxRoots[inputs.Unit] {
		return fmt.Errorf("%s is applied by make flux-bootstrap, not by Flux", inputs.Unit)
	}
	switch inputs.Action {
	case FluxReconcile:
		return run.reconcile()
	case FluxSuspend, FluxResume:
		return run.setSuspended("Kustomization", fluxNamespace, inputs.Unit, inputs.Action == FluxSuspend)
	}
	return &Refusal{"unit.action", "not_reconcile_suspend_or_resume"}
}

// fluxRun is one action's run: the cluster it changes and the lines it reports.
type fluxRun struct {
	ctx    context.Context
	inputs FluxInputs
}

// say reports one line to whoever shows the job: the console's job page today, a person's terminal otherwise.
func (r *fluxRun) say(format string, args ...any) {
	if r.inputs.Progress != nil {
		r.inputs.Progress(fmt.Sprintf(format, args...))
	}
}

// reconcile requests the unit's source and then the unit itself, and waits for each to report the request handled.
func (r *fluxRun) reconcile() error {
	unit, err := r.unit()
	if err != nil {
		return err
	}
	if unit.Spec.Suspend {
		return fmt.Errorf("%s is suspended; resume it instead (resume also reconciles)", r.inputs.Unit)
	}
	if !fluxSources[unit.sourceKind()] {
		return fmt.Errorf("%s has an unsupported source kind %s", r.inputs.Unit, quotedSourceKind(unit.Spec.SourceRef))
	}
	if err := r.request(unit.Spec.SourceRef.Kind, unit.sourceNamespace(), unit.Spec.SourceRef.Name); err != nil {
		return err
	}
	return r.request("Kustomization", fluxNamespace, r.inputs.Unit)
}

// setSuspended suspends the unit or release, or resumes it and reconciles: resuming one that Flux has not applied
// would leave the cluster behind Git.
func (r *fluxRun) setSuspended(kind, namespace, name string, suspend bool) error {
	action := "resuming"
	if suspend {
		action = "suspending"
	}
	where := ""
	if namespace != fluxNamespace {
		where = " in " + namespace + " namespace"
	}
	r.say("► %s %s %s%s", action, kind, name, where)
	ref, err := fluxRefOf(kind, namespace, name)
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]bool{"suspend": suspend}})
	if err != nil {
		return err
	}
	if err := r.inputs.Cluster.Writer.Patch(r.ctx, ref, patch); err != nil {
		if failureReason(err) == observe.ReasonNotFound {
			return fmt.Errorf("no %s named '%s'%s", kind, name, where)
		}
		return err
	}
	if suspend {
		r.say("✔ %s %s suspended", kind, name)
		return nil
	}
	return r.request(kind, namespace, name)
}

// request annotates one object with a token, then waits until the controller reports that request handled.
func (r *fluxRun) request(kind, namespace, name string) error {
	ref, err := fluxRefOf(kind, namespace, name)
	if err != nil {
		return err
	}
	token := r.inputs.Now().UTC().Format(time.RFC3339Nano)
	r.say("► annotating %s %s in %s namespace", kind, name, namespace)
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{fluxRequestedAt: token}}})
	if err != nil {
		return err
	}
	if err := r.inputs.Cluster.Writer.Patch(r.ctx, ref, patch); err != nil {
		return err
	}
	r.say("◎ waiting for %s reconciliation", kind)
	return r.wait(kind, ref, token)
}

// wait polls one object until this request is handled and Ready says how it went, the deadline passes, or the read
// fails. A handled request that is not Ready has failed, and the controller's own message is the reason.
func (r *fluxRun) wait(kind string, ref observe.Ref, token string) error {
	started := r.inputs.Now()
	for {
		state, err := r.stateOf(kind, ref)
		if err != nil {
			return err
		}
		ready, message := state.ready()
		handled := state.Status.LastHandledReconcileAt == token &&
			state.Status.ObservedGeneration == state.Metadata.Generation
		if handled && ready == "True" {
			r.applied(kind, state.Status)
			return nil
		}
		if handled && ready == "False" {
			return fmt.Errorf("%s %s reconciliation failed: %s", kind, ref.Name, message)
		}
		if r.inputs.Now().Sub(started) >= fluxTimeout {
			return fmt.Errorf("timed out after %.0fm waiting for %s %s; it may still finish (check the unit page)",
				fluxTimeout.Minutes(), kind, ref.Name)
		}
		r.inputs.Sleep(fluxPoll)
	}
}

// applied is what a handled request fetched or applied, as the unit's own status states it.
func (r *fluxRun) applied(kind string, status fluxStatus) {
	switch kind {
	case "Kustomization":
		r.say("✔ applied revision %s", status.LastAppliedRevision)
		return
	case "HelmRelease":
		r.say("✔ applied revision %s", status.LastAttemptedRevision)
		return
	}
	r.say("✔ fetched revision %s", status.Artifact.Revision)
}

// unit reads the Kustomization this action is for.
func (r *fluxRun) unit() (fluxUnit, error) {
	ref, err := fluxRefOf("Kustomization", fluxNamespace, r.inputs.Unit)
	if err != nil {
		return fluxUnit{}, err
	}
	read, err := r.inputs.Cluster.Reader.Read(r.ctx, ref)
	if err != nil {
		return fluxUnit{}, err
	}
	if read.Status != observe.Present {
		return fluxUnit{}, absentOrUnavailable("Kustomization", r.inputs.Unit, read)
	}
	var unit fluxUnit
	if err := json.Unmarshal(read.Object, &unit); err != nil {
		return fluxUnit{}, unavailable("Kustomization "+r.inputs.Unit, observe.ReasonMalformed)
	}
	return unit, nil
}

// stateOf reads one object's generation and status, the two facts a wait compares.
func (r *fluxRun) stateOf(kind string, ref observe.Ref) (fluxState, error) {
	read, err := r.inputs.Cluster.Reader.Read(r.ctx, ref)
	if err != nil {
		return fluxState{}, err
	}
	if read.Status != observe.Present {
		return fluxState{}, absentOrUnavailable(kind, ref.Name, read)
	}
	var state fluxState
	if err := json.Unmarshal(read.Object, &state); err != nil {
		return fluxState{}, unavailable(kind+" "+ref.Name, observe.ReasonMalformed)
	}
	return state, nil
}

// absentOrUnavailable is the refusal a missing object and a failed read each state: the console shows both.
func absentOrUnavailable(kind, name string, read observe.Read) error {
	if read.Status == observe.Absent {
		return fmt.Errorf("no %s named '%s'", kind, name)
	}
	return unavailable(kind+" "+name, reasonOf(read))
}

// fluxRefOf is the reference to one object this action reads or annotates: a unit, one of its sources, or a release.
func fluxRefOf(kind, namespace, name string) (observe.Ref, error) {
	group := fluxSourceGroup
	switch kind {
	case "Kustomization":
		group = fluxUnitGroup
	case "HelmRelease":
		group = fluxHelmGroup
	}
	return kube.RefOf(group, kind, namespace, name)
}

// quotedSourceKind is how a source kind this action cannot request is named, as the console words a missing one.
func quotedSourceKind(ref *fluxRef) string {
	if ref == nil || ref.Kind == "" {
		return "None"
	}
	return "'" + ref.Kind + "'"
}

// fluxUnit is a Kustomization as this action reads it: what it is suspended by, what it applies from, and how far it
// has got.
type fluxUnit struct {
	Metadata fluxMetadata `json:"metadata"`
	Spec     struct {
		Suspend   bool     `json:"suspend"`
		SourceRef *fluxRef `json:"sourceRef"`
	} `json:"spec"`
	Status fluxStatus `json:"status"`
}

// sourceKind is the kind of source the unit applies from, or "" when it names none.
func (u fluxUnit) sourceKind() string {
	if u.Spec.SourceRef == nil {
		return ""
	}
	return u.Spec.SourceRef.Kind
}

// sourceNamespace is the namespace the unit's source lives in; the unit's own when it names none.
func (u fluxUnit) sourceNamespace() string {
	if u.Spec.SourceRef == nil || u.Spec.SourceRef.Namespace == "" {
		return fluxNamespace
	}
	return u.Spec.SourceRef.Namespace
}

// fluxRef is a unit's source reference.
type fluxRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// fluxMetadata is the generation a request has to reach.
type fluxMetadata struct {
	Generation int64 `json:"generation"`
}

// fluxStatus is a Flux object's status: the reconcile request it last handled, the revision it applied or fetched,
// and its conditions.
type fluxStatus struct {
	ObservedGeneration     int64  `json:"observedGeneration"`
	LastHandledReconcileAt string `json:"lastHandledReconcileAt"`
	LastAppliedRevision    string `json:"lastAppliedRevision"`
	LastAttemptedRevision  string `json:"lastAttemptedRevision"`
	Artifact               struct {
		Revision string `json:"revision"`
	} `json:"artifact"`
	Conditions []fluxCondition `json:"conditions"`
}

// fluxState is the two facts a wait compares, from either a unit or a source.
type fluxState struct {
	Metadata fluxMetadata `json:"metadata"`
	Status   fluxStatus   `json:"status"`
}

// ready is the first Ready condition's status and message; a missing condition is not ready.
func (s fluxState) ready() (string, string) {
	for _, condition := range s.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status, condition.Message
		}
	}
	return "", ""
}

// fluxCondition is one condition a Flux object reports.
type fluxCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
}
