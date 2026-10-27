package kernel

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateRestore = flag.Bool("update-restore", false, "rewrite the golden restore objects in testdata/restore")

// restoreFixture is one restore's facts: obvious fake credentials, so a failure names a field and never
// something that could be mistaken for a real key.
func restoreFixture() RestoreJob {
	return RestoreJob{
		Name:          RestoreJobName("demo-orders", time.Date(2026, 10, 27, 3, 4, 5, 0, time.UTC)),
		Archive:       "app-sqlite/demo-orders-staging/sqlite-20261026T082103Z.db.age",
		ArchiveSHA256: strings.Repeat("a1", 32), PlainSHA256: strings.Repeat("b2", 32), Tables: 2,
		AgeIdentity: "AGE-SECRET-KEY-1" + strings.Repeat("Q", 58),
		ReadURL: "https://swhurl-platform-backups-110927251694.s3.eu-west-2.amazonaws.com/" +
			"app-sqlite/demo-orders-staging/sqlite-20261026T082103Z.db.age?X-Amz-Expires=3600" +
			"&X-Amz-Signature=0123456789abcdef",
	}
}

// restoreObjects is the render every restore test starts from: a database instance with an allocation.
func restoreObjects(t *testing.T) (RestoreObjects, RestoreJob) {
	t.Helper()
	job := restoreFixture()
	objects, err := RenderRestore(instance(t, "sqlite.json"), releaseFile(t, "r43.json"), profile(t),
		&Egress{Port: 30102}, job)
	if err != nil {
		t.Fatal(err)
	}
	return objects, job
}

// A restore renders two objects: the Secret holding its two credentials, and the Job that runs the backup
// image's restore command on the app's own claim. The Job carries no credential: both travel by reference.
func TestRestoreRendersASecretAndAJob(t *testing.T) {
	objects, job := restoreObjects(t)
	secret := restoreDocument(t, objects.Secret.Document)
	if secret["kind"] != "Secret" || objects.Secret.Name != "demo-orders-restore-key" {
		t.Fatalf("the restore Secret is %v", secret)
	}
	stringData := secret["stringData"].(map[string]any)
	if stringData[restoreKeyName] != job.AgeIdentity || stringData[restoreURLName] != job.ReadURL ||
		len(stringData) != 2 {
		t.Fatalf("the restore Secret holds %v", stringData)
	}
	if meta := secret["metadata"].(map[string]any); meta["namespace"] != "demo-orders-staging" {
		t.Fatalf("the Secret's namespace is %v", meta["namespace"])
	}

	jobDocument := restoreDocument(t, objects.Job.Document)
	if jobDocument["kind"] != "Job" || jobDocument["apiVersion"] != "batch/v1" || objects.Job.Name != job.Name {
		t.Fatalf("the restore Job is %v", jobDocument)
	}
	for _, credential := range []string{job.AgeIdentity, job.ReadURL} {
		if strings.Contains(string(objects.Job.Document), credential) {
			t.Fatal("the Job document carries a credential; it belongs in the Secret only")
		}
	}
	spec := jobDocument["spec"].(map[string]any)
	if spec["backoffLimit"] != float64(0) || spec["activeDeadlineSeconds"] != float64(RestoreDeadline/time.Second) {
		t.Fatalf("the Job's retries: %v", spec)
	}
	pod := spec["template"].(map[string]any)
	podSpec := pod["spec"].(map[string]any)
	labels := pod["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels[backupLabel] != "true" || labels[RestoreLabel] != job.Name {
		t.Fatalf("the pod's labels are %v: it must be admitted by the backup policy and findable by the restore", labels)
	}
	if security := podSpec["securityContext"].(map[string]any); security["runAsUser"] != float64(65532) ||
		security["runAsNonRoot"] != true || security["fsGroup"] != float64(65532) {
		t.Fatalf("the pod runs as %v, not the release's uid", security)
	}
	container := podSpec["containers"].([]any)[0].(map[string]any)
	if want := profile(t).Backup.Image + "@" + profile(t).Backup.Digest; container["image"] != want {
		t.Fatalf("the restore's image is %v, want the backup image %v", container["image"], want)
	}
	if args := container["args"].([]any); len(args) != 1 || args[0] != "restore-job-sqlite" {
		t.Fatalf("the restore's command: %v", args)
	}
	if container["envFrom"] != nil {
		t.Fatalf("the restore reads no upload key: %v", container["envFrom"])
	}
	env := envByName(t, container)
	for name, want := range map[string]any{
		"DATABASE_PATH":  DatabasePath,
		"BACKUP_SOURCE":  "demo-orders-staging/demo-orders",
		"ARCHIVE_SHA256": job.ArchiveSHA256,
		"PLAIN_SHA256":   job.PlainSHA256,
		"TABLES":         "2",
		"AGE_KEY_FILE":   "/keys/age.key",
		"HTTP_PROXY":     "http://egress.egress.svc.cluster.local:30102",
		"HTTPS_PROXY":    "http://egress.egress.svc.cluster.local:30102",
	} {
		if env[name] != want {
			t.Fatalf("the restore's %s is %v, want %v", name, env[name], want)
		}
	}
	if ref, ok := env["ARCHIVE_URL"].(map[string]any)["secretKeyRef"].(map[string]any); !ok ||
		ref["name"] != "demo-orders-restore-key" || ref["key"] != restoreURLName {
		t.Fatalf("the read URL is not a Secret reference: %v", env["ARCHIVE_URL"])
	}
	mounts := map[string]any{}
	for _, raw := range container["volumeMounts"].([]any) {
		mount := raw.(map[string]any)
		mounts[mount["name"].(string)] = mount["mountPath"]
	}
	if mounts["data"] != DataMount || mounts["keys"] != restoreKeyMount || mounts["scratch"] != "/scratch" {
		t.Fatalf("the restore's mounts are %v", mounts)
	}
}

