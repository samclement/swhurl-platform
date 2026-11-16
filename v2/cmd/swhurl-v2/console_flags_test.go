package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/adapters/nats/natstest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// flagServer is a console with a key whose store opens as the opens list says, one entry per attempt, and a clock
// the test moves.
func flagServer(t *testing.T, opens ...error) (*consoleServer, *int, *time.Time) {
	t.Helper()
	attempts, clock := 0, time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	server := &consoleServer{natsSeed: "SU-a-key", now: func() time.Time { return clock },
		natsOpen: func(context.Context, string, string, string) (nats.KV, error) {
			attempts++
			if attempts > len(opens) {
				t.Fatalf("opened %d times", attempts)
			}
			if opens[attempts-1] != nil {
				return nil, opens[attempts-1]
			}
			return natstest.New(), nil
		}}
	return server, &attempts, &clock
}

var flagInstance = pages.Instance{App: "demo-shop", Env: "staging"}

// A key whose sign-in failed shows why, not that there is no key, and offers no change; a minute later the console
// signs in again, and once open it keeps the store.
func TestTheFlagCardStatesAFailedSignIn(t *testing.T) {
	refused := errors.New("cannot reach NATS at nats://nats.messaging.svc.cluster.local:4222: connection refused")
	server, attempts, clock := flagServer(t, refused, nil)
	declaration := []producer.Flag{{Name: "shout", Declaration: producer.Object{}}}
	for _, step := range []struct {
		after    time.Duration
		problem  string
		writable bool
		attempts int
	}{
		{0, "Overrides could not be read: " + refused.Error(), false, 1},
		{30 * time.Second, "Overrides could not be read: " + refused.Error(), false, 1},
		{time.Minute, "", true, 2},
		{time.Hour, "", true, 2},
	} {
		*clock = clock.Add(step.after)
		card, writable := server.flagCard(context.Background(), flagInstance, declaration, "")
		if card.Problem != step.problem || writable != step.writable || *attempts != step.attempts ||
			len(card.Rows) != 1 || strings.Contains(card.Problem, orchestration.NoFlagsProblem) {
			t.Fatalf("after %s: %+v, writable %v, %d attempts", step.after, card, writable, *attempts)
		}
	}
}

// No key is still the no-key card, and never an attempt to sign in.
func TestTheFlagCardWithoutAKey(t *testing.T) {
	server, attempts, _ := flagServer(t)
	server.natsSeed = ""
	card, writable := server.flagCard(context.Background(), flagInstance, nil, "")
	if card.Problem != orchestration.NoFlagsProblem || writable || *attempts != 0 {
		t.Fatalf("no key: %+v, writable %v, %d attempts", card, writable, *attempts)
	}
}
