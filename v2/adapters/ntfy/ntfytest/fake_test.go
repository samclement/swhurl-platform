package ntfytest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy/ntfytest"
	"github.com/samclement/swhurl-platform/v2/notify"
)

// errRefused stands in for a failure the publisher would report instead of sending.
var errRefused = errors.New("refused")

func scenario() ntfytest.Scenario {
	return ntfytest.Scenario{
		Accepting:   "https://ntfy.example/private-topic-0123",
		Refusing:    "https://ntfy.example/private-topic-4567",
		Publication: notify.Publication{Title: "payments/production: Orders failing", Message: "Not drained.", Priority: 4, Tags: []string{"warning"}},
	}
}

func TestContract(t *testing.T) {
	ntfytest.Contract(t, scenario(), func(t *testing.T, scenario ntfytest.Scenario) (ntfy.Publisher, func() [][]byte) {
		fake := ntfytest.New()
		fake.Refusing[scenario.Refusing] = true
		return fake, func() [][]byte { return fake.Received(scenario.Accepting) }
	})
}

// The fake counts every attempt, and a configured failure is returned without sending.
func TestFakeCountsAndCanFail(t *testing.T) {
	fake := ntfytest.New()
	fake.Fail = errRefused
	if err := fake.Publish(context.Background(), scenario().Accepting, scenario().Publication); err != errRefused {
		t.Fatalf("a configured failure was not returned: %v", err)
	}
	if fake.Attempts != 1 || len(fake.Sent) != 0 {
		t.Fatalf("%d attempts, %d sent", fake.Attempts, len(fake.Sent))
	}
}
