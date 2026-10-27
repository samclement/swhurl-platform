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
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
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
	target, status := c.observationTarget(r)
	if status != 0 {
		return status
	}
	maxAge, status := c.maxAge(r)
	if status != 0 {
		return status
	}
	report, status := c.observeLive(r, target, maxAge)
	if status != 0 {
		return status
	}
	if report.Backup.State == observe.BackupNotApplicable {
		return fail(c.diagnostic, errors.New("unsupported: instance: it keeps no database to restore"))
	}
	spec, status := c.instance(r.instance)
	if status != 0 {
		return status
	}
	release, status := c.release(r.release)
	if status != 0 {
		return status
	}
	plan, status := c.restorePlan(r, target, maxAge)
	if status != 0 {
		return status
	}
	// The plan's own read is the archive's evidence, so the observation block states the same fact the plan
	// was decided from rather than a separate one.
	report.Backup.Archive = plan.Archive
	if r.confirm == "" {
		document, err := encodeRestore(target, spec, release, report, plan, nil)
		if err != nil {
			return fail(c.diagnostic, err)
		}
		fmt.Fprintln(c.diagnostic, restoreSummary(plan))
		return emit(c.out, c.diagnostic, document)
	}
	return c.performRestore(r, target, spec, release, report, plan)
}

// performRestore is the half that changes the cluster: it confirms the request, reads the two credentials,
// runs the use case, and reports what it did. A refusal changed nothing and is an exit 2; a failure part-way
// is written as the document and is an exit 1.
func (c cli) performRestore(r request, target observe.Target, spec kernel.InstanceSpec, release kernel.ReleaseBundle,
	report observe.Report, plan observe.RestorePlan) int {
	if want := spec.Application + "/" + spec.Environment; r.confirm != want {
		return fail(c.diagnostic, fmt.Errorf("invalid: confirm: expected %s", want))
	}
	if !plan.Ready() {
		return fail(c.diagnostic, fmt.Errorf("refused: restore.plan: %s", plan.State))
	}
	identity, status := c.ageIdentity(r)
	if status != 0 {
		return status
	}
	readURL, status := c.presignedRead(r, target, plan)
	if status != 0 {
		return status
	}
	cluster, status := c.restoreCluster(r)
	if status != 0 {
		return status
	}
	inputs, status := c.restoreInputs(r, spec, target, plan, identity, readURL, cluster)
	if status != 0 {
		return status
	}
	// An interrupt stops the restore the same way a failure does: the use case unwinds on a context of its own,
	// so the app is put back rather than left stopped. A SIGTERM is left alone — the v2 module names no process
	// signal beyond the console's own Ctrl-C — and leaves the state any crash would.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()
	outcome, err := orchestration.Restore(ctx, inputs)
	if err != nil {
		var refusal *orchestration.Refusal
		// A refusal, or a request that never took a step, changed nothing: it is reported as such, with no
		// document claiming a restore was attempted.
		if errors.As(err, &refusal) || len(outcome.Steps) == 0 {
			return fail(c.diagnostic, err)
		}
		return c.reportRestore(target, spec, release, report, plan, &outcome, 1)
	}
	return c.reportRestore(target, spec, release, report, plan, &outcome, 0)
}

// reportRestore writes the document and the one human line, and returns the status.
func (c cli) reportRestore(target observe.Target, spec kernel.InstanceSpec, release kernel.ReleaseBundle,
	report observe.Report, plan observe.RestorePlan, outcome *orchestration.RestoreOutcome, status int) int {
	document, err := encodeRestore(target, spec, release, report, plan, outcome)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, restoreActionSummary(*outcome))
	return max(emit(c.out, c.diagnostic, document), status)
}

