package observe_test

import (
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// backed is the target with a nightly protection.
func backed(target observe.Target) observe.Target {
	target.Backup = &observe.BackupTarget{
		CronJob:  observe.Ref{APIVersion: "batch/v1", Kind: "CronJob", Namespace: target.Namespace, Name: "hello-backup"},
		Pods:     observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: target.Namespace},
		Selector: "platform.swhurl.com/backup=true",
		Schedule: "35 3 * * *",
	}
	return target
}

// cronJob is the CronJob document the observation reads.
func cronJob(t *testing.T, suspended bool, scheduled, succeeded string) []byte {
	t.Helper()
	status := map[string]any{}
	if scheduled != "" {
		status["lastScheduleTime"] = scheduled
	}
	if succeeded != "" {
		status["lastSuccessfulTime"] = succeeded
	}
	return encode(t, map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]any{"name": "hello-backup", "namespace": "hello-staging"},
		"spec":     map[string]any{"schedule": "35 3 * * *", "suspend": suspended},
		"status":   status,
	})
}

// backupPod is a pod one run created: with an owner, it names the run's Job.
func backupPod(t *testing.T, name, created, owner string) []byte {
	t.Helper()
	owners := []any{}
	if owner != "" {
		owners = append(owners, map[string]any{"kind": "Job", "name": owner})
	}
	return encode(t, map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": "hello-staging",
			"creationTimestamp": created, "ownerReferences": owners},
	})
}

// job is one run's document: a terminal condition, a run in progress, or neither.
func job(t *testing.T, name, created, completed, outcome string) []byte {
	t.Helper()
	status := map[string]any{"startTime": created, "conditions": []any{}}
	switch outcome {
	case observe.RunComplete, observe.RunFailed:
		status["completionTime"] = completed
		condition := map[string]string{observe.RunComplete: "Complete", observe.RunFailed: "Failed"}[outcome]
		status["conditions"] = []any{map[string]any{"type": condition, "status": "True"}}
	case observe.RunRunning:
		status["active"] = 1
	}
	return encode(t, map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "namespace": "hello-staging", "creationTimestamp": created},
		"status":   status,
	})
}

// protection is the healthy evidence with the protection's own reads.
func protection(t *testing.T, maxAge time.Duration, cronJobDocument, podDocument, runDocument []byte) observe.Evidence {
	t.Helper()
	evidence := healthy(t)
	// The success the tests use is on the 26th; healthy's own observation time is the day before.
	evidence.ObservedAt = observedAt
	items := [][]byte{}
	if podDocument != nil {
		items = append(items, podDocument)
	}
	backup := &observe.BackupEvidence{
		CronJob: present(backed(target()).Backup.CronJob, cronJobDocument),
		Pods: observe.List{Ref: backed(target()).Backup.Pods, Selector: "platform.swhurl.com/backup=true",
			Status: observe.Present, Items: items},
		MaxAge: maxAge,
	}
	if runDocument != nil {
		read := present(observe.Ref{APIVersion: "batch/v1", Kind: "Job", Namespace: "hello-staging", Name: "hello-backup-1"}, runDocument)
		backup.Run = &read
	}
	evidence.Backup = backup
	return evidence
}

