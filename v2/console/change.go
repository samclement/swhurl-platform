package console

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// A change is one kind of pull request the console opens, as the command that opens it reads it: the branch and
// pull request pages states for the kind, the flags the kind takes, and the operations it runs, in order. Branch
// facts stay in pages until Activity's reading of a branch moves here; a kind is looked up there, never re-typed,
// and a test holds both lists to the same kinds.

// Operation is one of v2's own operations a change runs on its working copy, named as the command that runs it.
type Operation string

// The operations a change may run.
const (
	// Create states a new app's records from the image its repository published.
	Create Operation = "create"
	// Edit changes one setting of an instance's records.
	Edit Operation = "edit"
	// Remove deletes one instance and everything v2 rendered for it.
	Remove Operation = "remove"
	// Promote selects production's release from staging's record.
	Promote Operation = "promote"
	// Render writes what the whole tree of records derives.
	Render Operation = "render"
	// Team changes the team registry.
	Team Operation = "team"
)

// Change is one kind of console change.
type Change struct {
	// ConsoleChange is the kind's branch, title, environments and owned paths.
	pages.ConsoleChange
	// Takes are the flags the kind may state, Needs the ones it must, and Any a group it needs one of.
	Takes, Needs, Any []string
	// Operations are what the change runs, in this order; an operation may run more than once or not at all, as
	// an edit runs once per setting.
	Operations []Operation
}

// changes are every kind, in pages' order.
var changes = []Change{
	{ConsoleChange: branchOf("new"), Takes: []string{"set", "image", "preface"}, Needs: []string{"set", "image"},
		Operations: []Operation{Create, Edit, Render}},
	{ConsoleChange: branchOf("team-add"), Takes: []string{"description"}, Operations: []Operation{Team}},
	{ConsoleChange: branchOf("team-edit"), Takes: []string{"description", "rename"}, Operations: []Operation{Team}},
	{ConsoleChange: branchOf("team-remove"), Operations: []Operation{Team}},
	{ConsoleChange: branchOf("team-channel-set"), Takes: []string{"transport"}, Operations: []Operation{Team}},
	{ConsoleChange: branchOf("team-channel-remove"), Operations: []Operation{Team}},
	{ConsoleChange: branchOf("v2-scale"), Takes: []string{"set"}, Needs: []string{"set"},
		Operations: []Operation{Edit, Render}},
	{ConsoleChange: branchOf("v2-expose"), Takes: []string{"set"}, Needs: []string{"set"},
		Operations: []Operation{Edit, Render}},
	{ConsoleChange: branchOf("v2-connect"), Takes: []string{"call", "subscribe"}, Any: []string{"call", "subscribe"},
		Operations: []Operation{Edit, Render}},
	{ConsoleChange: branchOf("v2-disconnect"), Takes: []string{"drop-call", "drop-subscription"},
		Any: []string{"drop-call", "drop-subscription"}, Operations: []Operation{Edit, Render}},
	{ConsoleChange: branchOf("v2-allow"), Takes: []string{"allow", "disallow"}, Any: []string{"allow", "disallow"},
		Operations: []Operation{Edit, Render}},
	{ConsoleChange: branchOf("v2-team"), Takes: []string{"set"}, Needs: []string{"set"},
		Operations: []Operation{Edit, Render}},
	{ConsoleChange: branchOf("v2-remove"), Operations: []Operation{Remove, Render}},
	{ConsoleChange: branchOf("v2-promote"), Operations: []Operation{Promote, Render}},
}

// branchOf is pages' row for one kind; a kind pages does not know has no name, which the table's test refuses.
func branchOf(kind string) pages.ConsoleChange {
	found, _ := pages.ConsoleChangeNamed(kind)
	return found
}

// Changes is every kind of console change.
func Changes() []Change { return slices.Clone(changes) }

// ChangeNamed reads one kind by name; false for a kind the console does not open.
func ChangeNamed(kind string) (Change, bool) {
	for _, one := range changes {
		if one.Name == kind {
			return one, true
		}
	}
	return Change{}, false
}

// CheckFlags refuses a change that states a flag its kind does not take, or omits one it needs, in the change's own
// words rather than in a command's whole usage text. stated is every optional flag by name, true where it was given.
func (c Change) CheckFlags(stated map[string]bool) error {
	for _, flag := range slices.Sorted(maps.Keys(stated)) {
		if stated[flag] && !slices.Contains(c.Takes, flag) {
			return fmt.Errorf("invalid: --%s: %s does not take it", flag, c.Name)
		}
	}
	for _, flag := range c.Needs {
		if !stated[flag] {
			return fmt.Errorf("invalid: --%s: %s needs it", flag, c.Name)
		}
	}
	if len(c.Any) > 0 && !slices.ContainsFunc(c.Any, func(flag string) bool { return stated[flag] }) {
		return fmt.Errorf("invalid: %s needs one of --%s", c.Name, strings.Join(c.Any, ", --"))
	}
	return nil
}
