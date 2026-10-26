package observe

import (
	"time"
)

// An instance's protection has its own answer, beside the running verdict and never inside it: an instance
// can run while its backups are stale, and its job can be current for an instance that is not running.
const (
	BackupCurrent       = "current"
	BackupStale         = "stale"
	BackupFailing       = "failing"
	BackupSuspended     = "suspended"
	BackupAbsent        = "absent"
	BackupUnavailable   = "unavailable"
	BackupNotAssessed   = "not_assessed"
	BackupNotApplicable = "not_applicable"
)

// The backup reasons. A positive state carries none.
const (
	ReasonNoRuns       = "no_runs"
	ReasonNoSuccess    = "no_successful_run"
	ReasonTooOld       = "last_success_too_old"
	ReasonRunFailed    = "last_run_failed"
	ReasonRunsDown     = "runs_unavailable"
	ReasonNoOwner      = "no_owner_job"
	ReasonMaxAge       = "max_age_required"
	ReasonUnexpected   = "unexpected_backup"
	ReasonObservedTime = "observed_at"
)

// A run's own verdict, as the Job's conditions state it.
const (
	RunComplete = "complete"
	RunFailed   = "failed"
	RunRunning  = "running"
	RunUnknown  = "unknown"
)

// BackupTarget is one database instance's protection as its compiled documents state it: the CronJob, the
// pods its runs create (selected by the labels the job template's pods carry), and its schedule.
type BackupTarget struct {
	CronJob  Ref
	Pods     Ref
	Selector string
	Schedule string
}

// BackupEvidence is what the source answered about that protection, and the age the caller calls too old.
// A run's Job is read by the name its own pod's owner reference gives, because a Job carries no label the
// CronJob's selector can find. The threshold is supplied like every other fact: this layer reads no clock.
type BackupEvidence struct {
	CronJob Read
	Pods    List
	Run     *Read
	// Archive is the object store's listing for the instance's prefix, or the read the caller did not make.
	Archive *ArchiveList
	MaxAge  time.Duration
}

// BackupReport is the protection's own answer: whether the job runs, whether its last run finished, and how
// old the newest successful one is.
type BackupReport struct {
	State           string
	Reason          string
	Schedule        string
	LastScheduledAt string
	LastSucceededAt string
	MaxAge          time.Duration
	NewestRun       *RunReport
	// Archive is the object store's own answer, beside the job's and never folded into it.
	Archive ArchiveReport
}

// RunReport is the newest run's own facts. Outcome is the Job's verdict, not this layer's: a run that
// failed and was retried into completion is complete.
type RunReport struct {
	Name        string
	CreatedAt   string
	CompletedAt string
	Outcome     string
}

