package observe

// The checks of what the platform keeps: the notification routes and channels, the backups in the object store, and
// the console's image and job record.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ChannelEvidence is what the channels check reads: the routes Git renders from the team registry, the routes and
// channel Secrets the notify service reads, and which team owns each app.
type ChannelEvidence struct {
	// Wanted is the routes document the registry renders — each channel with its transport, each team's channels —
	// and Problem says why there is none.
	Wanted  []byte
	Problem string
	// Live is the routes document the cluster holds, nil when it holds none; Unread says why it or the Secrets
	// could not be read.
	Live   []byte
	Unread string
	// Secrets are the channels the notify service finds a Secret with an address for, by its label.
	Secrets []string
	// Owners are each team's apps, from the app records.
	Owners map[string][]string
}

func platformChannels(e ChannelEvidence) []PlatformEntry {
	var wanted struct {
		Channels map[string]string   `json:"channels"`
		Teams    map[string][]string `json:"teams"`
	}
	if e.Problem != "" || json.Unmarshal(e.Wanted, &wanted) != nil {
		return []PlatformEntry{bad(fmt.Sprintf("%s renders no notification routes (%s), so the routes and channels are "+
			"unchecked", CheckoutTeams, e.Problem))}
	}
	if e.Unread != "" {
		return []PlatformEntry{bad("could not read the notification routes and channels (make reconcile UNIT=platform-notify)")}
	}
	entries := []PlatformEntry{}
	if e.Live == nil || !sameJSON(e.Live, e.Wanted) {
		entries = append(entries, bad("the team routes in the cluster are not those of "+checkoutRoutes+": a team or "+
			"channel change rewrites them, so commit, push and make flux-reconcile"))
	} else {
		entries = append(entries, ok(fmt.Sprintf("%d team(s) notify a channel, as in Git", len(wanted.Teams))))
	}
	entries = append(entries, channelSecrets(wanted.Channels, e.Secrets)...)
	return append(entries, unroutedTeams(e.Owners, wanted.Teams)...)
}

