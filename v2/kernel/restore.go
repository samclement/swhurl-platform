package kernel

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The restore's own facts, stated once: the Secret's two keys and where the pod mounts it, the label that
// finds the instance's job pods, and the Job's own bounds. A restore pod runs the platform's backup image,
// so its numbers are the backup job's, not a second contract.
const (
	restoreSecretSuffix = "-restore-key"
	restoreKeyName      = "age.key"
	restoreURLName      = "archive-url"
	restoreKeyMount     = "/keys"
	restoreNameLimit    = 63
	restoreStampLayout  = "20060102t150405z"
)

// RestoreLabel marks the restore's own pod, so the one run's log is found by v2's label rather than one the
// cluster generates.
const RestoreLabel = labelDomain + "restore"

// RestoreDeadline is how long one restore pod may run: the Job's own activeDeadlineSeconds, and the bound the
// use case's wait is derived from.
const RestoreDeadline = 15 * time.Minute

// restoreChecksum is a SHA-256 checksum as the backup job's metadata records it: hex, no prefix.
var restoreChecksum = regexp.MustCompile(`^[0-9a-f]{64}$`)

// RestoreJob is one restore's own facts, decided above the kernel: the archive the plan chose, the checksums
// and table count its metadata records, the two credentials its pod holds for one run, and the name the Job
// is created under. None of it is instance intent — the objects are created by the restore use case, deleted
// by it, and never compiled, claimed or written to Git.
type RestoreJob struct {
	Name                       string
	Archive                    string
	ArchiveSHA256, PlainSHA256 string
	Tables                     int32
	// AgeIdentity and ReadURL are the credentials the pod holds for its one run: the age identity that
	// decrypts the archive, and a presigned read for that one object. The kernel renders them into the
	// Secret and decides nothing about what they are.
	AgeIdentity, ReadURL string
}

// RestoreObject is one rendered document and the name it goes under.
type RestoreObject struct {
	Name     string
	Document []byte
}

// RestoreObjects are the two transient objects a restore creates: the Secret its pod reads, and the Job
// that restores the archive.
type RestoreObjects struct{ Secret, Job RestoreObject }

// RestoreJobName is the name one restore's Job is created under: the application and the UTC second it
// started, bounded by Kubernetes' own name limit. Two runs of one instance differ, so a run that was
// interrupted is still findable by name.
func RestoreJobName(application string, started time.Time) string {
	stamp := started.UTC().Format(restoreStampLayout)
	stem := "-restore-"
	room := restoreNameLimit - len(stem) - len(stamp)
	if len(application) > room {
		application = strings.TrimRight(application[:room], "-")
	}
	return application + stem + stamp
}

// RenderRestore renders the two objects one restore creates, from the same records the compiler reads. The
// Secret holds the two credentials the pod needs — the age identity and the presigned read URL — so the Job
// document carries no credential value. It refuses an instance a restore cannot act on, and decides nothing
// about the archive's content.
func RenderRestore(spec InstanceSpec, release ReleaseBundle, profile PlatformProfile, egress *Egress,
	job RestoreJob) (RestoreObjects, error) {
	if err := job.validate(); err != nil {
		return RestoreObjects{}, err
	}
	instance := instanceOf(spec, release, Connections{}, egress)
	switch {
	case release.Database != "sqlite":
		return RestoreObjects{}, refuse(Unsupported, "database", "expected_sqlite")
	case profile.Backup.absent():
		return RestoreObjects{}, refuse(Unsupported, "backup", "the profile states no backup")
	case instance.Egress == nil || profile.Egress.Address == "":
		return RestoreObjects{}, refuse(Unsupported, "egress", "the pod could not reach the object store")
	}
	name := restoreSecretName(instance)
	secret := manifest{"apiVersion": "v1", "kind": "Secret",
		"metadata": metadata(name, namespaceName(instance)), "type": "Opaque",
		"stringData": manifest{restoreKeyName: job.AgeIdentity, restoreURLName: job.ReadURL}}
	return RestoreObjects{
		Secret: RestoreObject{Name: name, Document: encodeManifest(secret)},
		Job:    RestoreObject{Name: job.Name, Document: encodeManifest(restoreJob(instance, release.Runtime.UID, profile, job))},
	}, nil
}

