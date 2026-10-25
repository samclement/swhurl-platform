package observe

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

// condition is one status condition as the API reports it: what it is about, whether it holds, and
// the generation its reporter observed.
type condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	ObservedGeneration int64  `json:"observedGeneration"`
}

// fluxObject is the part of a Flux object's document this slice reads. It is valid only when the
// object's own read was present.
type fluxObject struct {
	generation          int64
	suspended           bool
	conditions          []condition
	lastAppliedRevision string
}

// deploymentObject is the part of a Deployment's document this slice reads. A Deployment reports the
// generation it has observed once, on its status; its conditions carry no generation of their own.
type deploymentObject struct {
	generation        int64
	observed          int64
	availableReplicas int64
	conditions        []condition
}

// podsFact is what the instance's own pods say about its running image. images holds the started
// image of the target's container for each pod that has one; found is whether the selector selected
// any pod; complete is whether every selected pod carries that container; pinned is whether every
// such image is pinned to a digest rather than a tag.
type podsFact struct {
	list     List
	found    bool
	complete bool
	pinned   bool
	images   []string
}

// fact is one Flux object read and, when it was present, its decoded document.
type fact struct {
	read Read
	flux fluxObject
}

// workloadFact is the Deployment read and, when it was present, its decoded document.
type workloadFact struct {
	read   Read
	object deploymentObject
}

// facts is the decoded evidence for one target. A part whose read was not present is zero and is
// never interpreted.
type facts struct {
	unit, release fact
	workload      workloadFact
	pods          podsFact
}

// index matches the evidence to the target's objects. Evidence about anything else, or two answers
// about the same thing, is contradictory input rather than a gap.
func index(target Target, evidence Evidence) (map[Ref]Read, List, error) {
	reads := map[Ref]Read{}
	for _, read := range evidence.Objects {
		if !target.owns(read.Ref) {
			return nil, List{}, invalid("evidence.objects", "unexpected_ref")
		}
		if _, found := reads[read.Ref]; found {
			return nil, List{}, invalid("evidence.objects", "duplicate_ref")
		}
		if err := validateRead(read); err != nil {
			return nil, List{}, err
		}
		reads[read.Ref] = read
	}
	list, err := listOf(target, evidence.Pods)
	if err != nil {
		return nil, List{}, err
	}
	return reads, list, nil
}

// listOf finds the one pod list the target asked for; a list the target did not ask for is refused,
// so no evidence is silently dropped from the report.
func listOf(target Target, lists []List) (List, error) {
	missing := List{Ref: target.Pods, Selector: target.Selector, Status: NotRead}
	for _, list := range lists {
		if list.Ref != target.Pods || list.Selector != target.Selector {
			return List{}, invalid("evidence.pods", "unexpected_list")
		}
		if missing.Status != NotRead {
			return List{}, invalid("evidence.pods", "duplicate_list")
		}
		if err := validateList(list); err != nil {
			return List{}, err
		}
		missing = list
	}
	return missing, nil
}

// owns reports whether the target asked for this single object. The pod collection is read as a list,
// so an object read about it is not evidence the target asked for.
func (t Target) owns(ref Ref) bool {
	return ref == t.Unit || ref == t.Release || ref == t.Workload
}

// validateRead keeps absence, failure and presence apart, so a report cannot claim one from the other.
func validateRead(read Read) error {
	switch read.Status {
	case Present:
		if len(read.Object) == 0 || read.Reason != "" {
			return invalid("evidence.objects", "present_read_incomplete")
		}
	case Absent:
		if len(read.Object) != 0 || read.Reason != ReasonNotFound {
			return invalid("evidence.objects", "absent_read_malformed")
		}
	case Unavailable:
		if len(read.Object) != 0 || !slices.Contains(failureReasons(), read.Reason) {
			return invalid("evidence.objects", "unavailable_read_malformed")
		}
	default:
		return invalid("evidence.objects", "unsupported_status")
	}
	return nil
}

// validateList does the same for a collection read, which has no absent form: an empty list is present.
func validateList(list List) error {
	switch list.Status {
	case Present:
		if list.Reason != "" {
			return invalid("evidence.pods", "present_read_incomplete")
		}
	case Unavailable:
		if len(list.Items) != 0 || !slices.Contains(failureReasons(), list.Reason) {
			return invalid("evidence.pods", "unavailable_read_malformed")
		}
	default:
		return invalid("evidence.pods", "unsupported_status")
	}
	return nil
}

// failureReasons are the fixed names a failed read may carry. The adapter owns them; the pure layer
// refuses anything else rather than copying arbitrary text into a report.
func failureReasons() []string {
	return []string{ReasonForbidden, ReasonTimedOut, ReasonTransport, ReasonStatus, ReasonMalformed, ReasonTooLarge}
}

