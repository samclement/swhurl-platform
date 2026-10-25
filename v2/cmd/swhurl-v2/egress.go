package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// egressDocument is the platform's allocation: which port on the proxy each instance reaches the internet
// through, and the hosts it declares it needs. The port is how the proxy names the caller, so it is kept —
// a committed document gives an instance the same port every time, and only a new instance takes a new one.
type egressDocument struct {
	Kind      string            `json:"kind"`
	Version   int               `json:"version"`
	Revision  string            `json:"revision"`
	Instances []egressAllocated `json:"instances"`
}

type egressAllocated struct {
	Application string   `json:"application"`
	Environment string   `json:"environment"`
	Port        int32    `json:"port"`
	Grants      []string `json:"grants"`
}

// firstPort is where a new allocation starts: above the unprivileged range and stable across runs.
const firstPort = 30101

// egress allocates one proxy port per instance, keeping every port the previous document already gave.
func (c cli) egress(r request) int {
	data, status := c.read(r.snapshot, "snapshot")
	if status != 0 {
		return status
	}
	pinned, err := decodeSnapshot(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	previous := egressDocument{}
	if r.previous != "" {
		earlier, status := c.read(r.previous, "previous allocation")
		if status != 0 {
			return status
		}
		if previous, err = decodeEgress(earlier); err != nil {
			return fail(c.diagnostic, err)
		}
	}
	document := allocate(pinned, previous)
	encoded, err := encodeEgress(document)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintf(c.diagnostic, "egress: %d instance(s) allocated\n", len(document.Instances))
	return emit(c.out, c.diagnostic, encoded)
}

// allocate gives every instance in the snapshot a port: the one it already had, or the lowest free one.
func allocate(pinned snapshot, previous egressDocument) egressDocument {
	kept := map[string]int32{}
	used := map[int32]bool{}
	// v1's instances already hold ports in the proxy's configuration. Seeding from them does two things: a
	// migrated instance keeps the port its callers' grants and logs already name, and a new allocation can
	// never land on a port the proxy is serving for someone else.
	for _, instance := range pinned.Instances {
		if port := declaredPort(instance); port != 0 {
			kept[instance.Environment+"/"+instance.Application] = port
			used[port] = true
		}
	}
	for _, allocated := range previous.Instances {
		kept[allocated.Environment+"/"+allocated.Application] = allocated.Port
		used[allocated.Port] = true
	}
	document := egressDocument{Kind: "v2-egress", Version: 1, Revision: pinned.Revision,
		Instances: make([]egressAllocated, 0, len(pinned.Instances))}
	next := int32(firstPort)
	for _, instance := range pinned.Instances {
		port, had := kept[instance.Environment+"/"+instance.Application]
		if !had {
			for used[next] {
				next++
			}
			port, used[next] = next, true
		}
		document.Instances = append(document.Instances, egressAllocated{Application: instance.Application,
			Environment: instance.Environment, Port: port, Grants: declaredGrants(instance)})
	}
	sort.Slice(document.Instances, func(i, j int) bool {
		a, b := document.Instances[i], document.Instances[j]
		return a.Environment+" "+a.Application < b.Environment+" "+b.Application
	})
	return document
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
	sort.Strings(settings.Egress)
	return settings.Egress
}

// decodeEgress reads a previous allocation, refusing a document this command did not write.
func decodeEgress(data []byte) (egressDocument, error) {
	var document egressDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return egressDocument{}, fmt.Errorf("invalid: egress: single_json_object_required")
	}
	if document.Version != 1 || document.Kind != "v2-egress" {
		return egressDocument{}, fmt.Errorf("unsupported: egress: only v2-egress version 1")
	}
	for _, allocated := range document.Instances {
		if allocated.Port < 1024 || allocated.Port > 65535 || allocated.Application == "" || allocated.Environment == "" {
			return egressDocument{}, fmt.Errorf("invalid: egress: allocation_required")
		}
	}
	return document, nil
}

func encodeEgress(document egressDocument) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// resolutionFiles reads the optional documents compile takes: the resolved connections and the egress
// allocation, each reduced to this instance's own facts.
func (c cli) resolutionFiles(r request, spec kernel.InstanceSpec) (kernel.Connections, *kernel.Egress, int) {
	connections := kernel.Connections{}
	if r.callFile != "" {
		data, status := c.read(r.callFile, "calls")
		if status != 0 {
			return kernel.Connections{}, nil, status
		}
		resolved, err := compileConnections(data, spec.Application, spec.Environment)
		if err != nil {
			return kernel.Connections{}, nil, fail(c.diagnostic, err)
		}
		connections = resolved
	}
	var egress *kernel.Egress
	if r.egressFile != "" {
		data, status := c.read(r.egressFile, "egress")
		if status != 0 {
			return kernel.Connections{}, nil, status
		}
		allocated, err := compileEgress(data, spec.Application, spec.Environment)
		if err != nil {
			return kernel.Connections{}, nil, fail(c.diagnostic, err)
		}
		egress = allocated
	}
	return connections, egress, 0
}

// compileEgress picks one instance's allocation out of the document.
func compileEgress(data []byte, application, environment string) (*kernel.Egress, error) {
	document, err := decodeEgress(data)
	if err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(document.Revision) {
		return nil, fmt.Errorf("invalid: egress: revision_required")
	}
	for _, allocated := range document.Instances {
		if allocated.Application == application && allocated.Environment == environment {
			return &kernel.Egress{Port: allocated.Port, Grants: allocated.Grants}, nil
		}
	}
	return nil, fmt.Errorf("missing: egress: the instance has no allocation")
}
