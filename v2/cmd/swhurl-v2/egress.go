package main

import (
	"encoding/json"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// egress allocates one proxy port per instance and writes the platform's allocation. The decision is the
// kernel's; this reads the snapshot's facts and the profile's backup host, hands them over and prints the
// document.
func (c cli) egress(r request) int {
	profile, status := c.profile(r.profile)
	if status != 0 {
		return status
	}
	data, status := c.read(r.snapshot, "snapshot")
	if status != 0 {
		return status
	}
	pinned, err := decodeSnapshot(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	previous := kernel.EgressDocument{}
	if r.previous != "" {
		earlier, status := c.read(r.previous, "previous allocation")
		if status != 0 {
			return status
		}
		if previous, err = kernel.DecodeEgressDocument(earlier); err != nil {
			return fail(c.diagnostic, err)
		}
	}
	allocations, err := kernel.Allocate(egressFacts(pinned), previous.Instances, profile.Backup.Host())
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document := kernel.EgressDocument{Kind: "v2-egress", Version: 1, Revision: pinned.Revision, Instances: allocations}
	encoded, err := kernel.EncodeEgressDocument(document)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintf(c.diagnostic, "egress: %d instance(s) allocated\n", len(document.Instances))
	return emit(c.out, c.diagnostic, encoded)
}

// egressFacts is what the snapshot says about each instance's way out: the port it already holds, the hosts
// it declares, and whether its release keeps a database whose backup must upload.
func egressFacts(pinned snapshot) []kernel.EgressFacts {
	facts := make([]kernel.EgressFacts, 0, len(pinned.Instances))
	for _, instance := range pinned.Instances {
		facts = append(facts, kernel.EgressFacts{Application: instance.Application, Environment: instance.Environment,
			Port: declaredPort(instance), Grants: declaredGrants(instance), Database: declaredDatabase(instance)})
	}
	return facts
}

// declaredPort is the port an instance already holds in the proxy's configuration, or zero.
func declaredPort(instance snapshotInstance) int32 {
	var settings struct {
		Allocations struct {
			EgressPort int32 `json:"egressPort"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil {
		return 0
	}
	return settings.Allocations.EgressPort
}

// declaredGrants is the hosts an instance says it needs, read from its record.
func declaredGrants(instance snapshotInstance) []string {
	var settings struct {
		Egress []string
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil {
		return []string{}
	}
	if settings.Egress == nil {
		return []string{}
	}
	return settings.Egress
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
