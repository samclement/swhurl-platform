package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// reloaderDocument is what `reloader` writes: the namespaces the platform watches, the revision its facts came
// from, and the object it claims. The list itself is the evidence.
type reloaderDocument struct {
	Kind       string         `json:"kind"`
	Version    int            `json:"version"`
	Revision   string         `json:"revision"`
	Namespaces int            `json:"namespaces"`
	Files      []kernel.File  `json:"files"`
	Inventory  []kernel.Claim `json:"inventory"`
}

// reloader renders the values Reloader's HelmRelease reads from one pinned snapshot. The platform's own
// namespaces are the profile's; an instance's is watched when it declares a Secret that a change must restart
// it for, so the list says what that revision of Git says.
func (c cli) reloader(r request) int {
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
	namespaces, rendered, err := renderReloader(pinned, profile)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document := reloaderDocument{Kind: "v2-reloader", Version: 1, Revision: pinned.Revision,
		Namespaces: len(namespaces), Files: rendered.Files, Inventory: rendered.Inventory}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fail(c.diagnostic, err)
	}
	encoded := bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
	fmt.Fprintf(c.diagnostic, "reloader: %d namespace(s), %d file(s)\n", document.Namespaces, len(document.Files))
	if r.out != "" {
		return c.writeFiles(r.out, rendered.Files)
	}
	return emit(c.out, c.diagnostic, encoded)
}

// renderReloader is Reloader's values for one snapshot, and the namespaces they watch.
func renderReloader(pinned snapshot, profile kernel.PlatformProfile) ([]string, kernel.ResourceSet, error) {
	namespaces, err := reloaderFacts(pinned, profile.Reloader.Namespaces).Namespaces()
	if err != nil {
		return nil, kernel.ResourceSet{}, err
	}
	rendered, err := kernel.RenderReloader(namespaces)
	return namespaces, rendered, err
}

// reloaderFacts is every instance's declaration about its own Secrets: the public key it signs in with and the
// Secret names its release reads. An instance with neither is listed and the renderer leaves it out, so the
// decision stays in one place.
func reloaderFacts(pinned snapshot, platform []string) kernel.Reloader {
	reloader := kernel.Reloader{Platform: platform}
	for _, instance := range pinned.Instances {
		reloader.Instances = append(reloader.Instances, kernel.ReloaderInstance{
			Application: instance.Application, Environment: instance.Environment,
			BrokerKey: eventsKey(instance), Secrets: declaredSecrets(instance)})
	}
	return reloader
}

// declaredSecrets is the Secret names an instance's release declares its image reads, from either record shape:
// v2's release record states the same `secrets` list v1's does.
func declaredSecrets(instance snapshotInstance) []string {
	var release struct{ Secrets []string }
	if err := json.Unmarshal(instance.Records["release"], &release); err != nil {
		return nil
	}
	return release.Secrets
}
