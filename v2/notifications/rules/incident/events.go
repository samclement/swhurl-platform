package incident

import (
	"fmt"
	"slices"
)

func action(reason string) string {
	return map[string]string{"InstallSucceeded": "deployed", "UpgradeSucceeded": "deployed", "RollbackSucceeded": "rolled back", "UninstallSucceeded": "uninstalled"}[reason]
}
func failure(reason string) bool {
	return slices.Contains([]string{"InstallFailed", "UpgradeFailed", "RollbackFailed", "UninstallFailed", "ReconciliationFailed", "BuildFailed", "ValidationFailed", "ArtifactFailed", "PruneFailed"}, reason)
}

type evidence struct {
	key                            string
	entry                          Object
	action, reason, uid, noticeKey string
}

func (s *State) eventEntry(event Object) (string, Object) {
	obj := object(event, "involvedObject")
	key := text(obj, "namespace") + "/" + text(obj, "name")
	if text(obj, "kind") == "Kustomization" && text(obj, "namespace") == "flux-system" {
		key = ""
		for k, v := range object(s.document, "instances") {
			if text(v.(map[string]any), "unit") == text(obj, "name") {
				key = k
			}
		}
	} else if text(obj, "kind") != "HelmRelease" {
		return "", nil
	}
	entry, ok := object(s.document, "instances")[key].(map[string]any)
	if !ok {
		return "", nil
	}
	if text(obj, "kind") == "HelmRelease" && text(entry, "uid") != "" && text(obj, "uid") != text(entry, "uid") {
		return "", nil
	}
	return key, entry
}
func (s *State) eligible(event Object, current map[string]target, releases map[string]Object, data Snapshot, now float64) *evidence {
	key, entry := s.eventEntry(event)
	if entry == nil {
		return nil
	}
	obj := object(event, "involvedObject")
	kind, reason := text(obj, "kind"), text(event, "reason")
	when, _ := eventTime(event)
	target, active := current[key]
	a := eventAction(kind, reason)
	if !s.eventWindow(kind, when, now, active) || (a == "" && !failure(reason)) {
		return nil
	}
	if s.deploymentHeld(a, active, event, entry, target, releases[key], data) {
		return nil
	}
	if !lifecycleController(event, kind) {
		return nil
	}
	return eventEvidence(event, key, entry, a, reason, when)
}
func eventEvidence(event Object, key string, entry Object, a, reason string, when float64) *evidence {
	uid := fallback(text(object(event, "involvedObject"), "uid"), key)
	meta := object(event, "metadata")
	attempt := text(meta, "uid")
	annotations := object(meta, "annotations")
	if has(annotations, "helm.toolkit.fluxcd.io/token") {
		attempt = text(annotations, "helm.toolkit.fluxcd.io/token")
	}
	noticeKey := "failure:" + uid + ":" + attempt + ":" + reason
	if a != "" {
		count := float64(1)
		if has(event, "count") {
			count = number(event, "count")
		}
		noticeKey = fmt.Sprintf("event:%s:%g:%s", text(meta, "uid"), count, epochKey(when))
	}
	if a == "" && slices.Contains(array(entry, "failed_actions"), any(noticeKey)) {
		return nil
	}
	if a == "uninstalled" && text(entry, "uninstalled") == uid {
		return nil
	}
	return &evidence{key, entry, a, reason, uid, noticeKey}
}
func eventText(event Object, e evidence, release Object) (string, string) {
	verb := e.action
	if verb == "" {
		verb = "deployment failed"
		if e.reason == "UninstallFailed" {
			verb = "uninstall failed"
		}
	}
	title := text(e.entry, "title") + " " + verb
	message := "Flux " + e.reason + " for " + e.key + "."
	if chart := text(object(event, "metadata", "annotations"), "helm.toolkit.fluxcd.io/revision"); chart != "" {
		message += " Chart revision: " + chart + "."
	}
	message += requestedImageText(event, e.reason, release)
	if e.action == "uninstalled" && flag(e.entry, "retained") {
		message += " Storage is retained; data has not been deleted."
	}
	if e.action == "rolled back" {
		message += " The requested upgrade failed; Helm restored the previous release."
	}
	return title, message
}
func (s *State) apply(event Object, e evidence, releases map[string]Object, now float64, base string) {
	title, message := eventText(event, e, releases[e.key])
	s.combineRollback(e)
	s.enqueue(e.noticeKey, title, message, e.action == "" || e.action == "rolled back", link(base, e.entry, e.action == "uninstalled"), now, array(e.entry, "about"))
	if e.action == "uninstalled" {
		e.entry["uninstalled"] = e.uid
	}
	if e.action != "" && e.action != "rolled back" {
		return
	}
	record := object(e.entry, "incident")
	if !has(record, "since") {
		record["since"] = clockValue(now)
	}
	record["notified"], record["last_notice"] = true, clockValue(now)
	if e.action == "" {
		failed := append(array(e.entry, "failed_actions"), e.noticeKey)
		if len(failed) > 16 {
			failed = failed[len(failed)-16:]
		}
		e.entry["failed_actions"] = failed
	}
}
func (s *State) removals(current map[string]target, releases map[string]Object, data Snapshot, now float64, base string) {
	for _, key := range sortedKeys(object(s.document, "instances")) {
		entry := object(object(s.document, "instances"), key)
		if _, active := current[key]; active {
			continue
		}
		if releases[key] != nil || text(entry, "uid") == "" || text(entry, "uninstalled") != "" {
			continue
		}
		if remainingWorkload(entry, data.Workloads) {
			continue
		}
		message := "Managed HelmRelease " + key + " has finished removal."
		if flag(entry, "retained") {
			message += " Storage is retained; data has not been deleted."
		}
		s.enqueue("removed:"+text(entry, "uid"), text(entry, "title")+" uninstalled", message, false, link(base, entry, true), now, array(entry, "about"))
		entry["uninstalled"] = text(entry, "uid")
	}
}
func remainingWorkload(entry Object, workloads []Object) bool {
	for _, w := range workloads {
		if text(object(w, "metadata"), "namespace") == text(entry, "namespace") && text(object(w, "metadata", "labels"), "app.kubernetes.io/instance") == text(entry, "name") {
			return true
		}
	}
	return false
}

