package rules

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

// archiveSuffix is what a SQLite archive is called: the job publishes `sqlite-<UTC>.db.age`, then its metadata
// `sqlite-<UTC>.json` beside it.
const archiveSuffix = ".db.age"

// MetadataKey is where the backup job publishes a SQLite archive's metadata: beside it, `sqlite-<UTC>.json`.
func MetadataKey(archive string) string { return metadataKey(archive, archiveSuffix) }

// metadataKey is the metadata beside an archive whose name ends with the suffix.
func metadataKey(archive, suffix string) string { return strings.TrimSuffix(archive, suffix) + ".json" }

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

// InstanceArchive is one instance's newest archive, judged for an instance report. It is a fact beside the
// backup job's: a job can report success and leave no archive, and an archive can be current while the job
// that wrote it has since failed — the two disagreeing is what the check exists to expose. The listing must be
// confined to the instance's namespace; anything else is refused rather than judged.
func InstanceArchive(listed ArchiveList, namespace string, maxAge time.Duration, now time.Time) (ArchiveReport, error) {
	if err := validateArchiveQuery(listed.Query, namespace); err != nil {
		return ArchiveReport{}, err
	}
	return NewestArchive(listed, archiveSuffix, maxAge, now)
}

// NewestArchive is the newest complete archive under the listing's prefix whose name ends with the suffix, judged
// against the oldest one allowed. It is the same verdict as InstanceArchive without confining the prefix to a
// namespace: for a platform backup, such as MongoDB's `.archive.gz.age`, whose prefix its own job states.
func NewestArchive(listed ArchiveList, suffix string, maxAge time.Duration, now time.Time) (ArchiveReport, error) {
	if suffix == "" {
		return ArchiveReport{}, invalid("archive.suffix", "expected_a_suffix")
	}
	return selectArchive(listed, listed.Query.Prefix, suffix, "", maxAge, now)
}

// SelectArchive is one archive out of a listing: the newest complete one, or the one the operator names. An
// archive is complete once its metadata is listed beside it, so a half-uploaded newest gives way to the one
// before it. A named archive is chosen without that test, so a restore can say its metadata is absent; one the
// bucket does not hold is `chosen_archive_absent`, not the newest by default: a restore is a point-in-time
// choice.
func SelectArchive(listed ArchiveList, prefix, key string, maxAge time.Duration, now time.Time) (ArchiveReport, error) {
	return selectArchive(listed, prefix, archiveSuffix, key, maxAge, now)
}

// selectArchive is SelectArchive for archives whose names end with the suffix.
func selectArchive(listed ArchiveList, prefix, suffix, key string, maxAge time.Duration, now time.Time) (ArchiveReport, error) {
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
	return judgeArchive(report, listed.Objects, suffix, key, now)
}

// judgeArchive states which archive was chosen, how old it is, and whether that is recent enough. A named key
// that is not in the listing is no archive at all.
func judgeArchive(report ArchiveReport, objects []Archive, suffix, key string, now time.Time) (ArchiveReport, error) {
	newest, found, err := newestArchive(objects, report.Prefix, suffix, key)
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

// BackupLocation is an s3://<bucket>/<prefix>/ URI as the bucket and prefix it names; false for any other text.
func BackupLocation(uri string) (ArchiveQuery, bool) {
	rest, found := strings.CutPrefix(uri, "s3://")
	bucket, prefix, _ := strings.Cut(rest, "/")
	if !found || !bucketPattern().MatchString(bucket) || !strings.HasSuffix(uri, "/") {
		return ArchiveQuery{}, false
	}
	return ArchiveQuery{Bucket: bucket, Prefix: prefix}, true
}

// InstanceArchiveQuery is where one instance's archives live: its namespace's directory under the platform's
// backup root, `s3://<bucket>/<prefix>/`. A root that is not such a URI, or a namespace that would not confine
// the prefix to the instance, is refused rather than turned into a broader listing.
func InstanceArchiveQuery(root, namespace string) (ArchiveQuery, error) {
	location, ok := BackupLocation(root)
	if !ok {
		return ArchiveQuery{}, invalid("backup.s3Uri", "expected_an_s3_uri_ending_with_a_slash")
	}
	query := ArchiveQuery{Bucket: location.Bucket, Prefix: location.Prefix + namespace + "/"}
	if err := validateArchiveQuery(query, namespace); err != nil {
		return ArchiveQuery{}, err
	}
	return query, nil
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

// newestArchive is the newest complete archive among the listed objects, aged by the bucket's own time: an
// archive with the suffix whose metadata is listed beside it, or the named key whatever is beside it. An
// archive whose time was not read is a refusal: this layer judges the objects it was given, and it would make
// that judgement wrong.
func newestArchive(objects []Archive, prefix, suffix, key string) (Archive, bool, error) {
	listed := map[string]bool{}
	for _, object := range objects {
		listed[object.Key] = true
	}
	newest, found := Archive{}, false
	for _, object := range objects {
		if !candidate(object.Key, prefix, suffix, key) {
			continue
		}
		if object.LastModified.IsZero() {
			return Archive{}, false, invalid("archive", ReasonMalformed)
		}
		if key == "" && !listed[metadataKey(object.Key, suffix)] {
			continue
		}
		if found && !object.LastModified.After(newest.LastModified) {
			continue
		}
		newest, found = object, true
	}
	return newest, found, nil
}

// candidate is whether a listed key is an archive the choice weighs: under the prefix, with the suffix, and the
// named key when one is named.
func candidate(listed, prefix, suffix, key string) bool {
	return strings.HasPrefix(listed, prefix) && strings.HasSuffix(listed, suffix) && (key == "" || listed == key)
}

// bucketPattern is a DNS-compatible bucket name, which is what S3 accepts.
func bucketPattern() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
}
