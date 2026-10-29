package kernel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// databaseInstance is a SQLite instance with its volume and its allocation: what a backup needs.
func databaseInstance() (InstanceFacts, WorkloadFacts) {
	instance, workload := previewFacts()
	instance.Application, instance.Environment = "demo-orders", "staging"
	instance.Database = "sqlite"
	instance.Storage = Storage{SizeMiB: 1024}
	instance.Egress = &Egress{Port: 30102, Grants: []string{}}
	return instance, workload
}

// fileOf is one rendered document, by name.
func fileOf(t *testing.T, rendered ResourceSet, name string) map[string]any {
	t.Helper()
	for _, file := range rendered.Files {
		if strings.HasSuffix(file.Path, "/"+name) {
			document := map[string]any{}
			if err := json.Unmarshal(file.Document, &document); err != nil {
				t.Fatal(err)
			}
			return document
		}
	}
	t.Fatalf("no %s among the files", name)
	return nil
}

// A database instance renders its backup: the job, the reference to the upload key it reads, and a policy
// for the pod that reaches S3. The key itself never appears.
func TestBackupRendersForADatabaseInstance(t *testing.T) {
	instance, workload := databaseInstance()
	rendered, err := RenderInstance(instance, workload, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, file := range rendered.Files {
		paths = append(paths, file.Path)
	}
	for _, want := range []string{"backup-secret.json", "backup-cronjob.json", "backup-networkpolicy.json"} {
		if !containsPath(paths, want) {
			t.Fatalf("%s is not rendered: %v", want, paths)
		}
	}
	kustomization := fileOf(t, rendered, "kustomization.yaml")
	resources := kustomization["resources"].([]any)
	if len(resources) != 7 {
		t.Fatalf("the kustomization lists %v", resources)
	}

	// The key is a reference the unit substitutes, never a value.
	secret := fileOf(t, rendered, "backup-secret.json")
	stringData := secret["stringData"].(map[string]any)
	if secret["kind"] != "Secret" || secret["metadata"].(map[string]any)["name"] != "demo-orders-backup" ||
		stringData[backupKeyVariable] != backupKeyReference ||
		stringData[backupSecretVariable] != backupSecretReference || len(stringData) != 2 {
		t.Fatalf("the backup Secret is %v", secret)
	}

	job := fileOf(t, rendered, "backup-cronjob.json")
	if job["kind"] != "CronJob" || job["apiVersion"] != "batch/v1" {
		t.Fatalf("the backup job is %v", job["kind"])
	}
	spec := job["spec"].(map[string]any)
	for key, want := range map[string]any{
		"schedule": "13 4 * * *", "timeZone": "Etc/UTC", "suspend": false, "concurrencyPolicy": "Forbid",
		"startingDeadlineSeconds": float64(3600), "successfulJobsHistoryLimit": float64(1), "failedJobsHistoryLimit": float64(3),
	} {
		if spec[key] != want {
			t.Fatalf("the job's %s is %v, want %v", key, spec[key], want)
		}
	}
	jobSpec := spec["jobTemplate"].(map[string]any)["spec"].(map[string]any)
	if jobSpec["backoffLimit"] != float64(2) || jobSpec["activeDeadlineSeconds"] != float64(backupDeadlineSeconds) {
		t.Fatalf("the job's retries: %v", jobSpec)
	}
	pod := jobSpec["template"].(map[string]any)
	if pod["metadata"].(map[string]any)["labels"].(map[string]any)[backupLabel] != "true" {
		t.Fatalf("the pod is not marked as the platform's backup: %v", pod["metadata"])
	}
	podSpec := pod["spec"].(map[string]any)
	if podSpec["restartPolicy"] != "Never" || podSpec["automountServiceAccountToken"] != false {
		t.Fatalf("the pod's own policy: %v", podSpec)
	}
	if security := podSpec["securityContext"].(map[string]any); security["runAsUser"] != float64(101) || security["runAsNonRoot"] != true {
		t.Fatalf("the pod runs as %v, not the release's uid", security)
	}
	container := podSpec["containers"].([]any)[0].(map[string]any)
	if want := profile(t).Backup.Image + "@" + profile(t).Backup.Digest; container["image"] != want {
		t.Fatalf("the job's image is %v, want %v", container["image"], want)
	}
	if args := container["args"].([]any); len(args) != 1 || args[0] != "backup-job-sqlite" {
		t.Fatalf("the job's command: %v", args)
	}
	env := map[string]any{}
	for _, raw := range container["env"].([]any) {
		entry := raw.(map[string]any)
		if value, given := entry["value"]; given {
			env[entry["name"].(string)] = value
		} else {
			env[entry["name"].(string)] = entry["valueFrom"]
		}
	}
	for name, want := range map[string]any{
		"HTTP_PROXY":    "http://egress.egress.svc.cluster.local:30102",
		"HTTPS_PROXY":   "http://egress.egress.svc.cluster.local:30102",
		"NO_PROXY":      "localhost,127.0.0.1,.svc,.cluster.local,$(HOST_IP)",
		"AGE_RECIPIENT": profile(t).SOPS.Recipient,
		"BACKUP_S3_URI": "s3://swhurl-platform-backups-110927251694/app-sqlite/demo-orders-staging/",
		"BACKUP_SOURCE": "demo-orders-staging/demo-orders",
		"DATABASE_PATH": DatabasePath,
	} {
		if env[name] != want {
			t.Fatalf("the job's %s is %v, want %v", name, env[name], want)
		}
	}
	if _, ok := env["HOST_IP"].(map[string]any); !ok {
		t.Fatalf("HOST_IP is not the downward API: %v", env["HOST_IP"])
	}
	if from := container["envFrom"].([]any)[0].(map[string]any)["secretRef"].(map[string]any); from["name"] != "demo-orders-backup" {
		t.Fatalf("the job reads its key from %v", from)
	}
	// The app's own claim, so the job reads the data the app wrote, and no other volume.
	volumes := podSpec["volumes"].([]any)
	claim := volumes[0].(map[string]any)
	if claim["persistentVolumeClaim"].(map[string]any)["claimName"] != "demo-orders" {
		t.Fatalf("the job mounts %v", claim)
	}

	// The pod that must leave the cluster has its own policy: DNS and the instance's proxy port, nothing in.
	policy := fileOf(t, rendered, "backup-networkpolicy.json")
	policySpec := policy["spec"].(map[string]any)
	if policySpec["podSelector"].(map[string]any)["matchLabels"].(map[string]any)[backupLabel] != "true" {
		t.Fatalf("the policy selects %v", policySpec["podSelector"])
	}
	if ingress := policySpec["ingress"].([]any); len(ingress) != 0 {
		t.Fatalf("the backup pod admits ingress: %v", ingress)
	}
	egress := policySpec["egress"].([]any)
	if len(egress) != 2 {
		t.Fatalf("the backup pod's egress is %v", egress)
	}
	if dns := egress[0].(map[string]any)["to"].([]any)[0].(map[string]any); dns["podSelector"].(map[string]any)["matchLabels"].(map[string]any)["k8s-app"] != "kube-dns" {
		t.Fatalf("the job cannot resolve a name: %v", dns)
	}
	if proxy := egress[1].(map[string]any); proxy["ports"].([]any)[0].(map[string]any)["port"] != float64(30102) {
		t.Fatalf("the job cannot reach its proxy port: %v", proxy)
	}

	// The unit waits for the platform's backup unit and substitutes the key from its Secret.
	unit := fileOf(t, rendered, "app-demo-orders-staging.json")
	unitSpec := unit["spec"].(map[string]any)
	dependsOn := unitSpec["dependsOn"].([]any)
	if len(dependsOn) != 2 || dependsOn[1].(map[string]any)["name"] != backupUnit {
		t.Fatalf("the unit depends on %v", dependsOn)
	}
	substitute := unitSpec["postBuild"].(map[string]any)["substituteFrom"].([]any)[0].(map[string]any)
	if substitute["kind"] != "Secret" || substitute["name"] != "backup-writer" || substitute["optional"] != false {
		t.Fatalf("the unit substitutes from %v", substitute)
	}
	// Three more objects claimed: the Secret, the CronJob and the policy.
	owners := map[string]bool{}
	for _, claim := range rendered.Inventory {
		owners[claim.Kind] = true
	}
	for _, kind := range []string{"Secret", "CronJob", "NetworkPolicy"} {
		if !owners[kind] {
			t.Fatalf("%s is not claimed: %v", kind, rendered.Inventory)
		}
	}
}

func containsPath(paths []string, name string) bool {
	for _, path := range paths {
		if strings.HasSuffix(path, "/"+name) {
			return true
		}
	}
	return false
}

// The schedule is the namespace's own minute, derived as v1 derives it, so a migrated database keeps the
// slot its logs and the operator's habits name.
func TestBackupScheduleKeepsV1Slots(t *testing.T) {
	for namespace, want := range map[string]string{
		"demo-orders-staging":   "13 4 * * *",
		"demo-orders-prod":      "42 3 * * *",
		"swhurl-try-10-staging": "58 3 * * *",
		"swhurl-try-10-prod":    "7 4 * * *",
	} {
		if got := backupSchedule(namespace); got != want {
			t.Fatalf("%s runs at %q, v1 at %q", namespace, got, want)
		}
	}
	if backupSchedule("demo-orders-staging") != backupSchedule("demo-orders-staging") {
		t.Fatal("the schedule is not stable")
	}
}

// A backup that cannot be rendered is refused rather than guessed: no platform facts, no allocation to
// upload through, or a document Flux would substitute something v2 did not mean.
func TestBackupRefusals(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*InstanceFacts, *PlatformProfile)
		code   Code
		field  string
	}{
		{"no backup facts", func(s *InstanceFacts, p *PlatformProfile) { p.Backup = Backup{} }, Unsupported, "backup"},
		{"no allocation", func(s *InstanceFacts, p *PlatformProfile) { s.Egress = nil }, Unsupported, "backup.egress"},
		{"suspended only", func(s *InstanceFacts, p *PlatformProfile) {
			p.Backup = Backup{Suspended: true}
		}, Invalid, "backup.image"},
		{"image", func(s *InstanceFacts, p *PlatformProfile) { p.Backup.Image = "Backup" }, Invalid, "backup.image"},
		{"digest", func(s *InstanceFacts, p *PlatformProfile) { p.Backup.Digest = "sha256:short" }, Invalid, "backup.digest"},
		{"s3 uri", func(s *InstanceFacts, p *PlatformProfile) { p.Backup.S3URI = "s3://bucket/prefix" }, Invalid, "backup.s3Uri"},
		{"s3 uri without prefix", func(s *InstanceFacts, p *PlatformProfile) { p.Backup.S3URI = "s3://bucket" }, Invalid, "backup.s3Uri"},
		{"no region", func(s *InstanceFacts, p *PlatformProfile) { p.Backup.Region = "" }, Invalid, "backup.region"},
		{"region", func(s *InstanceFacts, p *PlatformProfile) { p.Backup.Region = "s3.eu-west-2" }, Invalid, "backup.region"},
		{"recipient", func(s *InstanceFacts, p *PlatformProfile) { p.SOPS.Recipient = "age1notakey" }, Invalid, "sops.recipient"},
		{"writer namespace", func(s *InstanceFacts, p *PlatformProfile) { p.Backup.WriterNamespace = "Flux" }, Invalid, "backup.writerNamespace"},
		{"writer name", func(s *InstanceFacts, p *PlatformProfile) { p.Backup.WriterName = "" }, Invalid, "backup.writerName"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instance, workload := databaseInstance()
			p := profile(t)
			tt.change(&instance, &p)
			rendered, err := RenderInstance(instance, workload, p)
			refusal(t, err, tt.code, tt.field)
			if !reflect.DeepEqual(rendered, ResourceSet{}) {
				t.Fatal("a refusal rendered something")
			}
		})
	}
}

