package rules_test

import (
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/recovery/rules"
)

// prefix is the instance's own archive directory: the platform's root and the namespace.
const prefix = "app-sqlite/hello-staging/"

// observedAt is the moment the tests judge archives at; forbidden is a listing's own reason, as the store states it.
const (
	observedAt = "2026-10-26T09:14:00Z"
	forbidden  = "forbidden"
)

// query is what a listing of the instance's prefix names.
func query() rules.ArchiveQuery {
	return rules.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: prefix}
}

// archive is one listed object, with its time stated as the bucket states it.
func archive(t *testing.T, key, modified string) rules.Archive {
	t.Helper()
	at, err := time.Parse(time.RFC3339, modified)
	if err != nil {
		t.Fatal(err)
	}
	return rules.Archive{Key: prefix + key, LastModified: at}
}

// current is the archive report a plan is decided from: one recent archive under the instance's prefix.
func current(t *testing.T) rules.ArchiveReport {
	t.Helper()
	return rules.ArchiveReport{State: rules.BackupCurrent, Bucket: "backups", Prefix: prefix,
		Newest: prefix + "sqlite-20261026T082103Z.db.age", LastModified: "2026-10-26T08:21:10Z",
		Age: 13 * time.Hour, MaxAge: 26 * time.Hour}
}

// metadata is what the backup job records beside the archive.
func metadata(t *testing.T) *rules.ArchiveMetadata {
	t.Helper()
	return &rules.ArchiveMetadata{SHA256: strings.Repeat("a", 64), PlainSHA256: strings.Repeat("b", 64),
		Tables: 2, Source: "hello-staging/hello"}
}

// A current archive with metadata that names this instance, a checksum and a table count is ready.
func TestRestoreIsReadyWhenTheArchiveVerifies(t *testing.T) {
	plan := rules.RestoreOf(rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
		Metadata: metadata(t)})
	if plan.State != rules.RestoreReady || plan.Reason != "" {
		t.Fatalf("plan %+v", plan)
	}
	if plan.Metadata.Tables != 2 || plan.Archive.Newest == "" {
		t.Fatalf("plan facts %+v", plan)
	}
}

// Every reason a plan is not ready is its own state, and a failed read is never an incomplete upload.
func TestRestoreStates(t *testing.T) {
	refused := current(t)
	refused.State, refused.Reason = rules.BackupUnavailable, forbidden
	for _, tt := range []struct {
		name  string
		input rules.RestoreInput
		state string
		want  string
	}{
		{"unreachable", rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: refused},
			rules.RestoreUnavailable, forbidden},
		{"not read", rules.RestoreInput{Namespace: "hello-staging", Application: "hello",
			Archive: rules.ArchiveReport{State: rules.BackupNotAssessed}}, rules.RestoreUnavailable, rules.NotRead},
		{"no archive", rules.RestoreInput{Namespace: "hello-staging", Application: "hello",
			Archive: rules.ArchiveReport{State: rules.BackupStale, Reason: rules.ReasonNoArchive}},
			rules.RestoreNoArchive, rules.ReasonNoArchive},
		{"chosen archive gone", rules.RestoreInput{Namespace: "hello-staging", Application: "hello",
			Archive: rules.ArchiveReport{State: rules.BackupStale, Reason: rules.ReasonArchiveMissing}},
			rules.RestoreNoArchive, rules.ReasonArchiveMissing},
		{"too old", rules.RestoreInput{Namespace: "hello-staging", Application: "hello",
			Archive: rules.ArchiveReport{State: rules.BackupStale, Reason: rules.ReasonArchiveOld}},
			rules.RestoreStale, rules.ReasonNotRestorableStale},
		{"no metadata", rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Failure: rules.ReasonMetadataAbsent}, rules.RestoreIncomplete, rules.ReasonMetadataAbsent},
		{"metadata unreadable", rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Failure: rules.ReasonMetadataMalformed}, rules.RestoreIncomplete, rules.ReasonMetadataMalformed},
		{"metadata unreachable", rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Failure: rules.ReasonMetadataDown}, rules.RestoreUnavailable, rules.ReasonMetadataDown},
		{"another instance", rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Metadata: &rules.ArchiveMetadata{SHA256: strings.Repeat("a", 64), PlainSHA256: strings.Repeat("b", 64),
				Tables: 2, Source: "other-staging/other"}},
			rules.RestoreIncomplete, rules.ReasonAnotherInstance},
		{"no checksum", rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Metadata: &rules.ArchiveMetadata{Tables: 2, Source: "hello-staging/hello"}},
			rules.RestoreIncomplete, rules.ReasonNoChecksum},
		{"no plaintext checksum", rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Metadata: &rules.ArchiveMetadata{SHA256: strings.Repeat("a", 64), Tables: 2,
				Source: "hello-staging/hello"}}, rules.RestoreIncomplete, rules.ReasonNoPlainChecksum},
		{"no tables", rules.RestoreInput{Namespace: "hello-staging", Application: "hello", Archive: current(t),
			Metadata: &rules.ArchiveMetadata{SHA256: strings.Repeat("a", 64), PlainSHA256: strings.Repeat("b", 64),
				Source: "hello-staging/hello"}},
			rules.RestoreIncomplete, rules.ReasonNoTables},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan := rules.RestoreOf(tt.input)
			if plan.State != tt.state || plan.Reason != tt.want {
				t.Fatalf("plan %+v, want %s/%s", plan, tt.state, tt.want)
			}
		})
	}
}

