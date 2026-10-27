package observe_test

import (
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// current is the archive report a plan is decided from: one recent archive under the instance's prefix.
func current(t *testing.T) observe.ArchiveReport {
	t.Helper()
	return observe.ArchiveReport{State: observe.BackupCurrent, Bucket: "backups", Prefix: prefix,
		Newest: prefix + "sqlite-20261026T082103Z.db.age", LastModified: "2026-10-26T08:21:10Z",
		Age: 13 * time.Hour, MaxAge: 26 * time.Hour}
}

// metadata is what the backup job records beside the archive.
func metadata(t *testing.T) *observe.ArchiveMetadata {
	t.Helper()
	return &observe.ArchiveMetadata{SHA256: strings.Repeat("a", 64), PlainSHA256: strings.Repeat("b", 64),
		Tables: 2, Source: "hello-staging/hello"}
}

// A current archive with metadata that names this instance, a checksum and a table count is ready.
func TestRestoreIsReadyWhenTheArchiveVerifies(t *testing.T) {
	plan := observe.RestoreOf(observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
		Metadata: metadata(t)})
	if plan.State != observe.RestoreReady || plan.Reason != "" {
		t.Fatalf("plan %+v", plan)
	}
	if plan.Metadata.Tables != 2 || plan.Archive.Newest == "" {
		t.Fatalf("plan facts %+v", plan)
	}
}

// Every reason a plan is not ready is its own state, and a failed read is never an incomplete upload.
func TestRestoreStates(t *testing.T) {
	refused := current(t)
	refused.State, refused.Reason = observe.BackupUnavailable, observe.ReasonForbidden
	for _, tt := range []struct {
		name  string
		input observe.RestoreInput
		state string
		want  string
	}{
		{"unreachable", observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: refused},
			observe.RestoreUnavailable, observe.ReasonForbidden},
		{"not read", observe.RestoreInput{Namespace: "hello-staging", Application: "hello",
			Archive: observe.ArchiveReport{State: observe.BackupNotAssessed}}, observe.RestoreUnavailable, observe.NotRead},
		{"no archive", observe.RestoreInput{Namespace: "hello-staging", Application: "hello",
			Archive: observe.ArchiveReport{State: observe.BackupStale, Reason: observe.ReasonNoArchive}},
			observe.RestoreNoArchive, observe.ReasonNoArchive},
		{"chosen archive gone", observe.RestoreInput{Namespace: "hello-staging", Application: "hello",
			Archive: observe.ArchiveReport{State: observe.BackupStale, Reason: observe.ReasonArchiveMissing}},
			observe.RestoreNoArchive, observe.ReasonArchiveMissing},
		{"too old", observe.RestoreInput{Namespace: "hello-staging", Application: "hello",
			Archive: observe.ArchiveReport{State: observe.BackupStale, Reason: observe.ReasonArchiveOld}},
			observe.RestoreStale, observe.ReasonNotRestorableStale},
		{"no metadata", observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Failure: observe.ReasonMetadataAbsent}, observe.RestoreIncomplete, observe.ReasonMetadataAbsent},
		{"metadata unreadable", observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Failure: observe.ReasonMetadataMalformed}, observe.RestoreIncomplete, observe.ReasonMetadataMalformed},
		{"metadata unreachable", observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Failure: observe.ReasonMetadataDown}, observe.RestoreUnavailable, observe.ReasonMetadataDown},
		{"another instance", observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Metadata: &observe.ArchiveMetadata{SHA256: strings.Repeat("a", 64), PlainSHA256: strings.Repeat("b", 64),
				Tables: 2, Source: "other-staging/other"}},
			observe.RestoreIncomplete, observe.ReasonAnotherInstance},
		{"no checksum", observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Metadata: &observe.ArchiveMetadata{Tables: 2, Source: "hello-staging/hello"}},
			observe.RestoreIncomplete, observe.ReasonNoChecksum},
		{"no plaintext checksum", observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Metadata: &observe.ArchiveMetadata{SHA256: strings.Repeat("a", 64), Tables: 2,
				Source: "hello-staging/hello"}}, observe.RestoreIncomplete, observe.ReasonNoPlainChecksum},
		{"no tables", observe.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Metadata: &observe.ArchiveMetadata{SHA256: strings.Repeat("a", 64), PlainSHA256: strings.Repeat("b", 64),
				Source: "hello-staging/hello"}},
			observe.RestoreIncomplete, observe.ReasonNoTables},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan := observe.RestoreOf(tt.input)
			if plan.State != tt.state || plan.Reason != tt.want {
				t.Fatalf("plan %+v, want %s/%s", plan, tt.state, tt.want)
			}
		})
	}
}

