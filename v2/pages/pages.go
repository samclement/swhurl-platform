// Package pages computes what the console's pages show: rows, labels, states, reasons, ages and the sorted slices
// a template iterates. It prints nothing and renders nothing, so no page's data can slip html/template's escaper.
package pages

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// State is a page's status vocabulary: the key selects the mark and the tint, the label is the word, and the detail
// is the one fact that decided it.
type State struct {
	Key, Detail string
}

// Label is the state as a page writes it.
func (s State) Label() string {
	switch s.Key {
	case "healthy":
		return "Healthy"
	case "updating":
		return "Updating"
	case "failing":
		return "Failing"
	case "suspended":
		return "Suspended"
	case "stopped":
		return "Stopped"
	}
	return "Unknown"
}

// The states an instance or unit can be in: the one vocabulary the console shows.
const (
	StateHealthy   = "healthy"
	StateUpdating  = "updating"
	StateFailing   = "failing"
	StateSuspended = "suspended"
	StateStopped   = "stopped"
	StateUnknown   = "unknown"
)

// StateOf is an instance's state as a report decided it: its name and its reason, underscored words made plain.
func StateOf(name, reason string) State {
	return State{Key: name, Detail: strings.ReplaceAll(reason, "_", " ")}
}

// Unit is one Flux Kustomization as the platform pages show it.
type Unit struct {
	Name      string
	Ready     State // as the page shows it: a suspended unit reads Suspended whatever its Ready condition says
	Condition State // its Ready condition alone, so a suspended unit that is not Ready counts as failing too
	Suspended bool
	DependsOn []string
	Revision  string
	// ReadyStatus and ReadyMessage are the Ready condition as Flux states it, before the page words it: the unit
	// page shows the message of a unit that is Ready.
	ReadyStatus, ReadyMessage string
	Level                     int
	Path                      string
	Interval                  string
	NeededBy                  []string
}

// Healthy is whether the unit is Ready and not suspended.
func (u Unit) Healthy() bool { return u.Ready.Key == StateHealthy }

// Layer is the section of the platform page the unit belongs to.
func (u Unit) Layer() string { return LayerOf(u.Name) }

// Layers are the unit name prefixes, in the order they build on each other: title and what the layer holds.
var Layers = []struct{ Prefix, Title, Text string }{
	{"cluster-", "Cluster", "Flux's own Git source and unit list, applied by make flux-bootstrap"},
	{"infra-", "Infrastructure", "Namespaces, certificates and the ingress controller"},
	{"platform-", "Platform services", "Sign-in, observability, this console and automation"},
	{"app-", "Deployments", "One unit per app instance"},
}

// OtherLayer is the layer of a unit no prefix names.
const OtherLayer = "Other"

// LayerOf is a unit's layer title by its name prefix.
func LayerOf(name string) string {
	for _, layer := range Layers {
		if strings.HasPrefix(name, layer.Prefix) {
			return layer.Title
		}
	}
	return OtherLayer
}

// waitingReasons are the Ready=False reasons by which Flux says "not yet" rather than "failed".
var waitingReasons = []string{"DependencyNotReady", "Progressing", "ProgressingWithRetry"}

var dependency = regexp.MustCompile(`dependency '(?:[^/']+/)?([^']+)' is not ready`)

// unitState is a Flux unit's state, in the console's vocabulary: waiting for a dependency or applying a change is
// Updating, not Failing, because every push re-checks every unit.
func unitState(status, message, reason string, suspended bool) State {
	if suspended {
		return State{Key: StateSuspended, Detail: "Git changes are not applied until it is resumed"}
	}
	if status == "True" {
		return State{Key: StateHealthy}
	}
	if waiting := dependency.FindStringSubmatch(message); waiting != nil {
		return State{Key: StateUpdating, Detail: "waiting for " + waiting[1]}
	}
	if status == "Unknown" || slices.Contains(waitingReasons, reason) {
		detail := message
		if detail == "" {
			detail = "applying a change"
		}
		return State{Key: StateUpdating, Detail: detail}
	}
	return State{Key: StateFailing, Detail: message}
}

// unitItem is the Kustomization fields the platform page reads.
type unitItem struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		DependsOn []struct {
			Name string `json:"name"`
		} `json:"dependsOn"`
		Suspend  bool   `json:"suspend"`
		Path     string `json:"path"`
		Interval string `json:"interval"`
	} `json:"spec"`
	Status struct {
		LastAppliedRevision string `json:"lastAppliedRevision"`
		Conditions          []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		} `json:"conditions"`
	} `json:"status"`
}

