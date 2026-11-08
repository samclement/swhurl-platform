package main

import (
	"encoding/json"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// renderReloader is Reloader's values for the records, and the namespaces they watch: the platform's own as the
// profile states them, then one per instance that declares a Secret a change must restart it for.
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
