// Package datadestroy decides destroy-data, the only command that deletes data: which claim or volume a target
// names, whether the operator repeated it, whether anything still uses or would recreate it, and the changes that
// delete it. It reads the documents it is given and has no effects; the messages are the operator's, in the words
// make destroy-data has always used.
package datadestroy

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Usage is the command's own usage line, printed for a target of a kind it does not take.
const Usage = "destroy-data pvc/<ns>/<name> | pv/<name>     (CONFIRM=<same target>)"

// FluxNamespace is where a claim's Flux Kustomization lives when its label does not say.
const FluxNamespace = "flux-system"

// ReclaimDelete is the patch that makes the provisioner remove a volume's host directory once it is released; with
// Retain the directory would be left behind.
const ReclaimDelete = `{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`

// Kind is what a target names: a claim, or a volume whose claim is already gone.
type Kind string

const (
	Claim  Kind = "pvc"
	Volume Kind = "pv"
)

// Target is one parsed target. Namespace is empty for a volume; Text is the target as the operator wrote it.
type Target struct {
	Kind            Kind
	Namespace, Name string
	Text            string
}

// Refusal is a decision not to delete: printed as one [ERROR] line, exit 2, with nothing changed.
type Refusal struct{ Message string }

func (r *Refusal) Error() string { return r.Message }

// ErrUsage is a target of a kind destroy-data does not take: the usage line, exit 2.
var ErrUsage = errors.New(Usage)

func refuse(format string, args ...any) error { return &Refusal{fmt.Sprintf(format, args...)} }

// ParseTarget reads pvc/<namespace>/<name> or pv/<name>. Its shape is checked before anything reaches a cluster.
func ParseTarget(text string) (Target, error) {
	parts := strings.Split(text, "/")
	kind, parts := Kind(parts[0]), parts[1:]
	if len(parts) > 2 {
		return Target{}, refuse("Invalid target: %s", text)
	}
	count, hint := 0, ""
	switch kind {
	case Claim:
		count, hint = 2, "Use pvc/<namespace>/<name>"
	case Volume:
		count, hint = 1, "Use pv/<name>"
	default:
		return Target{}, ErrUsage
	}
	if len(parts) != count || slices.Contains(parts, "") {
		return Target{}, refuse("%s", hint)
	}
	if kind == Volume {
		return Target{Kind: kind, Name: parts[0], Text: text}, nil
	}
	return Target{Kind: kind, Namespace: parts[0], Name: parts[1], Text: text}, nil
}

// Confirm refuses unless confirm repeats the target exactly.
func Confirm(target Target, confirm string) error {
	if confirm != target.Text {
		return refuse("destroy-data permanently deletes %s and its data. Re-run with CONFIRM=%s", target.Text, target.Text)
	}
	return nil
}

// ClaimFacts are what a claim's own document says about where its data is and what would recreate it.
type ClaimFacts struct {
	Volume        string // the bound volume; empty for a claim that never bound
	Release       string // the Helm release that installed it, from meta.helm.sh/release-name
	Unit          string // the Flux Kustomization that applied it, from kustomize.toolkit.fluxcd.io/name
	UnitNamespace string // that Kustomization's namespace, FluxNamespace when its label does not say
}

