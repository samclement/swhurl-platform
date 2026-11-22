package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/apps"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
	"github.com/samclement/swhurl-platform/v2/recovery"
	"github.com/samclement/swhurl-platform/v2/recovery/rules"
)

// ageIdentityPattern is an age X25519 identity: the one credential the restore pod decrypts with. A file that
// is not one is refused before anything is stopped.
var ageIdentityPattern = regexp.MustCompile(`^AGE-SECRET-KEY-1[0-9A-Z]{58}$`)

// restoreNote is what a plan document says about itself; restorePerformedNote what a performed one does.
const (
	restoreNote          = "a plan and a check; nothing is restored, stopped or changed by this command"
	restorePerformedNote = "a restore performed by v2: the app was stopped, its database replaced from the archive, and the app put back"
	// defaultAgeKey is where the operator's identity lives, as the plan's own environment states it.
	defaultAgeKey = "./age.agekey"
	// defaultURLTTL is how long the pod's read URL stays valid: longer than the Job's deadline, and no
	// longer than it needs to be.
	defaultURLTTL = time.Hour
	// minURLTTL is the shortest expiry v2 accepts: the pods-gone bound and the Job's own deadline, so a URL
	// cannot expire while the pod that needs it is still allowed to run.
	minURLTTL = 20 * time.Minute
	// restoreTimeout bounds one run's requests as a whole: the waits inside it are the use case's.
	restoreTimeout = 25 * time.Minute
)

// restore writes a v2-restore plan for one instance, or performs the restore when the operator confirms it.
// Without --confirm nothing is stopped or changed; with it, the use case stops the app, restores the archive
// and puts the app back, and this command reports what it did.
func (c cli) restore(r request) int {
	inputs, status := c.inputs(r)
	if status != 0 {
		return status
	}
	maxAge, status := c.maxAge(r)
	if status != 0 {
		return status
	}
	prepared, status := c.prepareRestore(r, inputs, maxAge)
	if status != 0 {
		return status
	}
	spec, status := c.instance(r.instance)
	if status != 0 {
		return status
	}
	release, status := c.release(r.release)
	if status != 0 {
		return status
	}
	if r.confirm == "" {
		document, err := encodeRestore(spec, release, prepared, nil, r.maxAge)
		if err != nil {
			return fail(c.diagnostic, err)
		}
		fmt.Fprintln(c.diagnostic, restoreSummary(prepared.Plan))
		return emit(c.out, c.diagnostic, document)
	}
	return c.performRestore(r, spec, release, prepared)
}

