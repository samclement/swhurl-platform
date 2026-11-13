// Package secretcheck decides what make check-secrets reports about the SOPS Secrets a checkout tracks, from their
// decrypted documents (docs/iteration-2-plan.md#27-check-secrets-in-go-approved-13-november-2026). It is pure: the
// caller decrypts and parses, and nothing here reads a file, a key or Git.
//
// Values are only ever measured — empty, the placeholder, probably encoded twice — or fingerprinted with SHA-256 to
// compare copies. No finding carries a value or a fingerprint: a line names a file, a key and a count.
package secretcheck

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Level is how a finding counts: an error fails the check, a warning is reported, OK is neither.
type Level string

const (
	OK    Level = "OK"
	Error Level = "ERROR"
	Warn  Level = "WARN"
)

// Line is one finding. Value is true for a line about one value of a document, which is shown under it.
type Line struct {
	Level Level
	Value bool
	Text  string
}

// Failure is why a tracked file has no documents to check; None when it was decrypted and parsed.
type Failure int

const (
	None Failure = iota
	Undecryptable
	Unparsable
)

// File is one tracked Secret file: its path from the checkout's top, and either its decrypted documents, in order,
// or why there are none. A nil document is an empty one, which holds nothing to check.
type File struct {
	Path      string
	Failure   Failure
	Documents []map[string]any
}

// Report is every finding in order and how many errors and warnings they hold.
type Report struct {
	Lines            []Line
	Errors, Warnings int
}

// Placeholder is the value a Secret holds until its real one is set; only the fixtures may keep it.
const Placeholder = "REPLACE_ME"

// IngestionKey is the ClickStack ingestion key, kept in exactly ingestionFiles with one value.
const IngestionKey = "CLICKSTACK_INGESTION_KEY"

var ingestionFiles = []string{"platform/clickstack/secret.sops.yaml", "platform/otel/secret.sops.yaml"}

// place is where a shared value is kept: a file and the key in it.
type place struct{ File, Key string }

// sharedValue is a value kept in two Secrets, which must be set and identical in both.
type sharedValue struct {
	What   string
	Copies [2]place
}

// sharedValues are the values two Secrets keep. The Receiver's token has a copy in the console, which adds it to new
// app repositories; Flux's github-dispatch Provider uses the console's GitHub token (docs/operations.md#secrets).
var sharedValues = []sharedValue{
	{"the image webhook token", [2]place{{"platform/flux-webhook/image-secret.sops.yaml", "token"},
		{"platform/console/secret.sops.yaml", "IMAGE_WEBHOOK_TOKEN"}}},
	{"the GitHub dispatch token", [2]place{{"platform/image-automation/dispatch-secret.sops.yaml", "token"},
		{"platform/console/secret.sops.yaml", "GITHUB_TOKEN"}}},
}

// ntfyCopies is an ntfy channel's address, kept by its Flux provider and copied for the notification checker; the
// two must name the same topic once the provider's query and fragment are dropped.
type ntfyCopies struct {
	Channel         string
	Source, Checker place
}

// notificationSecret is the checker's Secret, which holds its copy of every destination.
const notificationSecret = "platform/console/notification-secret.sops.yaml"

var destinations = []ntfyCopies{
	{"platform", place{"platform/alerts/secret-platform.sops.yaml", "address"}, place{notificationSecret, "NTFY_PLATFORM_URL"}},
}

// Tracked is the Secret files among a checkout's tracked paths, in their order: every *.sops.yaml or *.sops.yml at
// any depth, but not the SOPS configuration itself.
func Tracked(paths []string) []string {
	var secrets []string
	for _, path := range paths {
		name := path[strings.LastIndex(path, "/")+1:]
		if name == ".sops.yaml" || name == ".sops.yml" {
			continue
		}
		if strings.HasSuffix(name, ".sops.yaml") || strings.HasSuffix(name, ".sops.yml") {
			secrets = append(secrets, path)
		}
	}
	return secrets
}

// fingerprints is what a run has seen of the values it compares, by SHA-256: each copy's file for the ingestion key,
// each shared value's copies, and each destination's two copies.
type fingerprints struct {
	ingestion    map[string]string
	shared       map[string]map[string]string
	destinations map[string]string
}

