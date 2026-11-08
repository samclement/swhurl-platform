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

// egressFacts is what the records say about each instance's way out: a database's backup must upload, and no
// record declares another host.
func egressFacts(pinned snapshot) []kernel.EgressFacts {
	facts := make([]kernel.EgressFacts, 0, len(pinned.Instances))
	for _, instance := range pinned.Instances {
		facts = append(facts, kernel.EgressFacts{Application: instance.Application, Environment: instance.Environment,
			Database: declaredDatabase(instance)})
	}
	return facts
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
