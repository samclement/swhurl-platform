package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/teamscenario"
)

// scenarioInstance is one recorded instance that calls, publishes and subscribes as given.
func scenarioInstance(t *testing.T, application, environment string, calls, publishes, subscriptions []string) Inputs {
	t.Helper()
	inputs := statusVariant(t, application, environment, false, false)
	release, err := kernel.DecodeRelease(inputs.Release)
	if err != nil {
		t.Fatal(err)
	}
	release.Publishes, release.ReleaseID = publishes, ""
	if len(publishes) > 0 {
		release.Documents = []string{"events"}
	}
	if release.ReleaseID, err = release.ID(); err != nil {
		t.Fatal(err)
	}
	if inputs.Release, err = kernel.EncodeRelease(release); err != nil {
		t.Fatal(err)
	}
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		t.Fatal(err)
	}
	spec.Release, spec.Calls, spec.Events.Subscriptions = release.ReleaseID, calls, subscriptions
	if len(publishes)+len(subscriptions) > 0 {
		spec.Events.Key = "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI" // a public key: a test's only
	}
	if inputs.Instance, err = kernel.EncodeInstance(spec); err != nil {
		t.Fatal(err)
	}
	return inputs
}

func scenarioInstances(t *testing.T) []Inputs {
	return []Inputs{
		scenarioInstance(t, "reports", "staging", []string{"orders"}, nil, nil),
		scenarioInstance(t, "orders", "staging", nil, []string{"created"}, nil),
		scenarioInstance(t, "notifier", "staging", nil, nil, []string{"orders.created"}),
		scenarioInstance(t, "orders", "prod", nil, []string{"created"}, nil),
	}
}

func answer(stdout string) kubetest.ExecAnswer {
	return kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte(stdout)}}
}

func TestTeamScenarioQueriesClickHouseAlongTheChainTheRecordsLink(t *testing.T) {
	executor := kubetest.NewExecutor(answer("ab12\t3\n"), answer("reports\tdemo\norders\tdemo\nnotifier\tdemo\n"), answer("1\n"))
	lines, err := TeamScenario(context.Background(), TeamScenarioInputs{Instances: scenarioInstances(t), Environment: "staging", Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	chain := teamscenario.Chain{Caller: "reports", Callee: "orders", Event: "created", Subscriber: "notifier"}
	queries := []string{chain.TraceQuery(), chain.SpansQuery("ab12"), chain.LogQuery("ab12")}
	if len(executor.Requests) != len(queries) {
		t.Fatalf("%d queries, want %d", len(executor.Requests), len(queries))
	}
	for i, request := range executor.Requests {
		if request.Namespace != "observability" || request.Pod != clickhousePod || request.Container != "clickhouse-server" ||
			len(request.Command) != 3 || request.Command[0] != "clickhouse-client" || request.Command[2] != queries[i] || request.Stdin != nil {
			t.Fatalf("query %d: %+v", i, request)
		}
	}
	if !teamscenario.Passes(lines) || lines[0] != (teamscenario.Line{Level: teamscenario.Section, Text: chain.Section("staging")}) {
		t.Fatalf("lines: %+v", lines)
	}
	// statusVariant's app record names team demo, which the spans carry.
	if text := teamscenario.Text(lines); !strings.Contains(text, "[OK] notifier's spans carry its team: demo") {
		t.Fatal(text)
	}
}

func TestTeamScenarioStopsAtTheFirstQueryClickHouseDoesNotAnswer(t *testing.T) {
	for name, executor := range map[string]*kubetest.Executor{
		"transport": kubetest.NewExecutor(kubetest.ExecAnswer{Err: &kube.Error{Code: kube.Failed, Subject: "exec", Reason: "refused"}}),
		"exit":      kubetest.NewExecutor(kubetest.ExecAnswer{Result: kube.ExecResult{ExitCode: 210}}),
		"spans":     kubetest.NewExecutor(answer("ab12\t3\n"), kubetest.ExecAnswer{Result: kube.ExecResult{ExitCode: 1}}),
		"no trace":  kubetest.NewExecutor(answer("")),
	} {
		t.Run(name, func(t *testing.T) {
			lines, err := TeamScenario(context.Background(), TeamScenarioInputs{Instances: scenarioInstances(t), Environment: "staging", Executor: executor})
			if err != nil || teamscenario.Passes(lines) || len(executor.Requests) != len(executor.Answers) {
				t.Fatalf("lines %+v, error %v, %d queries", lines, err, len(executor.Requests))
			}
			last := lines[len(lines)-1]
			if name != "no trace" && !strings.HasPrefix(last.Text, "ClickHouse query failed: ") {
				t.Fatalf("last line: %+v", last)
			}
		})
	}
}

func TestTeamScenarioWithoutAChainOrWithBadRecordsQueriesNothing(t *testing.T) {
	broken := scenarioInstances(t)
	broken[0].Profile = []byte("{")
	for name, inputs := range map[string]TeamScenarioInputs{
		"other environment": {Instances: scenarioInstances(t), Environment: "prod"},
		"no subscriber":     {Instances: scenarioInstances(t)[:2], Environment: "staging"},
		"broken records":    {Instances: broken, Environment: "staging"},
	} {
		t.Run(name, func(t *testing.T) {
			executor := kubetest.NewExecutor()
			inputs.Executor = executor
			_, err := TeamScenario(context.Background(), inputs)
			var preflight *teamscenario.Preflight
			if err == nil || errors.As(err, &preflight) == (name == "broken records") || len(executor.Requests) != 0 {
				t.Fatalf("error %v, %d queries", err, len(executor.Requests))
			}
		})
	}
}
