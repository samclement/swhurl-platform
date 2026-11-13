// Package certissuer decides one edit of the platform's Git-tracked settings ConfigMap: which certificate issuer
// CERT_ISSUER names. The file is edited line by line so its comments and layout survive, and the edit is accepted
// only when the data decoded again differs from the data before in that one value. It reads and writes nothing:
// its callers hand it decoded documents and the file's text.
package certissuer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Path is the settings ConfigMap; IssuersDir is the folder whose ClusterIssuers it may name; Key is the value edited.
const (
	Path       = "clusters/home/flux-system/sources/configmap-platform-settings.yaml"
	IssuersDir = "infra/issuers"
	Key        = "CERT_ISSUER"
)

// Reminder is the last line of every run that does not refuse: the edit is Git's until it is pushed.
const Reminder = "[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile"

// Refusal is why the edit is not made; nothing is written.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refuse(format string, args ...any) error { return &Refusal{fmt.Sprintf(format, args...)} }

// Issuers are the publicly trusted ClusterIssuers a set of decoded documents defines: those named letsencrypt-*,
// sorted and without repeats. A self-signed issuer is in Git but is not one a platform host may use.
func Issuers(documents []map[string]any) []string {
	var names []string
	for _, document := range documents {
		metadata, _ := document["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		if document["kind"] == "ClusterIssuer" && strings.HasPrefix(name, "letsencrypt-") && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// Data is the settings file's data: the file must be one document, a ConfigMap with a data mapping.
func Data(documents []map[string]any) (map[string]any, error) {
	if len(documents) == 1 && documents[0]["kind"] == "ConfigMap" {
		if data, ok := documents[0]["data"].(map[string]any); ok {
			return data, nil
		}
	}
	return nil, refuse("expected a ConfigMap with a data section")
}

// Allowed refuses an issuer Git does not define as one a platform host may use.
func Allowed(issuers []string, issuer string) error {
	if !slices.Contains(issuers, issuer) {
		return refuse("unknown issuer '%s'; expected one of %s", issuer, strings.Join(issuers, ", "))
	}
	return nil
}

// Current is CERT_ISSUER's value in data, refused when the key is absent; a value that is not a string is no issuer.
func Current(data map[string]any) (string, error) {
	value, ok := data[Key]
	if !ok {
		return "", refuse("Missing key '%s' in %s", Key, Path)
	}
	current, _ := value.(string)
	return current, nil
}

// line is the one indented `CERT_ISSUER:` line a settings file may hold.
var line = regexp.MustCompile(`(?m)^([ \t]+)` + regexp.QuoteMeta(Key) + `:[^\n]*$`)

// SetLine is text with the CERT_ISSUER line replaced by `<indent>CERT_ISSUER: <issuer>`; any trailing comment on the
// line goes with it. Text with no such line, or more than one, is refused.
func SetLine(text []byte, issuer string) ([]byte, error) {
	if len(line.FindAll(text, -1)) != 1 {
		return nil, refuse("expected exactly one '%s:' line", Key)
	}
	return line.ReplaceAll(text, []byte("${1}"+Key+": "+issuer)), nil
}

// Confirm accepts the edit only when after is before with CERT_ISSUER set to issuer and nothing else changed.
func Confirm(before, after map[string]any, issuer string) error {
	want := map[string]any{}
	for key, value := range before {
		want[key] = value
	}
	want[Key] = issuer
	expected, err := json.Marshal(want)
	if err != nil {
		return refuse("editing %s would change other settings; refusing to write", Key)
	}
	actual, err := json.Marshal(after)
	if err != nil || string(actual) != string(expected) {
		return refuse("editing %s would change other settings; refusing to write", Key)
	}
	return nil
}

// Outcome is what a run that does not refuse did.
type Outcome int

const (
	AlreadySet Outcome = iota
	WouldUpdate
	Updated
)

// Messages are the operator's lines for an outcome, ending with the reminder.
func Messages(outcome Outcome, issuer string) []string {
	first := map[Outcome]string{
		AlreadySet:  fmt.Sprintf("[INFO] %s already set to %s", Key, issuer),
		WouldUpdate: fmt.Sprintf("[INFO] %s would update to %s in %s", Key, issuer, Path),
		Updated:     fmt.Sprintf("[INFO] %s updated to %s in %s", Key, issuer, Path),
	}[outcome]
	return []string{first, Reminder}
}
