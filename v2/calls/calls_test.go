package calls_test

import (
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/calls"
)

// declaration is one instance: what it calls and who may call it.
func declaration(application, environment, team string, callsDeclared, allowsDeclared []string) calls.Declaration {
	return calls.Declaration{Application: application, Environment: environment, Team: team,
		Kind: "web", Port: 8080, Calls: callsDeclared, Allows: allowsDeclared}
}

func resolve(t *testing.T, declarations ...calls.Declaration) calls.Resolution {
	t.Helper()
	resolution, err := calls.Resolve(declarations)
	if err != nil {
		t.Fatal(err)
	}
	return resolution
}

// A pair connects only when the caller asks and the callee consents, by app name or by team.
func TestConnectedPairsNeedBothSides(t *testing.T) {
	resolution := resolve(t,
		declaration("shop", "staging", "demo", []string{"orders"}, nil),
		declaration("orders", "staging", "demo", nil, []string{"shop"}),
		declaration("ledger", "staging", "payments", []string{"orders"}, nil),
	)
	// orders allows shop by name, so ledger (another team's app) waits for consent.
	if len(resolution.Pairs) != 1 || resolution.Pairs[0] != (calls.Pair{"shop", "orders", "staging"}) {
		t.Fatalf("pairs: %+v", resolution.Pairs)
	}
	if len(resolution.Waiting) != 1 || resolution.Waiting[0] != (calls.Wait{"ledger", "orders", "staging", calls.NoConsent}) {
		t.Fatalf("waiting %+v", resolution.Waiting)
	}
	if len(resolution.Refused) != 0 {
		t.Fatalf("refused: %+v", resolution.Refused)
	}

	// A team grant follows the caller's team, and only for that team.
	team := resolve(t,
		declaration("orders", "staging", "demo", nil, []string{"team:demo"}),
		declaration("shop", "staging", "demo", []string{"orders"}, nil),
		declaration("ledger", "staging", "payments", []string{"orders"}, nil),
	)
	if len(team.Pairs) != 1 || team.Pairs[0].Caller != "shop" {
		t.Fatalf("a team grant: %+v", team.Pairs)
	}
	if len(team.Waiting) != 1 || team.Waiting[0] != (calls.Wait{"ledger", "orders", "staging", calls.NoConsent}) {
		t.Fatalf("a caller outside the team: %+v", team.Waiting)
	}
}

// The same app in another environment is a different instance: staging calls staging.
func TestEnvironmentsAreSeparate(t *testing.T) {
	resolution := resolve(t,
		declaration("shop", "staging", "demo", []string{"orders"}, nil),
		declaration("orders", "prod", "demo", nil, []string{"shop"}),
	)
	if len(resolution.Pairs) != 0 || len(resolution.Waiting) != 1 || resolution.Waiting[0].Reason != calls.CalleeAbsent {
		t.Fatalf("cross-environment: %+v / %+v", resolution.Pairs, resolution.Waiting)
	}
}

// Three outcomes are refusals whatever the other side says, and two are waits that render nothing.
func TestWaitingAndRefusedDeclarations(t *testing.T) {
	resolution := resolve(t,
		declaration("shop", "staging", "demo", []string{"orders", "worker", "shop"}, nil),
		declaration("orders", "staging", "demo", nil, nil),
		calls.Declaration{Application: "worker", Environment: "staging", Team: "demo", Kind: "worker", Port: 8080},
	)
	refusals := map[string]string{}
	for _, refusal := range resolution.Refused {
		refusals[refusal.Callee] = refusal.Reason
	}
	if len(refusals) != 2 || refusals["worker"] != calls.CalleeNotWeb || refusals["shop"] != calls.SelfCall {
		t.Fatalf("refusals: %+v", resolution.Refused)
	}
	if len(resolution.Waiting) != 1 || resolution.Waiting[0] != (calls.Wait{"shop", "orders", "staging", calls.NoConsent}) {
		t.Fatalf("waiting: %+v", resolution.Waiting)
	}
}

// A malformed declaration is an error even when nothing would use it.
func TestMalformedDeclarations(t *testing.T) {
	for _, tt := range []struct {
		name         string
		declarations []calls.Declaration
		field        string
		reason       string
	}{
		{"a grant with no team", []calls.Declaration{declaration("orders", "staging", "demo", nil, []string{"team:"})}, "allows", "invalid_team"},
		{"a grant with a bad name", []calls.Declaration{declaration("orders", "staging", "demo", nil, []string{"Not-An-App"})}, "allows", "invalid_app"},
		{"a call with a bad name", []calls.Declaration{declaration("shop", "staging", "demo", []string{"orders/one"}, nil)}, "calls", "invalid_app"},
		{"two declarations of one instance", []calls.Declaration{
			declaration("shop", "staging", "demo", nil, nil), declaration("shop", "staging", "demo", nil, nil),
		}, "calls", "repeated_instance"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := calls.Resolve(tt.declarations)
			var refusal *calls.Error
			if !errors.As(err, &refusal) || refusal.Field != tt.field || refusal.Reason != tt.reason {
				t.Fatalf("got %v, want %s/%s", err, tt.field, tt.reason)
			}
		})
	}
}

// A grant nobody uses renders nothing and is not an error: consent outlives the caller's declaration.
func TestAnUnusedGrantIsNotAnError(t *testing.T) {
	resolution := resolve(t, declaration("orders", "staging", "demo", nil, []string{"shop", "team:payments"}))
	if len(resolution.Pairs) != 0 || len(resolution.Waiting) != 0 || len(resolution.Refused) != 0 {
		t.Fatalf("an unused grant: %+v", resolution)
	}
}

// The same declarations always resolve the same way, in the same order.
func TestResolutionIsDeterministic(t *testing.T) {
	declarations := []calls.Declaration{
		declaration("c", "staging", "t", []string{"a", "b"}, nil),
		declaration("a", "staging", "t", nil, []string{"c"}),
		declaration("b", "staging", "t", nil, []string{"c"}),
	}
	first, second := resolve(t, declarations...), resolve(t, declarations...)
	if len(first.Pairs) != 2 || first.Pairs[0] != second.Pairs[0] || first.Pairs[1] != second.Pairs[1] {
		t.Fatalf("order: %+v / %+v", first.Pairs, second.Pairs)
	}
}
