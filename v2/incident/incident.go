package incident

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// target is a managed instance identity discovered from a Flux unit, plus that unit's observation.
type target struct {
	record Object
	unit   Object
}

func targets(data Snapshot, copies bool) map[string]target {
	found := map[string]target{}
	for _, unit := range data.Units {
		meta, spec := object(unit, "metadata"), object(unit, "spec")
		if text(meta, "deletionTimestamp") != "" {
			continue
		}
		path := text(spec, "path")
		app, env, ok := instancePath(path)
		console := text(meta, "name") == "platform-console" && path == "./platform/console"
		if !ok && !console {
			continue
		}
		ns, title := app+"-"+env, app+"/"+env
		if console {
			app, ns, title = "console", "console", "console"
		}
		record := Object{"title": title, "unit": text(meta, "name"), "namespace": ns, "name": app, "console": console}
		if copies && !console {
			record["about"] = []any{app, env}
		}
		found[ns+"/"+app] = target{record, unit}
	}
	return found
}
func instancePath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, "./") {
		return "", "", false
	}
	path = strings.TrimPrefix(path, "./")
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) < 3 {
		return "", "", false
	}
	root := strings.Join(parts[:len(parts)-2], "/")
	app, env := parts[len(parts)-2], parts[len(parts)-1]
	valid, _ := regexp.MatchString(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`, app)
	return app, env, valid && (env == "staging" || env == "prod") && (root == "apps" || root == "v2-apps/instances")
}
func link(base string, entry Object, removed bool) string {
	if flag(entry, "console") {
		return base + "/platform#units"
	}
	if removed {
		return base + "/activity"
	}
	return base + "/apps/" + text(entry, "title")
}
func (s *State) enqueue(key, title, message string, high bool, click string, now float64, about []any) {
	s.queue(key, title, message, high, click, now, nil)
	if len(about) > 0 {
		s.queue(key+"@notify", title, message, high, click, now, about)
	}
}
func (s *State) queue(key, title, message string, high bool, click string, now float64, about []any) {
	if has(object(s.document, "seen"), key) {
		return
	}
	for _, pending := range s.Pending() {
		if pending.Key() == key {
			return
		}
	}
	priority, tag := float64(3), "white_check_mark"
	if high {
		priority, tag = 4, "warning"
	}
	entry := Object{"key": key, "title": title, "message": message, "priority": priority, "tags": []any{tag}, "queued": clockValue(now)}
	if click != "" {
		entry["click"] = click
	}
	if len(about) > 0 {
		entry["about"] = about
	}
	s.document["pending"] = append(array(s.document, "pending"), entry)
}
func (s *State) incident(record Object, fault *string, title, detail string, grace float64, now float64, click string, about []any) {
	if fault == nil {
		delete(record, "healthy_since")
		return
	}
	if *fault != "" {
		s.fault(record, *fault, title, detail, grace, now, click, about)
	} else if flag(record, "notified") {
		if !has(record, "healthy_since") {
			record["healthy_since"] = clockValue(now)
		}
		if now-number(record, "healthy_since") >= Recovery {
			key := "recovery:" + title + ":" + epochKey(number(record, "since"))
			message := fmt.Sprintf("Healthy/current for 2 minutes; incident lasted %d minutes.", int((now-number(record, "since"))/60))
			s.enqueue(key, title+" recovered", message, false, click, now, about)
			clear(record)
		}
	} else {
		clear(record)
	}
}
func (s *State) fault(record Object, fault, title, detail string, grace, now float64, click string, about []any) {
	delete(record, "healthy_since")
	if !has(record, "since") {
		record["since"] = clockValue(now)
	}
	record["fault"] = fault
	last := float64(-Reminder)
	if has(record, "last_notice") {
		last = number(record, "last_notice")
	}
	if now-number(record, "since") < grace || now-last < Reminder {
		return
	}
	key := "incident:" + title + ":" + epochKey(number(record, "since")) + ":" + epochKey(now)
	message := fmt.Sprintf("%s. Observed for %d minutes.", detail, int((now-number(record, "since"))/60))
	s.enqueue(key, title+" "+fault, message, true, click, now, about)
	record["last_notice"], record["notified"] = clockValue(now), true
}
func known(s string) *string { return &s }

// Evaluate advances an owned copy, preserving previous and snapshot. Invalid event time is unavailable evidence,
// not health. A baseline remembers its start so existing Events are not replayed.
func Evaluate(previous *State, data Snapshot, now float64, base string, copies bool) (*State, error) {
	events, err := orderedEvents(data.Events)
	if err != nil {
		return nil, err
	}
	s := clone(previous, now)
	for key := range object(s.document, "seen") {
		if number(object(s.document, "seen"), key) <= now {
			delete(object(s.document, "seen"), key)
		}
	}
	current := targets(data, copies)
	releases := map[string]Object{}
	for _, r := range data.Releases {
		releases[identity(r)] = r
	}
	s.observe(current, releases, data, now, base)
	s.prune(current, releases, now)
	for _, event := range events {
		if evidence := s.eligible(event, current, releases, data, now); evidence != nil {
			s.apply(event, *evidence, releases, now, base)
		}
	}
	s.removals(current, releases, data, now, base)
	s.document["last_success"] = clockValue(now)
	s.incident(object(s.document, "monitor"), known(""), "notification monitor", "", Grace, now, base+"/platform", nil)
	return s, nil
}
func orderedEvents(events []Object) ([]Object, error) {
	type timed struct {
		event Object
		at    float64
	}
	sorted := []timed{}
	for _, event := range events {
		at, err := eventTime(event)
		if err != nil {
			return nil, err
		}
		sorted = append(sorted, timed{event, at})
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].at < sorted[j].at })
	result := []Object{}
	for _, item := range sorted {
		result = append(result, item.event)
	}
	return result, nil
}
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func (s *State) observe(current map[string]target, releases map[string]Object, data Snapshot, now float64, base string) {
	for _, key := range sortedKeys(current) {
		target := current[key]
		instances := object(s.document, "instances")
		if !has(instances, key) {
			instances[key] = Object{"incident": Object{}}
		}
		record := object(instances, key)
		for k, v := range target.record {
			record[k] = v
		}
		record["last_active"] = clockValue(now)
		if release := releases[key]; release != nil {
			updateRelease(record, release)
		}
		s.observeHealth(record, target, releases[key], data, now, base)
	}
}
func updateRelease(record, release Object) {
	meta := object(release, "metadata")
	if text(record, "uid") != "" && text(record, "uid") != text(meta, "uid") {
		clear(object(record, "incident"))
		delete(record, "uninstalled")
		delete(record, "failed_actions")
	}
	record["uid"], record["generation"] = text(meta, "uid"), number(meta, "generation")
	record["retained"] = len(object(release, "spec", "values", "persistence", "data")) > 0
}
func (s *State) prune(current map[string]target, releases map[string]Object, now float64) {
	for key, value := range object(s.document, "instances") {
		entry := value.(map[string]any)
		if _, exists := current[key]; exists {
			continue
		}
		clear(object(entry, "incident"))
		if releases[key] == nil && text(entry, "uninstalled") != "" && now-number(entry, "last_active") > History {
			delete(object(s.document, "instances"), key)
		}
	}
}

// Unknown keeps the last successful observations, cancels recovery and raises the monitor's own incident.
func Unknown(previous *State, now float64, base string) *State {
	s := clone(previous, now)
	s.incident(object(s.document, "monitor"), known("cannot read cluster health"), "notification monitor", "Kubernetes snapshot failed; app health is unknown", Grace, now, base+"/platform", nil)
	for _, entry := range object(s.document, "instances") {
		delete(object(entry.(map[string]any), "incident"), "healthy_since")
	}
	return s
}

// CopyHealth records a pass that attempted copies. No attempted copy says nothing about the service. Acceptance
// recovers immediately; its recovery notice, like Python's, is queued for the next pass.
func (s *State) CopyHealth(accepted, refused int, now float64, base string) {
	if accepted == 0 && refused == 0 {
		return
	}
	if !has(s.document, "notify_service") {
		s.document["notify_service"] = Object{}
	}
	record := object(s.document, "notify_service")
	fault := "unreachable"
	if refused == 0 {
		record["healthy_since"] = clockValue(now - Recovery)
		fault = ""
	}
	s.incident(record, known(fault), "notify service", "Copies of app messages for team channels are not being accepted", Grace, now, base+"/platform", nil)
}

func (s *State) observeHealth(record Object, target target, release Object, data Snapshot, now float64, base string) {
	fault, detail := health(target, release, data, true)
	incident := object(record, "incident")
	if fault != nil && (*fault == "stopped" || *fault == "suspended" && text(incident, "fault") != "unhealthy") {
		clear(incident)
		return
	}
	if fault != nil && *fault == "suspended" {
		fault = known("")
	}
	grace := float64(Grace)
	if flag(record, "console") {
		grace = ConsoleGrace
	}
	s.incident(incident, fault, text(record, "title"), detail, grace, now, link(base, record, false), array(record, "about"))
}
