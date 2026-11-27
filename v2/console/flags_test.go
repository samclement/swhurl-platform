package console_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/adapters/nats/natstest"
	"github.com/samclement/swhurl-platform/v2/console"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// flagStore is a console's store with a key, opening as the opens list says, one entry per attempt, at a clock the
// test moves. An attempt that opens returns the fake it is given.
func flagStore(t *testing.T, fake *natstest.Fake, opens ...error) (*console.FlagStore, *int, *time.Time) {
	t.Helper()
	attempts, clock := 0, time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	store := &console.FlagStore{Seed: "SU-a-key", URL: "nats://nats.test:4222", Now: func() time.Time { return clock },
		Open: func(context.Context, string, string, string) (nats.KV, error) {
			attempts++
			if attempts > len(opens) {
				t.Fatalf("opened %d times", attempts)
			}
			if opens[attempts-1] != nil {
				return nil, opens[attempts-1]
			}
			return fake, nil
		}}
	return store, &attempts, &clock
}

var flagInstance = pages.Instance{App: "demo-shop", Env: "staging"}

// A key whose sign-in failed shows why, not that there is no key, and offers no change; a minute later the console
// signs in again, and once open it keeps the store.
func TestTheFlagCardStatesAFailedSignIn(t *testing.T) {
	refused := errors.New("cannot reach NATS at nats://nats.messaging.svc.cluster.local:4222: connection refused")
	store, attempts, clock := flagStore(t, natstest.New(), refused, nil)
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
		card, writable := store.Card(context.Background(), flagInstance, declaration, "")
		if card.Problem != step.problem || writable != step.writable || *attempts != step.attempts ||
			len(card.Rows) != 1 || strings.Contains(card.Problem, orchestration.NoFlagsProblem) {
			t.Fatalf("after %s: %+v, writable %v, %d attempts", step.after, card, writable, *attempts)
		}
	}
}

// No key is still the no-key card, and never an attempt to sign in.
func TestTheFlagCardWithoutAKey(t *testing.T) {
	store, attempts, _ := flagStore(t, nil)
	store.Seed = ""
	card, writable := store.Card(context.Background(), flagInstance, nil, "")
	if card.Problem != orchestration.NoFlagsProblem || writable || *attempts != 0 {
		t.Fatalf("no key: %+v, writable %v, %d attempts", card, writable, *attempts)
	}
}

// A change made audits who made it and what the store holds after it; one not made says why and with which status,
// and audits nothing: a store that cannot be opened is the broker's failure, a value the declaration refuses is the
// form's.
func TestAFlagChangeSaysWhatTheStoreHolds(t *testing.T) {
	tree := map[string][]byte{console.FlagDocument(flagInstance): []byte(
		`{"theme": {"type": "string", "default": "plain", "enum": ["plain", "dark"]}}`)}
	refused := errors.New("connection refused")
	for _, test := range []struct {
		name    string
		opens   []error
		change  console.FlagChange
		audit   string
		problem string
		status  int
		writes  int
	}{
		{"set", []error{nil}, console.FlagChange{Name: "theme", Set: true, Value: "dark", Identity: "sam@swhurl.com"},
			"[AUDIT] sam@swhurl.com flag demo-shop/staging theme = \"dark\"", "", 0, 1},
		{"clear", []error{nil}, console.FlagChange{Name: "theme", Identity: "sam@swhurl.com"},
			"[AUDIT] sam@swhurl.com flag demo-shop/staging theme cleared", "", 0, 1},
		{"refused value", []error{nil}, console.FlagChange{Name: "theme", Set: true, Value: "loud"}, "",
			"theme: ", http.StatusBadRequest, 0},
		{"undeclared", []error{nil}, console.FlagChange{Name: "shout", Set: true, Value: "x"}, "",
			"shout: demo-shop/staging runs an image that does not declare shout", http.StatusBadRequest, 0},
		{"unreachable", []error{refused}, console.FlagChange{Name: "theme", Set: true, Value: "dark"}, "",
			"Overrides could not be written: connection refused", http.StatusBadGateway, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := natstest.New()
			store, _, _ := flagStore(t, fake, test.opens...)
			var audit strings.Builder
			store.Audit = func(line string) { audit.WriteString(line) }
			problem, status := store.Change(context.Background(), flagInstance, tree, test.change)
			if audit.String() != test.audit || !strings.HasPrefix(problem, test.problem) ||
				(problem == "") != (test.problem == "") || status != test.status || len(fake.Writes) != test.writes {
				t.Fatalf("%q, %q, %d, %d writes", audit.String(), problem, status, len(fake.Writes))
			}
		})
	}
}
