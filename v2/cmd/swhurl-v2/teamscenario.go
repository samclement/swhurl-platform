package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/teamscenario"
)

// teamScenario checks, in ClickHouse, the newest trace through a call and an event on three running apps the records
// link in one environment. A dry run prints the plan and reads nothing. No chain is one [ERROR] line and exit 1 with
// no query run; a failed check is exit 1 after the report.
func (c cli) teamScenario(r request) int {
	if r.dryRun {
		return emit(c.out, c.diagnostic, []byte(teamscenario.Plan))
	}
	instances, _, status := c.allStatusInputs(r)
	if status != 0 {
		return status
	}
	executor, status := c.scenarioExecutor(r)
	if status != 0 {
		return status
	}
	inputs := orchestration.TeamScenarioInputs{Instances: instances, Environment: r.env, Executor: executor}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	lines, err := orchestration.TeamScenario(ctx, inputs)
	var preflight *teamscenario.Preflight
	if errors.As(err, &preflight) {
		fmt.Fprintf(c.diagnostic, "[ERROR] %v\n", err)
		return 1
	}
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if status := emit(c.out, c.diagnostic, []byte(teamscenario.Text(lines))); status != 0 || teamscenario.Passes(lines) {
		return status
	}
	return 1
}

// scenarioExecutor is exec through the API server the kubeconfig names; executorOf is what a test supplies instead.
func (c cli) scenarioExecutor(r request) (kube.Executor, int) {
	if c.executorOf != nil {
		return c.executorOf(), 0
	}
	connection, _, status := c.connection(r)
	if status != 0 {
		return nil, status
	}
	executor, err := connection.Executor()
	if err != nil {
		return nil, fail(c.diagnostic, err)
	}
	return executor, 0
}
