// Package teamscenario decides live-test-team-scenario: which three apps the records link in an environment, the
// ClickHouse queries that find the newest trace through them, and the verdicts on what those queries answered. It
// reads the declarations and the answers it is given and has no effects; the messages are the operator's, in the
// words make live-test-team-scenario has always used.
package teamscenario

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Minutes is how far back a trace is looked for; demo-shopper calls every 30 seconds.
const Minutes = 10

// Plan is what the test does, printed by a dry run.
const Plan = `Check in ClickStack that the team platform's whole chain held for the traffic three running apps already carry:
  - CALLER calls CALLEE with its platform-issued token, and CALLEE names it as the caller
  - CALLEE publishes one of its events, and SUBSCRIBER receives it
  - one trace holds all three apps' spans, each with the team its records name, and the callee's log line names the caller

    make live-test-team-scenario [ENV=staging]

The three apps are chosen from the records' declarations (what swhurl-v2 status --all reports as each instance's
inventory): in ENV, the first app (by name) that another app calls and that publishes an event a third app
subscribes to, with its first caller and subscriber. Something must drive that call (the demo's demo-shopper does,
every 30 seconds): the check reads the newest trace of the last 10 minutes in which CALLEE served CALLER.
Sends nothing and creates nothing: it only reads.`

// Passed is the line a run with no failed check ends with.
const Passed = "Team scenario passed."

// App is what one instance's records declare that the scenario uses.
type App struct {
	Team                            string
	Calls, Publishes, Subscriptions []string
}

// Chain is the three apps and the event the scenario follows.
type Chain struct {
	Caller, Callee, Event, Subscriber string
}

// Preflight is why the scenario cannot start: no chain, or a name that cannot go into a query.
type Preflight struct{ Reason string }

func (p *Preflight) Error() string { return p.Reason }

// name is an app's name; event is an event's, which may hold dots. Neither can close a quoted SQL string.
var (
	name  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	event = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	trace = regexp.MustCompile(`^[0-9a-f]+$`)
)

// Choose is the first callee by name with a caller and an event a subscriber takes, with the first caller and
// subscriber by name. apps is the environment's instances by application.
func Choose(environment string, apps map[string]App) (Chain, error) {
	for _, callee := range slices.Sorted(maps.Keys(apps)) {
		callers := linked(apps, func(other App) bool { return slices.Contains(other.Calls, callee) })
		for _, published := range apps[callee].Publishes {
			subscribers := linked(apps, func(other App) bool { return slices.Contains(other.Subscriptions, callee+"."+published) })
			if len(callers) > 0 && len(subscribers) > 0 {
				chain := Chain{Caller: callers[0], Callee: callee, Event: published, Subscriber: subscribers[0]}
				return chain, chain.valid()
			}
		}
	}
	return Chain{}, &Preflight{Reason: fmt.Sprintf("no %s instances form a chain: an app calling an app that publishes "+
		"an event another app subscribes to (swhurl-v2 edit --set calls=… and --set subscriptions=…)", environment)}
}

func (c Chain) valid() error {
	for _, app := range []string{c.Caller, c.Callee, c.Subscriber} {
		if !name.MatchString(app) {
			return &Preflight{Reason: fmt.Sprintf("app name %q cannot go into a query", app)}
		}
	}
	if !event.MatchString(c.Event) {
		return &Preflight{Reason: fmt.Sprintf("event name %q cannot go into a query", c.Event)}
	}
	return nil
}

func linked(apps map[string]App, links func(App) bool) []string {
	found := []string{}
	for app, other := range apps {
		if links(other) {
			found = append(found, app)
		}
	}
	slices.Sort(found)
	return found
}

// Section is the heading the checks report under.
func (c Chain) Section(environment string) string {
	return fmt.Sprintf("The newest trace in which %s served %s (%s, last %d minutes)", c.Callee, c.Caller, environment, Minutes)
}

// TraceQuery finds the newest trace of the window in which the callee served the caller, a complete one first:
// each trace's verdicts as bits (1: the callee published, 2: the subscriber processed).
func (c Chain) TraceQuery() string {
	served := fmt.Sprintf("ServiceName = '%s' AND SpanKind = 'Server' "+
		"AND SpanAttributes['caller.type'] = 'app' AND SpanAttributes['caller.id'] = '%s'", c.Callee, c.Caller)
	chained := fmt.Sprintf("(ServiceName = '%s' AND SpanKind = 'Producer') + "+
		"(ServiceName = '%s' AND SpanKind = 'Consumer' AND SpanName = 'process %s.%s') * 2", c.Callee, c.Subscriber, c.Callee, c.Event)
	return fmt.Sprintf("SELECT TraceId, groupBitOr(%s) AS seen FROM otel_traces "+
		"WHERE Timestamp > now() - INTERVAL %d MINUTE AND ServiceName IN ('%s', '%s') "+
		"GROUP BY TraceId HAVING countIf(%s) > 0 ORDER BY seen = 3 DESC, max(Timestamp) DESC LIMIT 1 FORMAT TSV",
		chained, Minutes, c.Callee, c.Subscriber, served)
}

