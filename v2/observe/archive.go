package observe

import (
	"regexp"
	"strings"
	"time"
)

// The object store's own answer, in the same words the job's answer uses.
const (
	ReasonNoArchive      = "no_archive"
	ReasonArchiveMissing = "chosen_archive_absent"
	ReasonArchiveOld     = "last_archive_too_old"
	ReasonConfined       = "not_confined_to_the_instance"
)

// archiveSuffix is what a complete SQLite archive is called: the job publishes `sqlite-<UTC>.db.age`.
const archiveSuffix = ".db.age"

// ArchiveQuery is the bucket prefix one instance's archives live under. The prefix is the instance's own
// directory, never the bucket's root: the caller states it, and this layer refuses one that is not.
type ArchiveQuery struct{ Bucket, Prefix string }

// Archive is one object the bucket lists: its key and when S3 last wrote it.
type Archive struct {
	Key          string
	LastModified time.Time
}

// ArchiveList is what a listing answered: the objects under the prefix, or why it could not be read. A
// listing the caller never made is not read, which is neither an empty bucket nor a failed read.
type ArchiveList struct {
	Query   ArchiveQuery
	Status  string
	Reason  string
	Objects []Archive
}

// ArchiveReport is the object store's answer about one instance's newest archive.
type ArchiveReport struct {
	State        string
	Reason       string
	Bucket       string
	Prefix       string
	Newest       string
	LastModified string
	Age          time.Duration
	MaxAge       time.Duration
}

// archiveOf decides whether an archive exists and how old the newest one is. It is a fact beside the job's:
// a job can report success and leave no archive, and an archive can be current while the job that wrote it
// has since failed — the two disagreeing is what the check exists to expose.
func archiveOf(target Target, evidence Evidence, now time.Time) (ArchiveReport, error) {
	if target.Backup == nil {
		return ArchiveReport{State: BackupNotApplicable}, nil
	}
	if evidence.Backup == nil || evidence.Backup.Archive == nil {
		return ArchiveReport{State: BackupNotAssessed, Reason: NotRead}, nil
	}
	listed := *evidence.Backup.Archive
	report := ArchiveReport{Bucket: listed.Query.Bucket, Prefix: listed.Query.Prefix, MaxAge: evidence.Backup.MaxAge}
	if err := validateArchiveQuery(listed.Query, target.Namespace); err != nil {
		return ArchiveReport{}, err
	}
	switch listed.Status {
	case Present:
	case Unavailable:
		report.State, report.Reason = BackupUnavailable, reasonOf(listed.Reason)
		return report, nil
	default:
		report.State, report.Reason = BackupNotAssessed, NotRead
		return report, nil
	}
	return judgeArchive(report, listed.Objects, "", now)
}

// SelectArchive is one archive out of a listing: the newest, or the one the operator names. A named archive
// the bucket does not hold is `no_archive`, not the newest by default: a restore is a point-in-time choice.
func SelectArchive(listed ArchiveList, prefix, key string, maxAge time.Duration, now time.Time) (ArchiveReport, error) {
	report := ArchiveReport{Bucket: listed.Query.Bucket, Prefix: prefix, MaxAge: maxAge}
	switch listed.Status {
	case Present:
	case Unavailable:
		report.State, report.Reason = BackupUnavailable, reasonOf(listed.Reason)
		return report, nil
	default:
		report.State, report.Reason = BackupNotAssessed, NotRead
		return report, nil
	}
	return judgeArchive(report, listed.Objects, key, now)
}

// judgeArchive states which archive was chosen, how old it is, and whether that is recent enough. A named key
// that is not in the listing is no archive at all.
func judgeArchive(report ArchiveReport, objects []Archive, key string, now time.Time) (ArchiveReport, error) {
	newest, found, err := newestArchive(objects, report.Prefix, key)
	if err != nil {
		return ArchiveReport{}, err
	}
	if !found {
		report.State, report.Reason = BackupStale, ReasonNoArchive
		if key != "" {
			report.Reason = ReasonArchiveMissing
		}
		return report, nil
	}
	report.Newest = newest.Key
	report.LastModified = newest.LastModified.UTC().Format(time.RFC3339)
	report.Age = now.Sub(newest.LastModified)
	if report.Age > report.MaxAge {
		report.State, report.Reason = BackupStale, ReasonArchiveOld
		return report, nil
	}
	report.State = BackupCurrent
	return report, nil
}

// validateArchiveQuery keeps a listing inside the instance's own directory: a bucket, and a prefix that ends
// with the instance's namespace. Anything else would read another instance's archives or the whole bucket.
func validateArchiveQuery(query ArchiveQuery, namespace string) error {
	if !bucketPattern().MatchString(query.Bucket) {
		return invalid("archive.bucket", "expected_a_bucket_name")
	}
	if !strings.HasSuffix(query.Prefix, "/"+namespace+"/") || strings.HasSuffix(query.Prefix, "//"+namespace+"/") {
		return invalid("archive.prefix", ReasonConfined)
	}
	return nil
}

// newestArchive is the newest complete archive among the listed objects. A key under another prefix, or an
// object whose time was not read, is a refusal: this layer judges the objects it was given, and both would
// make that judgement wrong.
func newestArchive(objects []Archive, prefix, key string) (Archive, bool, error) {
	newest, found := Archive{}, false
	for _, object := range objects {
		if !strings.HasPrefix(object.Key, prefix) || !strings.HasSuffix(object.Key, archiveSuffix) {
			continue
		}
		if key != "" && object.Key != key {
			continue
		}
		if object.LastModified.IsZero() {
			return Archive{}, false, invalid("archive", ReasonMalformed)
		}
		if found && !object.LastModified.After(newest.LastModified) {
			continue
		}
		newest, found = object, true
	}
	return newest, found, nil
}

// bucketPattern is a DNS-compatible bucket name, which is what S3 accepts.
func bucketPattern() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
}
