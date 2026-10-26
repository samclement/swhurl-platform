package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// restoreNote is what the plan says about itself: it is a check and a command, not a restore.
const restoreNote = "a plan and a check; nothing is restored, stopped or changed by this command"

// restore writes a v2-restore plan: whether this instance's chosen archive is one it can verify, the facts
// the restore needs, and the command that performs it. It reads only, and restores nothing.
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
	document, err := encodeRestore(target, spec, release, report, plan)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, restoreSummary(plan))
	return emit(c.out, c.diagnostic, document)
}

// restorePlan chooses the archive and reads what the bucket records beside it.
func (c cli) restorePlan(r request, target observe.Target, maxAge time.Duration) (observe.RestorePlan, int) {
	query, status := c.archiveQuery(r, target)
	if status != 0 {
		return observe.RestorePlan{}, status
	}
	reader, err := c.archives(query.Bucket, r.region)
	if err != nil {
		return observe.RestorePlan{}, fail(c.diagnostic, err)
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
// was decided from, and what the operator runs and checks afterwards.
type restoreDocument struct {
	Kind         string                  `json:"kind"`
	Version      int                     `json:"version"`
	GeneratedAt  string                  `json:"generatedAt"`
	Instance     restoreInstanceDocument `json:"instance"`
	Plan         restorePlanDocument     `json:"plan"`
	Observation  restoreObserved         `json:"observation"`
	Steps        []restoreStep           `json:"steps"`
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
	SHA256 string `json:"sha256,omitempty"`
	Tables int32  `json:"tables"`
	Source string `json:"source,omitempty"`
}

// restoreObserved is what the observation said, so the plan's reader can see the app's state before it is
// stopped: a restore replaces the database under a running app, and the operator decides when.
type restoreObserved struct {
	Verdict string `json:"verdict"`
	Backup  string `json:"backup"`
	Archive string `json:"archive"`
}

type restoreStep struct {
	Command     string   `json:"command"`
	Environment []string `json:"environment"`
}

func encodeRestore(target observe.Target, spec kernel.InstanceSpec, release kernel.ReleaseBundle,
	report observe.Report, plan observe.RestorePlan) ([]byte, error) {
	claim := spec.Storage.ClaimName
	if claim == "" {
		// An instance that creates its own volume has no claim name in its record: the pinned chart names
		// the generated claim after the release, which its own render test ties.
		claim = spec.Application
	}
	document := restoreDocument{
		Kind: "v2-restore", Version: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Instance: restoreInstanceDocument{Application: spec.Application, Environment: spec.Environment,
			Namespace: target.Namespace, Claim: claim, Mount: kernel.DataMount,
			DatabasePath: kernel.DatabasePath, UID: release.Runtime.UID},
		Plan: restorePlanDocument{State: plan.State, Reason: plan.Reason, MaxAgeSeconds: int64(plan.Archive.MaxAge / time.Second)},
		Observation: restoreObserved{Verdict: string(report.Verdict), Backup: report.Backup.State,
			Archive: report.Backup.Archive.State},
		Steps: []restoreStep{{Command: restoreCommand(spec), Environment: []string{"AGE_KEY_FILE=./age.agekey"}}},
		Verification: []string{
			"observe again: verdict running, backup current, archive current",
			"the restore prints the restored table count and integrity check; it must match the metadata",
			"the live database is replaced; before-restore-<UTC>/ beside it holds what was there",
		},
		NotCovered: []string{"the restore itself", "the data being the point in time you want"},
		Note:       restoreNote,
	}
	if plan.Archive.Newest != "" {
		document.Plan.Archive = &restoreArchiveDocument{Key: plan.Archive.Newest,
			LastModified: plan.Archive.LastModified, AgeSeconds: int64(plan.Archive.Age / time.Second)}
	}
	if plan.Metadata.Source != "" || plan.Metadata.SHA256 != "" || plan.Metadata.Tables > 0 {
		document.Plan.Metadata = &restoreMetadata{SHA256: plan.Metadata.SHA256, Tables: plan.Metadata.Tables,
			Source: plan.Metadata.Source}
	}
	return marshalDocument(document)
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