// Substitution is one switch for every document in the folder, so a reference v2 did not write on purpose
// is a refusal: it would be substituted too, or fail to resolve, and nothing would say which.
func TestSubstitutionGuard(t *testing.T) {
	good := []File{{"x/backup-secret.json", []byte(`{"a":"` + backupKeyReference + `","b":"` + backupSecretReference + `"}`)}}
	if err := substitutionProblem(good); err != nil {
		t.Fatalf("the backup's own references were refused: %v", err)
	}
	for _, document := range []string{
		`{"a":"${OTHER_TOKEN}"}`,
		`{"a":"` + backupKeyReference + `","b":"prefix ${NOT_ALLOWED}"}`,
		// The Secret's own key name is not a reference: a document naming AWS_ACCESS_KEY_ID as a
		// reference would have Flux look for a variable the platform does not set.
		`{"a":"${AWS_ACCESS_KEY_ID}"}`,
		`{"a":"${{}}"}`,
	} {
		if err := substitutionProblem([]File{{"x/document.json", []byte(document)}}); err == nil {
			t.Fatalf("%s was accepted", document)
		} else {
			refusal(t, err, Invalid, "backup")
		}
	}
}

// The quota counts the pod the backup needs beside the app: a database that fits without it does not fit
// with it, and a stopped database still needs one pod for its job.
func TestQuotaCountsTheBackupPod(t *testing.T) {
	instance, workload := databaseInstance()
	instance.Configuration = Configuration{Replicas: 1, MemoryMiB: 128}
	p := profile(t)
	p.Quota.Pods = 2 // the app and one more for a rolling update, and no room for the backup
	if _, err := RenderInstance(instance, workload, p); err == nil {
		t.Fatal("a database without room for its backup was accepted")
	} else {
		refusal(t, err, OverQuota, "quota.pods")
	}
	instance.Database, instance.Storage = "", Storage{}
	// Without a database the same instance fits: the pod that did not fit was the backup's.
	if _, err := RenderInstance(instance, workload, p); err != nil {
		t.Fatal(err)
	}
	// A stopped database still runs its job, so it still needs one pod.
	instance, workload = databaseInstance()
	instance.Configuration = Configuration{Replicas: 0, MemoryMiB: 128}
	instance.Database, instance.Storage = "sqlite", Storage{SizeMiB: 1024}
	starved := profile(t)
	starved.Quota.RequestsCPUMilli = backupCPURequestMilli - 1
	if _, err := RenderInstance(instance, workload, starved); err == nil {
		t.Fatal("a stopped database with no room for its backup pod was accepted")
	} else {
		refusal(t, err, OverQuota, "quota.requestsCpuMilli")
	}
}

