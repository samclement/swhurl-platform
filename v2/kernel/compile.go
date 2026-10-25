package kernel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// CompilationID identifies a compilation's output: comp-sha256 over its canonical files,
// inventory and registrations. Inputs that compile to the same resources share an ID.
type CompilationID string

// Compilation is one instance's resources and what they claim. Inputs names what it was
// compiled from; it does not participate in the ID.
type Compilation struct {
	ID            CompilationID `json:"id"`
	Inputs        Inputs        `json:"inputs"`
	Files         []File        `json:"files"`
	Inventory     []Claim       `json:"inventory"`
	Registrations []string      `json:"registrations"`
}

type Inputs struct {
	Release  ReleaseID        `json:"release"`
	Instance InstanceRevision `json:"instance"`
	Profile  ProfileID        `json:"profile"`
}

// File is one file to deliver: its path from the repository root and its JSON document.
type File struct {
	Path     string          `json:"path"`
	Document json.RawMessage `json:"document"`
}

// Object is a cluster object's identity: API group (not version), kind, namespace, name.
type Object struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Claim is an object and the instance (app/env) that owns it.
type Claim struct {
	Object
	Owner string `json:"owner"`
}

const (
	WrongRelease      Code = "wrong_release"
	OverQuota         Code = "over_quota"
	OwnershipConflict Code = "ownership_conflict"
)

// The compiler's own choices: changing one changes every compilation ID.
const (
	helmInterval  = "30m"
	unitInterval  = "10m"
	failAfter     = "3m"
	retries       = 1
	labelDomain   = "platform.swhurl.com/"
	instanceLabel = "app.kubernetes.io/instance"
)

// manifest is a Kubernetes object or kustomization as the compiler builds it: fresh maps only.
type manifest = map[string]any

