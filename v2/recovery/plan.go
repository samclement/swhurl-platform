package recovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/recovery/rules"
)

// PrepareInputs is one restore's request before anything is stopped: the live observation of the instance (the
// apps capability's status, which the caller wires in), the store and directory of a namespace's archives, the
// archive the operator named (empty for the newest), and the threshold and clock the archive is judged by.
type PrepareInputs struct {
	Observe  func(context.Context) (observe.Report, error)
	Archives func(namespace string) (s3.Reader, rules.ArchiveQuery, error)
	From     string
	MaxAge   time.Duration
	Now      func() time.Time
}

// Prepared is what a restore is decided from: the observation, whose archive is the plan's own read, and the plan.
type Prepared struct {
	Report observe.Report
	Plan   rules.RestorePlan
}

// Prepare observes the instance, refuses one that keeps no database, and plans the restore from its own archives.
// The plan's read of the archive replaces the observation's, so the report states the same fact the plan was
// decided from rather than a separate one. It changes nothing.
func Prepare(ctx context.Context, inputs PrepareInputs) (Prepared, error) {
	report, err := inputs.Observe(ctx)
	if err != nil {
		return Prepared{}, err
	}
	if report.Backup.State == rules.BackupNotApplicable {
		return Prepared{}, errors.New("unsupported: instance: it keeps no database to restore")
	}
	reader, query, err := inputs.Archives(report.Target.Namespace)
	if err != nil {
		return Prepared{}, err
	}
	plan, err := planOf(ctx, reader, query, report.Target, inputs)
	if err != nil {
		return Prepared{}, err
	}
	report.Backup.Archive = plan.Archive
	return Prepared{Report: report, Plan: plan}, nil
}

// Confirm admits a prepared restore for performing: the confirmation names this instance as application/environment,
// and the plan is ready. It is checked before any credential is read or anything is stopped.
func (p Prepared) Confirm(spec kernel.InstanceSpec, confirm string) error {
	if want := spec.Application + "/" + spec.Environment; confirm != want {
		return fmt.Errorf("invalid: confirm: expected %s", want)
	}
	if !p.Plan.Ready() {
		return fmt.Errorf("refused: restore.plan: %s", p.Plan.State)
	}
	return nil
}

// planOf chooses the archive among the instance's own and reads what the bucket records beside it, then lets the
// rules decide whether a restore can verify it. A listing that failed is an error; an archive whose metadata could
// not be read is a plan that says why, so the operator sees which half is missing.
func planOf(ctx context.Context, reader s3.Reader, query rules.ArchiveQuery, target observe.Target,
	inputs PrepareInputs) (rules.RestorePlan, error) {
	listed, err := reader.List(ctx, query)
	if err != nil {
		return rules.RestorePlan{}, err
	}
	archive, err := rules.SelectArchive(listed, query.Prefix, inputs.From, inputs.MaxAge, inputs.Now())
	if err != nil {
		return rules.RestorePlan{}, err
	}
	plan := rules.RestoreInput{Namespace: target.Namespace, Application: target.Release.Name, Archive: archive}
	if archive.Newest != "" {
		plan.Metadata, plan.Failure = metadata(ctx, reader, query, archive.Newest)
	}
	return rules.RestoreOf(plan), nil
}

// metadata reads the record the backup job published beside one archive, and says why it could not when it
// could not: an incomplete upload is not an unreachable bucket.
func metadata(ctx context.Context, reader s3.Reader, query rules.ArchiveQuery, key string) (*rules.ArchiveMetadata, string) {
	body, err := reader.Get(ctx, query, rules.MetadataKey(key))
	switch {
	case err == nil:
		metadata, err := rules.ArchiveMetadataOf(body)
		if err != nil {
			return nil, rules.ReasonMetadataMalformed
		}
		return &metadata, ""
	case notFound(err):
		return nil, rules.ReasonMetadataAbsent
	}
	return nil, rules.ReasonMetadataDown
}

// notFound is whether the object store answered that the key is not there.
func notFound(err error) bool {
	var failure *s3.Error
	return errors.As(err, &failure) && failure.Code == "not_found"
}
