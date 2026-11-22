// Package rules is recovery's pure decisions: which archive one instance would be restored from, whether it is
// recent enough, what the backup job records beside it, and whether a restore can verify it. It owns the backup
// state vocabulary that health reports. Every fact is supplied, the clock included; nothing here reads a store.
package rules

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// An instance's protection has its own answer, beside the running verdict and never inside it: an instance
// can run while its backups are stale, and its job can be current for an instance that is not running.
const (
	BackupCurrent       = "current"
	BackupStale         = "stale"
	BackupFailing       = "failing"
	BackupSuspended     = "suspended"
	BackupAbsent        = "absent"
	BackupUnavailable   = "unavailable"
	BackupNotAssessed   = "not_assessed"
	BackupNotApplicable = "not_applicable"
)

// The backup reasons. A positive state carries none.
const (
	ReasonNoRuns       = "no_runs"
	ReasonNoSuccess    = "no_successful_run"
	ReasonTooOld       = "last_success_too_old"
	ReasonRunFailed    = "last_run_failed"
	ReasonRunsDown     = "runs_unavailable"
	ReasonNoOwner      = "no_owner_job"
	ReasonMaxAge       = "max_age_required"
	ReasonUnexpected   = "unexpected_backup"
	ReasonObservedTime = "observed_at"
)

// What a listing answered, in the same words a cluster read uses: read, failed with a reason, or never made.
const (
	Present     = "present"
	Unavailable = "unavailable"
	NotRead     = "not_read"
)

// ReasonMalformed is a supplied fact this package cannot judge: an object without a time.
const ReasonMalformed = "malformed"

// Error names a fixed safe field and category, never a rejected value or a document's content.
type Error struct{ Field, Reason string }

func (e *Error) Error() string           { return fmt.Sprintf("invalid: %s: %s", e.Field, e.Reason) }
func invalid(field, reason string) error { return &Error{field, reason} }

// reasonOf is a listing's own reason, or the one this package states when the listing gave none.
func reasonOf(reason string) string {
	if reason == "" {
		return ReasonRunsDown
	}
	return reason
}

// decodeDocument reads one JSON document, refusing anything that is not exactly one valid UTF-8 value.
func decodeDocument(data []byte, into any, field string) error {
	if !utf8.Valid(data) || !json.Valid(data) {
		return invalid(field, "single_utf8_json_required")
	}
	if err := json.Unmarshal(data, into); err != nil {
		return invalid(field, "incorrect_type_or_range")
	}
	return nil
}
