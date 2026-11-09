package orchestration_test

import (
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// A declaration change is a delta on the record's own list: what the record keeps, then what was asked for.
func TestConnectionSettingsAreADelta(t *testing.T) {
	current := kernel.InstanceSpec{Calls: []string{"orders"}, Events: kernel.Events{Subscriptions: []string{"orders.shipped"}}}
	for _, scenario := range []struct {
		name                                               string
		calls, subscriptions, dropCalls, dropSubscriptions []string
		want                                               string
	}{
		{name: "add a call", calls: []string{"billing"}, want: "calls=orders,billing"},
		{name: "add a subscription", subscriptions: []string{"billing.charged"}, want: "subscriptions=orders.shipped,billing.charged"},
		{name: "drop a call and add one", calls: []string{"billing"}, dropCalls: []string{"orders"},
			want: "calls=billing"},
		{name: "drop everything", dropCalls: []string{"orders"}, dropSubscriptions: []string{"orders.shipped"},
			want: "calls=,subscriptions="},
		{name: "an addition the record already states changes nothing else", calls: []string{"orders"},
			want: "nothing_changed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			got, err := orchestration.ConnectionSettings(current, scenario.calls, scenario.subscriptions,
				scenario.dropCalls, scenario.dropSubscriptions)
			if strings.Contains(scenario.want, "nothing_changed") {
				if err == nil || !strings.Contains(err.Error(), "nothing_changed") {
					t.Fatalf("err = %v, want nothing_changed", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if joined := strings.Join(got, ","); joined != scenario.want {
				t.Errorf("settings = %q, want %q", joined, scenario.want)
			}
		})
	}
}

// A connect with nothing to add is refused before a command runs, and so is an empty change.
func TestConnectionSettingsRefusals(t *testing.T) {
	current := kernel.InstanceSpec{Calls: []string{"orders"}}
	if _, err := orchestration.ConnectionSettings(current, nil, nil, nil, nil); err != nil {
		t.Errorf("an empty change is the caller's to refuse before this: %v", err)
	}
	if _, err := orchestration.ConnectionSettings(current, []string{"orders"}, nil, nil, nil); err == nil {
		t.Error("a change that alters no list was not refused")
	}
}

// Who may call an instance keeps the grants the record already states, and only a change that alters the list is
// written.
func TestAllowSettings(t *testing.T) {
	current := kernel.InstanceSpec{Allows: []string{"web", "team:payments"}}
	got, err := orchestration.AllowSettings(current, []string{"billing"}, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "allows=team:payments,billing" {
		t.Errorf("settings = %v", got)
	}
	if got, err := orchestration.AllowSettings(current, nil, nil); err != nil || got != nil {
		t.Errorf("an empty change = %v, %v", got, err)
	}
	// removing a grant the record does not state is nothing, and nothing is a refusal
	if _, err := orchestration.AllowSettings(current, nil, []string{"billing"}); err == nil {
		t.Error("a change that removes an absent grant was not refused")
	}
	if got, err := orchestration.AllowSettings(current, []string{"billing"}, []string{"billing"}); err != nil ||
		strings.Join(got, ",") != "allows=web,team:payments,billing" {
		t.Errorf("an addition beside its own removal = %v, %v", got, err)
	}
}

// A declaration the record states in one order is written back in that order, so an unchanged list is no diff.
func TestMergeDeclarationsKeepsOrder(t *testing.T) {
	current := []string{"b", "a", "c"}
	if got := kernel.MergeDeclarations(current, []string{"d", "a"}, []string{"c"}); strings.Join(got, ",") != "b,a,d" {
		t.Errorf("merged = %v", got)
	}
	if got := kernel.MergeDeclarations(current, nil, nil); strings.Join(got, ",") != "b,a,c" {
		t.Errorf("an untouched list = %v", got)
	}
	if got := kernel.MergeDeclarations(nil, []string{"a"}, nil); got[0] != "a" || len(got) != 1 {
		t.Errorf("a first declaration = %v", got)
	}
	if got := kernel.MergeDeclarations(nil, nil, []string{"a"}); got != nil {
		t.Errorf("a list emptied = %v, want nothing at all", got)
	}
}
