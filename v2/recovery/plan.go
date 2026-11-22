package recovery

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/recovery/rules"
)

// PlanInputs is one restore plan's request: the instance's own archive directory and the store that holds it,
// the instance the archive must be about, the archive the operator named (empty for the newest), and the
// threshold and moment the archive is judged at.
type PlanInputs struct {
	Archives               s3.Reader
	Query                  rules.ArchiveQuery
	Namespace, Application string
	From                   string
	MaxAge                 time.Duration
	Now                    time.Time
}

// Plan chooses the archive and reads what the bucket records beside it, then lets the rules decide whether a
// restore can verify it. A listing that failed is an error; an archive whose metadata could not be read is a
// plan that says why, so the operator sees which half is missing.
func Plan(ctx context.Context, inputs PlanInputs) (rules.RestorePlan, error) {
	listed, err := inputs.Archives.List(ctx, inputs.Query)
	if err != nil {
		return rules.RestorePlan{}, err
	}
	archive, err := rules.SelectArchive(listed, inputs.Query.Prefix, inputs.From, inputs.MaxAge, inputs.Now)
	if err != nil {
		return rules.RestorePlan{}, err
	}
	plan := rules.RestoreInput{Namespace: inputs.Namespace, Application: inputs.Application, Archive: archive}
	if archive.Newest != "" {
		plan.Metadata, plan.Failure = metadata(ctx, inputs.Archives, inputs.Query, archive.Newest)
	}
	return rules.RestoreOf(plan), nil
}

// metadata reads the record the backup job published beside one archive, and says why it could not when it
// could not: an incomplete upload is not an unreachable bucket.
func metadata(ctx context.Context, reader s3.Reader, query rules.ArchiveQuery, key string) (*rules.ArchiveMetadata, string) {
	body, err := reader.Get(ctx, query, strings.TrimSuffix(key, ".db.age")+".json")
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
