package legacy

import (
	"encoding/json"
	"sort"
)

// record is one legacy document decoded as fields. Every field is either taken by the translation or
// reported as unused, so a legacy fact cannot disappear because this translator did not look at it.
type record struct {
	name   string
	fields map[string]json.RawMessage
}

// open decodes one document: a JSON object whose version this translator knows.
func open(name string, data []byte) (*record, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, refuse(Invalid, name, "single_json_object_required")
	}
	version, found := fields["version"]
	var number int32
	if !found || json.Unmarshal(version, &number) != nil || number != 1 {
		return nil, refuse(Unsupported, name+": version", "only_version_1")
	}
	delete(fields, "version")
	return &record{name: name, fields: fields}, nil
}

// take removes one field, so a field the translation used is not also reported as unused.
func (r *record) take(field string) (json.RawMessage, bool) {
	raw, found := r.fields[field]
	delete(r.fields, field)
	return raw, found
}

// text is one string field, empty when the document does not state it.
func (r *record) text(field string) (string, error) {
	raw, found := r.take(field)
	if !found {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", refuse(Invalid, r.name+": "+field, "expected_a_string")
	}
	return value, nil
}

// texts is one list-of-strings field, empty when the document does not state it.
func (r *record) texts(field string) ([]string, error) {
	raw, found := r.take(field)
	if !found {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, refuse(Invalid, r.name+": "+field, "expected_a_list_of_strings")
	}
	for _, value := range values {
		if value == "" {
			return nil, refuse(Invalid, r.name+": "+field, "expected_a_list_of_strings")
		}
	}
	return values, nil
}

// peek reads one string field without taking it: a fact the translation reads only as evidence stays
// reported as unused, so it is not silently dropped by being looked at.
func (r *record) peek(field string) (string, error) {
	raw, found := r.fields[field]
	if !found {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", refuse(Invalid, r.name+": "+field, "expected_a_string")
	}
	return value, nil
}

// boolean is one boolean field, false when the document does not state it.
func (r *record) boolean(field string) (bool, error) {
	raw, found := r.take(field)
	if !found {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, refuse(Invalid, r.name+": "+field, "expected_a_boolean")
	}
	return value, nil
}

// integer is one integer field the preview requires, zero when the document does not state it.
func (r *record) integer(field string) (int32, error) {
	value, _, err := r.optionalInteger(field)
	return value, err
}

// optionalInteger is one integer field, and whether the document stated it: zero is a value.
func (r *record) optionalInteger(field string) (int32, bool, error) {
	raw, found := r.take(field)
	if !found {
		return 0, false, nil
	}
	var value int32
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false, refuse(Invalid, r.name+": "+field, "expected_an_integer")
	}
	return value, true, nil
}

// object is one nested document, decoded the same way so its fields are accounted for too.
func (r *record) object(field string) (*record, error) {
	raw, found := r.take(field)
	empty := &record{name: r.name + ": " + field}
	if !found {
		return empty, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, refuse(Invalid, r.name+": "+field, "expected_an_object")
	}
	return &record{name: empty.name, fields: fields}, nil
}

// unused is the fields this translation did not make a fact of, sorted: each becomes a gap.
func (r *record) unused() []string {
	names := make([]string, 0, len(r.fields))
	for name := range r.fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
