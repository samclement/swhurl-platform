package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/samclement/swhurl-platform/v2/lifecycletest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// lifecycleTest proves the lifecycle contract on the throwaway fixture, printing each line as it is decided. A dry
// run prints the plan and reads nothing. Leftovers are one [ERROR] line and exit 1 with nothing changed; a failed
// check or step is exit 1 after the report. An interrupt stops the step under way and the cleanup still runs.
func (c cli) lifecycleTest(r request) int {
	if r.dryRun {
		return emit(c.out, c.diagnostic, []byte(lifecycletest.Plan))
	}
	cluster, status := c.dataCluster(r)
	if status != 0 {
		return status
	}
	executor, status := c.scenarioExecutor(r)
	if status != 0 {
		return status
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	written := true
	lines, err := orchestration.LifecycleTest(ctx, orchestration.LifecycleTestInputs{
		Cluster: orchestration.Cluster{Reader: cluster.reader, Writer: cluster.writer}, Secrets: cluster.secrets,
		Executor: executor, Now: c.appRepoNow(), Sleep: c.sleeper(),
		Say: func(line lifecycletest.Line) {
			if _, err := fmt.Fprintln(c.out, lifecycletest.Format(line)); err != nil {
				written = false
			}
		}})
	var preflight *lifecycletest.Preflight
	if errors.As(err, &preflight) {
		fmt.Fprintf(c.diagnostic, "[ERROR] %v\n", err)
		return 1
	}
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if !written {
		fmt.Fprintln(c.diagnostic, "io: output: cannot write result")
		return 1
	}
	if lifecycletest.Passes(lines) {
		return 0
	}
	return 1
}
