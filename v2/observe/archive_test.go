package observe_test

import (
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// prefix is the instance's own archive directory: the platform's root and the namespace.
const prefix = "app-sqlite/hello-staging/"

// query is what a listing of the instance's prefix names.
func query() observe.ArchiveQuery {
	return observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: prefix}
}

// archive is one listed object, with its time stated as the bucket states it.
func archive(t *testing.T, key, modified string) observe.Archive {
	t.Helper()
	at, err := time.Parse(time.RFC3339, modified)
	if err != nil {
		t.Fatal(err)
	}
	return observe.Archive{Key: prefix + key, LastModified: at}
}

// listing is a successful listing under the instance's prefix.
func listing(t *testing.T, objects ...observe.Archive) observe.ArchiveList {
	t.Helper()
	return observe.ArchiveList{Query: query(), Status: observe.Present, Objects: objects}
}

// protected is the healthy evidence with the protection's job facts and one archive listing.
func protected(t *testing.T, listed observe.ArchiveList, maxAge time.Duration) observe.Evidence {
	t.Helper()
	evidence := protection(t, maxAge, cronJob(t, false, "2026-10-26T03:35:00Z", "2026-10-26T03:36:12Z"),
		backupPod(t, "hello-backup-1-abc", "2026-10-26T03:35:00Z", "hello-backup-1"), succeededRun(t))
	evidence.Backup.Archive = &listed
	return evidence
}

// An archive inside the threshold is current, and the report names it and its age.
func TestArchiveIsCurrentWhenTheNewestIsRecent(t *testing.T) {
	report := explainBackup(t, backed(target()), protected(t, listing(t,
		archive(t, "sqlite-20261026T071500Z.db.age", "2026-10-26T07:15:09Z"),
		archive(t, "sqlite-20261026T071500Z.db.age.json", "2026-10-26T07:15:10Z"),
	), 26*time.Hour))
	got := report.Backup.Archive
	if got.State != observe.BackupCurrent || got.Reason != "" {
		t.Fatalf("archive %+v", got)
	}
	if got.Newest != prefix+"sqlite-20261026T071500Z.db.age" || got.LastModified != "2026-10-26T07:15:09Z" {
		t.Fatalf("newest %q at %q", got.Newest, got.LastModified)
	}
	if got.Age != 1*time.Hour+58*time.Minute+51*time.Second {
		t.Fatalf("age %s", got.Age)
	}
	if got.Bucket != query().Bucket || got.Prefix != prefix {
		t.Fatalf("what was read: %+v", got)
	}
}

// The archive is judged by the object's own time, and its metadata file is not an archive: only the newest
// `sqlite-*.db.age` counts, whatever order the bucket lists them in.
func TestArchiveUsesTheNewestDbFile(t *testing.T) {
	report := explainBackup(t, backed(target()), protected(t, listing(t,
		archive(t, "sqlite-20261026T071500Z.db.age.json", "2026-10-26T09:00:00Z"),
		archive(t, "sqlite-20261025T035800Z.db.age", "2026-10-25T03:58:40Z"),
		archive(t, "sqlite-20261026T071500Z.db.age", "2026-10-26T07:15:09Z"),
	), 26*time.Hour))
	if report.Backup.Archive.Newest != prefix+"sqlite-20261026T071500Z.db.age" ||
		report.Backup.Archive.State != observe.BackupCurrent {
		t.Fatalf("archive %+v", report.Backup.Archive)
	}
}

// The threshold is the same one the job is judged by, and the boundary counts as current.
func TestArchiveAgeIsJudgedAgainstTheThreshold(t *testing.T) {
	objects := []observe.Archive{archive(t, "sqlite-20261026T071500Z.db.age", "2026-10-26T07:15:09Z")}
	// The observation is at 09:14:00Z, so the archive is 1h58m51s old.
	if report := explainBackup(t, backed(target()), protected(t, listing(t, objects...), 2*time.Hour)); report.Backup.Archive.State != observe.BackupCurrent {
		t.Fatalf("two hours: %+v", report.Backup.Archive)
	}
	if report := explainBackup(t, backed(target()), protected(t, listing(t, objects...), time.Hour)); report.Backup.Archive.State != observe.BackupStale ||
		report.Backup.Archive.Reason != observe.ReasonArchiveOld {
		t.Fatalf("one hour: %+v", report.Backup.Archive)
	}
	at, _ := time.Parse(time.RFC3339, "2026-10-26T07:15:09Z")
	observed, _ := time.Parse(time.RFC3339, observedAt)
	if report := explainBackup(t, backed(target()), protected(t, listing(t, objects...), observed.Sub(at))); report.Backup.Archive.State != observe.BackupCurrent {
		t.Fatalf("exactly the threshold: %+v", report.Backup.Archive)
	}
	if report := explainBackup(t, backed(target()), protected(t, listing(t, objects...), observed.Sub(at)-time.Second)); report.Backup.Archive.State != observe.BackupStale {
		t.Fatalf("a second over: %+v", report.Backup.Archive)
	}
}