// restoreInputs is what the use case takes: the three documents, the plan, the two credentials, the target
// and the cluster.
func (c cli) restoreInputs(r request, spec kernel.InstanceSpec, target observe.Target, plan observe.RestorePlan,
	identity, readURL string, cluster orchestration.Cluster) (orchestration.RestoreInputs, int) {
	instance, status := c.read(r.instance, "instance")
	if status != 0 {
		return orchestration.RestoreInputs{}, status
	}
	release, status := c.read(r.release, "release")
	if status != 0 {
		return orchestration.RestoreInputs{}, status
	}
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return orchestration.RestoreInputs{}, status
	}
	var egress []byte
	if r.egressFile != "" {
		egress, status = c.read(r.egressFile, "egress")
		if status != 0 {
			return orchestration.RestoreInputs{}, status
		}
	}
	return orchestration.RestoreInputs{Instance: instance, Release: release, Profile: profile, Egress: egress,
		Plan: plan, Target: target, AgeIdentity: identity, ReadURL: readURL, Cluster: cluster,
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
func (c cli) restoreCluster(r request) (orchestration.Cluster, int) {
	data, status := c.read(r.kubeconfig, "kubeconfig")
	if status != 0 {
		return orchestration.Cluster{}, status
	}
	connection, err := kube.Connect(data)
	if err != nil {
		return orchestration.Cluster{}, fail(c.diagnostic, err)
	}
	reader, err := connection.Reader()
	if err != nil {
		return orchestration.Cluster{}, fail(c.diagnostic, err)
	}
	writer, err := connection.Writer()
	if err != nil {
		return orchestration.Cluster{}, fail(c.diagnostic, err)
	}
	return orchestration.Cluster{Reader: reader, Writer: writer}, 0
}

// presignedRead signs one read of the chosen archive, so the pod holds a URL for that object instead of the
// bucket credential. The credentials are the operator's own environment, as they are for an observation, and
// the expiry is bounded by SigV4's own maximum.
func (c cli) presignedRead(r request, target observe.Target, plan observe.RestorePlan) (string, int) {
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
func (c cli) archiveOf(r request, target observe.Target) (s3.Reader, observe.ArchiveQuery, int) {
	query, status := c.archiveQuery(r, target)
	if status != 0 {
		return nil, observe.ArchiveQuery{}, status
	}
	reader, err := c.archives(query.Bucket, r.region)
	if err != nil {
		return nil, observe.ArchiveQuery{}, fail(c.diagnostic, err)
	}
	return reader, query, 0
}

// restorePlan chooses the archive and reads what the bucket records beside it.
func (c cli) restorePlan(r request, target observe.Target, maxAge time.Duration) (observe.RestorePlan, int) {
	reader, query, status := c.archiveOf(r, target)
	if status != 0 {
		return observe.RestorePlan{}, status
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	listed, err := reader.List(ctx, query)
	if err != nil {
		return observe.RestorePlan{}, fail(c.diagnostic, err)
	}
	archive, err := observe.SelectArchive(listed, query.Prefix, r.from, maxAge, time.Now().UTC())
	if err != nil {
		return observe.RestorePlan{}, fail(c.diagnostic, err)
	}
	plan := observe.RestoreInput{Namespace: target.Namespace, Application: target.Release.Name, Archive: archive}
	if archive.Newest != "" {
		metadata, failure := c.metadata(ctx, reader, query, archive.Newest)
		plan.Metadata, plan.Failure = metadata, failure
	}
	return observe.RestoreOf(plan), 0
}

// metadata reads the record the backup job published beside one archive, and says why it could not when it
// could not: an incomplete upload is not an unreachable bucket.
func (c cli) metadata(ctx context.Context, reader s3.Reader, query observe.ArchiveQuery,
	key string) (*observe.ArchiveMetadata, string) {
	body, err := reader.Get(ctx, query, strings.TrimSuffix(key, ".db.age")+".json")
	switch {
	case err == nil:
		metadata, err := observe.ArchiveMetadataOf(body)
		if err != nil {
			return nil, observe.ReasonMetadataMalformed
		}
		return &metadata, ""
	case notFound(err):
		return nil, observe.ReasonMetadataAbsent
	}
	return nil, observe.ReasonMetadataDown
}

// notFound is whether the object store answered that the key is not there.
func notFound(err error) bool {
	var failure *s3.Error
	return errors.As(err, &failure) && failure.Code == "not_found"
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

// restoreStep is the command that performs a restore in v1; it is only in a plan, never in a run v2 performed.
type restoreStep struct {
	Command     string   `json:"command"`
	Environment []string `json:"environment"`
}

// encodeRestore is the document: the plan, and the run when there was one (nil for a plan-only invocation).
func encodeRestore(target observe.Target, spec kernel.InstanceSpec, release kernel.ReleaseBundle,
	report observe.Report, plan observe.RestorePlan, outcome *orchestration.RestoreOutcome) ([]byte, error) {
	claim := spec.Storage.ClaimName
	if claim == "" {
		// An instance that creates its own volume has no claim name in its record: the pinned chart names
		// the generated claim after the release, which its own render test ties.
		claim = spec.Application
	}
	document := restoreDocument{
		Kind: "v2-restore", Version: 2, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Instance: restoreInstanceDocument{Application: spec.Application, Environment: spec.Environment,
			Namespace: target.Namespace, Claim: claim, Mount: kernel.DataMount,
			DatabasePath: kernel.DatabasePath, UID: release.Runtime.UID},
		Plan: restorePlanDocument{State: plan.State, Reason: plan.Reason,
			MaxAgeSeconds: int64(plan.Archive.MaxAge / time.Second)},
		Observation: restoreObserved{Verdict: string(report.Verdict), Backup: report.Backup.State,
			Archive: report.Backup.Archive.State},
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
		document.Steps = []restoreStep{{Command: restoreCommand(spec), Environment: []string{"AGE_KEY_FILE=" + defaultAgeKey}}}
	}
	return marshalDocument(document)
}

// restorePlannedAction is the action block of a document the command only planned.
func restorePlannedAction() restoreActionDocument {
	return restoreActionDocument{State: "not_attempted", Steps: []restoreActionStep{}}
}

// restorePerformedAction is the action block of a restore this command performed, step by step.
func restorePerformedAction(outcome orchestration.RestoreOutcome) restoreActionDocument {
	action := restoreActionDocument{State: outcome.State, Reason: outcome.Reason,
		StartedAt:  outcome.Started.UTC().Format(time.RFC3339),
		FinishedAt: outcome.Finished.UTC().Format(time.RFC3339),
		Steps:      make([]restoreActionStep, 0, len(outcome.Steps))}
	for _, step := range outcome.Steps {
		action.Steps = append(action.Steps, restoreActionStep{Name: step.Name, Outcome: step.Outcome,
			Detail: step.Detail})
	}
	action.Job = &restoreJobEvidence{Name: outcome.Job.Name, State: outcome.Job.State,
		Log: outcome.Job.Log, LogReason: outcome.Job.LogReason}
	action.App = &restoreAppDocument{Replicas: outcome.App.Replicas, Suspended: outcome.App.Suspended,
		Available: outcome.App.Available, State: outcome.App.State}
	return action
}

// restoreCommand is the v1 command that performs the restore, with the instance it is for.
func restoreCommand(spec kernel.InstanceSpec) string {
	return fmt.Sprintf("make restore-sqlite APP=%s ENV=%s CONFIRM=%s/%s",
		spec.Application, spec.Environment, spec.Application, spec.Environment)
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
func restoreSummary(plan observe.RestorePlan) string {
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
func restoreActionSummary(outcome orchestration.RestoreOutcome) string {
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
	return summary
}
