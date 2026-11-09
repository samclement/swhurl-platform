package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/samclement/swhurl-platform/v2/pages"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// The app page's documents: the stack catalogue the New app form shows, the variables a committed HelmRelease sets,
// the settings the Scale form starts from, and the Feature flags card in the template's own words.

// ConsoleStacks reads v2/contract/stacks.json from the pinned tree: the stacks in the order the file states them and
// the app types their templates build, each with the included answers its pills name.
func ConsoleStacks(tree map[string][]byte) (pages.StackCatalogue, error) {
	data, known := tree[consoleStacks]
	if !known {
		return pages.StackCatalogue{}, fmt.Errorf("cannot read %s: not in the repository", consoleStacks)
	}
	document, err := decodeOrdered(data)
	if err != nil {
		return pages.StackCatalogue{}, fmt.Errorf("cannot read %s: %w", consoleStacks, err)
	}
	types, err := appTypes(document.member("types"))
	if err != nil {
		return pages.StackCatalogue{}, fmt.Errorf("cannot read %s: %w", consoleStacks, err)
	}
	builders := map[string][]string{}
	// A stack's line names the types it builds, in the file's order.
	for _, stack := range document.member("stacks").fields {
		for _, appType := range types {
			for _, built := range appType.Stacks {
				if built == stack.Name {
					builders[stack.Name] = append(builders[stack.Name], appType.Key)
				}
			}
		}
	}
	catalogue := pages.StackCatalogue{Types: types}
	for _, stack := range document.member("stacks").fields {
		info := pages.StackInfo{Name: stack.Name, Label: pages.StackLabelOf(stack.Name),
			Repository:  scalarText(stack.Value.member("repository")),
			Description: scalarText(stack.Value.member("description"))}
		info.Types = join(builders[stack.Name], " ")
		catalogue.Stacks = append(catalogue.Stacks, info)
	}
	return catalogue, nil
}

// appTypes is the catalogue's types, in the file's order.
func appTypes(types orderedValue) ([]pages.AppType, error) {
	found := make([]pages.AppType, 0, len(types.fields))
	for _, field := range types.fields {
		answers := []pages.NamedValue{}
		for _, answer := range field.Value.member("answers").fields {
			answers = append(answers, pages.NamedValue{Name: answer.Name, Value: scalarText(answer.Value)})
		}
		stacks := []string{}
		for _, stack := range field.Value.member("stacks").items {
			stacks = append(stacks, scalarText(stack))
		}
		found = append(found, pages.AppType{Key: field.Name, Title: scalarText(field.Value.member("title")),
			Text: scalarText(field.Value.member("text")), Stacks: stacks, Answers: answers,
			Exposure: scalarText(field.Value.member("exposure")), Kind: answerKind(answers),
			Included: pages.IncludedOf(answers)})
	}
	return found, nil
}

// answerKind is a type's template answer that says what it runs as: web, worker or job.
func answerKind(answers []pages.NamedValue) string {
	for _, answer := range answers {
		if answer.Name == "kind" {
			return answer.Value
		}
	}
	return ""
}

// ConsoleDesiredVariables is the variables the instance's committed HelmRelease sets, in the document's order, and
// whether one is committed at all. A value that comes from a reference is shown by where it comes from, never read.
func ConsoleDesiredVariables(tree map[string][]byte, app, env string) ([]pages.NamedValue, bool) {
	helm, known := tree[consoleApps+"/"+app+"/"+env+"/helmrelease.json"]
	if !known {
		return nil, false
	}
	var release struct {
		Spec struct {
			Values struct {
				Controllers map[string]struct {
					Containers map[string]struct {
						Env json.RawMessage `json:"env"`
					} `json:"containers"`
				} `json:"controllers"`
			} `json:"values"`
		} `json:"spec"`
	}
	if json.Unmarshal(helm, &release) != nil {
		return nil, false
	}
	envBlock := release.Spec.Values.Controllers["main"].Containers["main"].Env
	if len(bytes.TrimSpace(envBlock)) == 0 {
		return []pages.NamedValue{}, true
	}
	found := []pages.NamedValue{}
	ordered, err := decodeOrdered(envBlock)
	if err == nil && ordered.kind == 'a' {
		for _, entry := range ordered.items {
			found = append(found, pages.NamedValue{Name: scalarText(entry.member("name")),
				Value: variableText(entry.member("value"), entry.member("valueFrom"))})
		}
		return found, true
	}
	if err == nil && ordered.kind == 'o' {
		for _, field := range ordered.fields {
			found = append(found, pages.NamedValue{Name: field.Name, Value: variableText(field.Value, field.Value)})
		}
		return found, true
	}
	return nil, false
}

// variableText is one variable as the page shows it: its value, or where the pod or a reference gets it from.
func variableText(value, from orderedValue) string {
	if text := scalarText(value); text != "" || (value.kind == 0 && value.scalar != nil) {
		return text
	}
	if value.kind == 'o' {
		from = value.member("valueFrom")
	}
	if path := scalarText(from.member("fieldRef").member("fieldPath")); path != "" {
		return "from the pod (" + path + ")"
	}
	return "from a reference"
}

