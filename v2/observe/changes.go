package observe

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Comparison is caller-proved changed-path evidence, never an ancestry inference. Paths includes both
// names of a rename and deleted paths. Incomplete and unavailable answers cannot grant an exemption.
type Comparison struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Status string   `json:"status"` // complete, incomplete or unavailable
	Paths  []string `json:"paths"`
}

// Changes is a strict, versioned document of distinct endpoint comparisons.
type Changes struct {
	Kind        string       `json:"kind"`
	Version     int          `json:"version"`
	Comparisons []Comparison `json:"comparisons"`
}

// ChangeScope names repository-relative inputs and generated scopes. Folders include files no longer
// rendered. Shared inputs are conservative: a changed profile/resolution/allocation requires applying,
// even if a caller omitted the corresponding rendered change. No scope means relevance is unknown.
type ChangeScope struct{ Files, Folders []string }

// Currentness separates revision currency from health and preserves the literal observed revision.
type Currentness struct {
	State           string `json:"state"` // applied, unaffected, pending or unknown
	Reason          string `json:"reason"`
	AppliedRevision string `json:"appliedRevision"`
}

// DecodeChanges refuses malformed evidence, including unknown, missing, duplicate or null fields.
func DecodeChanges(data []byte) (Changes, error) {
	if !utf8.Valid(data) {
		return Changes{}, invalid("changes", "utf8_required")
	}
	if err := strictChangesObject(data, []string{"kind", "version", "comparisons"}); err != nil {
		return Changes{}, err
	}
	var changes Changes
	if err := json.Unmarshal(data, &changes); err != nil {
		return Changes{}, invalid("changes", "incorrect_type")
	}
	if changes.Kind != "v2-changes" || changes.Version != 1 {
		return Changes{}, invalid("changes", "unsupported_document")
	}
	var raw struct {
		Comparisons []json.RawMessage `json:"comparisons"`
	}
	_ = json.Unmarshal(data, &raw)
	for _, item := range raw.Comparisons {
		if err := strictChangesObject(item, []string{"from", "to", "status", "paths"}); err != nil {
			return Changes{}, err
		}
	}
	return changes, changes.Validate()
}

// strictChangesObject checks the concrete shape before ordinary typed decoding. Comparing the number
// of decoder tokens' keys also catches duplicate keys, which encoding/json otherwise accepts.
func strictChangesObject(data []byte, fields []string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return invalid("changes", "object_required")
	}
	seen := map[string]bool{}
	for decoder.More() {
		if err := changesField(decoder, fields, seen); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil || len(seen) != len(fields) {
		return invalid("changes", "missing_field")
	}
	if !json.Valid(data) {
		return invalid("changes", "single_json_required")
	}
	return nil
}

func changesField(decoder *json.Decoder, fields []string, seen map[string]bool) error {
	token, err := decoder.Token()
	if err != nil {
		return invalid("changes", "invalid_json")
	}
	key, ok := token.(string)
	if !ok || seen[key] || !containsField(fields, key) {
		return invalid("changes", "unknown_or_duplicate_field")
	}
	seen[key] = true
	var value json.RawMessage
	if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return invalid("changes", "invalid_or_null_field")
	}
	return nil
}

func containsField(fields []string, key string) bool {
	for _, field := range fields {
		if field == key {
			return true
		}
	}
	return false
}

// Validate also protects callers constructing evidence directly instead of decoding JSON.
func (c Changes) Validate() error {
	if c.Kind != "v2-changes" || c.Version != 1 || c.Comparisons == nil {
		return invalid("changes", "unsupported_document")
	}
	seen := map[string]bool{}
	for _, comparison := range c.Comparisons {
		if err := comparison.validate(); err != nil {
			return err
		}
		key := comparison.From + ":" + comparison.To
		if seen[key] {
			return invalid("changes", "duplicate_endpoints")
		}
		seen[key] = true
	}
	return nil
}

