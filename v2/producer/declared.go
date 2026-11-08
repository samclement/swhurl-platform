package producer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A flag's name: kebab case where each part starts with a letter, so its camelCase accessor is unique.
const flagNamePattern = "[a-z][a-z0-9]*(-[a-z][a-z0-9]*)*"

// maxFlagName is the longest name a declaration may state, in bytes.
const maxFlagName = 40

// Flag is one flag an image declares, as its offered flags document states it: the source's own flags block, encoded
// to JSON. Its values are Object, []any, string, bool, int64, json.Number, float64 or nil, as a source mapping's are.
type Flag struct {
	Name        string
	Declaration Object
}

// Type is the flag's type: boolean, integer, number or string.
func (f Flag) Type() string { return text(f.Declaration.Get("type")) }

// Description is what the flag means, empty when the declaration says nothing.
func (f Flag) Description() string { return text(f.Declaration.Get("description")) }

// Declared is the value the flag has in one environment: the environment's own value when the declaration states
// one, and its default otherwise. Declarations refuses a flag with no default, so this always answers.
func (f Flag) Declared(environment string) any {
	if f.Declaration.has(environment) {
		return f.Declaration.Get(environment)
	}
	return f.Declaration.Get("default")
}

// Text is a value typed as the flag declares it: what a form's text becomes before anything is stored. It refuses,
// in the card's own words, text that is not of the declared type, a value outside min and max, and a string outside
// the enumeration. A number with no decimal point or exponent is kept whole, as v1 stored it.
func (f Flag) Text(input string) (any, error) {
	value, err := typedText(f.Type(), strings.TrimSpace(input))
	if err != nil {
		return nil, err
	}
	return f.accepted(value)
}

// accepted is the declaration's judgement of one typed value: its type, its bounds and its enumeration, in that
// order, so a refusal names the first thing wrong with it.
func (f Flag) accepted(value any) (any, error) {
	kind := f.Type()
	if !typed(value, kind) {
		return nil, fmt.Errorf("the value must be %s, not %s", article(kind), render(value))
	}
	if outsideBounds(f.Declaration, value, kind) {
		return nil, errors.New("the value is outside min and max")
	}
	if enum, ok := f.Declaration.Get("enum").([]any); ok && !contains(enum, value) {
		return nil, fmt.Errorf("the value must be one of %s", enumeration(enum))
	}
	return value, nil
}

// typedText reads a form's text as one of the four types a declaration may state. A number with no decimal point or
// exponent is kept whole, as v1 stored it, and a string is kept as written once the form's space is trimmed.
func typedText(kind, value string) (any, error) {
	switch kind {
	case "boolean":
		if value != "true" && value != "false" {
			return nil, errors.New("choose true or false")
		}
		return value == "true", nil
	case "integer":
		whole, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, errors.New("give a whole number")
		}
		return whole, nil
	case "number":
		if strings.ContainsAny(value, ".eE") {
			fraction, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return nil, errors.New("give a number")
			}
			return fraction, nil
		}
		whole, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, errors.New("give a number")
		}
		return whole, nil
	}
	return value, nil
}

// ValidFlagName says whether a name is one an override may carry: kebab case, each part starting with a letter, at
// most 40 characters. Clearing an override needs no declaration, so the name alone is checked.
func ValidFlagName(name string) bool {
	return len(name) <= maxFlagName && matches(flagNamePattern, name)
}

// Declarations reads back the flags document an image offers (flags.json, the encoded flags block of its
// swhurl.yaml). It is the source's own rules applied to the document, so a document swhurl.yaml could not have
// declared is refused with the same words. Flags are returned in name order, which is the order a card lists them.
func Declarations(document []byte) ([]Flag, error) {
	value, err := decodeDocument(document)
	if err != nil {
		return nil, err
	}
	object, ok := value.(Object)
	if !ok {
		return nil, errors.New("flags.json: must be a mapping of flag names to declarations")
	}
	if err := (flagRules{"flags.json"}).flags(object); err != nil {
		return nil, err
	}
	flags := make([]Flag, 0, len(object))
	for _, field := range object {
		name, ok := field.Name.(string)
		if !ok {
			continue // the rules refuse a name that is not text, so this cannot happen twice
		}
		flags = append(flags, Flag{Name: name, Declaration: field.Value.(Object)})
	}
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	return flags, nil
}

// decodeDocument decodes a JSON document into the source's own value types, refusing a duplicate key: an object
// states each name once, and mapping order is the document's, never a map's.
func decodeDocument(document []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("flags.json: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	if _, err := decoder.Token(); err == nil {
		return nil, errors.New("flags.json: trailing content after the document")
	}
	return value, nil
}

// decodeValue reads one JSON value, keeping an object's order and refusing a name it states twice. A number becomes
// the type the source states: a whole number int64 (json.Number when it is too large for one), anything with a
// decimal point or an exponent a float64.
func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		if token == '{' {
			return decodeObject(decoder)
		}
		if token == '[' {
			return decodeList(decoder)
		}
		return nil, fmt.Errorf("unexpected %v", token)
	case json.Number:
		return numberValue(token), nil
	}
	return token, nil
}

// decodeObject reads one object's fields in document order.
func decodeObject(decoder *json.Decoder) (Object, error) {
	object, names := Object{}, map[string]bool{}
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		text, ok := name.(string)
		if !ok || names[text] {
			return nil, fmt.Errorf("an object states %v twice", name)
		}
		names[text] = true
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		object = append(object, Field{Name: text, Value: value})
	}
	_, err := decoder.Token()
	return object, err
}

// decodeList reads one list's values in document order.
func decodeList(decoder *json.Decoder) ([]any, error) {
	list := []any{}
	for decoder.More() {
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		list = append(list, value)
	}
	_, err := decoder.Token()
	return list, err
}

// numberValue is a JSON number as the source would state it.
func numberValue(token json.Number) any {
	text := string(token)
	if !strings.ContainsAny(text, ".eE") {
		if whole, err := strconv.ParseInt(text, 10, 64); err == nil {
			return whole
		}
		return token
	}
	if fraction, err := strconv.ParseFloat(text, 64); err == nil {
		return fraction
	}
	return token
}

// article is how a type is named in a sentence: "an integer", "a boolean".
func article(kind string) string {
	if kind == "integer" {
		return "an " + kind
	}
	return "a " + kind
}

// render is a value as a person reads it, for a refusal that says what was given.
func render(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case nil:
		return "nothing"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// enumeration is a declaration's set of strings as a sentence: "none, sale".
func enumeration(values []any) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, fmt.Sprint(value))
	}
	return strings.Join(names, ", ")
}

// contains says whether a typed value is one the enumeration lists.
func contains(values []any, value any) bool {
	for _, each := range values {
		if each == value {
			return true
		}
	}
	return false
}