// A named archive is read as named, even when it is not the newest, and one the bucket does not hold is no
// archive rather than the newest silently.
func TestSelectArchiveChoosesWhatWasAsked(t *testing.T) {
	listed := observe.ArchiveList{Query: query(), Status: observe.Present, Objects: []observe.Archive{
		archive(t, "sqlite-20261025T035800Z.db.age", "2026-10-25T03:58:40Z"),
		archive(t, "sqlite-20261026T071500Z.db.age", "2026-10-26T07:15:09Z"),
	}}
	now, _ := time.Parse(time.RFC3339, observedAt)
	newest, err := observe.SelectArchive(listed, prefix, "", 26*time.Hour, now)
	if err != nil || newest.Newest != prefix+"sqlite-20261026T071500Z.db.age" {
		t.Fatalf("newest: %+v %v", newest, err)
	}
	chosen, err := observe.SelectArchive(listed, prefix, prefix+"sqlite-20261025T035800Z.db.age", 26*time.Hour, now)
	if err != nil || chosen.Newest != prefix+"sqlite-20261025T035800Z.db.age" {
		t.Fatalf("chosen: %+v %v", chosen, err)
	}
	// The 25th's archive is 29h old at the observation: stale, and named so.
	if chosen.State != observe.BackupStale || chosen.Reason != observe.ReasonArchiveOld {
		t.Fatalf("chosen is stale: %+v", chosen)
	}
	missing, err := observe.SelectArchive(listed, prefix, prefix+"sqlite-20261020T000000Z.db.age", 26*time.Hour, now)
	if err != nil || missing.State != observe.BackupStale || missing.Reason != observe.ReasonArchiveMissing {
		t.Fatalf("missing: %+v %v", missing, err)
	}
	down := observe.ArchiveList{Query: query(), Status: observe.Unavailable, Reason: observe.ReasonForbidden}
	if report, err := observe.SelectArchive(down, prefix, "", 26*time.Hour, now); err != nil ||
		report.State != observe.BackupUnavailable {
		t.Fatalf("unavailable: %+v %v", report, err)
	}
}

// The metadata is the backup job's document: a checksum, a table count and the instance it came from. A
// checksum of the wrong shape is refused rather than recorded.
func TestArchiveMetadataOf(t *testing.T) {
	metadata, err := observe.ArchiveMetadataOf([]byte(`{"sha256":"` + strings.Repeat("b", 64) +
		`","plain_sha256":"` + strings.Repeat("c", 64) + `","tables":2,"source":"hello-staging"}`))
	if err != nil || metadata.Tables != 2 || metadata.Source != "hello-staging" ||
		metadata.PlainSHA256 != strings.Repeat("c", 64) {
		t.Fatalf("metadata %+v %v", metadata, err)
	}
	for _, document := range []string{`{"sha256":"abc","tables":2}`, `{"plain_sha256":"abc","tables":2}`} {
		if _, err := observe.ArchiveMetadataOf([]byte(document)); err == nil {
			t.Fatalf("a short checksum was recorded: %s", document)
		}
	}
	if _, err := observe.ArchiveMetadataOf([]byte("not json")); err == nil {
		t.Fatal("a document that is not JSON was accepted")
	}
}