func (c Comparison) validate() error {
	if !revisionPattern().MatchString(c.From) || !revisionPattern().MatchString(c.To) {
		return invalid("changes", "exact_commits_required")
	}
	if c.Status != "complete" && c.Status != "incomplete" && c.Status != "unavailable" {
		return invalid("changes", "unsupported_status")
	}
	if c.Paths == nil {
		return invalid("changes", "paths_required")
	}
	for _, path := range c.Paths {
		if !repositoryPath(path) {
			return invalid("changes", "repository_path_required")
		}
	}
	return nil
}

func repositoryPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// CurrentnessOf applies only complete matching comparisons. Availability is the unit read's status;
// callers keep revision currency separate from reconciliation, suspension and workload health.
func CurrentnessOf(expected, literal, availability string, scope ChangeScope, changes *Changes) (Currentness, error) {
	if expected != "" && !revisionPattern().MatchString(expected) {
		return Currentness{}, invalid("revision", "expected_a_commit")
	}
	if availability != Present && availability != Absent && availability != Unavailable && availability != NotRead {
		return Currentness{}, invalid("unit", "unsupported_status")
	}
	if err := scope.validate(); err != nil {
		return Currentness{}, err
	}
	if changes != nil {
		if err := changes.Validate(); err != nil {
			return Currentness{}, err
		}
	}
	return currentnessOf(expected, literal, availability, scope, changes), nil
}

func currentnessOf(expected, literal, availability string, scope ChangeScope, changes *Changes) Currentness {
	result := Currentness{State: "unknown", Reason: "comparison_not_supplied", AppliedRevision: literal}
	if expected == "" {
		result.Reason = ReasonNotSupplied
		return result
	}
	if availability != Present {
		result.Reason = unitReason(availability)
		return result
	}
	if literal == "" {
		result.State, result.Reason = "pending", ReasonNotAppliedRev
		return result
	}
	commit, ok := appliedCommit(literal)
	if !ok || !revisionPattern().MatchString(commit) {
		result.Reason = ReasonRevisionShape
		return result
	}
	if commit == expected {
		result.State, result.Reason = Applied, "revision_matches"
		return result
	}
	return compareCurrentness(result, commit, expected, scope, changes)
}

func compareCurrentness(result Currentness, commit, expected string, scope ChangeScope, changes *Changes) Currentness {
	if changes == nil {
		return result
	}
	result.Reason = "comparison_endpoints_mismatch"
	for _, comparison := range changes.Comparisons {
		if comparison.From == commit && comparison.To == expected {
			return comparedCurrentness(result, scope, comparison)
		}
	}
	return result
}

func comparedCurrentness(result Currentness, scope ChangeScope, comparison Comparison) Currentness {
	if comparison.Status != "complete" {
		result.Reason = "comparison_" + comparison.Status
		return result
	}
	if len(scope.Files)+len(scope.Folders) == 0 || scope.validate() != nil {
		result.Reason = "relevance_unknown"
		return result
	}
	for _, path := range comparison.Paths {
		if scope.relevant(path) {
			result.State, result.Reason = "pending", "relevant_path_changed"
			return result
		}
	}
	result.State, result.Reason = "unaffected", "complete_comparison_unaffected"
	return result
}

func (s ChangeScope) relevant(path string) bool {
	for _, file := range s.Files {
		if file == path {
			return true
		}
	}
	for _, folder := range s.Folders {
		if path == folder || strings.HasPrefix(path, folder+"/") {
			return true
		}
	}
	return false
}

func changeScopeOf(target Target) ChangeScope {
	if target.Scope == nil {
		return ChangeScope{}
	}
	return *target.Scope
}

func (s ChangeScope) validate() error {
	for _, paths := range [][]string{s.Files, s.Folders} {
		for _, path := range paths {
			if !repositoryPath(path) {
				return invalid("scope", "repository_path_required")
			}
		}
	}
	return nil
}