// NewestRun is the run the job's pods show: the owner Job of the newest pod, which the caller then reads by
// name. It reports no name when there is no run yet, and refuses a pod it cannot order.
func NewestRun(items [][]byte) (string, bool, error) {
	newest, found := "", false
	var at time.Time
	for _, item := range items {
		var pod struct {
			Metadata struct {
				CreationTimestamp string `json:"creationTimestamp"`
				OwnerReferences   []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
		}
		if err := decodeDocument(item, &pod, "backup"); err != nil {
			return "", false, err
		}
		created, err := timestamp(pod.Metadata.CreationTimestamp, "backup")
		if err != nil {
			return "", false, err
		}
		if found && !created.After(at) {
			continue
		}
		for _, owner := range pod.Metadata.OwnerReferences {
			if owner.Kind != "Job" || owner.Name == "" {
				continue
			}
			at, newest, found = created, owner.Name, true
			break
		}
	}
	return newest, found, nil
}

// backupOf decides the protection's state from the evidence. The order is fixed, so the same evidence
// always gives the same answer: a failed read is unavailable before anything is read from it, a suspended
// job is suspended before its history matters, and a failed newest run outranks an older success — the
// newest data is not what was backed up.
func backupOf(target Target, evidence Evidence) (BackupReport, error) {
	if target.Backup == nil {
		if evidence.Backup != nil {
			return BackupReport{}, invalid("evidence", ReasonUnexpected)
		}
		return BackupReport{State: BackupNotApplicable, Archive: ArchiveReport{State: BackupNotApplicable}}, nil
	}
	if evidence.Backup == nil {
		return BackupReport{State: BackupNotAssessed, Reason: NotRead, Schedule: target.Backup.Schedule,
			Archive: ArchiveReport{State: BackupNotAssessed, Reason: NotRead}}, nil
	}
	report := BackupReport{Schedule: target.Backup.Schedule, MaxAge: evidence.Backup.MaxAge}
	if report.MaxAge <= 0 {
		return BackupReport{}, invalid("backup", ReasonMaxAge)
	}
	now, err := timestamp(evidence.ObservedAt, ReasonObservedTime)
	if err != nil {
		return BackupReport{}, err
	}
	if report.Archive, err = archiveOf(target, evidence, now); err != nil {
		return BackupReport{}, err
	}
	state, reason, err := cronJobState(evidence.Backup.CronJob, &report)
	if err != nil {
		return BackupReport{}, err
	}
	if state != "" {
		report.State, report.Reason = state, reason
		return report, nil
	}
	report.State, report.Reason, err = runState(evidence.Backup, &report, now)
	return report, err
}

// cronJobState reads the CronJob's own answer: absent, unavailable, suspended, or nothing yet. It fills the
// report's schedule facts and returns a state only when the answer is already decided.
func cronJobState(read Read, report *BackupReport) (string, string, error) {
	switch read.Status {
	case Present:
	case Absent:
		return BackupAbsent, ReasonNotFound, nil
	case Unavailable:
		return BackupUnavailable, read.Reason, nil
	default:
		return BackupNotAssessed, NotRead, nil
	}
	var document struct {
		Spec struct {
			Suspend bool `json:"suspend"`
		} `json:"spec"`
		Status struct {
			LastScheduleTime   string `json:"lastScheduleTime"`
			LastSuccessfulTime string `json:"lastSuccessfulTime"`
		} `json:"status"`
	}
	if err := decodeDocument(read.Object, &document, "backup"); err != nil {
		return "", "", err
	}
	for _, stated := range []struct {
		value string
		into  *string
	}{{document.Status.LastScheduleTime, &report.LastScheduledAt},
		{document.Status.LastSuccessfulTime, &report.LastSucceededAt}} {
		if stated.value == "" {
			continue
		}
		if _, err := timestamp(stated.value, "backup"); err != nil {
			return "", "", err
		}
		*stated.into = stated.value
	}
	if document.Spec.Suspend {
		return BackupSuspended, ReasonSuspended, nil
	}
	return "", "", nil
}

// runState reads the runs and decides the state from them: the newest run's verdict, then the age of the
// newest successful one against the caller's threshold.
func runState(backup *BackupEvidence, report *BackupReport, now time.Time) (string, string, error) {
	switch backup.Pods.Status {
	case Present:
	case Unavailable:
		return BackupUnavailable, reasonOf(backup.Pods.Reason), nil
	default:
		return BackupNotAssessed, NotRead, nil
	}
	if len(backup.Pods.Items) == 0 {
		return BackupStale, ReasonNoRuns, nil
	}
	if backup.Run == nil {
		return BackupNotAssessed, ReasonNoOwner, nil
	}
	switch backup.Run.Status {
	case Present:
	case Absent:
		// The pod outlived its Job, so the run's own verdict is gone: the schedule facts still decide the
		// age, but nothing is claimed about the last run.
		return successState(report, now)
	case Unavailable:
		return BackupUnavailable, reasonOf(backup.Run.Reason), nil
	default:
		return BackupNotAssessed, NotRead, nil
	}
	run, err := runOf(backup.Run.Object)
	if err != nil {
		return "", "", err
	}
	report.NewestRun = run.report()
	if run.outcome() == RunFailed {
		return BackupFailing, ReasonRunFailed, nil
	}
	return successState(report, now)
}

// successState judges how old the newest successful run is. A run that never finished is neither current
// nor old: it is a job that has not succeeded yet.
func successState(report *BackupReport, now time.Time) (string, string, error) {
	succeeded := report.LastSucceededAt
	if succeeded == "" && report.NewestRun != nil && report.NewestRun.Outcome == RunComplete {
		succeeded = report.NewestRun.CompletedAt
	}
	if succeeded == "" {
		return BackupStale, ReasonNoSuccess, nil
	}
	at, err := timestamp(succeeded, "backup")
	if err != nil {
		return "", "", err
	}
	if now.Sub(at) > report.MaxAge {
		return BackupStale, ReasonTooOld, nil
	}
	return BackupCurrent, "", nil
}

// reasonOf is a read's own reason, or the one this layer states when the read gave none.
func reasonOf(reason string) string {
	if reason == "" {
		return ReasonRunsDown
	}
	return reason
}

// run is the part of one Job the backup answer needs.
type run struct {
	Metadata struct {
		Name              string `json:"name"`
		CreationTimestamp string `json:"creationTimestamp"`
	} `json:"metadata"`
	Status struct {
		CompletionTime string `json:"completionTime"`
		Active         int32  `json:"active"`
		Conditions     []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

// outcome is the Job's own verdict: a terminal condition decides, a run in progress is running, and a Job
// that states neither is not construed as either.
func (r run) outcome() string {
	failed := false
	for _, condition := range r.Status.Conditions {
		if condition.Status != "True" {
			continue
		}
		switch condition.Type {
		case "Complete":
			return RunComplete
		case "Failed":
			failed = true
		}
	}
	switch {
	case failed:
		return RunFailed
	case r.Status.Active > 0:
		return RunRunning
	}
	return RunUnknown
}

func (r run) report() *RunReport {
	return &RunReport{Name: r.Metadata.Name, CreatedAt: r.Metadata.CreationTimestamp,
		CompletedAt: r.Status.CompletionTime, Outcome: r.outcome()}
}

// runOf reads the newest run's Job document.
func runOf(item []byte) (run, error) {
	var parsed run
	if err := decodeDocument(item, &parsed, "backup"); err != nil {
		return run{}, err
	}
	if _, err := timestamp(parsed.Metadata.CreationTimestamp, "backup"); err != nil {
		return run{}, err
	}
	return parsed, nil
}

// timestamp parses one supplied RFC3339 time. The layer formats and compares the times it is given; it never
// reads the clock, so a value it cannot parse is a refusal rather than a guess at what was meant.
func timestamp(value, field string) (time.Time, error) {
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, invalid(field, ReasonMalformed)
	}
	return at, nil
}
