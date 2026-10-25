package kernel

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// HTTPWorkload is supplied rendering evidence, not a complete or verified release.
type HTTPWorkload struct {
	Image, Digest string
	Port          int32
	HealthPath    string
	UID           int32
}

// HTTPInstance contains the environment-owned facts for a private HTTP preview.
type HTTPInstance struct {
	Application, Environment string
	Configuration            Configuration
}

// ResourceSet owns fresh resource bytes and slices, without release identities or readiness.
type ResourceSet struct {
	Files         []File
	Inventory     []Claim
	Registrations []string
}

// RenderHTTP builds resources from known facts without inventing release provenance.
// Validation order is names/environment, workload, configuration, profile, quota fit.
func RenderHTTP(spec HTTPInstance, http HTTPWorkload, profile PlatformProfile) (ResourceSet, error) {
	if err := validateNames(spec.Application, spec.Environment); err != nil {
		return ResourceSet{}, err
	}
	if err := validateImage(http.Image, http.Digest); err != nil {
		return ResourceSet{}, err
	}
	if err := validateHTTP(http.Port, http.HealthPath, http.UID); err != nil {
		return ResourceSet{}, err
	}
	if err := spec.Configuration.validate(); err != nil {
		return ResourceSet{}, err
	}
	if err := profile.validate(); err != nil {
		return ResourceSet{}, err
	}
	if err := fits(spec.Configuration, profile); err != nil {
		return ResourceSet{}, err
	}
	// Instances under the instance root; units under the profile's unit root, which kustomize can list.
	folder := profile.Delivery.OutputRoot + "/instances/" + spec.Application + "/" + spec.Environment
	unitPath := profile.Delivery.UnitsRoot + "/" + unitName(spec) + ".json"
	resources := []struct {
		name string
		doc  manifest
	}{
		{"namespace.json", namespaceManifest(spec)},
		{"helmrelease.json", helmRelease(spec, http, profile)},
		{"resourcequota.json", resourceQuota(spec, profile.Quota)},
		{"networkpolicy.json", networkPolicy(spec)},
	}
	var files []File
	var listed []any
	for _, resource := range resources {
		files = append(files, File{folder + "/" + resource.name, encodeManifest(resource.doc)})
		listed = append(listed, resource.name)
	}
	files = append(files,
		File{folder + "/kustomization.yaml", encodeManifest(manifest{
			"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization", "resources": listed})},
		File{unitPath, encodeManifest(unit(spec, profile, folder))})
	return ResourceSet{files, inventory(spec), []string{unitPath}}, nil
}

func namespaceName(spec HTTPInstance) string { return spec.Application + "-" + spec.Environment }
func unitName(spec HTTPInstance) string      { return "app-" + namespaceName(spec) }
func owner(spec HTTPInstance) string         { return spec.Application + "/" + spec.Environment }

func metadata(name, namespace string) manifest {
	meta := manifest{"name": name}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	return meta
}

func namespaceManifest(spec HTTPInstance) manifest {
	meta := metadata(namespaceName(spec), "")
	meta["labels"] = manifest{labelDomain + "managed": "true", labelDomain + "app": spec.Application,
		labelDomain + "environment": spec.Environment, labelDomain + "exposure": "private"}
	return manifest{"apiVersion": "v1", "kind": "Namespace", "metadata": meta}
}

func helmRelease(spec HTTPInstance, http HTTPWorkload, profile PlatformProfile) manifest {
	chart := manifest{"chart": profile.Chart.Name, "version": profile.Chart.Version, "interval": helmInterval,
		"sourceRef": manifest{"kind": "HelmRepository", "name": profile.Chart.Repository, "namespace": profile.Chart.RepositoryNamespace}}
	remediation := manifest{"remediation": manifest{"retries": retries}}
	return manifest{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": metadata(spec.Application, namespaceName(spec)),
		"spec": manifest{"interval": helmInterval, "timeout": failAfter, "chart": manifest{"spec": chart},
			"install": remediation, "upgrade": manifest{"remediation": manifest{"retries": retries}},
			"values": values(spec, http, profile.Workload)}}
}

// values are the chart's values: one controller, one container, its Service and /tmp.
func values(spec HTTPInstance, http HTTPWorkload, workload Workload) manifest {
	uid := http.UID
	pod := manifest{"automountServiceAccountToken": false, "securityContext": manifest{
		"runAsNonRoot": true, "runAsUser": uid, "runAsGroup": uid, "fsGroup": uid,
		"seccompProfile": manifest{"type": "RuntimeDefault"}}}
	controller := manifest{"replicas": spec.Configuration.Replicas, "containers": manifest{"main": container(spec, http, workload)}}
	return manifest{"defaultPodOptions": pod, "controllers": manifest{"main": controller},
		"persistence": manifest{"tmp": manifest{"type": "emptyDir", "globalMounts": []any{manifest{"path": "/tmp"}}}},
		"service": manifest{"main": manifest{"controller": "main",
			"ports": manifest{"http": manifest{"port": http.Port}}}}}
}