// The golden bytes: a change to either document is a change to a contract the pod and the cluster read.
func TestRestoreDocumentsAreGolden(t *testing.T) {
	objects, _ := restoreObjects(t)
	for name, rendered := range map[string][]byte{"secret.json": objects.Secret.Document, "job.json": objects.Job.Document} {
		path := filepath.Join("..", "testdata", "restore", name)
		if *updateRestore {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, rendered, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(rendered) != string(want) {
			t.Fatalf("%s differs from the golden:\n%s\nwant:\n%s", name, rendered, want)
		}
	}
}

// A restore refuses what it cannot act on, and names the field. Nothing here decides about the archive: the
// plan and the restore command do.
func TestRestoreRefusals(t *testing.T) {
	spec, rel, p := instance(t, "sqlite.json"), releaseFile(t, "r43.json"), profile(t)
	egress := &Egress{Port: 30102}
	stateless := releaseFile(t, "r42.json")
	noBackup := func() PlatformProfile {
		copied := p
		copied.Backup = Backup{}
		return copied
	}
	cases := []struct {
		name string
		spec InstanceSpec
		rel  ReleaseBundle
		p    PlatformProfile
		eg   *Egress
		job  RestoreJob
		want Code
		what string
	}{
		{"stateless", spec, stateless, p, egress, restoreFixture(), Unsupported, "database"},
		{"no backup", spec, rel, noBackup(), egress, restoreFixture(), Unsupported, "backup"},
		{"no egress", spec, rel, p, nil, restoreFixture(), Unsupported, "egress"},
	}
	for _, tt := range cases {
		_, err := RenderRestore(tt.spec, tt.rel, tt.p, tt.eg, tt.job)
		refusal(t, err, tt.want, tt.what)
	}

	for _, field := range []struct {
		name  string
		job   func(*RestoreJob)
		field string
	}{
		{"name", func(j *RestoreJob) { j.Name = "" }, "restore.name"},
		{"name shape", func(j *RestoreJob) { j.Name = "Demo Orders" }, "restore.name"},
		{"archive", func(j *RestoreJob) { j.Archive = "" }, "restore.archive"},
		{"identity", func(j *RestoreJob) { j.AgeIdentity = "" }, "restore.ageIdentity"},
		{"identity shape", func(j *RestoreJob) { j.AgeIdentity = "age1publickey" }, "restore.ageIdentity"},
		{"url", func(j *RestoreJob) { j.ReadURL = "" }, "restore.readUrl"},
		{"url scheme", func(j *RestoreJob) { j.ReadURL = "http://insecure/archive" }, "restore.readUrl"},
		{"archive checksum", func(j *RestoreJob) { j.ArchiveSHA256 = "abc" }, "restore.archiveSha256"},
		{"plain checksum", func(j *RestoreJob) { j.PlainSHA256 = strings.Repeat("A", 64) }, "restore.plainSha256"},
		{"tables", func(j *RestoreJob) { j.Tables = 0 }, "restore.tables"},
	} {
		job := restoreFixture()
		field.job(&job)
		_, err := RenderRestore(spec, rel, p, egress, job)
		refusal(t, err, Invalid, field.field)
	}
}

// A Job's name is the application and the second it started: unique per run, bounded by the cluster's own
// limit, and never ending in the truncation's dash.
func TestRestoreJobNames(t *testing.T) {
	first := time.Date(2026, 10, 27, 3, 4, 5, 0, time.UTC)
	if got, want := RestoreJobName("demo-orders", first), "demo-orders-restore-20261027t030405z"; got != want {
		t.Fatalf("name %q, want %q", got, want)
	}
	if RestoreJobName("demo-orders", first) == RestoreJobName("demo-orders", first.Add(time.Second)) {
		t.Fatal("two runs of one instance share a name")
	}
	long := RestoreJobName(strings.Repeat("a", 40), first)
	if len(long) > restoreNameLimit || !labelPattern.MatchString(long) {
		t.Fatalf("a long application's name is %q (%d characters)", long, len(long))
	}
	if got := RestoreJobName(strings.Repeat("a", 40)+"-", first); strings.Contains(got, "--restore") {
		t.Fatalf("the truncation left a trailing dash: %q", got)
	}
}

// document is one rendered document as a map.
func restoreDocument(t *testing.T, data []byte) map[string]any {
	t.Helper()
	decoded := map[string]any{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// envOf is a container's environment by name: a plain value, or the valueFrom when it is a reference.
func envByName(t *testing.T, container map[string]any) map[string]any {
	t.Helper()
	env := map[string]any{}
	for _, raw := range container["env"].([]any) {
		entry := raw.(map[string]any)
		if value, given := entry["value"]; given {
			env[entry["name"].(string)] = value
			continue
		}
		env[entry["name"].(string)] = entry["valueFrom"]
	}
	return env
}

// A restore mounts the claim the instance adopted, not one named after it: the live try-10 restore used an
// adopted claim, and there its name happened to equal the application's, so only a differently named claim
// can tell the two apart.
func TestRestoreUsesTheAdoptedClaim(t *testing.T) {
	spec := instance(t, "sqlite.json")
	spec.Storage.ClaimName = "orders-data"
	objects, err := RenderRestore(spec, releaseFile(t, "r43.json"), profile(t), &Egress{Port: 30102},
		restoreFixture())
	if err != nil {
		t.Fatal(err)
	}
	job := restoreDocument(t, objects.Job.Document)
	volumes := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["volumes"].([]any)
	claim := volumes[0].(map[string]any)["persistentVolumeClaim"].(map[string]any)["claimName"]
	if claim != "orders-data" {
		t.Fatalf("the restore mounts %v, not the adopted claim", claim)
	}
}
