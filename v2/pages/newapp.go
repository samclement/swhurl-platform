package pages

import (
	"fmt"
	"sort"
	"strings"
)

// NewAppChoices is what the New app form asks v2 to create: the repository to make and the settings its staging
// instance starts with. Record is what the job keeps to finish the work later — the same document the Python console
// wrote, so a job cut short before the cutover can still be finished after it.
type NewAppChoices struct {
	Name, Stack, Description, Type string
	Answers                        []NamedValue
	Flags                          []string
	Record                         map[string]any
}

// NewAppJob is the record's kind: the New app form's choices.
const NewAppRecordKind = "new-app"

// NewAppChoose reads the New app form. The type sets every template answer and the exposure staging starts with,
// unless the form chose one; the team must be a registered one; a worker has no exposure at all. When the lists the
// form offered are given (nil is a list that could not be read), a call or a subscription outside them is refused.
// The template's own questions are not read here: `swhurl-v2 app-repo` refuses an answer the template no longer
// offers.
func NewAppChoose(fields map[string][]string, catalogue StackCatalogue, teams map[string]TeamInfo,
	callable, offered map[string]bool) (NewAppChoices, error) {
	name, err := newAppName(fields)
	if err != nil {
		return NewAppChoices{}, err
	}
	stack := strings.TrimSpace(field(fields, "stack"))
	appType, err := newAppType(fields, stack, catalogue)
	if err != nil {
		return NewAppChoices{}, err
	}
	flags, err := newAppFlags(fields, appType, teams)
	if err != nil {
		return NewAppChoices{}, err
	}
	connections, err := newAppConnections(fields, callable, offered)
	if err != nil {
		return NewAppChoices{}, err
	}
	flags = append(flags, connections...)
	description := strings.Join(strings.Fields(field(fields, "description")), " ")
	if len(description) > 200 {
		description = description[:200]
	}
	chosen := NewAppChoices{Name: name, Stack: stack, Description: description, Type: appType.Key,
		Answers: appType.Answers, Flags: flags}
	chosen.Record = map[string]any{"kind": NewAppRecordKind, "name": chosen.Name, "stack": chosen.Stack,
		"description": chosen.Description, "answers": answersMap(chosen.Answers), "extra": flags, "type": chosen.Type}
	return chosen, nil
}

// newAppName is the repository's name: a DNS label, and not the platform's own notification service.
func newAppName(fields map[string][]string) (string, error) {
	name := strings.TrimSpace(field(fields, "name"))
	if !consoleName.MatchString(name) {
		return "", fmt.Errorf("name must be a DNS label: lowercase letters, digits and hyphens, at most 40 characters")
	}
	if name == "notify" {
		return "", fmt.Errorf("notify is reserved for the platform notification service")
	}
	return name, nil
}

// newAppType is the type the form chose, and the stack it is built with: the type sets every template answer.
func newAppType(fields map[string][]string, stack string, catalogue StackCatalogue) (AppType, error) {
	appType, known := catalogue.Type(strings.TrimSpace(field(fields, "type")))
	if !known {
		return AppType{}, fmt.Errorf("type must be one of %s", strings.Join(catalogue.Keys(), ", "))
	}
	if !oneOf(stack, appType.Stacks) {
		return AppType{}, fmt.Errorf("a %s is built with %s, not %s", appType.Title,
			strings.Join(appType.Stacks, " or "), stack)
	}
	return appType, nil
}

// newAppFlags are the settings staging starts with: the team that owns the app, and the exposure of a web app. A
// worker has none, because its own swhurl.yaml makes it private.
func newAppFlags(fields map[string][]string, appType AppType, teams map[string]TeamInfo) ([]string, error) {
	team := strings.TrimSpace(field(fields, "team"))
	if len(teams) == 0 {
		return nil, fmt.Errorf("no team is registered (%s): add one on the Teams page first; every app belongs to "+
			"a team", TeamsRecord)
	}
	if _, registered := teams[team]; !registered {
		return nil, fmt.Errorf("choose the team that owns the app: one of %s (%s)",
			strings.Join(sortedKeys(teams), ", "), TeamsRecord)
	}
	flags := []string{"team=" + team}
	exposure, host := strings.TrimSpace(field(fields, "exposure")), strings.TrimSpace(field(fields, "host"))
	if exposure == "" {
		exposure = appType.Exposure
	}
	if host != "" {
		return nil, fmt.Errorf("v2 derives the address from the app name and the exposure: it does not model a host")
	}
	if exposure == "public" {
		return nil, fmt.Errorf("v2 does not model public exposure yet: choose signed-in, or private for an API, " +
			"worker or job")
	}
	if exposure == "" || appType.Kind != "web" {
		return flags, nil
	}
	if !oneOf(exposure, consoleExposures) {
		return nil, fmt.Errorf("exposure must be one of %s", strings.Join(consoleExposures, ", "))
	}
	return append(flags, "exposure="+exposure), nil
}

// newAppConnections are the app's connections as v2's settings: its record starts with neither list, so these are the
// whole of what it calls and receives. A form that chose nothing is no refusal of the form itself.
func newAppConnections(fields map[string][]string, callable, offered map[string]bool) ([]string, error) {
	if len(fields["calls"]) == 0 && len(fields["subscribe"]) == 0 {
		return nil, nil
	}
	connections, err := connectFlags(fields)
	if err != nil {
		return nil, err
	}
	if callable != nil && offered != nil {
		if problem := Unoffered(connections.Calls, connections.Subscriptions, callable, offered); problem != "" {
			return nil, fmt.Errorf("%s", problem)
		}
	}
	found := []string{}
	if len(connections.Calls) > 0 {
		found = append(found, "calls="+strings.Join(connections.Calls, ","))
	}
	if len(connections.Subscriptions) > 0 {
		found = append(found, "subscriptions="+strings.Join(connections.Subscriptions, ","))
	}
	return found, nil
}

