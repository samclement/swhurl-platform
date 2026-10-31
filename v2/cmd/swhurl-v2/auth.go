package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// authDocument is what `auth` writes: the platform's verification facts rendered once for every managed
// namespace, with the source revision it was read at and the profile it was rendered for. The files are the
// evidence — the keys are the whole of it — and each namespace's object is identical but for its namespace.
type authDocument struct {
	Kind       string         `json:"kind"`
	Version    int            `json:"version"`
	Revision   string         `json:"revision"`
	Inputs     authInputs     `json:"inputs"`
	Namespaces []string       `json:"namespaces"`
	Files      []kernel.File  `json:"files"`
	Inventory  []kernel.Claim `json:"inventory"`
}

// authInputs names what the render was given: the profile, which decides the keys.
type authInputs struct {
	Profile kernel.ProfileID `json:"profile"`
}

// auth renders the platform's keys for every managed namespace in one pinned snapshot. It reads the two named
// files and, with --out, writes the rendered files and nothing else.
func (c cli) auth(r request) int {
	data, status := c.read(r.snapshot, "snapshot")
	if status != 0 {
		return status
	}
	pinned, err := decodeSnapshot(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	profileData, status := c.read(r.profile, "profile")
	if status != 0 {
		return status
	}
	profile, err := kernel.DecodeProfile(profileData)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	profileID, err := profile.ID()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	namespaces, rendered, err := renderAuth(pinned, profile)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document := authDocument{Kind: "v2-auth", Version: 1, Revision: pinned.Revision,
		Inputs: authInputs{Profile: profileID}, Namespaces: namespaces, Files: rendered.Files,
		Inventory: rendered.Inventory}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fail(c.diagnostic, err)
	}
	encoded := bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
	fmt.Fprintf(c.diagnostic, "auth: %d namespace(s), %d file(s)\n", len(document.Namespaces), len(document.Files))
	if r.out != "" {
		return c.writeFiles(r.out, rendered.Files)
	}
	return emit(c.out, c.diagnostic, encoded)
}

// renderAuth is the auth objects for every instance's namespace in one snapshot, and the namespaces they are for.
func renderAuth(pinned snapshot, profile kernel.PlatformProfile) ([]string, kernel.ResourceSet, error) {
	namespaces := make([]string, 0, len(pinned.Instances))
	for _, instance := range pinned.Instances {
		namespaces = append(namespaces, kernel.NamespaceOf(instance.Application, instance.Environment))
	}
	rendered, err := kernel.RenderAuth(namespaces, profile)
	return namespaces, rendered, err
}
