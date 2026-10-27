package observe

import "regexp"

// The states a restore plan can have. A plan is not an instruction: it says whether the archive this instance
// would be restored from is one it can verify, and what the operator must run.
const (
	RestoreReady       = "ready"
	RestoreStale       = "stale"
	RestoreIncomplete  = "incomplete"
	RestoreNoArchive   = "no_archive"
	RestoreUnavailable = "unavailable"
)

// The reasons a plan is not ready.
const (
	ReasonMetadataAbsent     = "archive_metadata_absent"
	ReasonMetadataMalformed  = "archive_metadata_malformed"
	ReasonMetadataDown       = "archive_metadata_unavailable"
	ReasonAnotherInstance    = "metadata_names_another_instance"
	ReasonNoChecksum         = "metadata_has_no_checksum"
	ReasonNoPlainChecksum    = "metadata_has_no_plain_checksum"
	ReasonNoTables           = "metadata_has_no_table_count"
	ReasonNotRestorableStale = "archive_older_than_the_threshold"
)

// ArchiveMetadata is what the backup job records beside an archive: the archive's own checksum, the
// plaintext's, its table count, and the instance it came from. A restore cannot verify without them, so an
// archive without them is incomplete rather than current.
type ArchiveMetadata struct {
	SHA256, PlainSHA256 string
	Tables              int32
	Source              string
}

// ArchiveMetadataOf reads the metadata document the backup job publishes next to its archive.
func ArchiveMetadataOf(document []byte) (ArchiveMetadata, error) {
	var record struct {
		SHA256      string `json:"sha256"`
		PlainSHA256 string `json:"plain_sha256"`
		Tables      int32  `json:"tables"`
		Source      string `json:"source"`
	}
	if err := decodeDocument(document, &record, "metadata"); err != nil {
		return ArchiveMetadata{}, err
	}
	for _, checksum := range []struct{ field, value string }{
		{"metadata.sha256", record.SHA256}, {"metadata.plain_sha256", record.PlainSHA256},
	} {
		if checksum.value != "" && !checksumPattern().MatchString(checksum.value) {
			return ArchiveMetadata{}, invalid(checksum.field, "expected_a_sha256_checksum")
		}
	}
	return ArchiveMetadata{SHA256: record.SHA256, PlainSHA256: record.PlainSHA256, Tables: record.Tables,
		Source: record.Source}, nil
}

// RestoreInput is what a restore plan is decided from: the archive chosen, what the bucket says about it, and
// the instance the plan is for.
type RestoreInput struct {
	// Namespace and Application name the instance the metadata must be about: the backup job records
	// "<namespace>/<application>", which is how a restore refuses another instance's archive.
	Namespace, Application string
	Archive                ArchiveReport
	Metadata               *ArchiveMetadata
	// Failure is why the metadata was not read: absent, malformed or unavailable. Empty when it was.
	Failure string
}

// RestorePlan is whether one instance can be restored, from which archive, and why not when it cannot.
type RestorePlan struct {
	State    string
	Reason   string
	Archive  ArchiveReport
	Metadata ArchiveMetadata
}

// RestoreOf decides the plan. A failed read stays unavailable, an archive is only ready when its metadata
// names this instance and records a checksum and a table count, and an archive older than the threshold is
// stale rather than silently restored.
func RestoreOf(input RestoreInput) RestorePlan {
	if state, reason := archiveReadiness(input.Archive); state != "" {
		return RestorePlan{State: state, Reason: reason, Archive: input.Archive}
	}
	plan := RestorePlan{State: RestoreReady, Archive: input.Archive}
	if input.Metadata != nil {
		plan.Metadata = *input.Metadata
	}
	if state, reason := metadataReadiness(input); state != "" {
		plan.State, plan.Reason = state, reason
	}
	return plan
}

// archiveReadiness is why the chosen archive cannot be restored, from the bucket's own answer: a failed read,
// a listing the caller never made, no archive at all, or one older than the threshold.
func archiveReadiness(archive ArchiveReport) (string, string) {
	switch archive.State {
	case BackupUnavailable:
		return RestoreUnavailable, archive.Reason
	case BackupNotAssessed:
		return RestoreUnavailable, NotRead
	case BackupStale:
		if archive.Reason == ReasonNoArchive || archive.Reason == ReasonArchiveMissing {
			return RestoreNoArchive, archive.Reason
		}
		return RestoreStale, ReasonNotRestorableStale
	}
	return "", ""
}

// metadataReadiness is why the archive cannot be verified: the record beside it was absent, unreadable or
// unreachable, or it does not name this instance with a checksum and a table count.
func metadataReadiness(input RestoreInput) (string, string) {
	switch input.Failure {
	case ReasonMetadataAbsent:
		return RestoreIncomplete, ReasonMetadataAbsent
	case ReasonMetadataDown:
		return RestoreUnavailable, ReasonMetadataDown
	case ReasonMetadataMalformed:
		return RestoreIncomplete, ReasonMetadataMalformed
	}
	if input.Metadata == nil {
		return RestoreIncomplete, ReasonMetadataAbsent
	}
	switch {
	case input.Metadata.Source != input.Namespace+"/"+input.Application:
		return RestoreIncomplete, ReasonAnotherInstance
	case input.Metadata.SHA256 == "":
		return RestoreIncomplete, ReasonNoChecksum
	case input.Metadata.PlainSHA256 == "":
		return RestoreIncomplete, ReasonNoPlainChecksum
	case input.Metadata.Tables < 1:
		return RestoreIncomplete, ReasonNoTables
	}
	return "", ""
}

// Ready is whether the plan may be performed: only a ready plan names an archive whose own checksums and
// table count a restore can verify, so a caller never has to re-read the state itself.
func (p RestorePlan) Ready() bool { return p.State == RestoreReady }

// checksumPattern is a SHA-256 checksum as the backup job records it.
func checksumPattern() *regexp.Regexp {
	return regexp.MustCompile(`^[0-9a-f]{64}$`)
}