// explainBackup is Explain over the protected target: a report the test reads the backup answer from.
func explainBackup(t *testing.T, target observe.Target, evidence observe.Evidence) observe.Report {
	t.Helper()
	report, err := observe.Explain(target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// The healthy evidence's observation time: a success five and a half hours earlier is current under a day.
const observedAt = "2026-10-26T09:14:00Z"

// The run the tests' CronJob reports.
func succeededRun(t *testing.T) []byte {
	return job(t, "hello-backup-1", "2026-10-26T03:35:00Z", "2026-10-26T03:36:12Z", observe.RunComplete)
}

// A run that succeeded within the threshold is current, and the report names the run it read.
func TestBackupIsCurrentWhenTheRunSucceeded(t *testing.T) {
	evidence := protection(t, 26*time.Hour,
		cronJob(t, false, "2026-10-26T03:35:00Z", "2026-10-26T03:36:12Z"),
		backupPod(t, "hello-backup-1-abc", "2026-10-26T03:35:00Z", "hello-backup-1"), succeededRun(t))
	report := explainBackup(t, backed(target()), evidence)
	if report.Backup.State != observe.BackupCurrent || report.Backup.Reason != "" {
		t.Fatalf("backup %+v", report.Backup)
	}
	if report.Backup.Schedule != "35 3 * * *" || report.Backup.MaxAge != 26*time.Hour {
		t.Fatalf("backup facts %+v", report.Backup)
	}
	if report.Backup.NewestRun == nil || report.Backup.NewestRun.Name != "hello-backup-1" ||
		report.Backup.NewestRun.Outcome != observe.RunComplete {
		t.Fatalf("newest run %+v", report.Backup.NewestRun)
	}
	// The protection is beside the verdict, never inside it.
	if report.Verdict != observe.IsRunning {
		t.Fatalf("verdict %s", report.Verdict)
	}
}

// The threshold is the caller's: the same evidence is current under a day and stale under an hour, and a
// success exactly at the threshold is still current.
func TestBackupAgeIsJudgedAgainstTheThreshold(t *testing.T) {
	document := cronJob(t, false, "2026-10-26T03:35:00Z", "2026-10-26T03:36:12Z")
	pod := backupPod(t, "hello-backup-1-abc", "2026-10-26T03:35:00Z", "hello-backup-1")
	runs := succeededRun(t)
	if report := explainBackup(t, backed(target()), protection(t, 6*time.Hour, document, pod, runs)); report.Backup.State != observe.BackupCurrent {
		t.Fatalf("six hours: %+v", report.Backup)
	}
	stale := explainBackup(t, backed(target()), protection(t, time.Hour, document, pod, runs))
	if stale.Backup.State != observe.BackupStale || stale.Backup.Reason != observe.ReasonTooOld {
		t.Fatalf("one hour: %+v", stale.Backup)
	}
	// The last success is 5h37m48s before the observation: exactly that threshold is still current.
	at, err := time.Parse(time.RFC3339, "2026-10-26T03:36:12Z")
	if err != nil {
		t.Fatal(err)
	}
	observed, _ := time.Parse(time.RFC3339, observedAt)
	if report := explainBackup(t, backed(target()), protection(t, observed.Sub(at), document, pod, runs)); report.Backup.State != observe.BackupCurrent {
		t.Fatalf("exactly the threshold: %+v", report.Backup)
	}
	if report := explainBackup(t, backed(target()), protection(t, observed.Sub(at)-time.Second, document, pod, runs)); report.Backup.State != observe.BackupStale {
		t.Fatalf("a second over: %+v", report.Backup)
	}
}

// A failed newest run outranks an older success: the newest data is not what was backed up.
func TestBackupFailsWhenTheNewestRunFailed(t *testing.T) {
	evidence := protection(t, 26*time.Hour,
		cronJob(t, false, "2026-10-26T03:35:00Z", "2026-10-26T03:36:12Z"),
		backupPod(t, "hello-backup-2-def", "2026-10-26T09:00:00Z", "hello-backup-2"),
		job(t, "hello-backup-2", "2026-10-26T09:00:00Z", "2026-10-26T09:04:00Z", observe.RunFailed))
	report := explainBackup(t, backed(target()), evidence)
	if report.Backup.State != observe.BackupFailing || report.Backup.Reason != observe.ReasonRunFailed {
		t.Fatalf("backup %+v", report.Backup)
	}
	if report.Backup.NewestRun == nil || report.Backup.NewestRun.Name != "hello-backup-2" {
		t.Fatalf("newest run %+v", report.Backup.NewestRun)
	}
}

// A suspended job is suspended before its history matters: the next run will not happen, whatever the last
// one did.
func TestBackupIsSuspended(t *testing.T) {
	evidence := protection(t, 26*time.Hour,
		cronJob(t, true, "2026-10-26T03:35:00Z", "2026-10-26T03:36:12Z"),
		backupPod(t, "hello-backup-1-abc", "2026-10-26T03:35:00Z", "hello-backup-1"), succeededRun(t))
	report := explainBackup(t, backed(target()), evidence)
	if report.Backup.State != observe.BackupSuspended || report.Backup.Reason != observe.ReasonSuspended {
		t.Fatalf("backup %+v", report.Backup)
	}
}

// No run at all, a run that never finished, and a run whose pod names no Job are three different answers:
// none is current, and the reason says which.
func TestBackupWithoutASuccessfulRun(t *testing.T) {
	noRuns := explainBackup(t, backed(target()), protection(t, 26*time.Hour, cronJob(t, false, "", ""), nil, nil))
	if noRuns.Backup.State != observe.BackupStale || noRuns.Backup.Reason != observe.ReasonNoRuns {
		t.Fatalf("no runs: %+v", noRuns.Backup)
	}
	unfinished := explainBackup(t, backed(target()),
		protection(t, 26*time.Hour, cronJob(t, false, "2026-10-26T09:00:00Z", ""),
			backupPod(t, "hello-backup-2-def", "2026-10-26T09:00:00Z", "hello-backup-2"),
			job(t, "hello-backup-2", "2026-10-26T09:00:00Z", "", observe.RunRunning)))
	if unfinished.Backup.State != observe.BackupStale || unfinished.Backup.Reason != observe.ReasonNoSuccess {
		t.Fatalf("an unfinished run: %+v", unfinished.Backup)
	}
	if unfinished.Backup.NewestRun == nil || unfinished.Backup.NewestRun.Outcome != observe.RunRunning {
		t.Fatalf("newest run %+v", unfinished.Backup.NewestRun)
	}
	orphan := explainBackup(t, backed(target()),
		protection(t, 26*time.Hour, cronJob(t, false, "2026-10-26T09:00:00Z", ""),
			backupPod(t, "hello-backup-2-def", "2026-10-26T09:00:00Z", ""), nil))
	if orphan.Backup.State != observe.BackupNotAssessed || orphan.Backup.Reason != observe.ReasonNoOwner {
		t.Fatalf("a pod without a Job: %+v", orphan.Backup)
	}
}

// Absent, unavailable and not read are three different answers: only a successful 404 is absence, and a
// failed read never becomes one.
func TestBackupSeparatesAbsentFromUnavailable(t *testing.T) {
	evidence := protection(t, 26*time.Hour, cronJob(t, false, "", ""), nil, nil)
	evidence.Backup.CronJob = absent(backed(target()).Backup.CronJob)
	if report := explainBackup(t, backed(target()), evidence); report.Backup.State != observe.BackupAbsent ||
		report.Backup.Reason != observe.ReasonNotFound {
		t.Fatalf("absent: %+v", report.Backup)
	}
	evidence.Backup.CronJob = unavailable(backed(target()).Backup.CronJob, observe.ReasonForbidden)
	if report := explainBackup(t, backed(target()), evidence); report.Backup.State != observe.BackupUnavailable ||
		report.Backup.Reason != observe.ReasonForbidden {
		t.Fatalf("unavailable: %+v", report.Backup)
	}
	evidence.Backup.CronJob = present(backed(target()).Backup.CronJob, cronJob(t, false, "", ""))
	evidence.Backup.Pods.Status, evidence.Backup.Pods.Reason = observe.Unavailable, observe.ReasonTimedOut
	if report := explainBackup(t, backed(target()), evidence); report.Backup.State != observe.BackupUnavailable ||
		report.Backup.Reason != observe.ReasonTimedOut {
		t.Fatalf("pods unavailable: %+v", report.Backup)
	}
	evidence = protection(t, 26*time.Hour, cronJob(t, false, "", ""),
		backupPod(t, "hello-backup-1-abc", "2026-10-26T03:35:00Z", "hello-backup-1"), nil)
	evidence.Backup.Run = &observe.Read{Ref: observe.Ref{APIVersion: "batch/v1", Kind: "Job",
		Namespace: "hello-staging", Name: "hello-backup-1"}, Status: observe.Unavailable, Reason: observe.ReasonStatus}
	if report := explainBackup(t, backed(target()), evidence); report.Backup.State != observe.BackupUnavailable ||
		report.Backup.Reason != observe.ReasonStatus {
		t.Fatalf("run unavailable: %+v", report.Backup)
	}
	if report := explainBackup(t, backed(target()), healthy(t)); report.Backup.State != observe.BackupNotAssessed ||
		report.Backup.Reason != observe.NotRead {
		t.Fatalf("not read: %+v", report.Backup)
	}
}

// An instance that renders no backup says so, and a caller that supplies backup evidence for it contradicts
// the target: one answer would otherwise describe two questions.
func TestBackupNotApplicable(t *testing.T) {
	report := explainBackup(t, target(), healthy(t))
	if report.Backup.State != observe.BackupNotApplicable {
		t.Fatalf("an instance without a backup: %+v", report.Backup)
	}
	evidence := healthy(t)
	evidence.Backup = &observe.BackupEvidence{MaxAge: 26 * time.Hour,
		CronJob: present(backed(target()).Backup.CronJob, cronJob(t, false, "", ""))}
	if _, err := observe.Explain(target(), evidence); err == nil ||
		err.Error() != "invalid: evidence: unexpected_backup" {
		t.Fatalf("evidence the target does not name: %v", err)
	}
}

// A time this layer cannot parse, and a threshold it cannot use, are refusals: the report would otherwise
// claim an age it did not establish.
func TestBackupRefusesWhatItCannotJudge(t *testing.T) {
	evidence := protection(t, 26*time.Hour, cronJob(t, false, "03:35", ""), nil, nil)
	if _, err := observe.Explain(backed(target()), evidence); err == nil ||
		err.Error() != "invalid: backup: malformed" {
		t.Fatalf("an unreadable schedule time: %v", err)
	}
	evidence = protection(t, 26*time.Hour, cronJob(t, false, "", ""), nil, nil)
	evidence.ObservedAt = "this morning"
	if _, err := observe.Explain(backed(target()), evidence); err == nil ||
		err.Error() != "invalid: observed_at: malformed" {
		t.Fatalf("an unreadable observation time: %v", err)
	}
	if _, err := observe.Explain(backed(target()), protection(t, 0, cronJob(t, false, "", ""), nil, nil)); err == nil ||
		err.Error() != "invalid: backup: max_age_required" {
		t.Fatalf("no threshold: %v", err)
	}
}

// The target reads the protection from the compiled CronJob: the job, the pods its runs create (selected by
// the labels the pod template states), and the schedule. A document that is not that CronJob is refused.
func TestBackupTargetOfReadsTheCompiledCronJob(t *testing.T) {
	protected, err := observe.BackupTargetOf([]byte(`{"apiVersion":"batch/v1","kind":"CronJob",
		"metadata":{"name":"hello-backup","namespace":"hello-staging"},
		"spec":{"schedule":"35 3 * * *","jobTemplate":{"spec":{"template":{"metadata":{"labels":{
			"platform.swhurl.com/backup":"true","app":"hello"}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := observe.BackupTarget{
		CronJob: observe.Ref{APIVersion: "batch/v1", Kind: "CronJob", Namespace: "hello-staging", Name: "hello-backup"},
		Pods:    observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: "hello-staging"},
		// The run's own labels, without the instance's restore pods.
		Selector: "app=hello,platform.swhurl.com/backup=true,!platform.swhurl.com/restore",
		Schedule: "35 3 * * *",
	}
	if protected != want {
		t.Fatalf("backup target %+v", protected)
	}
	for _, tt := range []struct{ name, document, want string }{
		{"not a cronjob", `{"apiVersion":"apps/v1","kind":"Deployment"}`, "invalid: backup: expected_a_cronjob"},
		{"no selector", `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":"hello-backup"},
			"spec":{"schedule":"35 3 * * *","jobTemplate":{"spec":{"template":{"metadata":{}}}}}}`,
			"invalid: backup: expected_a_selector"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := observe.BackupTargetOf([]byte(tt.document)); err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// The newest run is the owner Job of the newest pod, and a pod that names no Job names no run.
func TestNewestRunComesFromTheNewestPod(t *testing.T) {
	items := [][]byte{
		backupPod(t, "hello-backup-1-abc", "2026-10-26T03:35:00Z", "hello-backup-1"),
		backupPod(t, "hello-backup-2-def", "2026-10-26T09:00:00Z", "hello-backup-2"),
	}
	if name, found, err := observe.NewestRun(items); err != nil || !found || name != "hello-backup-2" {
		t.Fatalf("newest run = %q %v %v", name, found, err)
	}
	if name, found, err := observe.NewestRun([][]byte{
		backupPod(t, "hello-backup-2-def", "2026-10-26T09:00:00Z", ""),
	}); err != nil || found || name != "" {
		t.Fatalf("a pod without an owner = %q %v %v", name, found, err)
	}
	if name, found, err := observe.NewestRun(nil); err != nil || found || name != "" {
		t.Fatalf("no pods = %q %v %v", name, found, err)
	}
	if _, _, err := observe.NewestRun([][]byte{
		backupPod(t, "hello-backup-1-abc", "yesterday", "hello-backup-1"),
	}); err == nil || err.Error() != "invalid: backup: malformed" {
		t.Fatalf("an unreadable creation time: %v", err)
	}
}

// The backup's own references are validated like every other role: in the instance's namespace, and never the
// same object twice.
func TestBackupTargetIsValidated(t *testing.T) {
	elsewhere := backed(target())
	elsewhere.Backup.CronJob.Namespace = "flux-system"
	if _, err := observe.Explain(elsewhere, healthy(t)); err == nil ||
		err.Error() != "invalid: backup: namespace_mismatch" {
		t.Fatalf("another namespace: %v", err)
	}
	repeated := backed(target())
	repeated.Backup.CronJob = repeated.Release
	if _, err := observe.Explain(repeated, healthy(t)); err == nil ||
		err.Error() != "invalid: target: repeated_ref" {
		t.Fatalf("two roles on one object: %v", err)
	}
	pods := backed(target())
	pods.Backup.Pods.Namespace = "flux-system"
	if _, err := observe.Explain(pods, healthy(t)); err == nil ||
		err.Error() != "invalid: backup.pods: namespace_mismatch" {
		t.Fatalf("pods outside the namespace: %v", err)
	}
}