// ConsoleSettings is the instance's configuration the Scale form shows as placeholders.
func ConsoleSettings(tree map[string][]byte, app, env string) map[string]string {
	instance, known := tree[consoleRecords+"/"+app+"/"+env+"/instance.json"]
	if !known {
		return map[string]string{}
	}
	var spec struct {
		Configuration struct {
			Replicas  int64 `json:"replicas"`
			MemoryMiB int64 `json:"memoryMiB"`
		} `json:"configuration"`
	}
	if json.Unmarshal(instance, &spec) != nil {
		return map[string]string{}
	}
	return map[string]string{"replicas": strconv.FormatInt(spec.Configuration.Replicas, 10),
		"memoryMiB": strconv.FormatInt(spec.Configuration.MemoryMiB, 10)}
}

// ConsoleFlagView is the Feature flags card as the template prints it: each row's declaration, its override and what
// the app runs with, and the problem that kept any of them out.
func ConsoleFlagView(environment string, card FlagCard) pages.FlagView {
	view := pages.FlagView{Problem: card.Problem}
	for _, row := range card.Rows {
		line := pages.FlagLine{Name: row.Name, Undeclared: row.Undeclared, Overridden: row.Overridden,
			Stale: row.Stale, AgeKnown: row.AgeDays >= 0, AgeWord: pages.FlagAge(row.AgeDays)}
		if !row.Undeclared {
			line.Description, line.Type = row.Flag.Description(), row.Flag.Type()
			line.Declared = pages.JinjaJSON(plainValue(row.Flag.Declared(environment)))
			line.Min = scalarTextValue(row.Flag.Declaration.Get("min"))
			line.Max = scalarTextValue(row.Flag.Declaration.Get("max"))
			if values, ok := row.Flag.Declaration.Get("enum").([]any); ok {
				for _, value := range values {
					line.Enum = append(line.Enum, scalarTextValue(value))
				}
			}
			line.Effective = pages.JinjaJSON(plainValue(row.Effective))
			line.EffectiveRaw = scalarTextValueOf(row.Effective)
		}
		if row.Overridden && row.Override.Readable {
			line.OverrideText = pages.JinjaJSON(plainValue(row.Override.Value))
		}
		view.Rows = append(view.Rows, line)
	}
	return view
}

// plainValue is a declaration's value as the JSON a page prints it as: a source object becomes a mapping.
func plainValue(value any) any {
	switch found := value.(type) {
	case producer.Object:
		fields := map[string]any{}
		for _, field := range found {
			if name, ok := field.Name.(string); ok {
				fields[name] = plainValue(field.Value)
			}
		}
		return fields
	case []any:
		items := make([]any, 0, len(found))
		for _, item := range found {
			items = append(items, plainValue(item))
		}
		return items
	}
	return value
}

// scalarTextValue is a declared scalar as Python prints it, empty when the declaration states none.
func scalarTextValue(value any) string {
	if value == nil {
		return ""
	}
	return scalarTextValueOf(value)
}

// scalarTextValueOf is a scalar's own text: a string as written, a whole number, true or false, or a number.
func scalarTextValueOf(value any) string {
	switch found := value.(type) {
	case string:
		return found
	case bool:
		if found {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(found, 10)
	case json.Number:
		return found.String()
	case float64:
		return strconv.FormatFloat(found, 'g', -1, 64)
	}
	return fmt.Sprint(value)
}

// orderedValue is one JSON value with an object's keys in the document's order: what a page that prints a document's
// own order reads.
type orderedValue struct {
	kind   byte // 'o' for an object, 'a' for an array, 0 for a scalar
	fields []orderedField
	items  []orderedValue
	scalar any
}

// orderedField is one key of an ordered object.
type orderedField struct {
	Name  string
	Value orderedValue
}

// member is an object's field, or the zero value when it has none.
func (v orderedValue) member(name string) orderedValue {
	for _, field := range v.fields {
		if field.Name == name {
			return field.Value
		}
	}
	return orderedValue{}
}

// decodeOrdered reads one JSON document, keeping an object's keys in the document's order.
func decodeOrdered(data []byte) (orderedValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	found, err := readOrdered(decoder)
	if err != nil {
		return orderedValue{}, err
	}
	return found, nil
}

// readOrdered reads one value from a decoder.
func readOrdered(decoder *json.Decoder) (orderedValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return orderedValue{}, err
	}
	switch found := token.(type) {
	case json.Delim:
		switch found {
		case '{':
			value := orderedValue{kind: 'o'}
			for decoder.More() {
				name, err := decoder.Token()
				if err != nil {
					return orderedValue{}, err
				}
				member, err := readOrdered(decoder)
				if err != nil {
					return orderedValue{}, err
				}
				value.fields = append(value.fields, orderedField{Name: fmt.Sprint(name), Value: member})
			}
			_, err := decoder.Token()
			return value, err
		case '[':
			value := orderedValue{kind: 'a'}
			for decoder.More() {
				item, err := readOrdered(decoder)
				if err != nil {
					return orderedValue{}, err
				}
				value.items = append(value.items, item)
			}
			_, err := decoder.Token()
			return value, err
		}
	}
	return orderedValue{scalar: token}, nil
}

// scalarText is a scalar's text, empty for an absent value.
func scalarText(value orderedValue) string {
	if value.kind != 0 || value.scalar == nil {
		return ""
	}
	return scalarTextValueOf(value.scalar)
}

// join is a list of words with a separator between them.
func join(words []string, separator string) string {
	found := ""
	for i, word := range words {
		if i > 0 {
			found += separator
		}
		found += word
	}
	return found
}