// channelSecrets is whether every channel Git names has the Secret the notify service reads its address from.
func channelSecrets(channels map[string]string, secrets []string) []PlatformEntry {
	held := map[string]bool{}
	for _, name := range secrets {
		held[name] = true
	}
	missing := []string{}
	for name := range channels {
		if !held[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	switch {
	case len(missing) > 0:
		return []PlatformEntry{bad("channel " + strings.Join(missing, ", ") + " has no Secret in the cluster: its teams " +
			"get no notifications (make reconcile UNIT=platform-notify)")}
	case len(channels) > 0:
		return []PlatformEntry{ok(fmt.Sprintf("%d channel(s) have their Secret", len(channels)))}
	}
	return nil
}

// unroutedTeams warns of each team that owns apps and notifies no channel.
func unroutedTeams(owners map[string][]string, routes map[string][]string) []PlatformEntry {
	teams := make([]string, 0, len(owners))
	for team := range owners {
		teams = append(teams, team)
	}
	sort.Strings(teams)
	entries := []PlatformEntry{}
	for _, team := range teams {
		if team != "" && len(routes[team]) == 0 {
			entries = append(entries, warn(fmt.Sprintf("team %s owns %d app(s) and has no notification channel: their alerts "+
				"and messages reach nobody (make team-channel NAME=%s)", team, len(owners[team]), team)))
		}
	}
	return entries
}

// MongoBackupEvidence is what the MongoDB backup check reads: the backup CronJob, whose BACKUP_S3_URI says where its
// backups are, and what the object store listed there.
type MongoBackupEvidence struct {
	Job     Read
	Listing ArchiveList
}

// BackupJobLocation is where the backup CronJob publishes: its first container's BACKUP_S3_URI, as a bucket and a
// prefix; false when the job states none of the form s3://<bucket>/<prefix>/.
func BackupJobLocation(job Read) (ArchiveQuery, bool) {
	var document struct {
		Spec struct {
			JobTemplate struct {
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct {
								Env []struct{ Name, Value string } `json:"env"`
							} `json:"containers"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			} `json:"jobTemplate"`
		} `json:"spec"`
	}
	if job.Status != Present || json.Unmarshal(job.Object, &document) != nil || len(document.Spec.JobTemplate.Spec.Template.Spec.Containers) == 0 {
		return ArchiveQuery{}, false
	}
	for _, variable := range document.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Env {
		if variable.Name == "BACKUP_S3_URI" {
			return BackupLocation(variable.Value)
		}
	}
	return ArchiveQuery{}, false
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

// backupSet is one kind of backup: its archives are <name>-<UTC stamp><suffix>, each complete only once its metadata,
// <name>-<UTC stamp>.json, is beside it (the job publishes that last). What to run, and the namespace whose backup jobs
// to check, are the operator's.
type backupSet struct {
	name, suffix, what, run, jobs, prefix string
}

func platformBackups(e MongoBackupEvidence, credentials bool, maxAge time.Duration, now time.Time) []PlatformEntry {
	what := "the MongoDB backup job (observability/backup-mongodb)"
	if problem, failed := platformUnread(e.Job, what, "where its backups are is unknown (make reconcile UNIT=platform-backup)"); failed {
		return []PlatformEntry{problem}
	}
	location, found := BackupJobLocation(e.Job)
	if !found {
		return []PlatformEntry{bad(what + " states no BACKUP_S3_URI of the form s3://<bucket>/<prefix>/, so its backups are unchecked")}
	}
	set := backupSet{name: "clickstack-mongodb", suffix: ".archive.gz.age", what: "MongoDB", run: "make backup-mongodb",
		jobs: PlatformMongoBackup.Namespace}
	return []PlatformEntry{set.entry(location, e.Listing, credentials, maxAge, now)}
}

// entry is the set's newest complete backup under the location, judged against the oldest one allowed.
func (s backupSet) entry(location ArchiveQuery, listing ArchiveList, credentials bool, maxAge time.Duration, now time.Time) PlatformEntry {
	uri := "s3://" + location.Bucket + "/" + location.Prefix
	switch {
	case !credentials:
		return bad(fmt.Sprintf("%scannot list backups in %s: AWS credentials not supplied (make verify-platform exports "+
			"them with aws configure export-credentials)", s.prefix, uri))
	case listing.Status != Present:
		return bad(fmt.Sprintf("%scannot list backups in %s: %s", s.prefix, uri, listedReason(listing)))
	}
	taken, found := s.newest(listing.Objects, location.Prefix)
	if !found {
		return bad(fmt.Sprintf("%sno %s backup in %s (run: %s)", s.prefix, s.what, uri, s.run))
	}
	age := now.Sub(taken)
	if age > maxAge {
		return bad(fmt.Sprintf("%snewest %s backup in %s is %.0f h old; check: kubectl -n %s get jobs -l "+
			"platform.swhurl.com/backup=true", s.prefix, s.what, uri, age.Hours(), s.jobs))
	}
	return ok(fmt.Sprintf("%snewest %s backup in %s is %.1f h old", s.prefix, s.what, uri, age.Hours()))
}

// backupStamp is the UTC time a backup's name carries.
const backupStamp = "20060102T150405Z"

// newest is when the newest complete backup under the prefix was taken, by the stamp in its name; false without one.
func (s backupSet) newest(objects []Archive, prefix string) (time.Time, bool) {
	present := map[string]bool{}
	for _, object := range objects {
		if name, found := strings.CutPrefix(object.Key, prefix); found {
			present[name] = true
		}
	}
	newest, found := time.Time{}, false
	for name := range present {
		taken, complete := s.taken(name, present)
		if complete && (!found || taken.After(newest)) {
			newest, found = taken, true
		}
	}
	return newest, found
}

// taken is when the archive with this name was taken; false for any other file, or an archive whose metadata is not
// among the names.
func (s backupSet) taken(name string, present map[string]bool) (time.Time, bool) {
	stamp, named := strings.CutPrefix(name, s.name+"-")
	stamp, archive := strings.CutSuffix(stamp, s.suffix)
	if !named || !archive || len(stamp) != len(backupStamp) || !present[s.name+"-"+stamp+".json"] {
		return time.Time{}, false
	}
	taken, err := time.Parse(backupStamp, stamp)
	return taken, err == nil
}

// DatabaseEvidence is what the SQLite backup check reads: each instance whose release declares a SQLite database,
// with what the object store listed under its prefix of the platform's location.
type DatabaseEvidence struct {
	// Problem says why the records or the profile do not say which databases are backed up, or where.
	Problem string
	// Location is the profile's backup location, one prefix per namespace below it.
	Location  ArchiveQuery
	Databases []DatabaseBackup
}

// DatabaseBackup is one database instance's namespace and its listing.
type DatabaseBackup struct {
	Namespace string
	Listing   ArchiveList
}

func platformDatabases(e DatabaseEvidence, credentials bool, maxAge time.Duration, now time.Time) []PlatformEntry {
	switch {
	case e.Problem != "":
		return []PlatformEntry{bad(e.Problem)}
	case len(e.Databases) == 0:
		return []PlatformEntry{ok("no app has a SQLite database")}
	}
	entries := make([]PlatformEntry, 0, len(e.Databases))
	for _, database := range e.Databases {
		set := backupSet{name: "sqlite", suffix: ".db.age", what: "SQLite", run: "make backup-sqlite",
			jobs: database.Namespace, prefix: database.Namespace + ": "}
		location := ArchiveQuery{Bucket: e.Location.Bucket, Prefix: e.Location.Prefix + database.Namespace + "/"}
		entries = append(entries, set.entry(location, database.Listing, credentials, maxAge, now))
	}
	return entries
}

// ConsoleEvidence is what the console check reads beyond its HelmRelease: whether its job record exists, and this
// checkout's console content tag as the caller supplied it.
type ConsoleEvidence struct {
	// Jobs is whether console/console-jobs exists; JobsUnread says why that was not read.
	Jobs       bool
	JobsUnread string
	// Tag is the checkout's content tag, src-<hash>; empty when it was not supplied.
	Tag string
}

const consoleUnit = "(make reconcile UNIT=platform-console)"

// platformConsole is whether the console image is built from this checkout's tooling, compared by content tag, and
// whether the console keeps its jobs across a restart.
func platformConsole(release Read, e ConsoleEvidence) []PlatformEntry {
	tag := ""
	if release.Status == Present {
		tag = platformConsoleTag(release.Object)
	}
	if tag == "" {
		return []PlatformEntry{bad("cannot read the console HelmRelease image tag (console/console), so its image is unchecked " + consoleUnit)}
	}
	entries := []PlatformEntry{}
	switch {
	case e.JobsUnread != "":
		entries = append(entries, bad("cannot read console/console-jobs ("+e.JobsUnread+"), so whether the console keeps its "+
			"jobs on restart is unknown "+consoleUnit))
	case !e.Jobs:
		entries = append(entries, bad("console/console-jobs is missing: the console forgets its jobs on restart "+consoleUnit))
	}
	return append(entries, consoleImage(tag, e.Tag))
}

func consoleImage(running, checkout string) PlatformEntry {
	content := strings.HasPrefix(running, "src-")
	label := running
	if !content && len(label) > 7 {
		label = label[:7]
	}
	switch {
	case checkout == "":
		return bad("this checkout's console content tag was not supplied, so console image " + label + " is unchecked " +
			"(make verify-platform computes it with images/content-tag.sh console)")
	case !content:
		return warn("cannot compare console image " + label + " with this checkout: it is not a content tag (src-…)")
	case running == checkout:
		return ok("console image " + label + " is built from the current tooling")
	}
	return warn("the console runs image " + label + ", older than the tooling in this checkout. Validate deploys a new " +
		"one about 2 minutes after a push (gh run list --workflow validate.yml); if it failed, or this checkout is ahead " +
		"of main: make console-image, commit and push")
}

// listedReason is why a listing was not read: its own reason, or that it never was.
func listedReason(listing ArchiveList) string {
	if listing.Reason == "" {
		return NotRead
	}
	return listing.Reason
}