// Check judges every file, then the copies across them.
func Check(files []File) Report {
	var report Report
	seen := fingerprints{ingestion: map[string]string{}, shared: map[string]map[string]string{}, destinations: map[string]string{}}
	for _, shared := range sharedValues {
		seen.shared[shared.What] = map[string]string{}
	}
	for _, file := range files {
		report.file(file, &seen)
	}
	report.compare(ingestionProblem(seen.ingestion), IngestionKey+" is identical in "+strings.Join(ingestionFiles, " and "))
	report.compare(destinationProblem(seen.destinations), "ntfy destinations match the notification checker copies (compared by hashes)")
	for _, shared := range sharedValues {
		report.compare(sharedProblem(shared, seen.shared[shared.What]),
			shared.What+" is identical in "+shared.Copies[0].File+" and "+shared.Copies[1].File)
	}
	return report
}

func (r *Report) add(level Level, value bool, format string, args ...any) {
	r.Lines = append(r.Lines, Line{Level: level, Value: value, Text: fmt.Sprintf(format, args...)})
	switch level {
	case Error:
		r.Errors++
	case Warn:
		r.Warnings++
	}
}

func (r *Report) compare(problem, okay string) {
	if problem != "" {
		r.add(Error, false, "%s", problem)
		return
	}
	r.add(OK, false, "%s", okay)
}

// file judges one file: why it could not be read, or each of its documents.
func (r *Report) file(file File, seen *fingerprints) {
	var documents []map[string]any
	for _, document := range file.Documents {
		if document != nil {
			documents = append(documents, document)
		}
	}
	switch {
	case file.Failure == Undecryptable:
		r.add(Error, false, "%s: cannot decrypt", file.Path)
	case file.Failure == Unparsable:
		r.add(Error, false, "%s: cannot read the decrypted document", file.Path)
	case len(documents) == 0:
		r.add(Error, false, "%s: no document", file.Path)
	}
	if file.Failure != None {
		return
	}
	fixture := strings.HasPrefix(file.Path, "tests/fixtures/")
	for _, document := range documents {
		r.document(file.Path, document, fixture, seen)
	}
}

// secretFields are the two fields a Secret keeps values in, in the order they are reported.
var secretFields = []string{"data", "stringData"}

// document judges one Secret: its summary line, then each value in field and key order. A document with no metadata
// map, or a field that is not a map, is not a Secret.
func (r *Report) document(path string, document map[string]any, fixture bool, seen *fingerprints) {
	metadata, isMap := document["metadata"].(map[string]any)
	fields := map[string]map[string]any{}
	for _, field := range secretFields {
		values, isValues := document[field].(map[string]any)
		if document[field] != nil && !isValues {
			isMap = false
		}
		fields[field] = values
	}
	if !isMap {
		r.add(Error, false, "%s: a document is not a Secret", path)
		return
	}
	var summary []string
	for _, field := range secretFields {
		if len(fields[field]) > 0 {
			summary = append(summary, fmt.Sprintf("%d %s", len(fields[field]), field))
		}
	}
	r.add(OK, false, "%s: %s/%s (%s)", path, named(metadata["namespace"]), named(metadata["name"]), strings.Join(summary, ", "))
	for _, field := range secretFields {
		keys := make([]string, 0, len(fields[field]))
		for key := range fields[field] {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			r.value(path, field, key, fields[field][key], fixture, seen)
		}
	}
}

// named is a metadata field as the summary shows it; "-" when it is not set.
func named(value any) string {
	if text, isText := value.(string); isText && text != "" {
		return text
	}
	if value == nil {
		return "-"
	}
	return fmt.Sprint(value)
}

// value judges one value and fingerprints the copies it is one of.
func (r *Report) value(path, field, key string, value any, fixture bool, seen *fingerprints) {
	where := path + ": " + field + "." + key
	raw, readable := valueBytes(field, value)
	if !readable {
		r.add(Error, true, "%s: not base64", where)
		return
	}
	seen.record(path, key, raw, fixture)
	switch {
	case len(raw) == 0:
		r.add(Error, true, "%s: empty", where)
	case string(raw) == Placeholder && !fixture:
		r.add(Error, true, "%s: still the %s placeholder; set it with sops", where, Placeholder)
	case field == "data" && LooksDoubleEncoded(raw):
		r.add(Warn, true, "%s: decodes to base64 text; probably encoded twice", where)
	}
}

