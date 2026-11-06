package kubetest

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
)

// ExecAnswer is one completed command or a failed transport/protocol, with no partial result.
type ExecAnswer struct {
	Result kube.ExecResult
	Err    error
}

// Executor scripts exec answers in order and records owned input copies, never formats their bytes.
type Executor struct {
	Answers  []ExecAnswer
	Requests []kube.ExecRequest
}

var _ kube.Executor = (*Executor)(nil)

// NewExecutor takes owned copies of mutable scenario bytes.
func NewExecutor(answers ...ExecAnswer) *Executor {
	f := &Executor{Answers: append([]ExecAnswer(nil), answers...)}
	for i := range f.Answers {
		f.Answers[i].Result.Stdout = bytes.Clone(f.Answers[i].Result.Stdout)
	}
	return f
}

// Exec records an owned request and returns an owned result. An unscripted command fails closed.
func (f *Executor) Exec(ctx context.Context, r kube.ExecRequest) (kube.ExecResult, error) {
	r.Command = append([]string(nil), r.Command...)
	r.Stdin = bytes.Clone(r.Stdin)
	index := len(f.Requests)
	f.Requests = append(f.Requests, r)
	if ctx.Err() != nil {
		return kube.ExecResult{}, &kube.Error{Code: kube.Failed, Subject: "exec", Reason: "timed_out"}
	}
	if index >= len(f.Answers) {
		return kube.ExecResult{}, &kube.Error{Code: kube.Failed, Subject: "exec", Reason: "unscripted"}
	}
	answer := f.Answers[index]
	if answer.Err != nil {
		return kube.ExecResult{}, answer.Err
	}
	result := answer.Result
	result.Stdout = bytes.Clone(result.Stdout)
	return result, nil
}

// ExecutorContract checks completed success/nonzero exits, transport failure without partial output,
// repeated result ownership and cancellation against the real upgraded server and the fake.
func ExecutorContract(t *testing.T, request kube.ExecRequest, open func(*testing.T, kube.ExecResult, bool) kube.Executor) {
	t.Helper()
	for _, code := range []int{0, 17} {
		t.Run(string(rune('A'+code)), func(t *testing.T) {
			want := kube.ExecResult{Stdout: []byte("query answer\n"), ExitCode: code}
			executor := open(t, want, false)
			for i := 0; i < 2; i++ {
				result, err := executor.Exec(context.Background(), request)
				if err != nil || !reflect.DeepEqual(result, want) {
					t.Fatalf("completed command: exit %d, error %v", result.ExitCode, err)
				}
				result.Stdout[0] ^= 0xff
			}
		})
	}
	executor := open(t, kube.ExecResult{Stdout: []byte("not a complete answer")}, true)
	result, err := executor.Exec(context.Background(), request)
	if err == nil || len(result.Stdout) != 0 {
		t.Fatal("failed protocol returned output or passed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.Exec(ctx, request); err == nil {
		t.Fatal("canceled command passed")
	}
}
