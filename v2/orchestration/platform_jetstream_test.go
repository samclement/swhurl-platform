package orchestration

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

func TestPlatformAllReadsJetStreamThroughTheExistingExecutor(t *testing.T) {
	_, inputs := scopeCluster(t)
	output := []byte(`{"config":{"max_storage":1073741824},"reserved_storage":0}`)
	executor := kubetest.NewExecutor(
		kubetest.ExecAnswer{}, kubetest.ExecAnswer{}, kubetest.ExecAnswer{},
		kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: output}},
	)
	inputs.Executor = executor
	report, err := PlatformAll(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	want := kube.ExecRequest{Namespace: "messaging", Pod: "nats-0", Container: "nats",
		Command: []string{"wget", "-qO-", "http://127.0.0.1:8222/jsz?accounts=true&streams=true&config=true"}}
	if len(executor.Requests) != 4 || !reflect.DeepEqual(executor.Requests[3], want) {
		t.Fatalf("expected three retention reads followed by the local JetStream monitor: %+v", executor.Requests)
	}
	for _, check := range groupsNamed(report, "events") {
		for _, entry := range check.Entries {
			if entry.Message == "JetStream has 1024 of 1024 MiB unreserved" && entry.Level == observe.CheckOK {
				return
			}
		}
	}
	t.Fatal("the events group did not judge the JetStream answer")
}

func TestJetStreamExecFailureDoesNotExposeOutputOrDiagnostics(t *testing.T) {
	for _, answer := range []kubetest.ExecAnswer{
		{Result: kube.ExecResult{ExitCode: 1, Stdout: []byte("private-output")}},
		{Err: errors.New("private-diagnostic"), Result: kube.ExecResult{Stdout: []byte("private-output")}},
	} {
		scope := observe.ScopeEvidence{}
		err := readJetStream(context.Background(), PlatformAllInputs{Executor: kubetest.NewExecutor(answer)}, &scope)
		if err != nil || !scope.Events.Storage.Failed || len(scope.Events.Storage.Output) != 0 {
			t.Fatal("an exec failure must be unavailable evidence, without its raw output")
		}
	}
}