// Units decodes the Flux unit collection as the platform page shows it: one row per unit, ordered by the layer it
// belongs to and then by name, each with its level in the dependency graph and which units need it.
func Units(items [][]byte) ([]Unit, error) {
	decoded, err := decodeUnits(items)
	if err != nil {
		return nil, err
	}
	levels := unitLevels(decoded)
	needed := neededBy(decoded)
	found := make([]Unit, 0, len(decoded))
	for name, unit := range decoded {
		found = append(found, unitOf(name, unit, levels[name], needed[name]))
	}
	slices.SortFunc(found, func(a, b Unit) int {
		if a.Level != b.Level {
			return a.Level - b.Level
		}
		return strings.Compare(a.Name, b.Name)
	})
	return found, nil
}

func decodeUnits(items [][]byte) (map[string]unitItem, error) {
	decoded := map[string]unitItem{}
	for _, item := range items {
		var one unitItem
		if err := json.Unmarshal(item, &one); err != nil || one.Metadata.Name == "" {
			return nil, fmt.Errorf("invalid Flux unit")
		}
		decoded[one.Metadata.Name] = one
	}
	return decoded, nil
}

// unitLevels is each unit's depth: 0 with no dependency, else one more than the deepest dependency.
func unitLevels(decoded map[string]unitItem) map[string]int {
	levels := map[string]int{}
	var level func(string, []string) int
	level = func(name string, seen []string) int {
		if found, ok := levels[name]; ok {
			return found
		}
		deepest := -1
		for _, dependency := range decoded[name].Spec.DependsOn {
			if _, known := decoded[dependency.Name]; known && dependency.Name != name && !slices.Contains(seen, name) {
				deepest = max(deepest, level(dependency.Name, append(seen, name)))
			}
		}
		levels[name] = deepest + 1
		return levels[name]
	}
	for name := range decoded {
		level(name, nil)
	}
	return levels
}

func neededBy(decoded map[string]unitItem) map[string][]string {
	needed := map[string][]string{}
	for name, unit := range decoded {
		for _, dependency := range unit.Spec.DependsOn {
			needed[dependency.Name] = append(needed[dependency.Name], name)
		}
	}
	return needed
}

