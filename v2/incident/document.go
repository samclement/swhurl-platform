// Package incident decides notification-checker incidents and lifecycle notices from supplied facts and time.
// Its state is the version-1 JSON document shared with the Python checker. Unknown fields are retained at every
// depth; evaluation works on an owned copy. Nothing here reads a clock, sends a message or saves state.
package incident

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	Grace         = 300
	ConsoleGrace  = 600
	Recovery      = 120
	Reminder      = 3600
	History       = 7200
	MaxStateBytes = 700_000
	Namespace     = "console"
	StateName     = "notification-state"
	StateKey      = "state.json"
	FieldManager  = "notification-check"
)

// Object is a JSON object. Kubernetes documents and extensible persisted records keep their original fields.
type Object = map[string]any

// State is an owned, validated version-1 incident document. Use DecodeState to resume one, Evaluate to advance it.
type State struct {
	document Object
	held     []string
}

// Entry is a persisted notice, including its delivery identity and optional app/environment copy destination.
type Entry struct{ document Object }

// Snapshot contains complete collections, not partial reads. Workloads contain Deployments and StatefulSets only.
type Snapshot struct {
	Units     []Object `json:"units"`
	Releases  []Object `json:"releases"`
	Workloads []Object `json:"workloads"`
	Pods      []Object `json:"pods"`
	Events    []Object `json:"events"`
}

// DecodeState refuses invalid history rather than resetting it. An empty document is absence, represented by nil.
func DecodeState(raw string) (*State, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > MaxStateBytes {
		return nil, errors.New("notification state exceeds its bounded size")
	}
	var doc Object
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&doc) != nil || !json.Valid([]byte(raw)) || !validState(doc) {
		return nil, errors.New("notification state is invalid; refusing to reset incident or delivery history")
	}
	return &State{document: doc}, nil
}

func validState(doc Object) bool {
	if number(doc, "version") != 1 || !numeric(doc, "started") || !numeric(doc, "last_success") {
		return false
	}
	for _, key := range []string{"instances", "seen", "monitor"} {
		if _, ok := doc[key].(map[string]any); !ok {
			return false
		}
	}
	return validPending(doc) && validInstances(doc) && validSeen(object(doc, "seen")) && validIncident(object(doc, "monitor")) && validNotifyService(doc)
}
func validPending(doc Object) bool {
	pending, ok := doc["pending"].([]any)
	if !ok {
		return false
	}
	for _, item := range pending {
		entry, ok := item.(map[string]any)
		if !ok || !validEntry(entry) {
			return false
		}
	}
	return true
}
func validInstances(doc Object) bool {
	for _, item := range object(doc, "instances") {
		entry, ok := item.(map[string]any)
		if !ok {
			return false
		}
		record, ok := entry["incident"].(map[string]any)
		if !ok || !validIncident(record) || !validInstance(entry) {
			return false
		}
	}
	return true
}
func validSeen(doc Object) bool {
	for key := range doc {
		if !numeric(doc, key) {
			return false
		}
	}
	return true
}

