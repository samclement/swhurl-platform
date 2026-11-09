package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// A declaration change asked for as a delta: what to connect and what to disconnect, applied to the list the pinned
// record states. The console's reader shows what the cluster holds now, and a change that was merged but not yet
// applied is not in it; asking for additions and removals instead of a whole list is what keeps that change from
// being silently undone by the next one, whichever way round the two arrive.

// ConnectionSettings is the settings a connect or a disconnect writes, in v2's own words: the calls list and then the
// subscriptions one, and only those the change alters. A change that alters neither is refused before any file is
// written, in the words the operator's form can show.
func ConnectionSettings(current kernel.InstanceSpec, calls, subscriptions, dropCalls, dropSubscriptions []string) ([]string, error) {
	settings, err := declarationSetting("calls", current.Calls, calls, dropCalls)
	if err != nil {
		return nil, err
	}
	subscriptions, err = declarationSetting("subscriptions", current.Events.Subscriptions, subscriptions, dropSubscriptions)
	if err != nil {
		return nil, err
	}
	return append(settings, subscriptions...), nil
}

// AllowSettings is the one setting a change to who may call an instance writes: the allows list of the record, with
// the grants it already states kept.
func AllowSettings(current kernel.InstanceSpec, allows, disallows []string) ([]string, error) {
	settings, err := declarationSetting("allows", current.Allows, allows, disallows)
	if err != nil {
		return nil, err
	}
	return settings, nil
}

// declarationSetting is one list's change as one setting, or nothing when the change leaves the list as it is.
func declarationSetting(name string, current, adds, drops []string) ([]string, error) {
	if len(adds) == 0 && len(drops) == 0 {
		return nil, nil
	}
	merged := kernel.MergeDeclarations(current, adds, drops)
	if equalDeclaration(merged, current) {
		return nil, &Refusal{"change", "nothing_changed: " + name + " already states exactly that"}
	}
	return []string{name + "=" + joinList(merged)}, nil
}

// equalDeclaration is whether two declaration lists state the same names in the same order.
func equalDeclaration(made, current []string) bool {
	if len(made) != len(current) {
		return false
	}
	for index := range made {
		if made[index] != current[index] {
			return false
		}
	}
	return true
}

// joinList is a declaration list as a setting's value.
func joinList(names []string) string {
	value := ""
	for index, name := range names {
		if index > 0 {
			value += ","
		}
		value += name
	}
	return value
}