func (s *State) holdDeployment(event, entry Object, target target, release Object, data Snapshot) bool {
	fault, detail := health(target, release, data, false)
	if fault != nil && (*fault == "" || *fault == "suspended") {
		return false
	}
	s.held = append(s.held, "Holding "+text(entry, "title")+" deployed: "+detail)
	return true
}
func lifecycleController(event Object, kind string) bool {
	expected := "helm-controller"
	if kind == "Kustomization" {
		expected = "kustomize-controller"
	}
	return fallback(text(event, "reportingComponent"), text(object(event, "source"), "component")) == expected
}

func requestedImageText(event Object, reason string, release Object) string {
	if release == nil || (reason != "InstallSucceeded" && reason != "UpgradeSucceeded") {
		return ""
	}
	if text(condition(release, "Ready"), "message") != text(event, "message") {
		return ""
	}
	return " Requested image: " + desiredImage(release) + "."
}

func (s *State) combineRollback(e evidence) {
	if e.action == "rolled back" {
		pending := []any{}
		for _, p := range s.Pending() {
			if p.Title() != text(e.entry, "title")+" deployment failed" {
				pending = append(pending, p.document)
			}
		}
		s.document["pending"] = pending
	}
}

func eventAction(kind, reason string) string {
	if kind == "HelmRelease" {
		return action(reason)
	}
	return ""
}
func (s *State) eventWindow(kind string, when, now float64, active bool) bool {
	if when <= number(s.document, "started") || now-when > History {
		return false
	}
	return kind != "Kustomization" || (active && now-when >= 60)
}
func (s *State) deploymentHeld(action string, active bool, event, entry Object, target target, release Object, data Snapshot) bool {
	return action == "deployed" && active && s.holdDeployment(event, entry, target, release, data)
}