// An empty prefix, or one holding only metadata, is no archive at all: not current, and not a failed read.
func TestArchiveAbsent(t *testing.T) {
	report := explainBackup(t, backed(target()), protected(t, listing(t), 26*time.Hour))
	if report.Backup.Archive.State != observe.BackupStale || report.Backup.Archive.Reason != observe.ReasonNoArchive {
		t.Fatalf("empty prefix: %+v", report.Backup.Archive)
	}
	report = explainBackup(t, backed(target()), protected(t, listing(t,
		archive(t, "sqlite-20261026T071500Z.db.age.json", "2026-10-26T07:15:10Z")), 26*time.Hour))
	if report.Backup.Archive.State != observe.BackupStale || report.Backup.Archive.Reason != observe.ReasonNoArchive {
		t.Fatalf("metadata only: %+v", report.Backup.Archive)
	}
}

// A failed listing is unavailable, and a listing the caller never made is not read: neither is an empty
// bucket.
func TestArchiveSeparatesUnavailableFromNotRead(t *testing.T) {
	refused := observe.ArchiveList{Query: query(), Status: observe.Unavailable, Reason: observe.ReasonForbidden}
	report := explainBackup(t, backed(target()), protected(t, refused, 26*time.Hour))
	if report.Backup.Archive.State != observe.BackupUnavailable || report.Backup.Archive.Reason != observe.ReasonForbidden {
		t.Fatalf("refused: %+v", report.Backup.Archive)
	}
	evidence := protection(t, 26*time.Hour, cronJob(t, false, "", ""), nil, nil)
	report = explainBackup(t, backed(target()), evidence)
	if report.Backup.Archive.State != observe.BackupNotAssessed || report.Backup.Archive.Reason != observe.NotRead {
		t.Fatalf("not read: %+v", report.Backup.Archive)
	}
	if report := explainBackup(t, target(), healthy(t)); report.Backup.Archive.State != observe.BackupNotApplicable {
		t.Fatalf("an instance without a backup: %+v", report.Backup.Archive)
	}
}

// The two answers disagree on purpose: a failed run does not erase a current archive, and a current run does
// not invent one.
func TestArchiveAndJobAnswerSeparately(t *testing.T) {
	evidence := protection(t, 26*time.Hour, cronJob(t, false, "2026-10-26T03:35:00Z", "2026-10-26T03:36:12Z"),
		backupPod(t, "hello-backup-2-def", "2026-10-26T09:00:00Z", "hello-backup-2"),
		job(t, "hello-backup-2", "2026-10-26T09:00:00Z", "2026-10-26T09:04:00Z", observe.RunFailed))
	listed := listing(t, archive(t, "sqlite-20261026T071500Z.db.age", "2026-10-26T07:15:09Z"))
	evidence.Backup.Archive = &listed
	report := explainBackup(t, backed(target()), evidence)
	if report.Backup.State != observe.BackupFailing || report.Backup.Archive.State != observe.BackupCurrent {
		t.Fatalf("job %+v, archive %+v", report.Backup, report.Backup.Archive)
	}
	// A last success with no archive behind it is still no archive.
	empty := protection(t, 26*time.Hour, cronJob(t, false, "2026-10-26T03:35:00Z", "2026-10-26T03:36:12Z"),
		backupPod(t, "hello-backup-1-abc", "2026-10-26T03:35:00Z", "hello-backup-1"), succeededRun(t))
	none := listing(t)
	empty.Backup.Archive = &none
	report = explainBackup(t, backed(target()), empty)
	if report.Backup.State != observe.BackupCurrent || report.Backup.Archive.State != observe.BackupStale {
		t.Fatalf("a current job with no archive: job %+v, archive %+v", report.Backup, report.Backup.Archive)
	}
}

// The listing is confined to the instance's own directory and every object has a time: anything else would
// read another instance's archives, or judge an object this layer could not date.
func TestArchiveRefusesWhatItCannotConfine(t *testing.T) {
	wide := observe.ArchiveList{Query: observe.ArchiveQuery{Bucket: query().Bucket, Prefix: "app-sqlite/"},
		Status: observe.Present}
	if _, err := observe.Explain(backed(target()), protected(t, wide, 26*time.Hour)); err == nil ||
		err.Error() != "invalid: archive.prefix: not_confined_to_the_instance" {
		t.Fatalf("a bucket-wide prefix: %v", err)
	}
	undated := observe.ArchiveList{Query: query(), Status: observe.Present,
		Objects: []observe.Archive{{Key: prefix + "sqlite-1.db.age"}}}
	if _, err := observe.Explain(backed(target()), protected(t, undated, 26*time.Hour)); err == nil ||
		err.Error() != "invalid: archive: malformed" {
		t.Fatalf("an object without a time: %v", err)
	}
}
