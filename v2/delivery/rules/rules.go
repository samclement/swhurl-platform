// Package rules is delivery's pure decisions: which image metadata a release accepts, which release a promotion
// carries, whether it may go ahead, whether a requested revision has landed, and whether a deployed release is healthy.
// It owns the vocabulary those decisions share. Every fact is supplied;
// nothing here reads a store, the clock included.
package rules

import (
	"fmt"
	"regexp"
	"strings"
)

// Code is a stable refusal category; messages name fields, never rejected values.
type Code string

const (
	Invalid          Code = "invalid"
	Unsupported      Code = "unsupported"
	WrongApplication Code = "wrong_application"
	WrongRelease     Code = "wrong_release"
)

// Error names a fixed safe field and category, never a rejected value or a document's content.
type Error struct {
	Code   Code
	Field  string
	Reason string
}

func (e *Error) Error() string                     { return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Reason) }
func refuse(code Code, field, reason string) error { return &Error{code, field, reason} }
func invalid(field, reason string) error           { return refuse(Invalid, field, reason) }

// What a read answered, in the same words a cluster read uses: read, the object absent, failed with a reason,
// or never made.
const (
	Present     = "present"
	Absent      = "absent"
	Unavailable = "unavailable"
	NotRead     = "not_read"
)

// The reasons a revision's evidence is missing or cannot be judged.
const (
	ReasonNotSupplied   = "revision_not_supplied"
	ReasonNotAppliedRev = "revision_not_applied"
	ReasonRevisionShape = "unrecognised_revision"
	ReasonUnitGone      = "unit_absent"
	ReasonUnitDown      = "unit_unavailable"
)

func revisionPattern() *regexp.Regexp { return regexp.MustCompile(`^[0-9a-f]{40}$`) }

func digestPattern() *regexp.Regexp { return regexp.MustCompile(`^sha256:[0-9a-f]{64}$`) }

// appliedCommit is the commit a Flux source revision names: `<ref>@<algo>:<commit>` yields the text
// after its last colon, and anything without a digest part is not interpreted.
func appliedCommit(revision string) (string, bool) {
	at := strings.LastIndex(revision, "@")
	if at < 0 {
		return "", false
	}
	colon := strings.LastIndex(revision[at:], ":")
	if colon < 0 {
		return "", false
	}
	commit := revision[at+colon+1:]
	return commit, commit != ""
}

// unitReason explains why the unit's own facts could not be read: the object is absent, the read
// failed, or it was never made.
func unitReason(status string) string {
	switch status {
	case Absent:
		return ReasonUnitGone
	case Unavailable:
		return ReasonUnitDown
	}
	return NotRead
}

// repositoryPath is a relative path inside the repository: no leading slash, empty, `.` or `..` part, or
// control character.
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

func containsField(fields []string, key string) bool {
	for _, field := range fields {
		if field == key {
			return true
		}
	}
	return false
}
