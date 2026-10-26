package kernel

import (
	"slices"
	"sort"
)

// The platform allocates one proxy port to every instance that needs one. It is a pure decision over the
// facts the caller supplies — the port an instance already holds, the hosts it declares, whether its release
// keeps a database — so it is testable without a checkout, a clock or a cluster. What it produces is an
// allocation document: the port is how the proxy names the caller, so a port, once given, is kept.

// FirstPort is where a new allocation starts: above the unprivileged range and stable across runs.
const FirstPort int32 = 30101

// EgressFacts is what one instance needs for the decision: who it is, the port it already holds in the
// platform's configuration (zero for none), the hosts it declares, and whether its release keeps a database
// whose nightly backup must upload through the same port.
type EgressFacts struct {
	Application string
	Environment string
	Port        int32
	Grants      []string
	Database    bool
}

// NeedsEgress is whether an instance depends on the proxy at all: a grant it declares, or a database whose
// backup uploads through its port. An instance with neither is given none, rather than one the proxy serves
// for nobody.
func (f EgressFacts) NeedsEgress() bool { return len(f.Grants) > 0 || f.Database }

// Allocate gives every instance that needs one a port: the one it already holds, or the lowest free one. An
// allocation whose instance the caller no longer lists is kept as it was — deleting an instance's records
// does not take its port away from an instance the platform still owns. Two instances on one port is a
// refusal: the port names the caller, so one of them would answer for the other.
func Allocate(facts []EgressFacts, previous []EgressAllocation) ([]EgressAllocation, error) {
	kept := make(map[string]int32, len(facts)+len(previous))
	used := make(map[int32]bool, len(facts)+len(previous))
	for _, fact := range facts {
		if fact.Port != 0 {
			kept[fact.Environment+"/"+fact.Application] = fact.Port
			used[fact.Port] = true
		}
	}
	for _, allocated := range previous {
		kept[allocated.Environment+"/"+allocated.Application] = allocated.Port
		used[allocated.Port] = true
	}
	// The order the caller lists instances in is not a fact about them: a new port is the lowest free one in
	// the document's own order, so the same facts always give the same document.
	ordered := slices.Clone(facts)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		return a.Environment+" "+a.Application < b.Environment+" "+b.Application
	})
	present := make(map[string]bool, len(facts))
	for _, fact := range facts {
		present[fact.Environment+"/"+fact.Application] = true
	}
	allocations := make([]EgressAllocation, 0, len(facts)+len(previous))
	next := FirstPort
	for _, fact := range ordered {
		allocated, given := allocateOne(fact, kept, used, &next)
		if given {
			allocations = append(allocations, allocated)
		}
	}
	for _, allocated := range previous {
		if !present[allocated.Environment+"/"+allocated.Application] {
			allocations = append(allocations, EgressAllocation{Application: allocated.Application,
				Environment: allocated.Environment, Port: allocated.Port, Grants: sortedGrants(allocated.Grants)})
		}
	}
	if err := distinctAllocationPorts(allocations); err != nil {
		return nil, err
	}
	sort.Slice(allocations, func(i, j int) bool {
		a, b := allocations[i], allocations[j]
		return a.Environment+" "+a.Application < b.Environment+" "+b.Application
	})
	return allocations, nil
}

// distinctAllocationPorts refuses a port two instances would share: the port names the caller.
func distinctAllocationPorts(allocations []EgressAllocation) error {
	seen := make(map[int32]string, len(allocations))
	for _, allocated := range allocations {
		key := allocated.Environment + "/" + allocated.Application
		if other, taken := seen[allocated.Port]; taken && other != key {
			return refuse(Invalid, "egress.port", "two instances cannot share one port: the port names the caller")
		}
		seen[allocated.Port] = key
	}
	return nil
}

// allocateOne is one instance's allocation: the port it holds, the lowest free one, or none when nothing
// about it leaves the cluster. next is the lowest port not yet tried, so the search stays linear.
func allocateOne(fact EgressFacts, kept map[string]int32, used map[int32]bool, next *int32) (EgressAllocation, bool) {
	port, had := kept[fact.Environment+"/"+fact.Application]
	if !had && !fact.NeedsEgress() {
		return EgressAllocation{}, false
	}
	if !had {
		for used[*next] {
			*next++
		}
		port, used[*next] = *next, true
	}
	return EgressAllocation{Application: fact.Application, Environment: fact.Environment,
		Port: port, Grants: sortedGrants(fact.Grants)}, true
}

// sortedGrants is an allocation's grants in one order, so the document is the same bytes for the same facts.
func sortedGrants(grants []string) []string {
	sorted := slices.Clone(grants)
	slices.Sort(sorted)
	if sorted == nil {
		return []string{}
	}
	return sorted
}
