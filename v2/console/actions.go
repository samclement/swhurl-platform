package console

import (
	"context"
	"time"

	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// The console's actions on a Flux unit. The command decides that a job may start and answers the browser; the work
// the job runs is here.

// UnitWork is one unit action as a job's work: Flux's own lines, and the refusal it ends with when there is one. The
// action's refusal — a root unit, a suspended unit's reconcile — is the job's own last line.
func UnitWork(cluster orchestration.Cluster, now func() time.Time, action, unit string) JobWork {
	return func(ctx context.Context, line func(string)) (string, error) {
		return "", orchestration.Flux(ctx, orchestration.FluxInputs{Action: orchestration.FluxAction(action), Unit: unit,
			Cluster: cluster, Now: now, Sleep: time.Sleep, Progress: line})
	}
}

// NoJobsProblem says why a form did nothing: this console would have no record of the job it started.
const NoJobsProblem = "This console runs no jobs: it has no cluster record, so nothing would remember them. Start it " +
	"with --in-cluster or --kubeconfig."
