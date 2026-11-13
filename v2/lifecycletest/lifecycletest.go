// Package lifecycletest decides live-test-lifecycle: the fixed names it touches, the documents it creates, what each
// object it reads says, and the verdict on each check. It reads the documents it is given and has no effects; the
// messages are the operator's, in the words make live-test-lifecycle has always used.
package lifecycletest

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Plan is what the test does, printed by a dry run.
const Plan = `Prove the lifecycle contract on a disposable app (tests/fixtures/lifecycle-app,
reconciled from the pushed Git revision). Touches only resources it creates:
Flux Kustomizations named lifecycle-test*, namespace lifecycle-test and its PVs.

  1. suspend leaves the workload and data running; resume reconciles again
  2. uninstall (deleting an app-style unit) removes the workload but keeps
     the prune-protected namespace, claim and data
  3. destroy-data refuses without CONFIRM, then deletes the claim, PV and data
  4. a unit with deletionPolicy: Orphan (as the shared units use) leaves its
     resources running when the unit itself is deleted`

// Passed is the line a run with no failed check prints before its cleanup.
const Passed = "Lifecycle test passed."

// The objects the test creates and reads: two units in FluxNamespace, and in Namespace the fixture's claim and
// workload, and the short-lived pod that reads the claim after the workload is gone.
const (
	FluxNamespace = "flux-system"
	Unit          = "lifecycle-test"
	OrphanUnit    = "lifecycle-test-orphan"
	Namespace     = "lifecycle-test"
	Label         = "platform.swhurl.com/lifecycle-test"
	Claim         = "data"
	Workload      = "writer"
	Container     = "writer"
	Selector      = "app=writer"
	ReaderPod     = "reader"
	Marker        = "/data/marker"
	FixturePath   = "./tests/fixtures/lifecycle-app"
)

// Target is the claim destroy-data is asked to delete.
const Target = "pvc/" + Namespace + "/" + Claim

// The sections, in the order the test runs them.
const (
	InstallStep   = "Install disposable app"
	SuspendStep   = "Suspend and resume"
	UninstallStep = "Uninstall (delete app unit)"
	DestroyStep   = "Destroy data"
	OrphanStep    = "Orphan deletion policy (shared units)"
)

// DeletionPolicy is what Flux does with a unit's objects when the unit itself is deleted.
type DeletionPolicy string

const (
	MirrorPrune DeletionPolicy = "MirrorPrune"
	Orphan      DeletionPolicy = "Orphan"
)

// Level is how a line is shown.
type Level string

const (
	Section Level = "section"
	OK      Level = "ok"
	Bad     Level = "bad"
	Info    Level = "info"
	// Done is the passed line, shown before the cleanup's.
	Done Level = "done"
)

// Line is one line of the report.
type Line struct {
	Level Level
	Text  string
}

// Preflight is why the test cannot start: leftovers from an earlier run, or a cluster it cannot read.
type Preflight struct{ Reason string }

func (p *Preflight) Error() string { return p.Reason }

// Leftovers is the refusal when the namespace or either unit already exists.
func Leftovers() error {
	return &Preflight{Reason: Namespace + " namespace or " + Unit + " Kustomizations already exist; clean up first"}
}

// UnitDocument is the Kustomization that applies the fixture, as make live-test-lifecycle has always created it.
func UnitDocument(name string, policy DeletionPolicy) []byte {
	return encode(map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": name, "namespace": FluxNamespace},
		"spec": map[string]any{"interval": "10m", "sourceRef": map[string]any{"kind": "GitRepository", "name": "swhurl-platform"},
			"path": FixturePath, "prune": true, "deletionPolicy": string(policy), "wait": true, "timeout": "5m"},
	})
}

