package main

import (
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// renderAuth is the auth objects for every instance's namespace, and the namespaces they are for.
func renderAuth(pinned snapshot, profile kernel.PlatformProfile) ([]string, kernel.ResourceSet, error) {
	namespaces := make([]string, 0, len(pinned.Instances))
	for _, instance := range pinned.Instances {
		namespaces = append(namespaces, kernel.NamespaceOf(instance.Application, instance.Environment))
	}
	rendered, err := kernel.RenderAuth(namespaces, profile)
	return namespaces, rendered, err
}