// NewAppFromRecord reads the choices a job kept. A record whose choices v2 no longer supports is refused rather than
// finished with them, because the pull request it would open is not the one the operator asked for.
func NewAppFromRecord(record map[string]any, catalogue StackCatalogue) (NewAppChoices, error) {
	text := func(name string) string {
		value, _ := record[name].(string)
		return value
	}
	name, stack := text("name"), text("stack")
	flags, err := recordFlags(record)
	if err != nil {
		return NewAppChoices{}, err
	}
	if !consoleName.MatchString(name) || !oneOf(stack, catalogue.Names()) {
		return NewAppChoices{}, errUnusableRecord
	}
	answers := []NamedValue{}
	if stored, ok := record["answers"].(map[string]any); ok {
		for _, answer := range sortedKeys(stored) {
			answers = append(answers, NamedValue{Name: answer, Value: stored[answer].(string)})
		}
	}
	appType := text("type")
	if appType == "" {
		derived, err := catalogue.TypeOf(stack, answers)
		if err != nil {
			return NewAppChoices{}, err
		}
		appType = derived
	}
	return NewAppChoices{Name: name, Stack: stack, Description: text("description"), Type: appType,
		Answers: answers, Flags: flags, Record: record}, nil
}

// TeamsRecord is the document a team is registered in: the console names it in the sentence that refuses an
// unregistered one, because that sentence is what an operator has to edit.
const TeamsRecord = "v2-records/teams.json"

// Type is one app type by key.
func (c StackCatalogue) Type(key string) (AppType, bool) {
	for _, one := range c.Types {
		if one.Key == key {
			return one, true
		}
	}
	return AppType{}, false
}

// Keys are the app types' keys, in the catalogue's order.
func (c StackCatalogue) Keys() []string {
	keys := make([]string, 0, len(c.Types))
	for _, one := range c.Types {
		keys = append(keys, one.Key)
	}
	return keys
}

// Names are the stacks' names, in the catalogue's order.
func (c StackCatalogue) Names() []string {
	names := make([]string, 0, len(c.Stacks))
	for _, one := range c.Stacks {
		names = append(names, one.Name)
	}
	return names
}

// TypeOf is the type these answers are, for a stack: a job record that states no type can be finished only when its
// answers are exactly one type's, because the command would otherwise render another app.
func (c StackCatalogue) TypeOf(stack string, answers []NamedValue) (string, error) {
	want := make(map[string]string, len(answers))
	for _, answer := range answers {
		want[answer.Name] = answer.Value
	}
	for _, one := range c.Types {
		if !oneOf(stack, one.Stacks) || len(one.Answers) != len(want) {
			continue
		}
		same := true
		for _, answer := range one.Answers {
			if want[answer.Name] != answer.Value {
				same = false
				break
			}
		}
		if same {
			return one.Key, nil
		}
	}
	return "", fmt.Errorf("the record of that job states no app type and its answers match none: create the app " +
		"again from New app")
}

// Unoffered refuses a choice outside the lists a form offered: a call to an app that is not a web app there, or an
// event nothing there publishes, before a repository is created for an app that could not then be added.
func Unoffered(calls, subscriptions []string, callable, offered map[string]bool) string {
	var missing []string
	for _, name := range calls {
		if !callable[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range subscriptions {
		if !offered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return strings.Join(missing, ", ") + ": not a web app, or an event, that staging has now; reload the form"
	}
	return ""
}

// oneOf is whether the list holds the value.
func oneOf(value string, list []string) bool {
	for _, found := range list {
		if found == value {
			return true
		}
	}
	return false
}

// sortedKeys is an object's keys, sorted.
func sortedKeys[V any](object map[string]V) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// errUnusableRecord is what a kept record says when v2 could not create the app it describes.
var errUnusableRecord = fmt.Errorf("the record of that job cannot be used to finish it; create the app again from New app")

// recordFlags is a kept record's settings, refused when they are not settings at all or ask for what v2 no longer
// models: the pull request it would open is not the one the operator asked for.
func recordFlags(record map[string]any) ([]string, error) {
	flags, isList := stringsOf(record["extra"])
	if !isList {
		return nil, errUnusableRecord
	}
	for _, flag := range flags {
		if !strings.Contains(flag, "=") {
			return nil, errUnusableRecord
		}
		if strings.HasPrefix(flag, "publish=") || strings.HasPrefix(flag, "host=") {
			return nil, fmt.Errorf("these choices are no longer supported: the image states what it publishes, " +
				"and v2 derives the address; start a new job")
		}
	}
	return flags, nil
}

// answersMap is a type's answers as the record keeps them: an object, whose keys the document sorts.
func answersMap(answers []NamedValue) map[string]any {
	found := make(map[string]any, len(answers))
	for _, answer := range answers {
		found[answer.Name] = answer.Value
	}
	return found
}

// stringsOf is a record's list of strings; anything else is no list at all.
func stringsOf(value any) ([]string, bool) {
	switch found := value.(type) {
	case []string:
		return found, true
	case []any:
		texts := make([]string, 0, len(found))
		for _, one := range found {
			text, ok := one.(string)
			if !ok {
				return nil, false
			}
			texts = append(texts, text)
		}
		return texts, true
	}
	return nil, false
}
