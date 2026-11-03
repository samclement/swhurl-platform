package observe

import (
	"encoding/json"
	"strings"
)

const fluxNameLabel = "kustomize.toolkit.fluxcd.io/name"
const fluxNamespaceLabel = "kustomize.toolkit.fluxcd.io/namespace"
const appInstanceLabel = "app.kubernetes.io/instance"

// ListRequest names a necessary candidate collection, not an instance or an ownership claim.
// Empty Namespace means the explicit all-namespaces operation; a scoped list remains scoped.
type ListRequest struct {
	Ref      Ref
	Selector string
}

func collection(ref Ref) Ref { ref.Name = ""; return ref }
func across(ref Ref) Ref     { ref.Name, ref.Namespace = "", ""; return ref }

func unitQuery(target Target) ListRequest { return ListRequest{collection(target.Unit), fluxNameLabel} }
func ownedQuery(ref Ref) ListRequest {
	return ListRequest{across(ref), fluxNameLabel + "," + fluxNamespaceLabel}
}
func workloadQuery(ref Ref) ListRequest                { return ListRequest{across(ref), appInstanceLabel} }
func backupQuery(ref Ref, selector string) ListRequest { return ListRequest{across(ref), selector} }
func cronSelector(target Target) string {
	before, _, _ := strings.Cut(target.Backup.Selector, ",")
	return before
}

// BulkPlan lists each needed kind/selector once. Jobs are intentionally absent: existing CronJobs
// guarantee labels on pods but not on Jobs. The newest selected pod supplies the one owner GET.
func BulkPlan(targets []Target) []ListRequest {
	requests := []ListRequest{}
	seen := map[ListRequest]bool{}
	for _, target := range targets {
		queries := []ListRequest{unitQuery(target), ownedQuery(target.Release), workloadQuery(target.Workload), workloadQuery(target.Pods)}
		if target.Route != nil {
			queries = append(queries, ownedQuery(target.Route.Ingress))
			for _, certificate := range target.Route.Certificates {
				queries = append(queries, ownedQuery(certificate))
			}
		}
		if target.Backup != nil {
			queries = append(queries, backupQuery(target.Backup.CronJob, cronSelector(target)), backupQuery(target.Backup.Pods, target.Backup.Selector))
		}
		for _, query := range queries {
			if !seen[query] {
				requests = append(requests, query)
				seen[query] = true
			}
		}
	}
	return requests
}

type candidate struct {
	ref      Ref
	labels   map[string]string
	document []byte
}
type indexedList struct {
	list       List
	objects    map[Ref]candidate
	namespaces map[string][]candidate
}

// Candidates indexes complete lists once by identity and namespace. Its contents are private so a
// failed list cannot accidentally supply absence, or another namespace's name supply an object.
type Candidates struct{ lists map[ListRequest]indexedList }

// IndexCandidates validates each item even for non-API readers. Invalid identity invalidates that
// collection, not the whole observation; its dependents receive unavailable evidence.
func IndexCandidates(lists []List) (Candidates, error) {
	result := Candidates{lists: map[ListRequest]indexedList{}}
	for _, list := range lists {
		key := ListRequest{list.Ref, list.Selector}
		if _, found := result.lists[key]; found {
			return Candidates{}, invalid("lists", "duplicate_collection")
		}
		if err := validateList(list); err != nil {
			return Candidates{}, err
		}
		result.lists[key] = indexCandidates(list)
	}
	return result, nil
}

func indexCandidates(list List) indexedList {
	result := indexedList{list: list, objects: map[Ref]candidate{}, namespaces: map[string][]candidate{}}
	if list.Status != Present {
		return result
	}
	for _, item := range list.Items {
		candidate, ok := candidateOf(list.Ref, item)
		_, duplicate := result.objects[candidate.ref]
		if !ok || duplicate {
			result.list = List{Ref: list.Ref, Selector: list.Selector, Status: Unavailable, Reason: ReasonMalformed}
			result.objects, result.namespaces = nil, nil
			return result
		}
		result.objects[candidate.ref] = candidate
		result.namespaces[candidate.ref.Namespace] = append(result.namespaces[candidate.ref.Namespace], candidate)
	}
	return result
}

func candidateOf(collection Ref, document []byte) (candidate, bool) {
	var object struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if decodeDocument(document, &object, "candidate") != nil {
		return candidate{}, false
	}
	ref := Ref{object.APIVersion, object.Kind, object.Metadata.Namespace, object.Metadata.Name}
	if ref.APIVersion != collection.APIVersion || ref.Kind != collection.Kind || ref.Name == "" || ref.Namespace == "" {
		return candidate{}, false
	}
	if validateRef("candidate", ref, true) != nil {
		return candidate{}, false
	}
	if collection.Namespace != "" && ref.Namespace != collection.Namespace {
		return candidate{}, false
	}
	return candidate{ref, object.Metadata.Labels, document}, true
}