// Compile builds one private stateless HTTP instance's files. It validates every input, then
// refuses a release of another application before one the instance does not select.
func Compile(spec InstanceSpec, release ReleaseBundle, profile PlatformProfile) (Compilation, error) {
	inputs, err := compileInputs(spec, release, profile)
	if err != nil {
		return Compilation{}, err
	}
	// Instances under instances/, units under units/: no app name can reach the units folder.
	folder := profile.Delivery.OutputRoot + "/instances/" + spec.Application + "/" + spec.Environment
	unitPath := profile.Delivery.OutputRoot + "/units/" + unitName(spec) + ".json"
	resources := []struct {
		name string
		doc  manifest
	}{
		{"namespace.json", namespaceManifest(spec)},
		{"helmrelease.json", helmRelease(spec, release, profile)},
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
	c := Compilation{Inputs: inputs, Files: files, Inventory: inventory(spec), Registrations: []string{unitPath}}
	c.ID = CompilationID(hash("comp-sha256:", compilationCanonical(c)))
	return c, nil
}

func compileInputs(spec InstanceSpec, release ReleaseBundle, profile PlatformProfile) (Inputs, error) {
	revision, err := spec.Revision()
	if err != nil {
		return Inputs{}, err
	}
	id, err := release.ID()
	if err != nil {
		return Inputs{}, err
	}
	profileID, err := profile.ID()
	if err != nil {
		return Inputs{}, err
	}
	if release.Application != spec.Application {
		return Inputs{}, refuse(WrongApplication, "application", "release belongs to a different application")
	}
	if id != spec.Release {
		return Inputs{}, refuse(WrongRelease, "release", "the instance selects a different release")
	}
	if err := fits(spec.Configuration, profile); err != nil {
		return Inputs{}, err
	}
	return Inputs{id, revision, profileID}, nil
}

// fits refuses an instance whose pods can never fit the profile's quota: its replicas and one more
// for a rolling update (none when stopped), each with the profile's CPU request and the instance's
// memory as request and limit. Checked in the order of the profile's quota fields.
func fits(c Configuration, profile PlatformProfile) error {
	pods := int64(0)
	if c.Replicas > 0 {
		pods = int64(c.Replicas) + 1
	}
	q, cpu, memory := profile.Quota, int64(profile.Workload.CPURequestMilli), int64(c.MemoryMiB)
	for _, need := range []struct {
		field         string
		needed, quota int64
	}{
		{"quota.pods", pods, int64(q.Pods)},
		{"quota.requestsCpuMilli", pods * cpu, int64(q.RequestsCPUMilli)},
		{"quota.requestsMemoryMiB", pods * memory, int64(q.RequestsMemoryMiB)},
		{"quota.limitsMemoryMiB", pods * memory, int64(q.LimitsMemoryMiB)},
	} {
		if need.needed > need.quota {
			return refuse(OverQuota, need.field, "the instance's pods, with one more for a rolling update, need more than this")
		}
	}
	return nil
}

func namespaceName(spec InstanceSpec) string { return spec.Application + "-" + spec.Environment }
func unitName(spec InstanceSpec) string      { return "app-" + namespaceName(spec) }
func owner(spec InstanceSpec) string         { return spec.Application + "/" + spec.Environment }

func metadata(name, namespace string) manifest {
	meta := manifest{"name": name}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	return meta
}

func namespaceManifest(spec InstanceSpec) manifest {
	meta := metadata(namespaceName(spec), "")
	meta["labels"] = manifest{labelDomain + "managed": "true", labelDomain + "app": spec.Application,
		labelDomain + "environment": spec.Environment, labelDomain + "exposure": spec.Grants.Exposure}
	return manifest{"apiVersion": "v1", "kind": "Namespace", "metadata": meta}
}

func helmRelease(spec InstanceSpec, release ReleaseBundle, profile PlatformProfile) manifest {
	chart := manifest{"chart": profile.Chart.Name, "version": profile.Chart.Version, "interval": helmInterval,
		"sourceRef": manifest{"kind": "HelmRepository", "name": profile.Chart.Repository, "namespace": profile.Chart.RepositoryNamespace}}
	remediation := manifest{"remediation": manifest{"retries": retries}}
	return manifest{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": metadata(spec.Application, namespaceName(spec)),
		"spec": manifest{"interval": helmInterval, "timeout": failAfter, "chart": manifest{"spec": chart},
			"install": remediation, "upgrade": manifest{"remediation": manifest{"retries": retries}},
			"values": values(spec, release, profile.Workload)}}
}

// values are the chart's values: one controller, one container, its Service and /tmp.
func values(spec InstanceSpec, release ReleaseBundle, workload Workload) manifest {
	uid := release.Runtime.UID
	pod := manifest{"automountServiceAccountToken": false, "securityContext": manifest{
		"runAsNonRoot": true, "runAsUser": uid, "runAsGroup": uid, "fsGroup": uid,
		"seccompProfile": manifest{"type": "RuntimeDefault"}}}
	controller := manifest{"replicas": spec.Configuration.Replicas, "containers": manifest{"main": container(spec, release, workload)}}
	return manifest{"defaultPodOptions": pod, "controllers": manifest{"main": controller},
		"persistence": manifest{"tmp": manifest{"type": "emptyDir", "globalMounts": []any{manifest{"path": "/tmp"}}}},
		"service": manifest{"main": manifest{"controller": "main",
			"ports": manifest{"http": manifest{"port": release.Runtime.Port}}}}}
}

func container(spec InstanceSpec, release ReleaseBundle, workload Workload) manifest {
	memory := fmt.Sprintf("%dMi", spec.Configuration.MemoryMiB)
	return manifest{
		"image": manifest{"repository": release.Artifact.Image, "digest": release.Artifact.Digest},
		"resources": manifest{"requests": manifest{"cpu": fmt.Sprintf("%dm", workload.CPURequestMilli), "memory": memory},
			"limits": manifest{"memory": memory}},
		"securityContext": manifest{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
			"capabilities": manifest{"drop": []any{"ALL"}}},
		"env":    manifest{"SWHURL_APP": spec.Application, "SWHURL_ENV": spec.Environment},
		"probes": probes(release.Runtime, workload),
	}
}

// probes: liveness waits until the app first answers, for up to the profile's startup allowance.
func probes(runtime Runtime, workload Workload) manifest {
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

func resourceQuota(spec InstanceSpec, quota Quota) manifest {
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
func networkPolicy(spec InstanceSpec) manifest {
	return manifest{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": metadata(spec.Application, namespaceName(spec)),
		"spec": manifest{"podSelector": manifest{"matchLabels": manifest{instanceLabel: spec.Application}},
			"policyTypes": []any{"Ingress"}, "ingress": []any{}}}
}

func unit(spec InstanceSpec, profile PlatformProfile, folder string) manifest {
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
func inventory(spec InstanceSpec) []Claim {
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

func compilationCanonical(c Compilation) []byte {
	files := []any{}
	for _, file := range c.Files {
		files = append(files, []any{file.Path, file.Document})
	}
	claims := []any{}
	for _, claim := range c.Inventory {
		claims = append(claims, []any{claim.Group, claim.Kind, claim.Namespace, claim.Name, claim.Owner})
	}
	return canonical([]any{"swhurl.compilation.v1", files, claims, slices.Clone(c.Registrations)})
}

// EncodeCompilation is the document `swhurl-v2 compile` prints.
func EncodeCompilation(c Compilation) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
		Compilation
	}{"v2-compilation", 1, c}); err != nil {
		panic(err)
	}
	return out.Bytes()
}

// CheckOwnership refuses an object claimed by two compilations, or by one and foreign (objects
// someone else owns). It reports the first such object in compilation and file order.
func CheckOwnership(compiled []Compilation, foreign []Object) error {
	owners := map[Object]string{}
	for _, object := range foreign {
		owners[object] = "another owner"
	}
	for _, c := range compiled {
		for _, claim := range c.Inventory {
			if previous, taken := owners[claim.Object]; taken {
				return refuse(OwnershipConflict, objectText(claim.Object),
					"claimed by "+claim.Owner+" and "+previous)
			}
			owners[claim.Object] = claim.Owner
		}
	}
	return nil
}

// objectText names an object as Kubernetes tools do: "HelmRelease.helm.toolkit.fluxcd.io ns/name".
func objectText(o Object) string {
	kind := strings.Join(slices.DeleteFunc([]string{o.Kind, o.Group}, func(s string) bool { return s == "" }), ".")
	if o.Namespace == "" {
		return kind + " " + o.Name
	}
	return kind + " " + o.Namespace + "/" + o.Name
}