// A named archive is read as named, even when it is not the newest, and one the bucket does not hold is no
// archive rather than the newest silently.
func TestSelectArchiveChoosesWhatWasAsked(t *testing.T) {
	listed := rules.ArchiveList{Query: query(), Status: rules.Present, Objects: []rules.Archive{
		archive(t, "sqlite-20261025T035800Z.db.age", "2026-10-25T03:58:40Z"),
		archive(t, "sqlite-20261026T071500Z.db.age", "2026-10-26T07:15:09Z"),
	}}
	now, _ := time.Parse(time.RFC3339, observedAt)
	newest, err := rules.SelectArchive(listed, prefix, "", 26*time.Hour, now)
	if err != nil || newest.Newest != prefix+"sqlite-20261026T071500Z.db.age" {
		t.Fatalf("newest: %+v %v", newest, err)
	}
	chosen, err := rules.SelectArchive(listed, prefix, prefix+"sqlite-20261025T035800Z.db.age", 26*time.Hour, now)
	if err != nil || chosen.Newest != prefix+"sqlite-20261025T035800Z.db.age" {
		t.Fatalf("chosen: %+v %v", chosen, err)
	}
	// The 25th's archive is 29h old at the observation: stale, and named so.
	if chosen.State != rules.BackupStale || chosen.Reason != rules.ReasonArchiveOld {
		t.Fatalf("chosen is stale: %+v", chosen)
	}
	missing, err := rules.SelectArchive(listed, prefix, prefix+"sqlite-20261020T000000Z.db.age", 26*time.Hour, now)
	if err != nil || missing.State != rules.BackupStale || missing.Reason != rules.ReasonArchiveMissing {
		t.Fatalf("missing: %+v %v", missing, err)
	}
	down := rules.ArchiveList{Query: query(), Status: rules.Unavailable, Reason: forbidden}
	if report, err := rules.SelectArchive(down, prefix, "", 26*time.Hour, now); err != nil ||
		report.State != rules.BackupUnavailable {
		t.Fatalf("unavailable: %+v %v", report, err)
	}
}

// The metadata is the backup job's document: a checksum, a table count and the instance it came from. A
// checksum of the wrong shape is refused rather than recorded.
func TestArchiveMetadataOf(t *testing.T) {
	metadata, err := rules.ArchiveMetadataOf([]byte(`{"sha256":"` + strings.Repeat("b", 64) +
		`","plain_sha256":"` + strings.Repeat("c", 64) + `","tables":2,"source":"hello-staging"}`))
	if err != nil || metadata.Tables != 2 || metadata.Source != "hello-staging" ||
		metadata.PlainSHA256 != strings.Repeat("c", 64) {
		t.Fatalf("metadata %+v %v", metadata, err)
	}
	for _, document := range []string{`{"sha256":"abc","tables":2}`, `{"plain_sha256":"abc","tables":2}`} {
		if _, err := rules.ArchiveMetadataOf([]byte(document)); err == nil {
			t.Fatalf("a short checksum was recorded: %s", document)
		}
	}
	if _, err := rules.ArchiveMetadataOf([]byte("not json")); err == nil {
		t.Fatal("a document that is not JSON was accepted")
	}
}

// The listing is confined to the instance's own directory and every object has a time: anything else would
// read another instance's archives, or judge an object this package could not date.
func TestInstanceArchiveRefusesWhatItCannotConfine(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, observedAt)
	wide := rules.ArchiveList{Query: rules.ArchiveQuery{Bucket: query().Bucket, Prefix: "app-sqlite/"},
		Status: rules.Present}
	if _, err := rules.InstanceArchive(wide, "hello-staging", 26*time.Hour, now); err == nil ||
		err.Error() != "invalid: archive.prefix: not_confined_to_the_instance" {
		t.Fatalf("a bucket-wide prefix: %v", err)
	}
	undated := rules.ArchiveList{Query: query(), Status: rules.Present,
		Objects: []rules.Archive{{Key: prefix + "sqlite-1.db.age"}}}
	if _, err := rules.InstanceArchive(undated, "hello-staging", 26*time.Hour, now); err == nil ||
		err.Error() != "invalid: archive: malformed" {
		t.Fatalf("an object without a time: %v", err)
	}
}

// A backup location is an s3://<bucket>/<prefix>/ URI: a bucket S3 accepts, and a directory, never one object.
func TestBackupLocation(t *testing.T) {
	for uri, ok := range map[string]bool{"s3://bucket/p/": true, "s3://bucket/": true, "s3://bucket/p": false, "bucket/p/": false, "s3:///p/": false} {
		if _, found := rules.BackupLocation(uri); found != ok {
			t.Errorf("%s: %v", uri, found)
		}
	}
}
