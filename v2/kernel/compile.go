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
	App      AppID            `json:"app"`
	Release  ReleaseID        `json:"release"`
	Instance InstanceRevision `json:"instance"`
	Profile  ProfileID        `json:"profile"`
	// Calls is the identity of the connected pairs this compilation rendered, empty when it took part in
	// none. The compilation's own ID hashes its rendered output, so a resolution that changes a file
	// changes the ID with it.
	Calls ConnectionsID `json:"calls"`
}

// Connections are the relationships one instance takes part in: what it calls, who may call it, and the event
// subscriptions that connected. They come from the resolver, not from the instance record, so rendering never
// decides consent or readership.
type Connections struct {
	Calls         []Call
	Callers       []string
	Subscriptions []EventSubscription
}

// ConnectionsID is a resolution's content identity.
type ConnectionsID string

// ID is the identity of the pairs a compilation rendered: the same edges in any order give the same ID,
// and no edges give an empty one.
func (c Connections) ID() ConnectionsID {
	if len(c.Calls) == 0 && len(c.Callers) == 0 && len(c.Subscriptions) == 0 {
		return ""
	}
	calls, callers := slices.Clone(c.Calls), slices.Clone(c.Callers)
	slices.SortFunc(calls, func(a, b Call) int {
		if a.Callee != b.Callee {
			return strings.Compare(a.Callee, b.Callee)
		}
		return int(a.Port - b.Port)
	})
	slices.Sort(callers)
	edges := []any{}
	for _, call := range calls {
		edges = append(edges, []any{"calls", call.Callee, call.Port})
	}
	for _, caller := range callers {
		edges = append(edges, []any{"called_by", caller})
	}
	for _, subscription := range c.Subscriptions {
		edges = append(edges, []any{"subscribes", subscription.Publisher, subscription.Event,
			subscription.Stream, subscription.Consumer, subscription.Subject, subscription.Object})
	}
	return ConnectionsID(hash("conn-sha256:", canonical([]any{"swhurl.connections.v1", edges})))
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
	// backupLabel marks the backup job and its pods: the platform's workload, not the app's.
	backupLabel = labelDomain + "backup"
	// backupUnit is the platform unit that holds the upload key and the MongoDB job; a database
	// instance's unit waits for it.
	backupUnit = "platform-backup"
	// reloadAnnotation is Reloader's: a Secret named here restarts the workload that reads it when the
	// Secret's content changes. An app reads its seed once, so a new key pair arrives with a restart.
	reloadAnnotation = "secret.reloader.stakater.com/reload"
	// DataMount is where a database instance's volume is mounted, and DatabasePath the file inside it. A
	// restore writes to the same two places, so they are stated once, here.
	DataMount    = "/data"
	DatabasePath = DataMount + "/app.db"
)

// manifest is a Kubernetes object or kustomization as the compiler builds it: fresh maps only.
type manifest = map[string]any

// Compile builds one stateless HTTP instance's files, private or signed-in. It validates every input,
// then refuses a release of another application before one the instance does not select.
func Compile(spec InstanceSpec, release ReleaseBundle, profile PlatformProfile) (Compilation, error) {
	return CompileWith(spec, release, AppSpec{}, profile, Connections{}, nil)
}

// CompileWith compiles an instance together with its app record and the connected pairs it takes part in.
// An empty app record is for a use case that does not deliver an instance (a preview or a live read); it
// renders no team label. A delivered instance always has one.
func CompileWith(spec InstanceSpec, release ReleaseBundle, app AppSpec, profile PlatformProfile, connections Connections, egress *Egress) (Compilation, error) {
	inputs, err := compileInputs(spec, release, app, profile)
	if err != nil {
		return Compilation{}, err
	}
	inputs.Calls = connections.ID()
	if err != nil {
		return Compilation{}, err
	}
	resources, err := RenderInstance(instanceOf(spec, release, app, connections, egress), workloadOf(release), profile)
	if err != nil {
		return Compilation{}, err
	}
	c := Compilation{Inputs: inputs, Files: resources.Files, Inventory: resources.Inventory, Registrations: resources.Registrations}
	c.ID = CompilationID(hash("comp-sha256:", compilationCanonical(c)))
	return c, nil
}

// instanceOf is the rendering input the records state: the compiler and the restore build it the same way,
// so a restore pod runs as the app runs and mounts the volume the app mounts.
func instanceOf(spec InstanceSpec, release ReleaseBundle, app AppSpec, connections Connections, egress *Egress) InstanceFacts {
	return InstanceFacts{Application: spec.Application, Environment: spec.Environment,
		Configuration: spec.Configuration, Exposure: spec.Grants.Exposure,
		Calls: connections.Calls, Callers: connections.Callers,
		Broker:   BrokerIdentity{Key: spec.Events.Key, Connected: connections.Subscriptions},
		Database: release.Database, Storage: spec.Storage, Egress: egress, Team: app.Team}
}