// ReaderDocument is the pod that prints the claim's marker and exits: it mounts the claim with no service-account
// token and is never restarted.
func ReaderDocument() []byte {
	return encode(map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": ReaderPod, "namespace": Namespace},
		"spec": map[string]any{
			"automountServiceAccountToken": false,
			"restartPolicy":                "Never",
			"volumes":                      []any{map[string]any{"name": "d", "persistentVolumeClaim": map[string]any{"claimName": Claim}}},
			"containers": []any{map[string]any{"name": ReaderPod, "image": "busybox:1.37", "command": []string{"cat", Marker},
				"volumeMounts": []any{map[string]any{"name": "d", "mountPath": "/data"}}}},
		},
	})
}

// ReclaimDelete is the patch that lets the provisioner delete a volume and its directory once its claim is gone.
const ReclaimDelete = `{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`

// encode is one of this package's own documents as JSON; each is a fixed shape that always encodes.
func encode(document map[string]any) []byte {
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil
	}
	return encoded
}

// object is what the test reads of any object it looks at.
type object struct {
	Metadata struct {
		Labels            map[string]string `json:"labels"`
		DeletionTimestamp string            `json:"deletionTimestamp"`
		Name              string            `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Suspend    bool   `json:"suspend"`
		VolumeName string `json:"volumeName"`
		ClaimRef   *struct {
			Namespace string `json:"namespace"`
		} `json:"claimRef"`
	} `json:"spec"`
	Status struct {
		Phase             string      `json:"phase"`
		AvailableReplicas int         `json:"availableReplicas"`
		Conditions        []condition `json:"conditions"`
	} `json:"status"`
}

// condition is one condition an object reports.
type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

func decode(document []byte, subject string) (object, error) {
	var read object
	if err := json.Unmarshal(document, &read); err != nil {
		return object{}, fmt.Errorf("%s: the answer is not an object", subject)
	}
	return read, nil
}

// Ready is whether a unit's Ready condition is True, as kubectl wait --for=condition=Ready asks.
func Ready(unit []byte) (bool, error) {
	read, err := decode(unit, "Kustomization")
	return slices.Contains(read.Status.Conditions, condition{"Ready", "True"}), err
}

// Suspended is whether a unit's spec says it is suspended.
func Suspended(unit []byte) (bool, error) {
	read, err := decode(unit, "Kustomization")
	return read.Spec.Suspend, err
}

// Available is whether the workload has its one replica available.
func Available(deployment []byte) (bool, error) {
	read, err := decode(deployment, "Deployment")
	return read.Status.AvailableReplicas == 1, err
}

// VolumeOf is the volume a claim is bound to; empty while it is not bound.
func VolumeOf(claim []byte) (string, error) {
	read, err := decode(claim, "PersistentVolumeClaim")
	return read.Spec.VolumeName, err
}

// PhaseOf is an object's status phase: a claim's Bound, a pod's Running or Succeeded.
func PhaseOf(document []byte) (string, error) {
	read, err := decode(document, "object")
	return read.Status.Phase, err
}

// Labelled is whether the namespace carries the label that marks it as this test's.
func Labelled(namespace []byte) bool {
	read, err := decode(namespace, "Namespace")
	return err == nil && read.Metadata.Labels[Label] == "true"
}

// Claimed is whether a volume's claim lived in the test's namespace: the only volumes cleanup may delete.
func Claimed(volume []byte) bool {
	read, err := decode(volume, "PersistentVolume")
	return err == nil && read.Spec.ClaimRef != nil && read.Spec.ClaimRef.Namespace == Namespace
}

// RunningPod is the first listed workload pod that is Running and not being deleted: the one the marker is read in.
func RunningPod(pods [][]byte) (string, error) {
	for _, pod := range pods {
		read, err := decode(pod, "Pod")
		if err != nil {
			return "", err
		}
		if read.Status.Phase == "Running" && read.Metadata.DeletionTimestamp == "" {
			return read.Metadata.Name, nil
		}
	}
	return "", fmt.Errorf("no Running pod of deploy/%s in %s", Workload, Namespace)
}

// check is one verdict: ok when it passed, bad otherwise.
func check(passed bool, ok, bad string) Line {
	if passed {
		return Line{OK, ok}
	}
	return Line{Bad, bad}
}

// Wrote is the install's verdict: the workload wrote its marker to the claim's volume.
func Wrote(marker, volume string) Line {
	return check(marker != "", "workload wrote data to "+volume, "no marker written")
}

// UnitSuspended is the verdict on the suspended unit's spec.
func UnitSuspended(suspended bool) Line {
	return check(suspended, "unit suspended", "unit not suspended")
}

// RunningWhileSuspended is the verdict on the workload while its unit is suspended.
func RunningWhileSuspended(available bool) Line {
	return check(available, "workload still running while suspended", "workload stopped while suspended")
}

// ResumedReady is the verdict on the unit after resume.
func ResumedReady(ready bool) Line {
	return check(ready, "unit resumed and Ready", "unit not Ready after resume")
}

// Pruned is the verdict on the workload once its unit is deleted.
func Pruned(gone bool) Line {
	return check(gone, "workload pruned", "workload not pruned")
}

// NamespaceKept is the verdict on the prune-protected namespace after uninstall.
func NamespaceKept(present bool) Line {
	return check(present, "prune-protected namespace kept", "namespace deleted")
}

// ClaimKept is the verdict on the prune-protected claim after uninstall, from its phase ("" when it is gone).
func ClaimKept(phase string) Line {
	return check(phase == "Bound", "prune-protected claim kept", "claim deleted")
}

// DataIntact is the verdict on the marker read back after uninstall.
func DataIntact(original, read string) Line {
	return check(read == original, "data intact after uninstall", "data changed or missing after uninstall")
}

// DestroyRefused is the verdict on destroy-data without a confirmation.
func DestroyRefused(refused bool) Line {
	return check(refused, "destroy-data refuses without CONFIRM", "destroy-data ran without CONFIRM")
}

// ClaimUntouched is the verdict on the claim after the refused destroy.
func ClaimUntouched(present bool) Line {
	return check(present, "claim untouched by refused destroy", "claim removed by refused destroy")
}

// ClaimDestroyed is the verdict on the claim after the confirmed destroy.
func ClaimDestroyed(present bool) Line {
	return check(!present, "claim destroyed", "claim still exists")
}

// VolumeDestroyed is the verdict on the claim's volume after the confirmed destroy.
func VolumeDestroyed(volume string, present bool) Line {
	return check(!present, "PV "+volume+" and its data destroyed", "PV "+volume+" still exists")
}

// OrphanKept is the verdict on the workload after its Orphan unit is deleted.
func OrphanKept(available bool) Line {
	return check(available, "workload kept running after its Orphan unit was deleted", "Orphan unit deletion removed the workload")
}

// Failed is a step that could not go on: what failed, in v2's words. The steps after it do not run.
func Failed(reason string) Line {
	return Line{Bad, reason}
}

// CleanedUp is the cleanup's one line, printed whatever the steps found.
func CleanedUp() Line {
	return Line{Info, "Cleaned up " + Namespace + " test resources"}
}

// Conclusion is the passed line when no step's line is a failed check, and nothing otherwise.
func Conclusion(lines []Line) []Line {
	if !Passes(lines) {
		return nil
	}
	return []Line{{Done, Passed}}
}

// Passes is whether no line is a failed check.
func Passes(lines []Line) bool {
	return !slices.ContainsFunc(lines, func(line Line) bool { return line.Level == Bad })
}

// Format is one line as the operator reads it: a section and the passed line each follow an empty line.
func Format(line Line) string {
	switch line.Level {
	case Section:
		return "\n== " + line.Text + " =="
	case Done:
		return "\n" + line.Text
	}
	return fmt.Sprintf("[%s] %s", strings.ToUpper(string(line.Level)), line.Text)
}

// Text is the lines as the operator reads them, one per line.
func Text(lines []Line) string {
	text := make([]string, 0, len(lines))
	for _, line := range lines {
		text = append(text, Format(line))
	}
	return strings.Join(text, "\n")
}