func container(spec HTTPInstance, http HTTPWorkload, workload Workload) manifest {
	memory := fmt.Sprintf("%dMi", spec.Configuration.MemoryMiB)
	return manifest{
		"image": manifest{"repository": http.Image, "digest": http.Digest},
		"resources": manifest{"requests": manifest{"cpu": fmt.Sprintf("%dm", workload.CPURequestMilli), "memory": memory},
			"limits": manifest{"memory": memory}},
		"securityContext": manifest{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
			"capabilities": manifest{"drop": []any{"ALL"}}},
		"env":    manifest{"SWHURL_APP": spec.Application, "SWHURL_ENV": spec.Environment},
		"probes": probes(http, workload),
	}
}

// probes: liveness waits until the app first answers, for up to the profile's startup allowance.
func probes(runtime HTTPWorkload, workload Workload) manifest {
	probe := func(extra manifest) manifest {
		spec := manifest{"httpGet": manifest{"path": runtime.HealthPath, "port": runtime.Port}}
		for key, value := range extra {
			spec[key] = value
		}
		return manifest{"enabled": true, "custom": true, "spec": spec}
	}
	return manifest{"readiness": probe(nil), "liveness": probe(nil), "startup": probe(manifest{
		"periodSeconds":    workload.StartupPeriodSeconds,
		"failureThreshold": workload.StartupSeconds / workload.StartupPeriodSeconds})}
}

func resourceQuota(spec HTTPInstance, quota Quota) manifest {
	return manifest{"apiVersion": "v1", "kind": "ResourceQuota", "metadata": metadata(spec.Application, namespaceName(spec)),
		"spec": manifest{"hard": manifest{
			"pods": fmt.Sprint(quota.Pods), "requests.cpu": cpuQuantity(quota.RequestsCPUMilli),
			"requests.memory": memoryQuantity(quota.RequestsMemoryMiB), "limits.memory": memoryQuantity(quota.LimitsMemoryMiB)}}}
}

func cpuQuantity(milli int32) string {
	if milli%1000 == 0 {
		return fmt.Sprint(milli / 1000)
	}
	return fmt.Sprintf("%dm", milli)
}

func memoryQuantity(mib int32) string {
	if mib%1024 == 0 {
		return fmt.Sprintf("%dGi", mib/1024)
	}
	return fmt.Sprintf("%dMi", mib)
}

// networkPolicy: a private instance's pods accept no connections. It selects the app's own pods,
// not the namespace, as v1's does.
// networkPolicy is the isolation boundary: a private instance accepts no ingress and, by the
// operator's decision of 25 October 2026, sends no egress either. An empty rule list denies all.
func networkPolicy(spec HTTPInstance) manifest {
	return manifest{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": metadata(spec.Application, namespaceName(spec)),
		"spec": manifest{"podSelector": manifest{"matchLabels": manifest{instanceLabel: spec.Application}},
			"policyTypes": []any{"Ingress", "Egress"}, "ingress": []any{}, "egress": []any{}}}
}

func unit(spec HTTPInstance, profile PlatformProfile, folder string) manifest {
	dependsOn := []any{}
	for _, name := range profile.Delivery.DependsOn {
		dependsOn = append(dependsOn, manifest{"name": name})
	}
	return manifest{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": metadata(unitName(spec), "flux-system"),
		"spec": manifest{"dependsOn": dependsOn, "interval": unitInterval,
			"sourceRef": manifest{"kind": "GitRepository", "name": profile.Delivery.Source},
			"path":      "./" + folder, "prune": true, "wait": true, "timeout": failAfter}}
}

// inventory is every cluster object the files create, in file order.
func inventory(spec HTTPInstance) []Claim {
	namespace, by := namespaceName(spec), owner(spec)
	return []Claim{
		{Object{"", "Namespace", "", namespace}, by},
		{Object{"helm.toolkit.fluxcd.io", "HelmRelease", namespace, spec.Application}, by},
		{Object{"", "ResourceQuota", namespace, spec.Application}, by},
		{Object{"networking.k8s.io", "NetworkPolicy", namespace, spec.Application}, by},
		{Object{"kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", unitName(spec)}, by},
	}
}

// encodeManifest is a document's canonical bytes: compact, keys sorted, no HTML escaping.
func encodeManifest(doc manifest) json.RawMessage {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	// Only maps, slices, strings, booleans and int32s built above reach here.
	if err := encoder.Encode(doc); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
}