// record fingerprints a value when it is one of the copies the platform keeps equal. A fixture's ingestion key is not
// one of the platform's.
func (seen *fingerprints) record(path, key string, raw []byte, fixture bool) {
	here := place{path, key}
	if key == IngestionKey && !fixture {
		seen.ingestion[path] = digest(raw)
	}
	for _, shared := range sharedValues {
		if slices.Contains(shared.Copies[:], here) {
			seen.shared[shared.What][path] = digest(raw)
		}
	}
	for _, channel := range destinations {
		switch here {
		case channel.Source:
			seen.destinations["source-"+channel.Channel] = digest([]byte(withoutQuery(string(raw))))
		case channel.Checker:
			seen.destinations["checker-"+channel.Channel] = digest([]byte(withoutQuery(string(raw))))
		}
	}
}

// valueBytes is a value's bytes: a data value decoded from strict base64, a stringData value as its text. A null is
// empty; a data value that is not base64 text is unreadable.
func valueBytes(field string, value any) ([]byte, bool) {
	if value == nil {
		return nil, true
	}
	text, isText := value.(string)
	if field != "data" {
		if !isText {
			text = fmt.Sprint(value)
		}
		return []byte(text), true
	}
	if !isText || strings.ContainsAny(text, "\r\n") {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(text)
	return raw, err == nil
}

// LooksDoubleEncoded is whether decoded data bytes are themselves base64 of printable text: the sign a value was
// encoded twice. Short values are not judged, since short words are often valid base64 by chance.
func LooksDoubleEncoded(raw []byte) bool {
	if len(raw) < 16 || strings.ContainsAny(string(raw), "\r\n") {
		return false
	}
	inner, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil || len(inner) == 0 {
		return false
	}
	for _, c := range inner {
		if c < 32 || c >= 127 {
			return false
		}
	}
	return true
}

// withoutQuery is an address without its query and fragment: the provider's copy carries the rendering options, the
// checker's does not. Text that is not a URL is compared as it is.
func withoutQuery(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		return address
	}
	parsed.RawQuery, parsed.Fragment, parsed.ForceQuery, parsed.RawFragment = "", "", false, ""
	return parsed.String()
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ingestionProblem is what is wrong with the ingestion key's copies, by file; "" when there is nothing.
func ingestionProblem(copies map[string]string) string {
	files := sortedKeys(copies)
	if !slices.Equal(files, ingestionFiles) {
		found := "none"
		if len(files) > 0 {
			found = strings.Join(files, ", ")
		}
		return fmt.Sprintf("%s must be in exactly %s; found in %s", IngestionKey, strings.Join(ingestionFiles, " and "), found)
	}
	if copies[files[0]] != copies[files[1]] {
		return fmt.Sprintf("%s differs between %s", IngestionKey, strings.Join(ingestionFiles, " and "))
	}
	return ""
}

// sharedProblem is what is wrong with one shared value's copies, by file; "" when there is nothing.
func sharedProblem(shared sharedValue, copies map[string]string) string {
	files := []string{shared.Copies[0].File, shared.Copies[1].File}
	want := slices.Clone(files)
	slices.Sort(want)
	if !slices.Equal(sortedKeys(copies), want) || copies[files[0]] != copies[files[1]] {
		return fmt.Sprintf("%s must be set and identical in %s", shared.What, strings.Join(files, " and "))
	}
	return ""
}

// destinationProblem is what is wrong with the destinations' copies; "" when there is nothing.
func destinationProblem(copies map[string]string) string {
	for _, destination := range destinations {
		source, checker := copies["source-"+destination.Channel], copies["checker-"+destination.Channel]
		if source == "" || checker == "" {
			return fmt.Sprintf("ntfy %s destination is missing from its source or checker Secret", destination.Channel)
		}
		if source != checker {
			return fmt.Sprintf("ntfy %s destination differs between platform/alerts and %s", destination.Channel, notificationSecret)
		}
	}
	return ""
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
