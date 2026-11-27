package rules_test

import (
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/connectivity/rules"
	"github.com/samclement/swhurl-platform/v2/kernel"
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
			got, err := rules.ConnectionSettings(current, scenario.calls, scenario.subscriptions,
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
	if _, err := rules.ConnectionSettings(current, nil, nil, nil, nil); err != nil {
		t.Errorf("an empty change is the caller's to refuse before this: %v", err)
	}
	if _, err := rules.ConnectionSettings(current, []string{"orders"}, nil, nil, nil); err == nil {
		t.Error("a change that alters no list was not refused")
	}
}

// Who may call an instance keeps the grants the record already states, and only a change that alters the list is
// written.
func TestAllowSettings(t *testing.T) {
	current := kernel.InstanceSpec{Allows: []string{"web", "team:payments"}}
	got, err := rules.AllowSettings(current, []string{"billing"}, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "allows=team:payments,billing" {
		t.Errorf("settings = %v", got)
	}
	if got, err := rules.AllowSettings(current, nil, nil); err != nil || got != nil {
		t.Errorf("an empty change = %v, %v", got, err)
	}
	// removing a grant the record does not state is nothing, and nothing is a refusal
	if _, err := rules.AllowSettings(current, nil, []string{"billing"}); err == nil {
		t.Error("a change that removes an absent grant was not refused")
	}
	if got, err := rules.AllowSettings(current, []string{"billing"}, []string{"billing"}); err != nil ||
		strings.Join(got, ",") != "allows=web,team:payments,billing" {
		t.Errorf("an addition beside its own removal = %v, %v", got, err)
	}
}

// Declaration deltas preserve the record's order, deduplicate additions, and apply removals before additions.
func TestConnectionSettingsMergeDeclarations(t *testing.T) {
	for _, scenario := range []struct {
		name                 string
		current, adds, drops []string
		want                 string
		refused              bool
	}{
		{name: "keep order and existing additions", current: []string{"b", "a", "c"}, adds: []string{"d", "a", "d"}, drops: []string{"c"}, want: "calls=b,a,d"},
		{name: "untouched list", current: []string{"b", "a", "c"}},
		{name: "first declaration", adds: []string{"a", "a"}, want: "calls=a"},
		{name: "remove every declaration", current: []string{"a", "b"}, drops: []string{"a", "b"}, want: "calls="},
		{name: "remove absent declaration", current: []string{"a"}, drops: []string{"b"}, refused: true},
		{name: "remove from empty list", drops: []string{"a"}, refused: true},
		{name: "existing addition changes nothing", current: []string{"b", "a"}, adds: []string{"a"}, refused: true},
		{name: "addition wins over removal", current: []string{"a", "b"}, adds: []string{"a"}, drops: []string{"a"}, want: "calls=b,a"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			before := strings.Join(scenario.current, ",")
			got, err := rules.ConnectionSettings(kernel.InstanceSpec{Calls: scenario.current}, scenario.adds, nil, scenario.drops, nil)
			if (err != nil) != scenario.refused {
				t.Fatalf("settings = %v, error = %v, want refused = %v", got, err, scenario.refused)
			}
			if strings.Join(got, ",") != scenario.want {
				t.Errorf("settings = %v, want %q", got, scenario.want)
			}
			if strings.Join(scenario.current, ",") != before {
				t.Errorf("current declarations were mutated: %v", scenario.current)
			}
		})
	}
}