// EvidenceFor partitions by expected namespace/name and labels. Extra candidates never create a
// target. Absence can only come from a complete successful collection with no matching object.
func (c Candidates) EvidenceFor(target Target) Evidence {
	evidence := Evidence{Objects: []Read{
		c.object(unitQuery(target), target.Unit, nil),
		c.object(ownedQuery(target.Release), target.Release, fluxOwner(target)),
		c.object(workloadQuery(target.Workload), target.Workload, selectorLabels(target.Selector)),
	}, Pods: []List{c.pods(workloadQuery(target.Pods), target.Pods, target.Selector)}}
	if target.Route != nil {
		evidence.Objects = append(evidence.Objects, c.object(ownedQuery(target.Route.Ingress), target.Route.Ingress, fluxOwner(target)))
		for _, certificate := range target.Route.Certificates {
			evidence.Objects = append(evidence.Objects, c.object(ownedQuery(certificate), certificate, fluxOwner(target)))
		}
	}
	if target.Backup != nil {
		evidence.Backup = &BackupEvidence{CronJob: c.object(backupQuery(target.Backup.CronJob, cronSelector(target)), target.Backup.CronJob, selectorLabels(cronSelector(target))),
			Pods: c.pods(backupQuery(target.Backup.Pods, target.Backup.Selector), target.Backup.Pods, target.Backup.Selector)}
	}
	return evidence
}

func fluxOwner(target Target) map[string]string {
	return map[string]string{fluxNameLabel: target.Unit.Name, fluxNamespaceLabel: target.Unit.Namespace}
}

func (c Candidates) answer(query ListRequest) indexedList {
	if list, ok := c.lists[query]; ok {
		return list
	}
	return indexedList{list: List{Ref: query.Ref, Selector: query.Selector, Status: Unavailable, Reason: ReasonIncomplete}}
}

func (c Candidates) object(query ListRequest, ref Ref, labels map[string]string) Read {
	answer := c.answer(query)
	if answer.list.Status != Present {
		return Read{Ref: ref, Status: Unavailable, Reason: answer.list.Reason}
	}
	if found, ok := answer.objects[ref]; ok && matchesLabels(found.labels, labels) {
		return Read{Ref: ref, Status: Present, Object: found.document}
	}
	return Read{Ref: ref, Status: Absent, Reason: ReasonNotFound}
}

func (c Candidates) pods(query ListRequest, ref Ref, selector string) List {
	answer := c.answer(query)
	list := List{Ref: ref, Selector: selector, Status: answer.list.Status, Reason: answer.list.Reason}
	if list.Status != Present {
		return list
	}
	list.Items = [][]byte{}
	for _, candidate := range answer.namespaces[ref.Namespace] {
		if matchesSelector(candidate.labels, selector) {
			list.Items = append(list.Items, candidate.document)
		}
	}
	return list
}

func selectorLabels(selector string) map[string]string {
	labels := map[string]string{}
	for _, term := range strings.Split(selector, ",") {
		if key, value, ok := strings.Cut(term, "="); ok {
			labels[key] = value
		}
	}
	return labels
}
func matchesLabels(labels, expected map[string]string) bool {
	for key, value := range expected {
		if labels[key] != value {
			return false
		}
	}
	return true
}
func matchesSelector(labels map[string]string, selector string) bool {
	for _, term := range strings.Split(selector, ",") {
		if key, negative := strings.CutPrefix(term, "!"); negative {
			if _, found := labels[key]; found {
				return false
			}
			continue
		}
		key, value, equal := strings.Cut(term, "=")
		actual, found := labels[key]
		if !found || (equal && actual != value) {
			return false
		}
	}
	return true
}

// InstanceReport binds a report to the record identity, never to a candidate discovered in Kubernetes.
type InstanceReport struct {
	Application, Environment string
	Report                   Report
}

// Collection is one bulk invocation, including the stable empty-records answer.
type Collection struct {
	Source, ObservedAt, Revision string
	Instances                    []InstanceReport
}

// EncodeCollection wraps unchanged v4 individual reports in a versioned v2-observations envelope.
func EncodeCollection(collection Collection) ([]byte, error) {
	type entry struct {
		Application string          `json:"application"`
		Environment string          `json:"environment"`
		Report      json.RawMessage `json:"report"`
	}
	document := struct {
		Kind       string  `json:"kind"`
		Version    int     `json:"version"`
		Source     string  `json:"source"`
		ObservedAt string  `json:"observedAt"`
		Revision   string  `json:"revision"`
		Instances  []entry `json:"instances"`
	}{Kind: "v2-observations", Version: 1, Source: collection.Source, ObservedAt: collection.ObservedAt, Revision: collection.Revision, Instances: []entry{}}
	for _, instance := range collection.Instances {
		report, err := EncodeReport(instance.Report)
		if err != nil {
			return nil, err
		}
		document.Instances = append(document.Instances, entry{instance.Application, instance.Environment, report})
	}
	return json.Marshal(document)
}
