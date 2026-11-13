package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/teamscenario"
)

func runTeamScenario(t *testing.T, executor *kubetest.Executor, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := runWith(append([]string{"team-scenario"}, args...), &out, &diagnostic, cli{readFile: os.ReadFile,
		executorOf: func() kube.Executor { return executor }})
	return status, out.String(), diagnostic.String()
}

// The repository's own records: the demo's staging apps form the chain the scenario follows.
var repositoryRecords = []string{"--records", "../../../v2-records", "--profile", "../../testdata/profile.json",
	"--calls", "../../../v2-records/calls.json", "--egress", "../../../platform/egress/allocation.json", "--kubeconfig", "unread.json"}

func TestTeamScenarioDryRunPrintsThePlanAndReadsNothing(t *testing.T) {
	var out, diagnostic bytes.Buffer
	read := false
	status := runWith([]string{"team-scenario", "--dry-run"}, &out, &diagnostic, cli{
		readFile:   func(string) ([]byte, error) { read = true; return nil, errors.New("no file") },
		executorOf: func() kube.Executor { read = true; return nil }})
	if status != 0 || out.String() != teamscenario.Plan+"\n" || diagnostic.Len() != 0 || read {
		t.Fatalf("%d %q %q read %v", status, out.String(), diagnostic.String(), read)
	}
}

func TestTeamScenarioExitsOnTheVerdicts(t *testing.T) {
	spans := "demo-shop\tdemo\ndemo-orders\tdemo\ndemo-notifier\tdemo\n"
	for _, c := range []struct {
		answers []kubetest.ExecAnswer
		status  int
		want    string
	}{
		{[]kubetest.ExecAnswer{clickhouseAnswer("ab12\t3\n"), clickhouseAnswer(spans), clickhouseAnswer("1\n")}, 0, "\nTeam scenario passed.\n"},
		{[]kubetest.ExecAnswer{clickhouseAnswer("ab12\t3\n"), clickhouseAnswer(spans), clickhouseAnswer("0\n")}, 1, "[BAD] demo-orders logged no line in this trace with caller.id = demo-shop\n"},
		{[]kubetest.ExecAnswer{{Result: kube.ExecResult{ExitCode: 1}}}, 1, "[BAD] ClickHouse query failed: clickhouse-client exited 1\n"},
	} {
		status, out, diagnostic := runTeamScenario(t, kubetest.NewExecutor(c.answers...), append([]string{"--env", "staging"}, repositoryRecords...)...)
		if status != c.status || diagnostic != "" || !strings.HasPrefix(out, "\n== The newest trace in which demo-orders served demo-shop (staging, last 10 minutes) ==\n") || !strings.Contains(out, c.want) {
			t.Fatalf("%d %q %q", status, out, diagnostic)
		}
	}
}

// No chain runs no query: one [ERROR] line and exit 1, as the Python test's preflight did.
func TestTeamScenarioWithoutAChainQueriesNothing(t *testing.T) {
	executor := kubetest.NewExecutor()
	status, out, diagnostic := runTeamScenario(t, executor, append([]string{"--env", "review"}, repositoryRecords...)...)
	if status != 1 || out != "" || len(executor.Requests) != 0 || !strings.HasPrefix(diagnostic, "[ERROR] no review instances form a chain: ") {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
	if status, _, _ := runTeamScenario(t, executor, "--records", "../../../v2-records"); status != 2 || len(executor.Requests) != 0 {
		t.Fatalf("missing flags: %d", status)
	}
}

func clickhouseAnswer(stdout string) kubetest.ExecAnswer {
	return kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte(stdout)}}
}