// Incident and instance fields are optional across supported v1 states, but a present known field must never
// fall through a zero-value accessor: that could reset a clock, clear an incident or discard delivery history.
func validIncident(doc Object) bool {
	return optionalNumbers(doc, "since", "last_notice", "healthy_since") && optionalStrings(doc, "fault", "run") && optionalBools(doc, "notified")
}
func validInstance(doc Object) bool {
	return optionalStrings(doc, "title", "unit", "namespace", "name", "uid", "uninstalled") &&
		optionalNumbers(doc, "generation", "last_active") && optionalBools(doc, "console", "retained") &&
		optionalStringArray(doc, "failed_actions") && validAbout(doc)
}
func validNotifyService(doc Object) bool {
	return optionalRecord(doc, "notify_service") && validIncident(object(doc, "notify_service"))
}
func validEntry(doc Object) bool {
	if text(doc, "key") == "" || !numeric(doc, "priority") || !numeric(doc, "queued") {
		return false
	}
	return requiredNoticeText(doc) && optionalStrings(doc, "click") && optionalNumbers(doc, "history") &&
		optionalStringArray(doc, "tags") && validAbout(doc)
}
func requiredNoticeText(doc Object) bool {
	for _, key := range []string{"key", "title", "message"} {
		if _, ok := doc[key].(string); !ok {
			return false
		}
	}
	return true
}
func optionalNumbers(doc Object, keys ...string) bool {
	for _, key := range keys {
		if has(doc, key) && !numeric(doc, key) {
			return false
		}
	}
	return true
}
func optionalStrings(doc Object, keys ...string) bool {
	for _, key := range keys {
		if has(doc, key) {
			if _, ok := doc[key].(string); !ok {
				return false
			}
		}
	}
	return true
}
func optionalBools(doc Object, keys ...string) bool {
	for _, key := range keys {
		if has(doc, key) {
			if _, ok := doc[key].(bool); !ok {
				return false
			}
		}
	}
	return true
}
func optionalStringArray(doc Object, key string) bool {
	if !has(doc, key) {
		return true
	}
	values, ok := doc[key].([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}
func validAbout(doc Object) bool {
	if !has(doc, "about") {
		return true
	}
	values, ok := doc["about"].([]any)
	if !ok || len(values) != 2 {
		return false
	}
	for _, value := range values {
		if s, ok := value.(string); !ok || s == "" {
			return false
		}
	}
	return true
}

func optionalRecord(doc Object, key string) bool {
	value, exists := doc[key]
	if !exists {
		return true
	}
	_, ok := value.(map[string]any)
	return ok
}

// Encode is compact, ASCII JSON, bounded by the same encoded-byte limit as Python, with unknown fields intact.
func (s *State) Encode() (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(s.document)
	raw := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if err != nil {
		return "", errors.New("notification state cannot be encoded")
	}
	var out strings.Builder
	for _, r := range string(raw) {
		escapeRune(&out, r)
	}
	if out.Len() > MaxStateBytes {
		return "", errors.New("notification state exceeds its bounded size; history retained")
	}
	return out.String(), nil
}

func escapeRune(out *strings.Builder, r rune) {
	switch {
	case r < 127:
		out.WriteRune(r)
	case r <= 0xffff:
		fmt.Fprintf(out, `\u%04x`, r)
	default:
		r -= 0x10000
		fmt.Fprintf(out, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
	}
}

func fresh(now float64) *State {
	return &State{document: Object{"version": float64(1), "started": clockValue(now), "last_success": float64(0), "instances": Object{}, "seen": Object{}, "pending": []any{}, "monitor": Object{}}}
}

func clone(previous *State, now float64) *State {
	if previous == nil {
		return fresh(now)
	}
	raw, _ := json.Marshal(previous.document)
	var doc Object
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&doc)
	return &State{document: doc}
}

// Pending is the owned notices waiting, in order. Changing the returned slice cannot reorder the outbox.
func (s *State) Pending() []Entry {
	found := []Entry{}
	for _, item := range array(s.document, "pending") {
		found = append(found, Entry{item.(map[string]any)})
	}
	return found
}

// Instances is the number of active or retained instance identities.
func (s *State) Instances() int { return len(object(s.document, "instances")) }

// Notice fields and delivery identity of an entry. About is empty for the platform topic.
func (e Entry) Title() string   { return text(e.document, "title") }
func (e Entry) Message() string { return text(e.document, "message") }
func (e Entry) Click() string   { return text(e.document, "click") }
func (e Entry) Key() string     { return text(e.document, "key") }
func (e Entry) Priority() int {
	if number(e.document, "priority") >= 4 {
		return 4
	}
	return 3
}
func (e Entry) About() []string {
	found := []string{}
	for _, v := range array(e.document, "about") {
		if s, ok := v.(string); ok {
			found = append(found, s)
		}
	}
	return found
}

func object(doc Object, path ...string) Object {
	for _, key := range path {
		doc, _ = doc[key].(map[string]any)
	}
	if doc == nil {
		return Object{}
	}
	return doc
}
func text(doc Object, key string) string { s, _ := doc[key].(string); return s }
func number(doc Object, key string) float64 {
	switch n := doc[key].(type) {
	case float64:
		return n
	case json.Number:
		v, _ := n.Float64()
		return v
	}
	return 0
}
func numeric(doc Object, key string) bool {
	switch n := doc[key].(type) {
	case float64:
		return !math.IsNaN(n) && !math.IsInf(n, 0)
	case json.Number:
		_, err := n.Float64()
		return err == nil
	}
	return false
}
func flag(doc Object, key string) bool   { b, _ := doc[key].(bool); return b }
func array(doc Object, key string) []any { a, _ := doc[key].([]any); return a }
func has(doc Object, key string) bool    { _, ok := doc[key]; return ok }
func fallback(value, other string) string {
	if value != "" {
		return value
	}
	return other
}
func epochKey(n float64) string {
	s := strconv.FormatFloat(n, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
func timestamp(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, errors.New("invalid lifecycle event timestamp")
	}
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9, nil
}
func eventTime(e Object) (float64, error) {
	return timestamp(fallback(text(e, "lastTimestamp"), fallback(text(e, "eventTime"), text(object(e, "metadata"), "creationTimestamp"))))
}
func identity(o Object) string {
	m := object(o, "metadata")
	return text(m, "namespace") + "/" + text(m, "name")
}

// Held reports deployment Events awaiting workload readiness, without adding diagnostics to the state document.
func (s *State) Held() []string { return append([]string{}, s.held...) }

func clockValue(now float64) json.Number { return json.Number(epochKey(now)) }
