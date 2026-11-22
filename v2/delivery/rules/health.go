package rules

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Deployment is what one instance's deployment health is judged on, as the API server answered: its Flux unit, its
// HelmRelease (nil when there is none), and every Deployment, StatefulSet and Pod read. The instance's own workloads
// and pods are those in Namespace labelled with its instance Name and the main controller.
type Deployment struct {
	Namespace, Name string
	Unit, Release   map[string]any
	Workloads, Pods []map[string]any
}

// DeploymentHealth is where a deployed instance stands.
type DeploymentHealth string

const (
	DeploymentUnjudged  DeploymentHealth = "unjudged"  // no release while reconciliation is suspended, or an uninstall in progress
	DeploymentHealthy   DeploymentHealth = "healthy"   // the workload is Ready and runs the requested release, or a rollback restored it
	DeploymentStopped   DeploymentHealth = "stopped"   // the release wants no replicas
	DeploymentSuspended DeploymentHealth = "suspended" // the workload is Ready and reconciliation is suspended
	DeploymentUnhealthy DeploymentHealth = "unhealthy" // a workload is missing, unreadable or not all Ready
	DeploymentStalled   DeploymentHealth = "stalled"   // the requested release has not landed
)

// DeploymentVerdict is a deployment's health and the fact that decided it, in words for a person.
type DeploymentVerdict struct {
	Health DeploymentHealth
	Detail string
	// RolledBack qualifies healthy: remediation restored the previous release, not the requested upgrade.
	RolledBack bool
}

// DeploymentHealthOf judges a deployment by its release, then requires its Flux unit to be Ready: a healthy release
// whose unit is blocked or still applying the request has stalled. A completed rollback is healthy whatever the unit.
func DeploymentHealthOf(d Deployment) DeploymentVerdict {
	return deploymentHealth(d, true)
}

// ReleaseHealthOf judges a deployment by its release alone. The rules run in this order and the first that matches
// wins:
//
//	unjudged   no release and the unit is suspended, or the release is being deleted
//	stalled    no release
//	stopped    the release wants no replicas
//	unhealthy  no owned workload, one without a replica count, or fewer Ready replicas than wanted
//	suspended  the release or the unit is suspended
//	healthy    a completed rollback at the release's generation
//	stalled    the release is not Ready at its generation, or a requested digest is not what every main container runs
//	healthy    otherwise
func ReleaseHealthOf(d Deployment) DeploymentVerdict {
	return deploymentHealth(d, false)
}

// RequestedImage is the main container image a HelmRelease asks for: repository, then `:tag` and `@digest` when
// given, and `?` for a missing repository.
func RequestedImage(release map[string]any) string {
	image := field(release, "spec", "values", "controllers", "main", "containers", "main", "image")
	result := fallback(text(image, "repository"), "?")
	if tag := text(image, "tag"); tag != "" {
		result += ":" + tag
	}
	if digest := text(image, "digest"); digest != "" {
		result += "@" + digest
	}
	return result
}

func deploymentHealth(d Deployment, unit bool) DeploymentVerdict {
	release := d.Release
	if release == nil {
		if flag(field(d.Unit, "spec"), "suspend") {
			return DeploymentVerdict{Health: DeploymentUnjudged, Detail: "No release yet and deployment reconciliation is suspended"}
		}
		return DeploymentVerdict{Health: DeploymentStalled, Detail: "HelmRelease has not been created"}
	}
	if text(field(release, "metadata"), "deletionTimestamp") != "" {
		return DeploymentVerdict{Health: DeploymentUnjudged, Detail: "Uninstall is in progress"}
	}
	main := field(release, "spec", "values", "controllers", "main")
	if has(main, "replicas") && number(main, "replicas") == 0 {
		return DeploymentVerdict{Health: DeploymentStopped, Detail: ""}
	}
	workloads, pods := d.owned()
	if detail, unready := replicaHealth(workloads, pods); unready {
		return DeploymentVerdict{Health: DeploymentUnhealthy, Detail: detail}
	}
	if flag(field(release, "spec"), "suspend") || flag(field(d.Unit, "spec"), "suspend") {
		return DeploymentVerdict{Health: DeploymentSuspended, Detail: "Healthy workload; deployment reconciliation is suspended"}
	}
	return landedHealth(d.Unit, release, pods, unit)
}

// owned is the instance's main-controller workloads and its pods not being deleted.
func (d Deployment) owned() ([]map[string]any, []map[string]any) {
	workloads, pods := []map[string]any{}, []map[string]any{}
	for _, w := range d.Workloads {
		if d.owns(w) {
			workloads = append(workloads, w)
		}
	}
	for _, p := range d.Pods {
		if d.owns(p) && text(field(p, "metadata"), "deletionTimestamp") == "" {
			pods = append(pods, p)
		}
	}
	return workloads, pods
}

func (d Deployment) owns(obj map[string]any) bool {
	meta := field(obj, "metadata")
	labels := field(meta, "labels")
	return text(meta, "namespace") == d.Namespace && text(labels, "app.kubernetes.io/instance") == d.Name &&
		fallback(text(labels, "app.kubernetes.io/controller"), "main") == "main"
}

// replicaHealth names each workload's Ready replicas and its pods' waiting reasons when any workload is missing,
// states no replica count, or has fewer Ready replicas than it wants.
func replicaHealth(workloads, pods []map[string]any) (string, bool) {
	if len(workloads) == 0 {
		return "Desired workload is missing or replica status is unavailable", true
	}
	counts := []string{}
	unready := false
	for _, w := range workloads {
		spec, status := field(w, "spec"), field(w, "status")
		if !numeric(spec, "replicas") {
			return "Desired workload is missing or replica status is unavailable", true
		}
		ready, wanted := number(status, "readyReplicas"), number(spec, "replicas")
		counts = append(counts, fmt.Sprintf("%s/%s %g/%g Ready", text(w, "kind"), text(field(w, "metadata"), "name"), ready, wanted))
		unready = unready || ready < wanted
	}
	if !unready {
		return "", false
	}
	detail := strings.Join(counts, ", ")
	if reasons := waitingReasons(pods); len(reasons) > 0 {
		detail += "; " + strings.Join(reasons, ", ")
	}
	return detail, true
}

