// Package delivery is the delivery capability's use cases: release selection, staging-to-production promotion,
// deployment, and the evidence that a requested revision landed. Its decisions are delivery/rules'; this package
// reads, sequences and writes.
package delivery

import (
	"errors"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Refusal is the use case's own refusal: it will not start, and nothing was changed. A failure
// after the first change is never a refusal, so a caller can tell "not started" from "stopped part-way" by
// the type alone. (Copied from orchestration/usecase.go.)
type Refusal struct{ Subject, Reason string }

func (r *Refusal) Error() string { return "refused: " + r.Subject + ": " + r.Reason }

// reasonOf is the fixed reason a read states, or the malformed document it could not be read from.
// (Copied from orchestration/usecase.go.)
func reasonOf(read observe.Read) string {
	if read.Reason != "" {
		return read.Reason
	}
	return observe.ReasonNotFound
}

// unavailable is a read that failed part-way through a use case: a failure of this run, never a refusal,
// because the run may already have changed something. (Copied from orchestration/usecase.go.)
func unavailable(subject, reason string) error {
	if reason == "" {
		reason = observe.ReasonNotFound
	}
	return errors.New("unavailable: " + subject + ": " + reason)
}