// SpansQuery is each of the three apps' spans in the trace, with the team its resource names.
func (c Chain) SpansQuery(traceID string) string {
	return fmt.Sprintf("SELECT ServiceName, ResourceAttributes['swhurl.team'] FROM otel_traces "+
		"WHERE TraceId = '%s' AND ServiceName IN ('%s', '%s', '%s') FORMAT TSV", traceID, c.Caller, c.Callee, c.Subscriber)
}

// LogQuery counts the callee's log lines in the trace that name the caller.
func (c Chain) LogQuery(traceID string) string {
	return fmt.Sprintf("SELECT count() FROM otel_logs WHERE TraceId = '%s' AND ServiceName = '%s' "+
		"AND LogAttributes['caller.type'] = 'app' AND LogAttributes['caller.id'] = '%s' FORMAT TSV", traceID, c.Callee, c.Caller)
}

// Level is how a line reports: a result (ok, bad), a note (info) or a heading (section).
type Level string

const (
	Section Level = "section"
	OK      Level = "ok"
	Bad     Level = "bad"
	Info    Level = "info"
)

// Line is one reported line.
type Line struct {
	Level Level
	Text  string
}

func check(passed bool, ok, bad string) Line {
	if passed {
		return Line{OK, ok}
	}
	return Line{Bad, bad}
}

// Served judges the trace query's answer: the trace it found, if any, and whether its publish and processing were
// both seen. A trace ID that is not hex is refused, since the next queries quote it.
func (c Chain) Served(answer string) (string, []Line) {
	id, seen, _ := strings.Cut(strings.TrimSpace(answer), "\t")
	if id == "" {
		return "", []Line{{Bad, fmt.Sprintf("no trace in the last %d minutes in which %s served %s: does anything drive %s?",
			Minutes, c.Callee, c.Caller, c.Caller)}}
	}
	if !trace.MatchString(id) {
		return "", []Line{{Bad, fmt.Sprintf("ClickHouse answered a trace ID that is not hex: %q", id)}}
	}
	return id, []Line{
		{OK, fmt.Sprintf("%s served %s in trace %s", c.Callee, c.Caller, id)},
		check(strings.TrimSpace(seen) == "3",
			fmt.Sprintf("%s published %s.%s and %s processed it, in the same trace", c.Callee, c.Callee, c.Event, c.Subscriber),
			fmt.Sprintf(`no publish span from %s or no "process %s.%s" span from %s in the trace`, c.Callee, c.Callee, c.Event, c.Subscriber)),
	}
}

// Teams judges the spans query's answer: each app's spans carry exactly the team its records name.
func (c Chain) Teams(apps map[string]App, answer string) []Line {
	recorded := map[string][]string{}
	for _, row := range strings.Split(answer, "\n") {
		if row == "" {
			continue
		}
		service, team, _ := strings.Cut(row, "\t")
		if !slices.Contains(recorded[service], team) {
			recorded[service] = append(recorded[service], team)
		}
	}
	lines := []Line{}
	for _, app := range []string{c.Caller, c.Callee, c.Subscriber} {
		wanted, teams := apps[app].Team, recorded[app]
		slices.Sort(teams)
		lines = append(lines, check(slices.Equal(teams, []string{wanted}), fmt.Sprintf("%s's spans carry its team: %s", app, wanted),
			fmt.Sprintf("%s's spans carry team %s, its records say %s", app, teamList(teams), wanted)))
	}
	return lines
}

// teamList is the teams as the Python test printed a list, or none.
func teamList(teams []string) string {
	if len(teams) == 0 {
		return "none"
	}
	quoted := make([]string, len(teams))
	for i, team := range teams {
		quoted[i] = "'" + team + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// Logged judges the log query's answer, then names the trace to look for in HyperDX.
func (c Chain) Logged(traceID, answer string) []Line {
	count := strings.TrimSpace(answer)
	return []Line{
		check(count != "" && count != "0", fmt.Sprintf("%s's log line for the request names the caller", c.Callee),
			fmt.Sprintf("%s logged no line in this trace with caller.id = %s", c.Callee, c.Caller)),
		{Info, fmt.Sprintf("In HyperDX, search for trace %s", traceID)},
	}
}

// QueryFailed is the line for a query ClickHouse did not answer; the scenario stops there.
func QueryFailed(reason string) Line {
	return Line{Bad, "ClickHouse query failed: " + reason}
}

// Passes is whether no line is a failed check.
func Passes(lines []Line) bool {
	return !slices.ContainsFunc(lines, func(line Line) bool { return line.Level == Bad })
}

// Text is the lines as the operator reads them, ending with Passed when every check passed.
func Text(lines []Line) string {
	text := []string{}
	for _, line := range lines {
		if line.Level == Section {
			text = append(text, "", "== "+line.Text+" ==")
			continue
		}
		text = append(text, fmt.Sprintf("[%s] %s", strings.ToUpper(string(line.Level)), line.Text))
	}
	if Passes(lines) {
		text = append(text, "", Passed)
	}
	return strings.Join(text, "\n")
}