// prepareRestore asks recovery for what a restore is decided from: the observation is apps' status over the
// connection status takes, and the archives are the instance's own prefix read with the operator's credentials.
func (c cli) prepareRestore(r request, inputs provisioningrules.Inputs, maxAge time.Duration) (recovery.Prepared, int) {
	request, status := c.liveStatusInputs(r, inputs.Profile, maxAge)
	if status != 0 {
		return recovery.Prepared{}, status
	}
	request.Inputs = inputs
	// The observation keeps a read's own bound; the plan's reads have the rest of the two.
	ctx, cancel := context.WithTimeout(context.Background(), 2*readTimeout)
	defer cancel()
	prepared, err := recovery.Prepare(ctx, recovery.PrepareInputs{
		Observe: func(ctx context.Context) (observe.Report, error) {
			ctx, cancel := context.WithTimeout(ctx, readTimeout)
			defer cancel()
			return apps.Status(ctx, request)
		},
		Archives: func(namespace string) (s3.Reader, rules.ArchiveQuery, error) {
			query, err := archiveQueryOf(inputs.Profile, namespace)
			if err != nil {
				return nil, rules.ArchiveQuery{}, err
			}
			reader, err := c.archives(query.Bucket, r.region)
			return reader, query, err
		},
		From: r.from, MaxAge: maxAge, Now: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return recovery.Prepared{}, fail(c.diagnostic, err)
	}
	return prepared, 0
}

// performRestore is the half that changes the cluster: recovery admits the request, this command reads the two
// credentials, the use case runs, and this command reports what it did. A refusal changed nothing and is an exit
// 2; a failure part-way is written as the document and is an exit 1.
func (c cli) performRestore(r request, spec kernel.InstanceSpec, release kernel.ReleaseBundle,
	prepared recovery.Prepared) int {
	if err := prepared.Confirm(spec, r.confirm); err != nil {
		return fail(c.diagnostic, err)
	}
	identity, status := c.ageIdentity(r)
	if status != 0 {
		return status
	}
	readURL, status := c.presignedRead(r, prepared.Report.Target, prepared.Plan)
	if status != 0 {
		return status
	}
	cluster, status := c.restoreCluster(r)
	if status != 0 {
		return status
	}
	inputs, status := c.restoreInputs(r, prepared, identity, readURL, cluster)
	if status != 0 {
		return status
	}
	// An interrupt stops the restore the same way a failure does: the use case unwinds on a context of its own,
	// so the app is put back rather than left stopped. A SIGTERM is deliberately not caught here — the console catches
	// one to drain its jobs, and a restore must leave the state any crash would.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()
	outcome, err := recovery.Restore(ctx, inputs)
	if err != nil {
		var refusal *recovery.Refusal
		// A refusal, or a request that never took a step, changed nothing: it is reported as such, with no
		// document claiming a restore was attempted.
		if errors.As(err, &refusal) || len(outcome.Steps) == 0 {
			return fail(c.diagnostic, err)
		}
		return c.reportRestore(spec, release, prepared, &outcome, 1)
	}
	return c.reportRestore(spec, release, prepared, &outcome, 0)
}

// reportRestore writes the document and the one human line, and returns the status.
func (c cli) reportRestore(spec kernel.InstanceSpec, release kernel.ReleaseBundle, prepared recovery.Prepared,
	outcome *recovery.RestoreOutcome, status int) int {
	document, err := encodeRestore(spec, release, prepared, outcome, "")
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, restoreActionSummary(*outcome))
	return max(emit(c.out, c.diagnostic, document), status)
}

// restoreInputs is what the use case takes: the three documents, the plan, the two credentials, the target
// and the cluster.
func (c cli) restoreInputs(r request, prepared recovery.Prepared, identity, readURL string,
	cluster recovery.Cluster) (recovery.RestoreInputs, int) {
	instance, status := c.read(r.instance, "instance")
	if status != 0 {
		return recovery.RestoreInputs{}, status
	}
	release, status := c.read(r.release, "release")
	if status != 0 {
		return recovery.RestoreInputs{}, status
	}
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return recovery.RestoreInputs{}, status
	}
	var egress []byte
	if r.egressFile != "" {
		egress, status = c.read(r.egressFile, "egress")
		if status != 0 {
			return recovery.RestoreInputs{}, status
		}
	}
	return recovery.RestoreInputs{Instance: instance, Release: release, Profile: profile, Egress: egress,
		Plan: prepared.Plan, Target: prepared.Report.Target, AgeIdentity: identity, ReadURL: readURL, Cluster: cluster,
		Sleep: time.Sleep, Now: time.Now}, 0
}

// ageIdentity is the operator's age identity: the one credential the restore pod decrypts with. It is read
// from the file the operator names and never printed.
func (c cli) ageIdentity(r request) (string, int) {
	path := r.ageKey
	if path == "" {
		path = defaultAgeKey
	}
	data, status := c.read(path, "age-key-file")
	if status != 0 {
		return "", status
	}
	identity, ok := ageIdentityOf(string(data))
	if !ok {
		return "", fail(c.diagnostic, errors.New("invalid: age-key-file: expected_an_age_identity"))
	}
	return identity, 0
}

// ageIdentityOf is the one identity in an age key file. It accepts the file age-keygen writes — comment lines,
// then the key — and refuses a file with none or with more than one.
func ageIdentityOf(file string) (string, bool) {
	identity := ""
	for _, line := range strings.Split(file, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !ageIdentityPattern.MatchString(line) || identity != "" {
			return "", false
		}
		identity = line
	}
	return identity, identity != ""
}