// factsOf decodes each read the target supplied. A malformed present object is invalid input and
// stops the report: it is not a semantic gap.
func factsOf(target Target, reads map[Ref]Read, list List) (facts, error) {
	out := facts{
		unit:     fact{read: readFor(reads, target.Unit)},
		release:  fact{read: readFor(reads, target.Release)},
		workload: workloadFact{read: readFor(reads, target.Workload)},
	}
	if out.unit.read.Status == Present {
		flux, err := fluxOf("unit", out.unit.read.Object)
		if err != nil {
			return facts{}, err
		}
		out.unit.flux = flux
	}
	if out.release.read.Status == Present {
		flux, err := fluxOf("release", out.release.read.Object)
		if err != nil {
			return facts{}, err
		}
		out.release.flux = flux
	}
	if out.workload.read.Status == Present {
		object, err := deploymentOf(out.workload.read.Object)
		if err != nil {
			return facts{}, err
		}
		out.workload.object = object
	}
	pods, err := podsOf(target, list)
	if err != nil {
		return facts{}, err
	}
	out.pods = pods
	return out, nil
}

// readFor returns the read for one object, or the fact that the caller supplied none.
func readFor(reads map[Ref]Read, ref Ref) Read {
	if read, found := reads[ref]; found {
		return read
	}
	return Read{Ref: ref, Status: NotRead}
}

func fluxOf(field string, data []byte) (fluxObject, error) {
	var document struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Suspend bool `json:"suspend"`
		} `json:"spec"`
		Status struct {
			LastAppliedRevision string      `json:"lastAppliedRevision"`
			Conditions          []condition `json:"conditions"`
		} `json:"status"`
	}
	if err := decodeDocument(data, &document, field); err != nil {
		return fluxObject{}, err
	}
	if document.Metadata.Generation < 1 {
		return fluxObject{}, invalid(field, "missing_generation")
	}
	return fluxObject{document.Metadata.Generation, document.Spec.Suspend, document.Status.Conditions,
		document.Status.LastAppliedRevision}, nil
}

func deploymentOf(data []byte) (deploymentObject, error) {
	var document struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Status struct {
			ObservedGeneration int64       `json:"observedGeneration"`
			AvailableReplicas  int64       `json:"availableReplicas"`
			Conditions         []condition `json:"conditions"`
		} `json:"status"`
	}
	if err := decodeDocument(data, &document, "workload"); err != nil {
		return deploymentObject{}, err
	}
	if document.Metadata.Generation < 1 {
		return deploymentObject{}, invalid("workload", "missing_generation")
	}
	return deploymentObject{document.Metadata.Generation, document.Status.ObservedGeneration,
		document.Status.AvailableReplicas, document.Status.Conditions}, nil
}

// podContainer and podContainerStatus are the two container views a pod document carries: the image
// its spec pins, and the node's record that the container was pulled and started.
type podContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type podContainerStatus struct {
	Name    string `json:"name"`
	ImageID string `json:"imageID"`
}

type podObject struct {
	Spec struct {
		Containers []podContainer `json:"containers"`
	} `json:"spec"`
	Status struct {
		ContainerStatuses []podContainerStatus `json:"containerStatuses"`
	} `json:"status"`
}

// podsOf reads which image the target's container is running in each selected pod. A pod whose
// container status carries no image ID has not pulled its image, so nothing of it is running.
func podsOf(target Target, list List) (podsFact, error) {
	fact := podsFact{list: list, complete: true, pinned: true, images: []string{}}
	if list.Status != Present {
		return fact, nil
	}
	for _, item := range list.Items {
		var pod podObject
		if err := decodeDocument(item, &pod, "pods"); err != nil {
			return podsFact{}, err
		}
		fact.found = true
		image, ok := specImage(pod, target.Container)
		if !ok {
			fact.complete = false
			continue
		}
		if !pinnedImage(image) {
			fact.pinned = false
			continue
		}
		if started(pod, target.Container) {
			fact.images = append(fact.images, image)
		}
	}
	return fact, nil
}

// specImage is the container's image as its pod spec pins it: what a pull by digest runs, whatever
// name the node later records for the same content.
func specImage(pod podObject, name string) (string, bool) {
	for _, candidate := range pod.Spec.Containers {
		if candidate.Name == name {
			return candidate.Image, true
		}
	}
	return "", false
}

// started reports whether the container has been pulled and started in this pod.
func started(pod podObject, name string) bool {
	for _, candidate := range pod.Status.ContainerStatuses {
		if candidate.Name == name {
			return candidate.ImageID != ""
		}
	}
	return false
}

// pinnedImage reports whether an image reference names a digest, which is what makes the running
// content knowable; a tag alone is not the intended artifact.
func pinnedImage(image string) bool {
	_, digest, found := strings.Cut(image, "@")
	return found && digestPattern().MatchString(digest)
}

// statusOf finds one condition by type.
func statusOf(conditions []condition, kind string) (condition, bool) {
	for _, candidate := range conditions {
		if candidate.Type == kind {
			return candidate, true
		}
	}
	return condition{}, false
}

// decodeDocument reads one API document. These bytes are one API server's serialisation, so the strict
// duplicate-key reading shadow applies to authored documents buys nothing here; malformed bytes,
// trailing content and wrong types are still refused.
func decodeDocument(data []byte, into any, field string) error {
	if !utf8.Valid(data) || !json.Valid(data) {
		return invalid(field, "single_utf8_json_required")
	}
	if err := json.Unmarshal(data, into); err != nil {
		return invalid(field, "incorrect_type_or_range")
	}
	return nil
}
