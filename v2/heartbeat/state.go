package heartbeat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// State is every job's flag by its Key, including jobs the document names that are no longer watched.
type State map[string]Flag

// The two documents a run refuses, leaving them unchanged: one that is not JSON, and one that is not a state.
var (
	ErrUnreadable = errors.New("heartbeat state cannot be read; leaving it unchanged")
	ErrInvalid    = errors.New("heartbeat state is invalid; leaving it unchanged")
)

// Field names of a flag, in the order the document writes them.
const sinceField, lastField, othersField = "alerting_since", "last_alert", "others"

// DecodeState reads the state document: the first watched job's flag at the top level and any other job's under
// "others", an object that does not name the first job. A flag has exactly its two fields, both null or both finite
// numbers (integers included, booleans not).
func DecodeState(document []byte) (State, error) {
	if !json.Valid(document) {
		return nil, ErrUnreadable
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(document, &top) != nil || top == nil {
		return nil, ErrInvalid
	}
	others, err := othersOf(top)
	if err != nil {
		return nil, err
	}
	first := Watched()[0].Key()
	flag, err := flagOf(top)
	state := State{first: flag}
	for key, raw := range others {
		var fields map[string]json.RawMessage
		if err == nil && json.Unmarshal(raw, &fields) != nil {
			err = ErrInvalid
		}
		if err == nil {
			state[key], err = flagOf(fields)
		}
	}
	if err != nil {
		return nil, err
	}
	return state, nil
}

// othersOf takes "others" out of the top level: absent is none, and anything but an object that does not name the
// first watched job is invalid.
func othersOf(top map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	others := map[string]json.RawMessage{}
	raw, has := top[othersField]
	if !has {
		return others, nil
	}
	delete(top, othersField)
	if json.Unmarshal(raw, &others) != nil || others == nil {
		return nil, ErrInvalid
	}
	if _, named := others[Watched()[0].Key()]; named {
		return nil, ErrInvalid
	}
	return others, nil
}

// flagOf is a flag from its fields.
func flagOf(fields map[string]json.RawMessage) (Flag, error) {
	since, hasSince := fields[sinceField]
	last, hasLast := fields[lastField]
	if len(fields) != 2 || !hasSince || !hasLast {
		return Flag{}, ErrInvalid
	}
	sinceNull, lastNull := isNull(since), isNull(last)
	if sinceNull && lastNull {
		return Flag{}, nil
	}
	sinceValue, sinceOK := number(since)
	lastValue, lastOK := number(last)
	if sinceNull || lastNull || !sinceOK || !lastOK {
		return Flag{}, ErrInvalid
	}
	return Flag{Alerting: true, Since: sinceValue, LastAlert: lastValue}, nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// number is a JSON number's finite value; any other value, or one too large for a float, is not one.
func number(raw json.RawMessage) (float64, bool) {
	text := string(bytes.TrimSpace(raw))
	if text == "" || text[0] != '-' && (text[0] < '0' || text[0] > '9') {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	return value, err == nil && !math.IsInf(value, 0) && !math.IsNaN(value)
}

// Encode is the document as Python's json.dump wrote it, compact and ending in a newline: the first watched job's
// flag (an empty one when the state has none), then "others" when another job has a flag, its keys in sorted order.
// Times are written as Python writes a float, so 1791028800 is 1791028800.0.
func (s State) Encode() []byte {
	first := Watched()[0].Key()
	var out strings.Builder
	out.WriteString("{" + flagFields(s[first]))
	var others []string
	for key := range s {
		if key != first {
			others = append(others, key)
		}
	}
	slices.Sort(others)
	for i, key := range others {
		if i == 0 {
			out.WriteString(`,"` + othersField + `":{`)
		} else {
			out.WriteString(",")
		}
		out.WriteString(pythonString(key) + ":{" + flagFields(s[key]) + "}")
	}
	if len(others) > 0 {
		out.WriteString("}")
	}
	out.WriteString("}\n")
	return []byte(out.String())
}

// flagFields is a flag's two fields, without the braces around them.
func flagFields(f Flag) string {
	since, last := "null", "null"
	if f.Alerting {
		since, last = pythonFloat(f.Since), pythonFloat(f.LastAlert)
	}
	return `"` + sinceField + `":` + since + `,"` + lastField + `":` + last
}

// pythonFloat is how Python's repr spells a finite float: the shortest digits that read back, with ".0" on an
// integral value, and in exponent form below 1e-4 or from 1e16.
func pythonFloat(f float64) string {
	exponent := strconv.FormatFloat(f, 'e', -1, 64)
	_, power, _ := strings.Cut(exponent, "e")
	if p, _ := strconv.Atoi(power); p < -4 || p >= 16 {
		return exponent
	}
	fixed := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed
}

// pythonEscapes are the characters Python's JSON encoder writes with a short escape.
var pythonEscapes = map[rune]string{'"': `\"`, '\\': `\\`, '\n': `\n`, '\r': `\r`, '\t': `\t`, '\b': `\b`, '\f': `\f`}

// pythonString is a JSON string as Python's json.dump writes it with its default ensure_ascii: printable ASCII as
// itself and anything else as \u escapes, a character beyond the Basic Multilingual Plane as a surrogate pair.
func pythonString(s string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range s {
		escape, short := pythonEscapes[r]
		switch {
		case short:
			out.WriteString(escape)
		case r >= 0x20 && r < 0x7f:
			out.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&out, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	out.WriteByte('"')
	return out.String()
}