func unitOf(name string, unit unitItem, level int, needed []string) Unit {
	status, message, reason := "Unknown", "no Ready condition", ""
	for _, condition := range unit.Status.Conditions {
		if condition.Type == "Ready" {
			status, message, reason = condition.Status, condition.Message, condition.Reason
			break
		}
	}
	revision := unit.Status.LastAppliedRevision
	if _, after, found := strings.Cut(revision, ":"); found {
		revision = after
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	slices.Sort(needed)
	return Unit{Name: name, Ready: unitState(status, message, reason, unit.Spec.Suspend),
		Condition: unitState(status, message, reason, false), Suspended: unit.Spec.Suspend,
		ReadyStatus: status, ReadyMessage: message,
		DependsOn: dependencyNames(unit), Revision: revision, Level: level,
		Path: unit.Spec.Path, Interval: unit.Spec.Interval, NeededBy: needed}
}

func dependencyNames(unit unitItem) []string {
	names := make([]string, 0, len(unit.Spec.DependsOn))
	for _, dependency := range unit.Spec.DependsOn {
		names = append(names, dependency.Name)
	}
	return names
}

// Entry is one platform check's result.
type Entry struct {
	Level   string // ok, warn or bad
	Message string
}

// Section is one group of checks, in the order swhurl-v2 verify runs them.
type Section struct {
	Title   string
	Entries []Entry
}

// Checks is a platform report as the pages show it.
type Checks struct {
	Passed   bool
	Sections []Section
}

// Count is the number of checks reported.
func (c Checks) Count() int {
	total := 0
	for _, section := range c.Sections {
		total += len(section.Entries)
	}
	return total
}

// Check is one named group of results, in the order the checks run: the section a row lists it under, and its
// entries. It is the shape a verify report has, without this package knowing the report.
type Check struct {
	Section string
	Entries []Entry
}

// ChecksOf groups a verify report by section, in its own order, and says whether it passed: a warning passes.
func ChecksOf(checks []Check) Checks {
	found := Checks{Passed: true}
	index := map[string]int{}
	for _, check := range checks {
		at, known := index[check.Section]
		if !known {
			at = len(found.Sections)
			index[check.Section] = at
			found.Sections = append(found.Sections, Section{Title: check.Section})
		}
		for _, entry := range check.Entries {
			if entry.Level == "bad" {
				found.Passed = false
			}
			found.Sections[at].Entries = append(found.Sections[at].Entries, entry)
		}
	}
	return found
}

// Group is a section of checks as the platform page opens it: one check on one line, or a group that opens.
type Group struct {
	Title   string
	Entries []Entry
	Failing []Entry
	Index   int // 1 for the first group, so a fragment's id is stable within a page
	Mark    string
}

// CheckGroups is the platform page's health checks: every section but the units, which the page lists itself,
// with an app's image check worded as every page words it.
func CheckGroups(checks Checks) []Group {
	var found []Group
	for _, section := range checks.Sections {
		if section.Title == FluxSection {
			continue
		}
		group := Group{Title: section.Title, Entries: Condensed(section.Entries), Index: len(found) + 1, Mark: "ok"}
		for _, entry := range group.Entries {
			if entry.Level != "ok" {
				group.Failing = append(group.Failing, entry)
				group.Mark = "bad"
			}
		}
		found = append(found, group)
	}
	return found
}

// CommonRevision is the revision nearly every unit has applied: said once, so a row shows one only when it differs.
func CommonRevision(units []Unit) string {
	counts := map[string]int{}
	best, most := "", 0
	for _, unit := range units {
		if unit.Revision == "" {
			continue
		}
		counts[unit.Revision]++
		if counts[unit.Revision] > most {
			best, most = unit.Revision, counts[unit.Revision]
		}
	}
	return best
}

// UnitRow is one unit's row on the platform page: its state, the revision when it differs from the common one, and
// what it applies and waits for, ready for a template to print. Whether a job is running on it is the caller's.
type UnitRow struct {
	Unit            Unit
	Open            bool
	AppLabel        string
	AppLink         string
	At              bool
	RevisionText    string
	Waits           string
	Applies         string
	AppliedRevision string
	Interval        string
	Needed          []string
}

// UnitRowOf is one row of the units list, given the revision nearly every unit is at.
func UnitRowOf(unit Unit, revision string) UnitRow {
	row := UnitRow{Unit: unit, Open: !unit.Healthy() && unit.Ready.Key != StateUpdating,
		At: unit.Revision != revision, RevisionText: unit.Revision, Waits: strings.Join(unit.DependsOn, ", "),
		Applies: unit.Path, AppliedRevision: unit.Revision, Interval: unit.Interval, Needed: unit.NeededBy}
	if unit.Layer() == "Deployments" {
		row.AppLabel, row.AppLink = Target(unit.Name)
	}
	if row.RevisionText == "" {
		row.RevisionText = "no revision"
	}
	if row.Waits == "" {
		row.Waits = "—"
	}
	if row.Applies == "" {
		row.Applies = "—"
	}
	if row.AppliedRevision == "" {
		row.AppliedRevision = "—"
	}
	if row.Interval == "" {
		row.Interval = "—"
	}
	return row
}

// RefusedUnits are the units Flux does not apply, so no action is offered: its fix is make flux-bootstrap.
var RefusedUnits = map[string]string{
	"cluster-sources": "applied by make flux-bootstrap, not by Flux",
	"cluster-stack":   "applied by make flux-bootstrap, not by Flux",
}

var newestImage = regexp.MustCompile(`(\S+)-staging: (newest image .*)`)

// Condensed words an app's image check as every page names the instance (<app>/staging) rather than its ImagePolicy.
func Condensed(entries []Entry) []Entry {
	found := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if match := newestImage.FindStringSubmatch(entry.Message); match != nil {
			label, _ := Target(match[1] + "/staging")
			entry.Message = label + ": " + match[2]
		}
		found = append(found, entry)
	}
	return found
}

// FluxSection is the check section the units list shows; its failures still count as problems.
const FluxSection = "Flux Kustomizations"

// Problem is one thing that needs attention, and where to read it.
type Problem struct {
	Level string // a mark: failing, warn, suspended or updating
	Text  string
	Link  string
}

// Updating lists the units applying a change or waiting for a dependency: normal for a few seconds after every push.
func Updating(units []Unit) []Problem {
	found := []Problem{}
	for _, unit := range units {
		if unit.Ready.Key == StateUpdating {
			label, link := Target(unit.Name)
			found = append(found, Problem{Level: "updating", Text: label + ": " + unit.Ready.Detail, Link: link})
		}
	}
	return found
}

