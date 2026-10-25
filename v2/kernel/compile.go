package kernel

import (
	"bytes"
	"encoding/json"
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
	resources, err := RenderHTTP(
		HTTPInstance{spec.Application, spec.Environment, spec.Configuration},
		HTTPWorkload{release.Artifact.Image, release.Artifact.Digest, release.Runtime.Port, release.Runtime.HealthPath, release.Runtime.UID}, profile)
	if err != nil {
		return Compilation{}, err
	}
	c := Compilation{Inputs: inputs, Files: resources.Files, Inventory: resources.Inventory, Registrations: resources.Registrations}
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
