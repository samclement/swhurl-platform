package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// OrderedValue is one JSON value with an object's keys in the document's order: what a reader that keeps a
// document's own order (the stack catalogue, a HelmRelease's environment) decodes.
type OrderedValue struct {
	Kind   byte // 'o' for an object, 'a' for an array, 0 for a scalar
	Fields []OrderedField
	Items  []OrderedValue
	Scalar any
}

// OrderedField is one key of an ordered object.
type OrderedField struct {
	Name  string
	Value OrderedValue
}

// Member is an object's field, or the zero value when it has none.
func (v OrderedValue) Member(name string) OrderedValue {
	for _, field := range v.Fields {
		if field.Name == name {
			return field.Value
		}
	}
	return OrderedValue{}
}

// Text is a scalar's text, empty for an absent value or one that is not a scalar.
func (v OrderedValue) Text() string {
	if v.Kind != 0 || v.Scalar == nil {
		return ""
	}
	return ScalarText(v.Scalar)
}

// DecodeOrdered reads one JSON document, keeping an object's keys in the document's order.
func DecodeOrdered(data []byte) (OrderedValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	found, err := readOrdered(decoder)
	if err != nil {
		return OrderedValue{}, err
	}
	return found, nil
}

// readOrdered reads one value from a decoder.
func readOrdered(decoder *json.Decoder) (OrderedValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return OrderedValue{}, err
	}
	switch found := token.(type) {
	case json.Delim:
		switch found {
		case '{':
			value := OrderedValue{Kind: 'o'}
			for decoder.More() {
				name, err := decoder.Token()
				if err != nil {
					return OrderedValue{}, err
				}
				member, err := readOrdered(decoder)
				if err != nil {
					return OrderedValue{}, err
				}
				value.Fields = append(value.Fields, OrderedField{Name: fmt.Sprint(name), Value: member})
			}
			_, err := decoder.Token()
			return value, err
		case '[':
			value := OrderedValue{Kind: 'a'}
			for decoder.More() {
				item, err := readOrdered(decoder)
				if err != nil {
					return OrderedValue{}, err
				}
				value.Items = append(value.Items, item)
			}
			_, err := decoder.Token()
			return value, err
		}
	}
	return OrderedValue{Scalar: token}, nil
}

// ScalarText is a scalar's own text as Python prints it: a string as written, a whole number, True or False, or a
// number.
func ScalarText(value any) string {
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