// restoreCluster is the API server the restore changes and reads, from the same kubeconfig a live
// observation takes.
func (c cli) restoreCluster(r request) (recovery.Cluster, int) {
	data, status := c.read(r.kubeconfig, "kubeconfig")
	if status != 0 {
		return recovery.Cluster{}, status
	}
	connection, err := kube.Connect(data)
	if err != nil {
		return recovery.Cluster{}, fail(c.diagnostic, err)
	}
	reader, err := connection.Reader()
	if err != nil {
		return recovery.Cluster{}, fail(c.diagnostic, err)
	}
	writer, err := connection.Writer()
	if err != nil {
		return recovery.Cluster{}, fail(c.diagnostic, err)
	}
	return recovery.Cluster{Reader: reader, Writer: writer}, 0
}

// presignedRead signs one read of the chosen archive, so the pod holds a URL for that object instead of the
// bucket credential. The credentials are the operator's own environment, as they are for an observation, and
// the expiry is bounded by SigV4's own maximum.
func (c cli) presignedRead(r request, target observe.Target, plan rules.RestorePlan) (string, int) {
	reader, query, status := c.archiveOf(r, target)
	if status != 0 {
		return "", status
	}
	ttl := defaultURLTTL
	if r.urlTTLSet {
		ttl = r.urlTTL
	}
	if ttl < minURLTTL {
		return "", fail(c.diagnostic, errors.New("invalid: url-ttl: shorter_than_the_restore_window"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	address, err := reader.PresignURL(ctx, query, plan.Archive.Newest, ttl)
	if err != nil {
		return "", fail(c.diagnostic, err)
	}
	return address, 0
}

// archiveOf is the instance's own prefix and the reader the operator's credentials make.
func (c cli) archiveOf(r request, target observe.Target) (s3.Reader, rules.ArchiveQuery, int) {
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return nil, rules.ArchiveQuery{}, status
	}
	query, err := archiveQueryOf(profile, target.Namespace)
	if err != nil {
		return nil, rules.ArchiveQuery{}, fail(c.diagnostic, err)
	}
	reader, err := c.archives(query.Bucket, r.region)
	if err != nil {
		return nil, rules.ArchiveQuery{}, fail(c.diagnostic, err)
	}
	return reader, query, 0
}

// restoreDocument is the plan's shape: the instance's facts, the archive and its metadata, the observation it
// was decided from, what the command did, and what the operator checks afterwards.
type restoreDocument struct {
	Kind         string                  `json:"kind"`
	Version      int                     `json:"version"`
	GeneratedAt  string                  `json:"generatedAt"`
	Instance     restoreInstanceDocument `json:"instance"`
	Plan         restorePlanDocument     `json:"plan"`
	Observation  restoreObserved         `json:"observation"`
	Action       restoreActionDocument   `json:"action"`
	Steps        []restoreStep           `json:"steps,omitempty"`
	Verification []string                `json:"verification"`
	NotCovered   []string                `json:"notCovered"`
	Note         string                  `json:"note"`
}

type restoreInstanceDocument struct {
	Application  string `json:"application"`
	Environment  string `json:"environment"`
	Namespace    string `json:"namespace"`
	Claim        string `json:"claim"`
	Mount        string `json:"mount"`
	DatabasePath string `json:"databasePath"`
	UID          int32  `json:"uid"`
}

type restorePlanDocument struct {
	State         string                  `json:"state"`
	Reason        string                  `json:"reason,omitempty"`
	Archive       *restoreArchiveDocument `json:"archive,omitempty"`
	Metadata      *restoreMetadata        `json:"metadata,omitempty"`
	MaxAgeSeconds int64                   `json:"maxAgeSeconds"`
}

type restoreArchiveDocument struct {
	Key          string `json:"key"`
	LastModified string `json:"lastModified"`
	AgeSeconds   int64  `json:"ageSeconds"`
}

type restoreMetadata struct {
	SHA256      string `json:"sha256,omitempty"`
	PlainSHA256 string `json:"plainSha256,omitempty"`
	Tables      int32  `json:"tables"`
	Source      string `json:"source,omitempty"`
}

// restoreObserved is what the observation said, so the plan's reader can see the app's state before it is
// stopped: a restore replaces the database under a running app, and the operator decides when.
type restoreObserved struct {
	Verdict string `json:"verdict"`
	Backup  string `json:"backup"`
	Archive string `json:"archive"`
}

// restoreActionDocument is what the command did: the run's state, every step in the order it ran, and the two
// facts kept apart — the Job's answer and the app's own state.
type restoreActionDocument struct {
	State      string              `json:"state"`
	Reason     string              `json:"reason,omitempty"`
	StartedAt  string              `json:"startedAt,omitempty"`
	FinishedAt string              `json:"finishedAt,omitempty"`
	Steps      []restoreActionStep `json:"steps"`
	Job        *restoreJobEvidence `json:"job,omitempty"`
	App        *restoreAppDocument `json:"app,omitempty"`
	// BackupSuspended is true only when the run left the backup schedule suspended: a restore pod may still
	// hold the claim, so the undo stopped before putting the schedule back.
	BackupSuspended bool `json:"backupSuspended,omitempty"`
}

type restoreActionStep struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

type restoreJobEvidence struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Log       string `json:"log,omitempty"`
	LogReason string `json:"logReason,omitempty"`
}

type restoreAppDocument struct {
	Replicas  int32  `json:"replicas"`
	Suspended bool   `json:"suspended"`
	Available bool   `json:"available"`
	State     string `json:"state"`
}

// restoreStep is the operator command that performs the planned restore; it is only in a plan, never in a run
// v2 performed.
type restoreStep struct {
	Command     string   `json:"command"`
	Environment []string `json:"environment"`
}

// encodeRestore is the document: the plan, and the run when there was one (nil for a plan-only invocation).
// maxAge is the --max-age the operator stated, empty for the default, which the plan's step repeats.
func encodeRestore(spec kernel.InstanceSpec, release kernel.ReleaseBundle, prepared recovery.Prepared,
	outcome *recovery.RestoreOutcome, maxAge string) ([]byte, error) {
	observed, plan := prepared.Report, prepared.Plan
	claim := spec.Storage.ClaimName
	if claim == "" {
		// An instance that creates its own volume has no claim name in its record: the pinned chart names
		// the generated claim after the release, which its own render test ties.
		claim = spec.Application
	}
	document := restoreDocument{
		Kind: "v2-restore", Version: 2, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Instance: restoreInstanceDocument{Application: spec.Application, Environment: spec.Environment,
			Namespace: observed.Target.Namespace, Claim: claim, Mount: kernel.DataMount,
			DatabasePath: kernel.DatabasePath, UID: release.Runtime.UID},
		Plan: restorePlanDocument{State: plan.State, Reason: plan.Reason,
			MaxAgeSeconds: int64(plan.Archive.MaxAge / time.Second)},
		Observation: restoreObserved{Verdict: string(observed.Verdict), Backup: observed.Backup.State,
			Archive: observed.Backup.Archive.State},
		Action: restorePlannedAction(),
		Note:   restoreNote,
		Verification: []string{
			"observe again: verdict running, backup current, archive current",
			"the restore prints the restored table count and integrity check; it must match the metadata",
			"the live database is replaced; before-restore-<UTC>/ beside it holds what was there",
		},
		NotCovered: []string{"the restore itself", "the data being the point in time you want"},
	}
	if plan.Archive.Newest != "" {
		document.Plan.Archive = &restoreArchiveDocument{Key: plan.Archive.Newest,
			LastModified: plan.Archive.LastModified, AgeSeconds: int64(plan.Archive.Age / time.Second)}
	}
	if plan.Metadata.Source != "" || plan.Metadata.SHA256 != "" || plan.Metadata.Tables > 0 {
		document.Plan.Metadata = &restoreMetadata{SHA256: plan.Metadata.SHA256,
			PlainSHA256: plan.Metadata.PlainSHA256, Tables: plan.Metadata.Tables, Source: plan.Metadata.Source}
	}
	if outcome != nil {
		document.Action, document.Note = restorePerformedAction(*outcome), restorePerformedNote
		document.NotCovered = []string{"the data being the point in time you want",
			"the app's own writes while it was stopped"}
		document.Verification = []string{
			"observe again: verdict running, backup current, archive current",
			"the log names before-restore-<UTC>/ beside the database; delete it once you are happy",
		}
	} else {
		document.Steps = []restoreStep{{Command: restoreCommand(spec, plan.Archive.Newest, maxAge),
			Environment: []string{"AGE_KEY_FILE=" + defaultAgeKey}}}
	}
	return marshalDocument(document)
}

// restorePlannedAction is the action block of a document the command only planned.
func restorePlannedAction() restoreActionDocument {
	return restoreActionDocument{State: "not_attempted", Steps: []restoreActionStep{}}
}

// restorePerformedAction is the action block of a restore this command performed, step by step.
func restorePerformedAction(outcome recovery.RestoreOutcome) restoreActionDocument {
	action := restoreActionDocument{State: outcome.State, Reason: outcome.Reason,
		StartedAt:  outcome.Started.UTC().Format(time.RFC3339),
		FinishedAt: outcome.Finished.UTC().Format(time.RFC3339),
		Steps:      make([]restoreActionStep, 0, len(outcome.Steps))}
	for _, step := range outcome.Steps {
		action.Steps = append(action.Steps, restoreActionStep{Name: step.Name, Outcome: step.Outcome,
			Detail: step.Detail})
	}
	action.BackupSuspended = outcome.BackupSuspended
	action.Job = &restoreJobEvidence{Name: outcome.Job.Name, State: outcome.Job.State,
		Log: outcome.Job.Log, LogReason: outcome.Job.LogReason}
	action.App = &restoreAppDocument{Replicas: outcome.App.Replicas, Suspended: outcome.App.Suspended,
		Available: outcome.App.Available, State: outcome.App.State}
	return action
}

// restoreCommand is the make target that performs the restore (this command with --confirm), with its instance
// and the archive the plan chose, so performing it restores what was planned even after a newer backup; the
// stated --max-age is repeated, since the archive is judged again when the restore is performed.
func restoreCommand(spec kernel.InstanceSpec, archive, maxAge string) string {
	command := fmt.Sprintf("make restore-sqlite APP=%s ENV=%s CONFIRM=%s/%s",
		spec.Application, spec.Environment, spec.Application, spec.Environment)
	if archive != "" {
		command += " FROM=" + archive
	}
	if maxAge != "" {
		command += " MAX_AGE=" + maxAge
	}
	return command
}

// marshalDocument is the document's bytes: indented, no HTML escaping, one trailing newline trimmed.
func marshalDocument(document any) ([]byte, error) {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(out.String(), "\n")), nil
}

// restoreSummary is the one human line for stderr: whether the archive can be restored, and from which.
func restoreSummary(plan rules.RestorePlan) string {
	summary := "restore: " + plan.State
	if plan.Reason != "" {
		summary += "/" + plan.Reason
	}
	if plan.Archive.Newest != "" {
		summary += fmt.Sprintf(" from %s (%s old)", plan.Archive.Newest, plan.Archive.Age.Round(time.Minute))
	}
	return summary
}

// restoreActionSummary is the one line for a restore that ran: what it did, and the app's own state.
func restoreActionSummary(outcome recovery.RestoreOutcome) string {
	summary := "restore: " + outcome.State
	if outcome.Reason != "" {
		summary += "/" + outcome.Reason
	}
	if outcome.App.State != "" {
		summary += "; app " + outcome.App.State
		if outcome.App.Suspended {
			summary += ", release still suspended"
		}
	}
	if outcome.BackupSuspended {
		summary += ", backup schedule still suspended"
	}
	return summary
}