// restoreJob is the Job: the backup image's restore command, the app's own claim, and the Secret's two keys.
// backoffLimit is zero because a failed restore must not be retried on top of a database it may have moved.
func restoreJob(instance HTTPInstance, uid int32, profile PlatformProfile, job RestoreJob) manifest {
	name := restoreSecretName(instance)
	labels := manifest{backupLabel: "true", RestoreLabel: job.Name}
	container := jobContainer("restore", profile.Backup.Image+"@"+profile.Backup.Digest, []any{"restore-job-sqlite"},
		restoreEnv(instance, profile, job), nil,
		jobMounts(manifest{"name": "keys", "mountPath": restoreKeyMount, "readOnly": true}))
	pod := jobPod(uid, claimName(instance), labels, container,
		[]any{manifest{"name": "keys", "secret": manifest{"secretName": name}}})
	return manifest{"apiVersion": "batch/v1", "kind": "Job",
		"metadata": manifest{"name": job.Name, "namespace": namespaceName(instance), "labels": labels},
		"spec": manifest{"backoffLimit": 0, "activeDeadlineSeconds": int(RestoreDeadline / time.Second),
			"template": pod}}
}

// restoreEnv is what the restore command requires and refuses without, plus the pod's own way out. The read
// URL comes from the Secret by reference, so it is not in this document.
func restoreEnv(instance HTTPInstance, profile PlatformProfile, job RestoreJob) []any {
	return append(proxyEnv(instance, profile),
		manifest{"name": "AGE_KEY_FILE", "value": restoreKeyMount + "/" + restoreKeyName},
		manifest{"name": "ARCHIVE_URL", "valueFrom": manifest{"secretKeyRef": manifest{
			"name": restoreSecretName(instance), "key": restoreURLName}}},
		manifest{"name": "ARCHIVE_SHA256", "value": job.ArchiveSHA256},
		manifest{"name": "PLAIN_SHA256", "value": job.PlainSHA256},
		manifest{"name": "TABLES", "value": strconv.Itoa(int(job.Tables))},
		manifest{"name": "BACKUP_SOURCE", "value": namespaceName(instance) + "/" + instance.Application},
		manifest{"name": "DATABASE_PATH", "value": DatabasePath})
}

// restoreSecretName is the Secret one instance's restore reads: transient, and named after the instance so a
// failure leaves it where the operator expects to find it.
func restoreSecretName(spec HTTPInstance) string { return spec.Application + restoreSecretSuffix }

// validate checks the facts a restore's own documents state. Each refusal names its field and never echoes
// the value: the two credentials are not repeated in a diagnostic.
func (job RestoreJob) validate() error {
	for _, field := range []struct{ name, value string }{
		{"restore.name", job.Name}, {"restore.archive", job.Archive},
		{"restore.ageIdentity", job.AgeIdentity}, {"restore.readUrl", job.ReadURL},
	} {
		if field.value == "" {
			return refuse(Invalid, field.name, "required")
		}
	}
	if !labelPattern.MatchString(job.Name) {
		return refuse(Invalid, "restore.name", "expected_a_kubernetes_name")
	}
	if !strings.HasPrefix(job.AgeIdentity, "AGE-SECRET-KEY-1") {
		return refuse(Invalid, "restore.ageIdentity", "expected_an_age_identity")
	}
	if !strings.HasPrefix(job.ReadURL, "https://") {
		return refuse(Invalid, "restore.readUrl", "expected_an_https_url")
	}
	for _, field := range []struct{ name, value string }{
		{"restore.archiveSha256", job.ArchiveSHA256}, {"restore.plainSha256", job.PlainSHA256},
	} {
		if !restoreChecksum.MatchString(field.value) {
			return refuse(Invalid, field.name, "expected_a_sha256_checksum")
		}
	}
	if job.Tables < 1 {
		return refuse(Invalid, "restore.tables", "expected_a_table_count")
	}
	return nil
}
