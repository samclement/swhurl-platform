package pages

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A console form's change: the fields an operator filled in, read as the flags v2's own command takes. Every refusal
// here is the console's, decided before a job starts, in the words its forms have always used; a value the command
// then refuses is the job's own line. The kinds are the change table's, so a form cannot ask for a change no branch
// could carry.

// ConsoleFormFlags is one app form's change as v2 states it: settings for `edit`, calls and drops for a connection
// change, allows for an access change, and the image a promotion was reviewed at. Each list is what the form asked to
// add or remove, never the whole list, so a pull request merged but not yet applied is not undone.
type ConsoleFormFlags struct {
	Settings                     []string
	Calls, Subscriptions         []string
	DropCalls, DropSubscriptions []string
	Allows, Disallows            []string
	Image                        string
	Description, Rename          string
	Transport, Address           string // a channel's transport and the address the command reads on its standard input
}

// consoleSubscription is an event a subscription names: <app>.<event>, both DNS labels.
var consoleSubscription = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?\.[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// consoleAllow is a caller an instance's allows name: an app, or team:<team>.
var consoleAllow = regexp.MustCompile(`^(team:)?[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// consoleExposures are what a form may offer to set, with the word the refusal lists: v2 models two of the three, and
// the sentence names all three as v2's records do.
var consoleExposures = []string{"private", "authenticated-web", "public"}

// ConsoleFormChange reads one app form's fields as the change its kind names: "scale", "expose", "connect",
// "disconnect", "allow", "team", "remove" or "promote". declared is the instance's own inventory from the records
// (nil when it has none), and teams the registered teams; both are only read by the form that names them.
func ConsoleFormChange(kind, name string, fields map[string][]string, declared *Inventory,
	teams map[string]TeamInfo) (ConsoleFormFlags, error) {
	if strings.HasPrefix(kind, "team-") {
		return ConsoleTeamChange(kind, name, fields)
	}
	return appChange(kind, name, fields, declared, teams)
}

// appChange reads one app instance's own form.
func appChange(kind, name string, fields map[string][]string, declared *Inventory,
	teams map[string]TeamInfo) (ConsoleFormFlags, error) {
	switch kind {
	case "scale":
		return settingsOf(scaleSettings(fields))
	case "expose":
		return settingsOf(exposureSettings(fields))
	case "connect":
		return connectFlags(fields)
	case "disconnect":
		return disconnectFlags(fields)
	case "allow":
		return allowFlags(fields, declared)
	case "team":
		return settingsOf(teamSettings(name, fields, declared, teams))
	case "remove":
		return ConsoleFormFlags{}, nil
	case "promote":
		if strings.TrimSpace(field(fields, "image")) == "" {
			return ConsoleFormFlags{}, fmt.Errorf("refresh the staging app page and review the image before promotion")
		}
		return ConsoleFormFlags{}, nil
	}
	return ConsoleFormFlags{}, fmt.Errorf("no such change")
}

// settingsOf is one field set as a change's flags.
func settingsOf(settings []string, err error) (ConsoleFormFlags, error) {
	if err != nil {
		return ConsoleFormFlags{}, err
	}
	return ConsoleFormFlags{Settings: settings}, nil
}

// scaleSettings is a replicas and memory change. CPU requests and limits are not modelled, and one of the two values
// must be given: an empty form changes nothing and says so.
func scaleSettings(fields map[string][]string) ([]string, error) {
	var settings []string
	if replicas := strings.TrimSpace(field(fields, "replicas")); replicas != "" {
		value, err := strconv.ParseUint(replicas, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("Replicas '%s' is not a whole number", replicas)
		}
		settings = append(settings, "replicas="+strconv.FormatUint(value, 10))
	}
	if memory := strings.TrimSpace(field(fields, "memory")); memory != "" {
		digits, _ := strings.CutSuffix(memory, "Mi")
		value, err := strconv.ParseUint(digits, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("Memory '%s' is not a whole number of MiB, such as 256 or 256Mi", memory)
		}
		settings = append(settings, "memoryMiB="+strconv.FormatUint(value, 10))
	}
	if strings.TrimSpace(field(fields, "cpu")) != "" || strings.TrimSpace(field(fields, "memory_limit")) != "" {
		return nil, fmt.Errorf("v2 does not model CPU requests or memory limits: change replicas and memory only")
	}
	if len(settings) == 0 {
		return nil, fmt.Errorf("change replicas or memory")
	}
	return settings, nil
}

// exposureSettings is the one name change a form may make: who can reach the app. A host is derived, not stored.
func exposureSettings(fields map[string][]string) ([]string, error) {
	exposure := strings.TrimSpace(field(fields, "exposure"))
	if !slices.Contains(consoleExposures, exposure) {
		return nil, fmt.Errorf("exposure must be one of %s", strings.Join(consoleExposures, ", "))
	}
	if strings.TrimSpace(field(fields, "host")) != "" {
		return nil, fmt.Errorf("v2 derives the address from the app name and the exposure: it does not model a host")
	}
	return []string{"exposure=" + exposure}, nil
}

// connectFlags is what an app's connection form adds: apps it calls and events it receives.
func connectFlags(fields map[string][]string) (ConsoleFormFlags, error) {
	calls := names(fields, "calls")
	subscriptions := names(fields, "subscribe")
	if bad := append(invalid(calls, consoleName), invalid(subscriptions, consoleSubscription)...); len(bad) > 0 {
		return ConsoleFormFlags{}, fmt.Errorf("%s: a call names an app, a subscription <app>.<event>", strings.Join(bad, ", "))
	}
	if len(calls) == 0 && len(subscriptions) == 0 {
		return ConsoleFormFlags{}, fmt.Errorf("choose an app to call or an event to receive")
	}
	return ConsoleFormFlags{Calls: calls, Subscriptions: subscriptions}, nil
}

// disconnectFlags is the connection a row's Remove names: one call, one subscription, or a refusal to remove a
// publication or an egress grant that is not a connection.
func disconnectFlags(fields map[string][]string) (ConsoleFormFlags, error) {
	role, other, event := strings.TrimSpace(field(fields, "role")), strings.TrimSpace(field(fields, "app")),
		strings.TrimSpace(field(fields, "event"))
	switch {
	case role == "calls" && consoleName.MatchString(other):
		return ConsoleFormFlags{DropCalls: []string{other}}, nil
	case role == "subscribes" && consoleSubscription.MatchString(other+"."+event):
		return ConsoleFormFlags{DropSubscriptions: []string{other + "." + event}}, nil
	case role == "publishes":
		return ConsoleFormFlags{}, fmt.Errorf("publications come from the release: v2 does not remove one on the platform")
	case role == "reaches":
		return ConsoleFormFlags{}, fmt.Errorf("an egress grant is set with swhurl-v2 edit --set egress=, so this cannot be removed here")
	}
	return ConsoleFormFlags{}, fmt.Errorf("no such connection to remove")
}

// allowFlags is who may call this instance: added and removed by name, and a form that would change nothing says so.
func allowFlags(fields map[string][]string, declared *Inventory) (ConsoleFormFlags, error) {
	add, remove := names(fields, "add"), names(fields, "remove")
	if bad := append(invalid(add, consoleAllow), invalid(remove, consoleAllow)...); len(bad) > 0 {
		return ConsoleFormFlags{}, fmt.Errorf("%s: an allowed caller is an app name or team:<team>", strings.Join(bad, ", "))
	}
	if len(add) == 0 && len(remove) == 0 {
		return ConsoleFormFlags{}, fmt.Errorf("name an app or team:<team> to allow or stop allowing")
	}
	if declared != nil {
		kept := make([]string, 0, len(declared.Allows))
		for _, allow := range declared.Allows {
			if !slices.Contains(remove, allow) {
				kept = append(kept, allow)
			}
		}
		if merged := append(kept, unique(add)...); slices.Equal(merged, declared.Allows) {
			return ConsoleFormFlags{}, fmt.Errorf("that changes nothing: name an app or team:<team> to allow or stop allowing")
		}
	}
	return ConsoleFormFlags{Allows: add, Disallows: remove}, nil
}

// teamSettings is the team an app belongs to: a registered one, and not the team it already has.
func teamSettings(name string, fields map[string][]string, declared *Inventory, teams map[string]TeamInfo) ([]string, error) {
	team := strings.TrimSpace(field(fields, "team"))
	if !consoleName.MatchString(team) {
		return nil, fmt.Errorf("choose the team that owns the app")
	}
	if teams != nil {
		registered := make([]string, 0, len(teams))
		for key := range teams {
			registered = append(registered, key)
		}
		slices.Sort(registered)
		if _, known := teams[team]; !known {
			listed := strings.Join(registered, ", ")
			if listed == "" {
				listed = "none"
			}
			return nil, fmt.Errorf("choose a registered team: one of %s", listed)
		}
	}
	if declared != nil && declared.Team == team {
		return nil, fmt.Errorf("%s already belongs to %s: that changes nothing", name, team)
	}
	return []string{"team=" + team}, nil
}

// teamChange reads one team form: the name a change is to, and what it changes. A team's name and its new name are
// DNS labels, because the branch and the registry both carry them; a channel's address is the command's to read on its
// standard input, so it is never an argument, a page's text or a log line.
func ConsoleTeamChange(kind, name string, fields map[string][]string) (ConsoleFormFlags, error) {
	if !consoleName.MatchString(name) {
		return ConsoleFormFlags{}, fmt.Errorf("team name: lowercase letters, digits and hyphens, at most 40 characters")
	}
	switch kind {
	case "team-channel-set":
		transport := strings.TrimSpace(field(fields, "transport"))
		if transport == "" {
			transport = "ntfy"
		}
		return ConsoleFormFlags{Transport: transport, Address: strings.TrimSpace(field(fields, "address"))}, nil
	case "team-channel-remove", "team-remove":
		return ConsoleFormFlags{}, nil
	}
	found := ConsoleFormFlags{Description: strings.Join(strings.Fields(field(fields, "description")), " ")}
	if kind == "team-add" {
		return found, nil
	}
	rename := strings.TrimSpace(field(fields, "rename"))
	if rename == "" || rename == name {
		return found, nil
	}
	if !consoleName.MatchString(rename) {
		return ConsoleFormFlags{}, fmt.Errorf("new name: lowercase letters, digits and hyphens, at most 40 characters")
	}
	found.Rename = rename
	return found, nil
}

// field is one form field's first value.
func field(fields map[string][]string, name string) string {
	if values := fields[name]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// names is a form's chosen names: every value of the field, each split on commas, trimmed, without the empty ones.
func names(fields map[string][]string, name string) []string {
	var found []string
	for _, value := range fields[name] {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				found = append(found, part)
			}
		}
	}
	return found
}

// invalid is the names that do not match the shape the form's label states.
func invalid(names []string, pattern *regexp.Regexp) []string {
	var bad []string
	for _, name := range names {
		if !pattern.MatchString(name) {
			bad = append(bad, name)
		}
	}
	return bad
}

// unique is the names without a repeat, in the order given.
func unique(names []string) []string {
	found := make([]string, 0, len(names))
	for _, name := range names {
		if !slices.Contains(found, name) {
			found = append(found, name)
		}
	}
	return found
}
