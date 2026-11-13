package orchestration

import (
	"context"
	"strconv"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/teamscenario"
)

// clickhousePod is the operator's naming: <cluster>-clickhouse-<shard>-<replica>-0.
const clickhousePod, clickhouseContainer = "clickstack-clickhouse-clickhouse-0-0-0", "clickhouse-server"

// TeamScenarioInputs is every recorded instance, the environment the scenario follows, and the exec port its
// queries reach ClickHouse through.
type TeamScenarioInputs struct {
	Instances   []Inputs
	Environment string
	Executor    kube.Executor
}

// TeamScenario chooses the chain from the records' declarations, never a read of the cluster, then asks ClickHouse
// for the newest trace through it and judges each answer. A records error or a missing chain is an error and no
// query runs; a query ClickHouse does not answer is a failed line, and the scenario stops there.
func TeamScenario(ctx context.Context, inputs TeamScenarioInputs) ([]teamscenario.Line, error) {
	apps := map[string]teamscenario.App{}
	for _, instance := range inputs.Instances {
		declared, err := declaredInstance(instance)
		if err != nil {
			return nil, err
		}
		if declared.environment == inputs.Environment {
			d := declared.declaration
			apps[declared.application] = teamscenario.App{Team: d.Team, Calls: d.Calls, Publishes: d.Publishes, Subscriptions: d.Subscriptions}
		}
	}
	chain, err := teamscenario.Choose(inputs.Environment, apps)
	if err != nil {
		return nil, err
	}
	lines := []teamscenario.Line{{Level: teamscenario.Section, Text: chain.Section(inputs.Environment)}}
	answer, failed := clickhouse(ctx, inputs.Executor, chain.TraceQuery())
	if failed != nil {
		return append(lines, *failed), nil
	}
	trace, judged := chain.Served(answer)
	if lines = append(lines, judged...); trace == "" {
		return lines, nil
	}
	if answer, failed = clickhouse(ctx, inputs.Executor, chain.SpansQuery(trace)); failed != nil {
		return append(lines, *failed), nil
	}
	lines = append(lines, chain.Teams(apps, answer)...)
	if answer, failed = clickhouse(ctx, inputs.Executor, chain.LogQuery(trace)); failed != nil {
		return append(lines, *failed), nil
	}
	return append(lines, chain.Logged(trace, answer)...), nil
}

// clickhouse runs one query with clickhouse-client in the ClickHouse pod, through the API server.
func clickhouse(ctx context.Context, executor kube.Executor, query string) (string, *teamscenario.Line) {
	result, err := executor.Exec(ctx, kube.ExecRequest{Namespace: clickstackNamespace, Pod: clickhousePod, Container: clickhouseContainer,
		Command: []string{"clickhouse-client", "-q", query}})
	if err != nil {
		failed := teamscenario.QueryFailed(err.Error())
		return "", &failed
	}
	if result.ExitCode != 0 {
		failed := teamscenario.QueryFailed("clickhouse-client exited " + strconv.Itoa(result.ExitCode))
		return "", &failed
	}
	return string(result.Stdout), nil
}
