package kernel

import (
	"slices"
	"strings"
)

// Reloader restarts an opted-in workload when a Secret it names changes. In scoped mode it watches only the
// namespaces it is given, so which ones those are is a platform document: the values its HelmRelease reads
// from a ConfigMap. The list is not an instance's own fact — an instance declares the Secret it reads, and
// the platform decides that a change to one must restart it — so one file states every namespace, the
// platform's own first.
const (
	reloaderFolder     = "reloader"
	reloaderValuesFile = reloaderFolder + "/values.json"
	reloaderObjectName = "reloader-values"
	reloaderNamespace  = "platform-system"
	reloaderValuesKey  = "values.yaml"
	// reloaderOwner is the platform: the object names no instance, and one claiming it would be a second owner.
	reloaderOwner = "platform"
)

// Reloader is what the watch list states: the namespaces the platform itself watches, in the profile's own
// order, and every instance whose workload a changed Secret must restart.
type Reloader struct {
	Platform  []string
	Instances []ReloaderInstance
}

// ReloaderInstance is one instance's declaration about its own Secrets: the public key it signs in with (its
// seed is a Secret), and the names of the Secrets its release declares its image reads. An instance with
// neither is not watched.
type ReloaderInstance struct {
	Application, Environment string
	BrokerKey                string
	Secrets                  []string
}

// watched is whether a changed Secret could mean anything to this instance: it reads one, or it signs in with
// a key whose seed it reads. An instance with neither is left out rather than listed for nothing.
func (r ReloaderInstance) watched() bool { return r.BrokerKey != "" || len(r.Secrets) > 0 }

// Namespaces is the watch list in order: the platform's own namespaces as the profile lists them, then staging's
// instances and production's, each by app name. A name may appear once: the same namespace twice, or a platform
// namespace that also spells an instance's, would be two owners of one list entry.
func (r Reloader) Namespaces() ([]string, error) {
	names := make([]string, 0, len(r.Platform)+len(r.Instances))
	claimed := map[string]bool{}
	for _, name := range r.Platform {
		if !labelPattern.MatchString(name) {
			return nil, refuse(Invalid, "reloader.namespaces", "expected a namespace name")
		}
		if claimed[name] {
			return nil, refuse(Invalid, "reloader.namespaces", "a namespace is listed twice")
		}
		claimed[name] = true
		names = append(names, name)
	}
	watched, err := watchedInstances(r.Instances)
	if err != nil {
		return nil, err
	}
	for _, instance := range watched {
		namespace := NamespaceOf(instance.Application, instance.Environment)
		if claimed[namespace] {
			return nil, refuse(Invalid, "reloader.namespaces", "a platform namespace and an instance namespace collide")
		}
		claimed[namespace] = true
		names = append(names, namespace)
	}
	return names, nil
}

// watchedInstances is the instances whose namespace must be watched, in the order the platform lists
// environments in. Every instance's names are checked, so a refused record is refused rather than skipped.
func watchedInstances(instances []ReloaderInstance) ([]ReloaderInstance, error) {
	watched := make([]ReloaderInstance, 0, len(instances))
	for _, instance := range instances {
		if !namePattern.MatchString(instance.Application) ||
			(instance.Environment != "staging" && instance.Environment != "prod") {
			return nil, refuse(Invalid, "reloader.instance", "expected an app name and an environment")
		}
		if instance.watched() {
			watched = append(watched, instance)
		}
	}
	slices.SortFunc(watched, func(a, b ReloaderInstance) int {
		if environmentRank(a.Environment) != environmentRank(b.Environment) {
			return environmentRank(a.Environment) - environmentRank(b.Environment)
		}
		return strings.Compare(a.Application, b.Application)
	})
	return watched, nil
}

// RenderReloader renders the values Reloader's HelmRelease reads: one ConfigMap in the Reloader's own namespace
// holding the namespaces it watches. Helm merges it as values, so the file's key is `reloader.namespaces` and the
// HelmRelease keeps no list of its own.
func RenderReloader(namespaces []string) (ResourceSet, error) {
	claimed := map[string]bool{}
	entries := make([]any, 0, len(namespaces))
	for _, name := range namespaces {
		if !labelPattern.MatchString(name) {
			return ResourceSet{}, refuse(Invalid, "reloader.namespaces", "expected a namespace name")
		}
		if claimed[name] {
			return ResourceSet{}, refuse(Invalid, "reloader.namespaces", "a namespace is listed twice")
		}
		claimed[name] = true
		entries = append(entries, name)
	}
	values := manifest{"reloader": manifest{"namespaces": entries}}
	document := manifest{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": manifest{"name": reloaderObjectName, "namespace": reloaderNamespace,
			"labels": manifest{"reconcile.fluxcd.io/watch": "Enabled"}},
		// The key holds JSON, which is YAML, so Helm merges it as it merges the list the HelmRelease used to
		// carry inline.
		"data": manifest{reloaderValuesKey: string(encodeManifest(values))}}
	return ResourceSet{
		Files:     []File{{reloaderValuesFile, encodeManifest(document)}},
		Inventory: []Claim{{Object{"", "ConfigMap", reloaderNamespace, reloaderObjectName}, reloaderOwner}},
	}, nil
}
