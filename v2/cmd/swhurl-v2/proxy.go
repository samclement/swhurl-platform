package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// proxyDocument is what `proxy` writes: the platform's egress proxy rendered from one allocation,
// with the source revision it was read at and the profile it was rendered for. The files are the
// evidence: a changed port or grant changes them, and nothing here is an identity of its own.
type proxyDocument struct {
	Kind      string         `json:"kind"`
	Version   int            `json:"version"`
	Revision  string         `json:"revision"`
	Inputs    proxyInputs    `json:"inputs"`
	Files     []kernel.File  `json:"files"`
	Inventory []kernel.Claim `json:"inventory"`
}

// proxyInputs names what the render was given. The allocation is not identity-bearing, because the
// port it decides is rendered into the files; the profile is, because it decides the proxy's namespace
// and pods.
type proxyInputs struct {
	Profile kernel.ProfileID `json:"profile"`
}

// proxy renders the proxy's own objects from the allocation: one port per instance, the hosts its
// grants allow, and a policy admitting each port from its own namespace. It reads the two named files
// and, with --out, writes the rendered files and nothing else.
func (c cli) proxy(r request) int {
	data, status := c.read(r.allocation, "allocation")
	if status != 0 {
		return status
	}
	allocation, err := checkedEgress(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	data, status = c.read(r.profile, "profile")
	if status != 0 {
		return status
	}
	profile, err := kernel.DecodeProfile(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	profileID, err := profile.ID()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	rendered, err := kernel.RenderProxy(proxyAllocations(allocation), profile)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document := proxyDocument{Kind: "v2-proxy", Version: 1, Revision: allocation.Revision,
		Inputs: proxyInputs{Profile: profileID}, Files: rendered.Files, Inventory: rendered.Inventory}
	encoded, err := encodeProxy(document)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintf(c.diagnostic, "proxy: %d instance port(s), %d file(s)\n", len(allocation.Instances), len(document.Files))
	if r.out != "" {
		return c.writeFiles(r.out, rendered.Files)
	}
	return emit(c.out, c.diagnostic, encoded)
}

// proxyAllocations is the allocation document as the renderer's own input.
func proxyAllocations(document egressDocument) []kernel.EgressAllocation {
	allocations := make([]kernel.EgressAllocation, 0, len(document.Instances))
	for _, allocated := range document.Instances {
		allocations = append(allocations, kernel.EgressAllocation{Application: allocated.Application,
			Environment: allocated.Environment, Port: allocated.Port, Grants: allocated.Grants})
	}
	return allocations
}

func encodeProxy(document proxyDocument) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}