// Problems lists what needs attention: failing or warning checks, then units that are not Ready, then suspended ones.
func Problems(checks Checks, units []Unit) []Problem {
	names := map[string]bool{}
	for _, unit := range units {
		names[unit.Name] = true
	}
	found := []Problem{}
	for _, section := range checks.Sections {
		for _, entry := range section.Entries {
			if entry.Level != "bad" && entry.Level != "warn" {
				continue
			}
			if section.Title == FluxSection && names[strings.Fields(entry.Message)[0]] {
				// The check read the units a moment before the list did, and a unit can become Ready in
				// between: the list alone says what a unit is, so every page agrees with itself.
				continue
			}
			level := "warn"
			if entry.Level == "bad" {
				level = "failing"
			}
			found = append(found, Problem{Level: level, Text: entry.Message})
		}
	}
	found = append(found, failingProblems(units)...)
	found = append(found, suspendedProblems(units)...)
	return found
}

func failingProblems(units []Unit) []Problem {
	found := []Problem{}
	for _, unit := range units {
		if unit.Condition.Key != StateFailing {
			continue
		}
		label, link := Target(unit.Name)
		text := label + " is not Ready"
		if unit.Condition.Detail != "" {
			text += ": " + unit.Condition.Detail
		}
		found = append(found, Problem{Level: "failing", Text: text, Link: link})
	}
	return found
}

func suspendedProblems(units []Unit) []Problem {
	found := []Problem{}
	for _, unit := range units {
		if !unit.Suspended {
			continue
		}
		label, link := Target(unit.Name)
		found = append(found, Problem{Level: "suspended", Link: link,
			Text: label + " is suspended: Git changes are not applied to it until it is resumed"})
	}
	return found
}

// environments are the two environments an instance can be in.
var environments = map[string]bool{"staging": true, "prod": true}

// Target is how every page names a job's or problem's subject: the label and the link. An app instance is always
// <app>/<env> and links to its app page, given as its unit (app-<app>-<env>) or as <app>/<env>; anything else is a
// Flux unit, by name.
func Target(name string) (label, link string) {
	if team, found := strings.CutPrefix(name, "team:"); found {
		return "team " + team, "/teams#" + team
	}
	if app, env, found := instanceOfUnit(name); found {
		return app + "/" + env, "/apps/" + app + "/" + env
	}
	if app, env, found := strings.Cut(name, "/"); found && environments[env] && app != "" {
		return name, "/apps/" + app + "/" + env
	}
	return name, "/units/" + name
}

// instanceOfUnit splits app-<app>-<env>, whose app name may contain hyphens.
func instanceOfUnit(unit string) (app, env string, found bool) {
	rest, isUnit := strings.CutPrefix(unit, "app-")
	if !isUnit {
		return "", "", false
	}
	at := strings.LastIndex(rest, "-")
	if at <= 0 || !environments[rest[at+1:]] {
		return "", "", false
	}
	return rest[:at], rest[at+1:], true
}

// Ago is a time as how long ago it was; text that is not a time is returned as it is.
func Ago(value string, now time.Time) string {
	if value == "" {
		return ""
	}
	if len(value) == 10 {
		date, err := time.Parse("2006-01-02", value)
		if err != nil {
			return value
		}
		days := int(now.Sub(date).Hours() / 24)
		switch days {
		case 0:
			return "today"
		case 1:
			return "yesterday"
		}
		return fmt.Sprintf("%d days ago", days)
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	seconds := now.Sub(at).Seconds()
	switch {
	case seconds < 90:
		return "just now"
	case seconds < 90*60:
		return fmt.Sprintf("%d min ago", int(round(seconds/60)))
	case seconds < 36*3600:
		return fmt.Sprintf("%d h ago", int(round(seconds/3600)))
	}
	return fmt.Sprintf("%d days ago", int(round(seconds/86400)))
}

func round(value float64) float64 {
	if value < 0 {
		return -round(-value)
	}
	return float64(int64(value + 0.5))
}

// Took is how long a finished job ran.
func Took(started, finished time.Time) string {
	seconds := int(round(finished.Sub(started).Seconds()))
	if seconds < 90 {
		return fmt.Sprintf("%d s", seconds)
	}
	return fmt.Sprintf("%d min", int(round(float64(seconds)/60)))
}