// workloadOf is the runtime facts a release states: what the chart runs, as whom, and what it serves.
func workloadOf(release ReleaseBundle) WorkloadFacts {
	return WorkloadFacts{Kind: release.Runtime.Kind, Image: release.Artifact.Image, Digest: release.Artifact.Digest,
		Source: release.Source.Repository,
		Port:   release.Runtime.Port, HealthPath: release.Runtime.HealthPath, UID: release.Runtime.UID,
		Telemetry: release.Telemetry, Publishes: release.Publishes}
}

func compileInputs(spec InstanceSpec, release ReleaseBundle, app AppSpec, profile PlatformProfile) (Inputs, error) {
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
	appID, err := appIDOf(app, spec)
	if err != nil {
		return Inputs{}, err
	}
	if err := fits(spec.Configuration, profile, release.Database == "sqlite"); err != nil {
		return Inputs{}, err
	}
	return Inputs{App: appID, Release: id, Instance: revision, Profile: profileID}, nil
}

// appIDOf is the app record's identity when the caller supplied one, and refuses a record for another app.
// An empty record is the use cases that do not deliver an instance.
func appIDOf(app AppSpec, spec InstanceSpec) (AppID, error) {
	if app.Application == "" && app.Team == "" {
		return "", nil
	}
	id, err := app.ID()
	if err != nil {
		return "", err
	}
	if app.Application != spec.Application {
		return "", refuse(WrongApplication, "application", "app record belongs to a different application")
	}
	return id, nil
}

// fits checks what an instance needs at once against the profile's quota: its replicas and one more for a
// rolling update (none when stopped), each with the profile's CPU request and the instance's memory, and
// — for a database — the backup pod, which runs beside the app on the same claim and the same node.
// Checked in the order of the profile's quota fields.
func fits(c Configuration, profile PlatformProfile, database bool) error {
	q, cpu, memory := profile.Quota, int64(profile.Workload.CPURequestMilli), int64(c.MemoryMiB)
	var pods, needCPU, needMemory, needLimit int64
	if c.Replicas > 0 {
		running := int64(c.Replicas) + 1
		pods, needCPU, needMemory, needLimit = running, running*cpu, running*memory, running*memory
	}
	if database {
		pods, needCPU = pods+1, needCPU+backupCPURequestMilli
		needMemory, needLimit = needMemory+backupMemoryMiB, needLimit+backupLimitMiB
	}
	for _, need := range []struct {
		field         string
		needed, quota int64
	}{
		{"quota.pods", pods, int64(q.Pods)},
		{"quota.requestsCpuMilli", needCPU, int64(q.RequestsCPUMilli)},
		{"quota.requestsMemoryMiB", needMemory, int64(q.RequestsMemoryMiB)},
		{"quota.limitsMemoryMiB", needLimit, int64(q.LimitsMemoryMiB)},
	} {
		if need.needed > need.quota {
			return refuse(OverQuota, need.field, "the pods this instance needs at once need more than the profile's quota")
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
	claims := make([]Claim, 0, len(foreign))
	for _, object := range foreign {
		claims = append(claims, Claim{object, "another owner"})
	}
	for _, c := range compiled {
		claims = append(claims, c.Inventory...)
	}
	// The first object two claims name, in claim order: what a compilation must refuse to apply.
	if conflicts := Ownership(claims); len(conflicts) > 0 {
		owners := conflicts[0].Owners
		return refuse(OwnershipConflict, objectText(conflicts[0].Object),
			"claimed by "+owners[1]+" and "+owners[0])
	}
	return nil
}

// Conflict is one object more than one owner claims, with the owners in the order they claimed it.
type Conflict struct {
	Object Object
	Owners []string
}

// Ownership reports every object more than one claim names, in the order the claims first appear. It is
// the plural form of CheckOwnership: a bridge needs all the collisions, not the first one.
func Ownership(claims []Claim) []Conflict {
	owners := map[Object][]string{}
	var order []Object
	for _, claim := range claims {
		if _, seen := owners[claim.Object]; !seen {
			order = append(order, claim.Object)
		}
		owners[claim.Object] = append(owners[claim.Object], claim.Owner)
	}
	var conflicts []Conflict
	for _, object := range order {
		if len(owners[object]) > 1 {
			conflicts = append(conflicts, Conflict{object, owners[object]})
		}
	}
	return conflicts
}

// objectText names an object as Kubernetes tools do: "HelmRelease.helm.toolkit.fluxcd.io ns/name".
func objectText(o Object) string {
	kind := strings.Join(slices.DeleteFunc([]string{o.Kind, o.Group}, func(s string) bool { return s == "" }), ".")
	if o.Namespace == "" {
		return kind + " " + o.Name
	}
	return kind + " " + o.Namespace + "/" + o.Name
}
