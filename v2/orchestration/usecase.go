package orchestration

import (
	"errors"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// Cluster is what a use case reads and changes: both halves of the kube adapter, because a use case that could
// not read the object it changes would have to guess what it is returning it to.
type Cluster struct {
	Reader kube.Reader
	Writer kube.Writer
}

// Refusal is the use case's own refusal: it will not start, and nothing was changed. A failure
// after the first change is never a refusal, so a caller can tell "not started" from "stopped part-way" by
// the type alone.
type Refusal struct{ Subject, Reason string }

func (r *Refusal) Error() string { return "refused: " + r.Subject + ": " + r.Reason }

// reasonOf is the fixed reason a read states, or the malformed document it could not be read from.
func reasonOf(read observe.Read) string {
	if read.Reason != "" {
		return read.Reason
	}
	return observe.ReasonNotFound
}

// unavailable is a read that failed part-way through a use case: a failure of this run, never a refusal,
// because the run may already have changed something.
func unavailable(subject, reason string) error {
	if reason == "" {
		reason = observe.ReasonNotFound
	}
	return errors.New("unavailable: " + subject + ": " + reason)
}

// failureReason is the fixed reason an adapter error states.
func failureReason(err error) string {
	var failure *kube.Error
	if errors.As(err, &failure) && failure.Reason != "" {
		return failure.Reason
	}
	return observe.ReasonStatus
}