func waitingReasons(pods []map[string]any) []string {
	reasons := map[string]bool{}
	for _, p := range pods {
		for _, item := range list(field(p, "status"), "containerStatuses") {
			status, _ := item.(map[string]any)
			if reason := text(field(status, "state", "waiting"), "reason"); reason != "" {
				reasons[reason] = true
			}
		}
	}
	return sortedKeys(reasons)
}

// landedHealth is whether a Ready workload runs the requested release: rolled back, the release Ready at its
// generation, the requested digest running and, when asked, the unit Ready.
func landedHealth(unitObject, release map[string]any, pods []map[string]any, unit bool) DeploymentVerdict {
	if rolledBack(release) {
		return DeploymentVerdict{Health: DeploymentHealthy, Detail: "Previous working version is healthy after rollback; requested upgrade failed", RolledBack: true}
	}
	generation := number(field(release, "metadata"), "generation")
	ready := condition(release, "Ready")
	if text(ready, "status") != "True" || !has(ready, "observedGeneration") || number(ready, "observedGeneration") != generation {
		return DeploymentVerdict{Health: DeploymentStalled, Detail: "HelmRelease is not current/Ready (" + fallback(text(ready, "reason"), "Unknown") + ")"}
	}
	image := RequestedImage(release)
	if strings.Contains(image, "@") && !imagesMatch(image, pods) && text(ready, "reason") != "RollbackSucceeded" {
		return DeploymentVerdict{Health: DeploymentStalled, Detail: "Running image differs from the requested digest"}
	}
	if unit && text(condition(unitObject, "Ready"), "status") != "True" {
		return DeploymentVerdict{Health: DeploymentStalled, Detail: "Flux unit is blocked or has not finished applying the request"}
	}
	return DeploymentVerdict{Health: DeploymentHealthy, Detail: "Workload is Ready and deployment is current"}
}

// rolledBack is a Remediated=True RollbackSucceeded condition at the release's generation with nothing reconciling.
func rolledBack(release map[string]any) bool {
	generation := number(field(release, "metadata"), "generation")
	remediated := condition(release, "Remediated")
	observed := generation
	if has(remediated, "observedGeneration") {
		observed = number(remediated, "observedGeneration")
	}
	return text(remediated, "status") == "True" && text(remediated, "reason") == "RollbackSucceeded" && observed == generation &&
		text(condition(release, "Reconciling"), "status") != "True"
}

// imagesMatch is whether every started main container runs the requested digest; none running is no match.
func imagesMatch(desired string, pods []map[string]any) bool {
	_, digest, _ := strings.Cut(desired, "@")
	running := runningImages(pods)
	if len(running) == 0 {
		return false
	}
	for _, image := range running {
		if !strings.HasSuffix(image, "@"+digest) {
			return false
		}
	}
	return true
}

// runningImages is the distinct images the main containers run: the pod spec's image when it pins a digest,
// otherwise the reported image ID.
func runningImages(pods []map[string]any) []string {
	images := map[string]bool{}
	for _, p := range pods {
		specs := map[string]string{}
		for _, item := range list(field(p, "spec"), "containers") {
			c, _ := item.(map[string]any)
			specs[text(c, "name")] = text(c, "image")
		}
		for _, item := range list(field(p, "status"), "containerStatuses") {
			c, _ := item.(map[string]any)
			if fallback(text(c, "name"), "main") != "main" || text(c, "imageID") == "" {
				continue
			}
			image := strings.TrimPrefix(text(c, "imageID"), "docker-pullable://")
			if pinned := specs[text(c, "name")]; strings.Contains(pinned, "@sha256:") {
				image = pinned
			}
			images[image] = true
		}
	}
	return sortedKeys(images)
}

// condition is an object's first status condition of a type; a missing one is empty.
func condition(obj map[string]any, kind string) map[string]any {
	for _, item := range list(field(obj, "status"), "conditions") {
		c, _ := item.(map[string]any)
		if text(c, "type") == kind {
			return c
		}
	}
	return map[string]any{}
}

// The JSON walkers read a decoded document leniently: a missing or wrongly typed value is the zero value. Numbers
// may be float64 or json.Number.
func field(doc map[string]any, path ...string) map[string]any {
	for _, key := range path {
		doc, _ = doc[key].(map[string]any)
	}
	if doc == nil {
		return map[string]any{}
	}
	return doc
}
func text(doc map[string]any, key string) string { s, _ := doc[key].(string); return s }
func number(doc map[string]any, key string) float64 {
	switch n := doc[key].(type) {
	case float64:
		return n
	case json.Number:
		v, _ := n.Float64()
		return v
	}
	return 0
}
func numeric(doc map[string]any, key string) bool {
	switch n := doc[key].(type) {
	case float64:
		return !math.IsNaN(n) && !math.IsInf(n, 0)
	case json.Number:
		_, err := n.Float64()
		return err == nil
	}
	return false
}
func flag(doc map[string]any, key string) bool  { b, _ := doc[key].(bool); return b }
func list(doc map[string]any, key string) []any { a, _ := doc[key].([]any); return a }
func has(doc map[string]any, key string) bool   { _, ok := doc[key]; return ok }
func fallback(value, other string) string {
	if value != "" {
		return value
	}
	return other
}
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