// A migrated instance adopts the claim its data already lives on instead of creating one: the chart mounts
// the same volume, the backup job mounts the same volume, and nothing asks for a new one.
func TestStorageAdoptsAnExistingClaim(t *testing.T) {
	instance, workload := databaseInstance()
	instance.Storage = Storage{ClaimName: "demo-orders"}
	rendered, err := RenderInstance(instance, workload, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	values := fileOf(t, rendered, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	data := values["persistence"].(map[string]any)["data"].(map[string]any)
	if data["existingClaim"] != "demo-orders" {
		t.Fatalf("the volume is %v", data)
	}
	for _, absent := range []string{"storageClass", "size", "accessMode", "retain"} {
		if _, given := data[absent]; given {
			t.Fatalf("an adopted claim states %s: %v", absent, data)
		}
	}
	if mount := data["globalMounts"].([]any)[0].(map[string]any); mount["path"] != DataMount {
		t.Fatalf("the adopted volume is mounted at %v", mount)
	}
	pod := fileOf(t, rendered, "backup-cronjob.json")["spec"].(map[string]any)["jobTemplate"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if claim := pod["volumes"].([]any)[0].(map[string]any)["persistentVolumeClaim"].(map[string]any)["claimName"]; claim != "demo-orders" {
		t.Fatalf("the backup job mounts %v", claim)
	}
	// The identity moved deliberately when the record gained the fact, and both shapes stay valid.
	if _, err := RenderInstance(func() InstanceFacts {
		other, _ := databaseInstance()
		other.Storage = Storage{SizeMiB: 1024}
		return other
	}(), workload, profile(t)); err != nil {
		t.Fatalf("a created volume no longer renders: %v", err)
	}
}

// A volume is created or adopted, never both, and a database needs one of them.
func TestStorageClaimRefusals(t *testing.T) {
	for _, tt := range []struct {
		name    string
		storage Storage
		field   string
	}{
		{"size and claim", Storage{SizeMiB: 1024, ClaimName: "demo-orders"}, "storage"},
		{"neither", Storage{}, "storage.sizeMiB"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instance, workload := databaseInstance()
			instance.Storage = tt.storage
			rendered, err := RenderInstance(instance, workload, profile(t))
			refusal(t, err, Invalid, tt.field)
			if !reflect.DeepEqual(rendered, ResourceSet{}) {
				t.Fatal("a refusal rendered something")
			}
		})
	}
	// The record's own boundary: a claim is a DNS label.
	spec := instance(t, "prod.json")
	spec.Storage = Storage{ClaimName: "Not A Label"}
	code(t, spec.validate(), Invalid)
}

// The backup host is the bucket's regional endpoint, the one the job uploads to, and nothing when the profile
// states no backup.
func TestBackupHost(t *testing.T) {
	p := profile(t)
	if got, want := p.Backup.Host(), "swhurl-platform-backups-110927251694.s3.eu-west-2.amazonaws.com:443"; got != want {
		t.Fatalf("host %q, want %q", got, want)
	}
	if got := (Backup{}).Host(); got != "" {
		t.Fatalf("no backup has a host: %q", got)
	}
}
