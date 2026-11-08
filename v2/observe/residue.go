package observe

import (
	"sort"
	"strings"
)

// The reasons a live object is listed as removed: the records no longer name an instance that still has a Flux
// unit, a HelmRelease, or both. The console keeps it visible until the namespace goes.
const (
	RemovedUnit           = "unit"
	RemovedRelease        = "release"
	RemovedUnitAndRelease = "unit_and_release"
)

// Removed is one instance the records no longer name but the cluster still shows, in stable app/environment order.
// It is not an instance with a report: its record is gone, so there is nothing to observe it against.
type Removed struct {
	Application, Environment, Reason string
}

const (
	// unitPrefix begins every instance's Flux unit: a unit is named after its namespace, and a namespace is named
	// after its instance. This is the compiler's own naming, not a guess about an unfamiliar object.
	unitPrefix     = "app-"
	stagingEnvName = "staging"
	prodEnvName    = "prod"
)

// InstanceOfUnit is the instance a Flux unit's name names: a unit is `app-<namespace>`, and a namespace is
// `<app>-<environment>`. A platform unit, whose name is not an app's, names no instance.
func InstanceOfUnit(name string) (application, environment string, ok bool) {
	namespace, prefixed := strings.CutPrefix(name, unitPrefix)
	if !prefixed {
		return "", "", false
	}
	return InstanceOfNamespace(namespace)
}

// InstanceOfNamespace is the instance a namespace names: `<app>-<environment>`, where an environment is one of the
// platform's two. A namespace with another suffix is not an instance's.
func InstanceOfNamespace(namespace string) (application, environment string, ok bool) {
	for _, candidate := range []string{stagingEnvName, prodEnvName} {
		if application, found := strings.CutSuffix(namespace, "-"+candidate); found && application != "" {
			return application, candidate, true
		}
	}
	return "", "", false
}

// Removed lists the units and releases the observation read that no compiled target claims: an instance whose
// record is gone while its namespace has not finished going. A list that could not be read contributes nothing,
// so a failed read never invents a removal. The candidate lists are the ones BulkPlan already asked for, so this
// reads nothing more.
func (c Candidates) Removed(targets []Target) []Removed {
	claimed := claimedInstances(targets)
	reason := map[[2]string]string{}
	for _, unit := range c.units(targets) {
		if application, environment, ok := InstanceOfUnit(unit); ok && !claimed[[2]string{application, environment}] {
			reason[[2]string{application, environment}] = RemovedUnit
		}
	}
	for _, release := range c.releases(targets) {
		key, ok := releaseInstance(release)
		if !ok || claimed[key] {
			continue
		}
		reason[key] = withRelease(reason[key])
	}
	return sortedRemoved(reason)
}

// claimedInstances is the identities the compiled targets name, by the same naming InstanceOf reads. A target
// whose unit names no instance claims by its unit's own text, so an unfamiliar name is never read as residue.
func claimedInstances(targets []Target) map[[2]string]bool {
	claimed := map[[2]string]bool{}
	for _, target := range targets {
		if application, environment, ok := InstanceOfUnit(target.Unit.Name); ok {
			claimed[[2]string{application, environment}] = true
		}
	}
	return claimed
}

// releaseInstance is the instance a listed release belongs to: its namespace names the instance and its own name
// must be the application, or it is not an app instance's release.
func releaseInstance(ref Ref) ([2]string, bool) {
	application, environment, ok := InstanceOfNamespace(ref.Namespace)
	if !ok || ref.Name != application {
		return [2]string{}, false
	}
	return [2]string{application, environment}, true
}

// withRelease is a removal's reason once a release is also unaccounted for.
func withRelease(known string) string {
	if known == RemovedUnit {
		return RemovedUnitAndRelease
	}
	return RemovedRelease
}

// sortedRemoved is every removal in stable app/environment order.
func sortedRemoved(reason map[[2]string]string) []Removed {
	removed := make([]Removed, 0, len(reason))
	for key, why := range reason {
		removed = append(removed, Removed{Application: key[0], Environment: key[1], Reason: why})
	}
	sort.Slice(removed, func(i, j int) bool {
		if removed[i].Application != removed[j].Application {
			return removed[i].Application < removed[j].Application
		}
		return removed[i].Environment < removed[j].Environment
	})
	return removed
}

// units is the names of every unit the read lists returned, in a stable order. Targets share one unit collection.
func (c Candidates) units(targets []Target) []string {
	if len(targets) == 0 {
		return nil
	}
	answer := c.answer(unitQuery(targets[0]))
	names := make([]string, 0, len(answer.objects))
	for ref := range answer.objects {
		names = append(names, ref.Name)
	}
	sort.Strings(names)
	return names
}

// releases is every release the read lists returned, in a stable order. Targets share one release collection.
func (c Candidates) releases(targets []Target) []Ref {
	if len(targets) == 0 {
		return nil
	}
	answer := c.answer(ownedQuery(targets[0].Release))
	refs := make([]Ref, 0, len(answer.objects))
	for ref := range answer.objects {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Namespace != refs[j].Namespace {
			return refs[i].Namespace < refs[j].Namespace
		}
		return refs[i].Name < refs[j].Name
	})
	return refs
}
