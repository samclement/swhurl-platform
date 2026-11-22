package incident

import (
	"fmt"
	"strings"
)

func owned(obj, entry Object) bool {
	meta := object(obj, "metadata")
	labels := object(meta, "labels")
	return text(meta, "namespace") == text(entry, "namespace") && text(labels, "app.kubernetes.io/instance") == text(entry, "name") && fallback(text(labels, "app.kubernetes.io/controller"), "main") == "main"
}
func condition(obj Object, kind string) Object {
	for _, item := range array(object(obj, "status"), "conditions") {
		c, _ := item.(map[string]any)
		if text(c, "type") == kind {
			return c
		}
	}
	return Object{}
}
func health(entry target, release Object, data Snapshot, unit bool) (*string, string) {
	if release == nil {
		if flag(object(entry.unit, "spec"), "suspend") {
			return nil, "No release yet and deployment reconciliation is suspended"
		}
		return known("deployment stalled"), "HelmRelease has not been created"
	}
	if text(object(release, "metadata"), "deletionTimestamp") != "" {
		return nil, "Uninstall is in progress"
	}
	main := object(release, "spec", "values", "controllers", "main")
	if has(main, "replicas") && number(main, "replicas") == 0 {
		return known("stopped"), ""
	}
	workloads, pods := ownedWorkloads(entry, data)
	if fault, detail := replicaHealth(workloads, pods); fault != "" {
		return known(fault), detail
	}
	return reconciliationHealth(entry, release, pods, unit)
}
func replicaHealth(workloads, pods []Object) (string, string) {
	if len(workloads) == 0 {
		return "unhealthy", "Desired workload is missing or replica status is unavailable"
	}
	counts := []string{}
	unready := false
	for _, w := range workloads {
		spec, status := object(w, "spec"), object(w, "status")
		if !numeric(spec, "replicas") {
			return "unhealthy", "Desired workload is missing or replica status is unavailable"
		}
		ready, wanted := number(status, "readyReplicas"), number(spec, "replicas")
		counts = append(counts, fmt.Sprintf("%s/%s %g/%g Ready", text(w, "kind"), text(object(w, "metadata"), "name"), ready, wanted))
		unready = unready || ready < wanted
	}
	if !unready {
		return "", ""
	}
	detail := strings.Join(counts, ", ")
	if reasons := waitingReasons(pods); len(reasons) > 0 {
		detail += "; " + strings.Join(reasons, ", ")
	}
	return "unhealthy", detail
}
func waitingReasons(pods []Object) []string {
	reasons := map[string]bool{}
	for _, p := range pods {
		for _, item := range array(object(p, "status"), "containerStatuses") {
			status, _ := item.(map[string]any)
			if reason := text(object(status, "state", "waiting"), "reason"); reason != "" {
				reasons[reason] = true
			}
		}
	}
	return sortedKeys(reasons)
}
func deploymentHealth(entry target, release Object, pods []Object, unit bool) (*string, string) {
	if rolledBack(release) {
		return known(""), "Previous working version is healthy after rollback; requested upgrade failed"
	}
	generation := number(object(release, "metadata"), "generation")
	ready := condition(release, "Ready")
	if text(ready, "status") != "True" || !has(ready, "observedGeneration") || number(ready, "observedGeneration") != generation {
		return known("deployment stalled"), "HelmRelease is not current/Ready (" + fallback(text(ready, "reason"), "Unknown") + ")"
	}
	image := desiredImage(release)
	if strings.Contains(image, "@") && !imagesMatch(image, pods) && text(ready, "reason") != "RollbackSucceeded" {
		return known("deployment stalled"), "Running image differs from the requested digest"
	}
	if unit && text(condition(entry.unit, "Ready"), "status") != "True" {
		return known("deployment stalled"), "Flux unit is blocked or has not finished applying the request"
	}
	return known(""), "Workload is Ready and deployment is current"
}
func desiredImage(release Object) string {
	image := object(release, "spec", "values", "controllers", "main", "containers", "main", "image")
	result := fallback(text(image, "repository"), "?")
	if tag := text(image, "tag"); tag != "" {
		result += ":" + tag
	}
	if digest := text(image, "digest"); digest != "" {
		result += "@" + digest
	}
	return result
}
func runningImages(pods []Object) []string {
	images := map[string]bool{}
	for _, p := range pods {
		specs := map[string]string{}
		for _, item := range array(object(p, "spec"), "containers") {
			c, _ := item.(map[string]any)
			specs[text(c, "name")] = text(c, "image")
		}
		for _, item := range array(object(p, "status"), "containerStatuses") {
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
	found := sortedKeys(images)
	return found
}
func imagesMatch(desired string, pods []Object) bool {
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

func ownedWorkloads(entry target, data Snapshot) ([]Object, []Object) {
	workloads, pods := []Object{}, []Object{}
	for _, w := range data.Workloads {
		if owned(w, entry.record) {
			workloads = append(workloads, w)
		}
	}
	for _, p := range data.Pods {
		if owned(p, entry.record) && text(object(p, "metadata"), "deletionTimestamp") == "" {
			pods = append(pods, p)
		}
	}
	return workloads, pods
}

func reconciliationHealth(entry target, release Object, pods []Object, unit bool) (*string, string) {
	if flag(object(release, "spec"), "suspend") || flag(object(entry.unit, "spec"), "suspend") {
		return known("suspended"), "Healthy workload; deployment reconciliation is suspended"
	}
	return deploymentHealth(entry, release, pods, unit)
}

func rolledBack(release Object) bool {
	generation := number(object(release, "metadata"), "generation")
	remediated := condition(release, "Remediated")
	observed := generation
	if has(remediated, "observedGeneration") {
		observed = number(remediated, "observedGeneration")
	}

	return text(remediated, "status") == "True" && text(remediated, "reason") == "RollbackSucceeded" && observed == generation && text(condition(release, "Reconciling"), "status") != "True"
}
