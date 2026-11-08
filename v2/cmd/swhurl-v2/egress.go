package main

import (
	"encoding/json"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// allocationAt is the records' allocation at the revision they were read at, keeping every port the previous
// document gave.
func allocationAt(pinned snapshot, previous kernel.EgressDocument, profile kernel.PlatformProfile) (kernel.EgressDocument, error) {
	allocations, err := kernel.Allocate(egressFacts(pinned), previous.Instances, profile.Backup.Host())
	if err != nil {
		return kernel.EgressDocument{}, err
	}
	return kernel.EgressDocument{Kind: "v2-egress", Version: 1, Revision: pinned.Revision, Instances: allocations}, nil
}

// egressFacts is what the records say about each instance's way out: the hosts its grants declare, and a database
// whose nightly backup must upload through the same port.
func egressFacts(pinned snapshot) []kernel.EgressFacts {
	facts := make([]kernel.EgressFacts, 0, len(pinned.Instances))
	for _, instance := range pinned.Instances {
		facts = append(facts, kernel.EgressFacts{Application: instance.Application, Environment: instance.Environment,
			Grants: declaredEgress(instance), Database: declaredDatabase(instance)})
	}
	return facts
}

// declaredEgress is the hosts the instance's own record grants it. The platform's backup host is not among them: it is
// the profile's grant, added to every instance that keeps a database.
func declaredEgress(instance snapshotInstance) []string {
	var record struct {
		Grants struct{ Egress []string }
	}
	if json.Unmarshal(instance.Records["instance"], &record) != nil {
		return nil
	}
	return record.Grants.Egress
}

// declaredDatabase is whether the instance's release keeps a database, whose nightly backup uploads through
// the instance's own proxy port.
func declaredDatabase(instance snapshotInstance) bool {
	var release struct {
		Database string
	}
	return json.Unmarshal(instance.Records["release"], &release) == nil && release.Database != ""
}

// allocation reads an allocation document the platform wrote, for the commands that take one.
func (c cli) allocation(path, what string) (kernel.EgressDocument, int) {
	data, status := c.read(path, what)
	if status != 0 {
		return kernel.EgressDocument{}, status
	}
	document, err := kernel.DecodeEgressDocument(data)
	if err != nil {
		return kernel.EgressDocument{}, fail(c.diagnostic, err)
	}
	return document, 0
}