type objectDocument struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
		Labels      map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		VolumeName string `json:"volumeName"`
		Volumes    []struct {
			PersistentVolumeClaim *struct {
				ClaimName string `json:"claimName"`
			} `json:"persistentVolumeClaim"`
		} `json:"volumes"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

func decode(object []byte, subject string) (objectDocument, error) {
	var document objectDocument
	if json.Unmarshal(object, &document) != nil {
		return document, fmt.Errorf("failed: %s: malformed", subject)
	}
	return document, nil
}

// ClaimFactsOf reads a claim's document.
func ClaimFactsOf(object []byte) (ClaimFacts, error) {
	document, err := decode(object, "pvc")
	if err != nil {
		return ClaimFacts{}, err
	}
	facts := ClaimFacts{Volume: document.Spec.VolumeName, Release: document.Metadata.Annotations["meta.helm.sh/release-name"],
		Unit: document.Metadata.Labels["kustomize.toolkit.fluxcd.io/name"], UnitNamespace: FluxNamespace}
	if namespace := document.Metadata.Labels["kustomize.toolkit.fluxcd.io/namespace"]; namespace != "" {
		facts.UnitNamespace = namespace
	}
	return facts, nil
}

// HelmSelector picks the Secrets in which Helm records a release: while one exists, the release would recreate the
// claim.
func HelmSelector(release string) string { return "owner=helm,name=" + release }

// MountedByActivePod says whether a running, pending or terminating pod mounts the claim; finished pods do not
// hold it.
func MountedByActivePod(pods [][]byte, claim string) (bool, error) {
	for _, pod := range pods {
		document, err := decode(pod, "pods")
		if err != nil {
			return false, err
		}
		if document.Status.Phase == "Succeeded" || document.Status.Phase == "Failed" {
			continue
		}
		for _, volume := range document.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claim {
				return true, nil
			}
		}
	}
	return false, nil
}

// VolumeReleased refuses a volume that is not Released: a bound one is destroyed through its claim.
func VolumeReleased(target Target, object []byte) error {
	document, err := decode(object, "pv")
	if err != nil {
		return err
	}
	if document.Status.Phase != "Released" {
		return refuse("PV %s is not Released; destroy its claim with pvc/<namespace>/<name> instead", target.Name)
	}
	return nil
}

// ClaimNotFound refuses a claim the cluster does not have.
func ClaimNotFound(target Target) error {
	return refuse("PVC %s/%s not found", target.Namespace, target.Name)
}

// VolumeNotFound refuses a volume the cluster does not have.
func VolumeNotFound(target Target) error { return refuse("PV %s not found", target.Name) }

// ClaimMounted refuses a claim an active pod mounts.
func ClaimMounted(target Target) error {
	return refuse("PVC %s/%s is mounted by a pod; stop or uninstall the workload first", target.Namespace, target.Name)
}

// ReleaseExists refuses a claim whose Helm release would recreate it.
func ReleaseExists(target Target, release string) error {
	return refuse("Helm release %s/%s still exists and would recreate the claim; uninstall it first", target.Namespace, release)
}

// UnitExists refuses a claim whose Flux Kustomization would recreate it.
func UnitExists(unit string) error {
	return refuse("Flux Kustomization %s still manages the claim; remove it from Git first", unit)
}

// Change is one of the changes that delete the data, in the order they are made.
type Change string

const (
	SetReclaimDelete Change = "reclaim" // the volume's reclaim policy becomes Delete
	DeleteClaim      Change = "claim"   // the claim is deleted, and gone
	AwaitVolumeGone  Change = "volume"  // the provisioner has deleted the volume and its host directory
)

// Changes are what destroys the target's data once every check has passed. The reclaim policy changes first, so the
// provisioner deletes the directory when the claim goes; a claim that never bound has no volume to wait for.
func Changes(target Target, volume string) []Change {
	var changes []Change
	if volume != "" {
		changes = append(changes, SetReclaimDelete)
	}
	if target.Kind == Claim {
		changes = append(changes, DeleteClaim)
	}
	if volume != "" {
		changes = append(changes, AwaitVolumeGone)
	}
	return changes
}

// Plan is what a dry run prints once every check has passed: the header, then each change it would make.
func Plan(target Target, volume string) []string {
	lines := []string{fmt.Sprintf("Plan (destroy-data %s): all checks passed", target.Text)}
	for _, change := range Changes(target, volume) {
		switch change {
		case SetReclaimDelete:
			lines = append(lines, fmt.Sprintf("  would set PV %s's reclaim policy to Delete", volume))
		case DeleteClaim:
			lines = append(lines, fmt.Sprintf("  would delete PVC %s/%s and wait for it to go", target.Namespace, target.Name))
		case AwaitVolumeGone:
			lines = append(lines, fmt.Sprintf("  would wait for PV %s and its data to go", volume))
		}
	}
	return lines
}

// Destroyed is the line a completed run prints.
func Destroyed(target Target, volume string) string {
	if volume == "" {
		return "[OK] Destroyed " + target.Text
	}
	return fmt.Sprintf("[OK] Destroyed %s (PV %s and its data)", target.Text, volume)
}
