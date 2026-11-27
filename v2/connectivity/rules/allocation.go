package rules

import (
	"encoding/json"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Allocation is the records' egress allocation at the revision they were read at, keeping every port the previous
// document gave. A database's nightly backup leaves through the instance's own port, so backupHost is granted to every
// instance that keeps one.
func Allocation(revision string, instances []Records, previous kernel.EgressDocument,
	backupHost string) (kernel.EgressDocument, error) {
	allocations, err := kernel.Allocate(egressFacts(instances), previous.Instances, backupHost)
	if err != nil {
		return kernel.EgressDocument{}, err
	}
	return kernel.EgressDocument{Kind: "v2-egress", Version: 1, Revision: revision, Instances: allocations}, nil
}

// egressFacts is what the records say about each instance's way out: the hosts its grants declare, and whether it
// keeps a database whose backup must upload through the same port.
func egressFacts(instances []Records) []kernel.EgressFacts {
	facts := make([]kernel.EgressFacts, 0, len(instances))
	for _, instance := range instances {
		facts = append(facts, kernel.EgressFacts{Application: instance.Application, Environment: instance.Environment,
			Grants: declaredEgress(instance), Database: declaredDatabase(instance)})
	}
	return facts
}

// declaredEgress is the hosts the instance's own record grants it. The platform's backup host is not among them: it
// is the profile's grant, added to every instance that keeps a database.
func declaredEgress(instance Records) []string {
	var record struct {
		Grants struct{ Egress []string }
	}
	if json.Unmarshal(instance.Instance, &record) != nil {
		return nil
	}
	return record.Grants.Egress
}

// declaredDatabase is whether the instance's release keeps a database.
func declaredDatabase(instance Records) bool {
	var release struct {
		Database string
	}
	return json.Unmarshal(instance.Release, &release) == nil && release.Database != ""
}
