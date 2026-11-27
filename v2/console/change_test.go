package console

import (
	"reflect"
	"slices"
	"testing"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// The table names exactly pages' kinds, in pages' order, each with pages' own row, so a branch Activity reads and a
// change the command opens cannot disagree.
func TestChangesAreThePagesKinds(t *testing.T) {
	branches := pages.ConsoleChanges()
	ours := Changes()
	if len(ours) != len(branches) {
		t.Fatalf("console has %d kinds, pages %d", len(ours), len(branches))
	}
	for i, one := range ours {
		if !reflect.DeepEqual(one.ConsoleChange, branches[i]) {
			t.Errorf("kind %d is %+v here and %+v in pages", i, one.ConsoleChange, branches[i])
		}
	}
}

// Every kind plans what it runs, each operation once, and needs only flags it takes.
func TestChangesAreWhole(t *testing.T) {
	for _, one := range Changes() {
		if len(one.Operations) == 0 {
			t.Errorf("%s plans no operation", one.Name)
		}
		for i, operation := range one.Operations {
			if slices.Contains(one.Operations[:i], operation) {
				t.Errorf("%s plans %s twice", one.Name, operation)
			}
		}
		for _, flag := range slices.Concat(one.Needs, one.Any) {
			if !slices.Contains(one.Takes, flag) {
				t.Errorf("%s needs --%s, which it does not take", one.Name, flag)
			}
		}
		if (one.Team() && one.Operations[0] != Team) || (!one.Team() && slices.Contains(one.Operations, Team)) {
			t.Errorf("%s plans %v for a change to %s", one.Name, one.Operations, one.Target("x", "staging"))
		}
	}
}

func TestChangeNamed(t *testing.T) {
	if one, found := ChangeNamed("v2-promote"); !found || one.TargetEnv != "prod" ||
		!slices.Equal(one.Operations, []Operation{Promote, Render}) {
		t.Errorf("v2-promote reads as %+v, %v", one, found)
	}
	if one, found := ChangeNamed("v2-nowhere"); found || one.Name != "" {
		t.Errorf("a kind the console does not open reads as %+v", one)
	}
}

// A kind refuses a flag it does not take before one it needs, names the first such flag by name, and refuses a group
// it needs one of only when none is given.
func TestCheckFlags(t *testing.T) {
	for _, test := range []struct {
		kind   string
		stated map[string]bool
		want   string
	}{
		{"v2-scale", map[string]bool{"set": true}, ""},
		{"v2-scale", map[string]bool{"set": false}, "invalid: --set: v2-scale needs it"},
		{"v2-scale", map[string]bool{"set": true, "image": true}, "invalid: --image: v2-scale does not take it"},
		{"v2-remove", map[string]bool{"set": true, "allow": true}, "invalid: --allow: v2-remove does not take it"},
		{"v2-remove", map[string]bool{"set": false, "allow": false}, ""},
		{"new", map[string]bool{"set": true}, "invalid: --image: new needs it"},
		{"new", map[string]bool{"set": true, "image": true, "preface": true}, ""},
		{"v2-connect", map[string]bool{}, "invalid: v2-connect needs one of --call, --subscribe"},
		{"v2-connect", map[string]bool{"subscribe": true}, ""},
		{"v2-allow", map[string]bool{"disallow": true}, ""},
		{"team-edit", map[string]bool{"rename": true}, ""},
		{"team-remove", map[string]bool{"description": true}, "invalid: --description: team-remove does not take it"},
	} {
		one, found := ChangeNamed(test.kind)
		if !found {
			t.Fatalf("%s is not a kind", test.kind)
		}
		got := ""
		if err := one.CheckFlags(test.stated); err != nil {
			got = err.Error()
		}
		if got != test.want {
			t.Errorf("%s %v: got %q, want %q", test.kind, test.stated, got, test.want)
		}
	}
}
