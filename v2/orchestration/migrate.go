package orchestration

import (
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/legacy"
)

// recordsRoot is where a v2-owned instance keeps its own records, under the checkout the caller writes
// into: the same root the snapshot exporter reads, so a migrated instance recompiles from Git alone. It is
// this use case's own output and becomes a profile fact only if a second writer needs it.
const recordsRoot = "v2-records"

// artifactPlatform is the platform's own fact about the images it runs: one amd64 node. The kernel accepts
// no other platform and no other media type, so a migration asserts what the compiler already enforces.
const artifactPlatform = "linux/amd64"

// MigrationInputs is what a migration takes: the predecessor's records, the source no record in this
// repository states, the platform profile, the two optional documents, and — for an instance that signs in to
// the broker — the encrypted Secret that holds its seed, byte for byte.
type MigrationInputs struct {
	Records                legacy.Records
	Supplied               legacy.Supplied
	Profile, Calls, Egress []byte
	// EventsSecret is the instance's own events.sops.yaml as Git holds it. v2 neither reads nor decrypts it: it
	// moves the file beside the manifests and lists it, because only Flux and the app ever need its plaintext.
	EventsSecret []byte
	// Offers are the app's published documents as Git holds them: the OpenAPI and AsyncAPI documents and the flags
	// file, each named by its name in the offers folder. v2 copies them and parses none of them.
	Offers []kernel.File
}

// Migration is what a migration produced: the translated facts with their sources and gaps, the record pair
// it wrote, the compilation that pair renders, the resolution's own edges for the instance, the
// predecessor's declarations it does not connect, the allocation's place for it, and every file outcome in
// the order they were written.
type Migration struct {
	Facts        legacy.Migration
	Release      kernel.ReleaseBundle
	Instance     kernel.InstanceSpec
	App          kernel.AppSpec
	Compilation  kernel.Compilation
	Rendered     kernel.Connections
	UnmetCalls   []string
	UnmetCallers []string
	Egress       *kernel.Egress
	Written      []files.Outcome
}

// Migrate translates one predecessor instance into v2's record pair, compiles the pair with the platform's
// documents, and writes the pair and the compiled files under destination. The compilation is the compile
// use case's own — the translation sits above it and decides nothing the kernel decides.
func Migrate(inputs MigrationInputs, destination string, writer files.Writer) (Migration, error) {
	facts, err := legacy.Migrate(inputs.Records, inputs.Supplied)
	if err != nil {
		return Migration{}, err
	}
	release, spec, app, err := recordsOf(facts)
	if err != nil {
		return Migration{}, err
	}
	releaseDocument, err := kernel.EncodeRelease(release)
	if err != nil {
		return Migration{}, err
	}
	instanceDocument, err := kernel.EncodeInstance(spec)
	if err != nil {
		return Migration{}, err
	}
	appDocument, err := kernel.EncodeApp(app)
	if err != nil {
		return Migration{}, err
	}
	compiled, err := Compile(Inputs{Instance: instanceDocument, Release: releaseDocument, App: appDocument,
		Profile: inputs.Profile, Calls: inputs.Calls, Egress: inputs.Egress})
	if err != nil {
		return Migration{}, err
	}
	// The same helper the compilation used, so the report names the edges and the port the files were
	// rendered from rather than a second reading of them.
	compilation := Inputs{Instance: instanceDocument, Release: releaseDocument, App: appDocument,
		Profile: inputs.Profile, Calls: inputs.Calls, Egress: inputs.Egress}
	rendered, egress, err := documents(compilation, facts.Application, facts.Environment)
	if err != nil {
		return Migration{}, err
	}
	files, err := WithEventsSecret(compiled, inputs.EventsSecret)
	if err != nil {
		return Migration{}, err
	}
	if files, err = withOffers(files, facts, inputs.Offers); err != nil {
		return Migration{}, err
	}
	result := Migration{Facts: facts, Release: release, Instance: spec, App: app, Compilation: compiled,
		Rendered: rendered, Egress: egress, UnmetCalls: unmet(facts.Calls, callees(rendered)),
		UnmetCallers: unmet(facts.Allows, rendered.Callers)}
	written := append(recordFiles(facts.Application, facts.Environment, appDocument, releaseDocument, instanceDocument,
		inputs.EventsSecret, inputs.Offers), files...)
	result.Written, err = writeCompiled(destination, compiled, written, writer)
	return result, err
}

// offered are the documents a migration writes beside the manifests, checked against the provenance the release
// states: a console reads a document by the commit its release names and shows nothing without it, so a record that
// states one of the two without the other would be a link to nothing. The check is this use case's because the
// compiler renders the annotation and the documents as they are given.
func withOffers(manifests []kernel.File, facts legacy.Migration, documents []kernel.File) ([]kernel.File, error) {
	switch {
	case facts.Offered.Commit != "" && len(documents) == 0:
		return nil, refuseOffers("offers_without_documents")
	case facts.Offered.Commit == "" && len(documents) > 0:
		return nil, refuseOffers("documents_without_offers")
	}
	return WithOffers(manifests, documents)
}

// recordsOf turns one predecessor's translated facts into v2's record pair. It maps fields and lets the
// kernel decide what a record may say: identity, validation and encoding stay there.
func recordsOf(facts legacy.Migration) (kernel.ReleaseBundle, kernel.InstanceSpec, kernel.AppSpec, error) {
	// Consent by team travels as the record states it: the grant names a team, the resolution matches it against
	// each caller's own team, and the migration neither widens nor narrows what the instance accepts.
	if facts.Instance.Team == "" {
		return kernel.ReleaseBundle{}, kernel.InstanceSpec{}, kernel.AppSpec{},
			&Refusal{Subject: "app.team", Reason: "a_team_is_required"}
	}
	release := kernel.ReleaseBundle{
		SchemaVersion: 1,
		Application:   facts.Application,
		Publishes:     facts.Workload.Publishes,
		Description:   facts.Offered.Description,
		APIPath:       facts.Offered.APIPath,
		Source:        kernel.Source{Repository: facts.Source.Repository, Commit: facts.Source.Commit},
		Artifact: kernel.Artifact{Image: facts.Workload.Image, Digest: facts.Workload.Digest,
			MediaType: kernel.ManifestMediaType, Platform: artifactPlatform},
		Runtime: kernel.Runtime{Kind: facts.Workload.Kind, Port: facts.Workload.Port,
			HealthPath: facts.Workload.HealthPath, UID: facts.Workload.UID},
		Database:  facts.Database,
		Telemetry: facts.Workload.Telemetry,
	}
	if facts.Offered.Commit != "" {
		// The documents were read at the commit the predecessor's image was built from: its record keeps only an
		// abbreviation, and sourceOf checked that against the commit the operator supplied. One fact, checked once.
		release.Offers = facts.Source.Commit
	}
	identity, err := release.ID()
	if err != nil {
		return kernel.ReleaseBundle{}, kernel.InstanceSpec{}, kernel.AppSpec{}, err
	}
	release.ReleaseID = identity
	spec := kernel.InstanceSpec{
		SchemaVersion: 1,
		Application:   facts.Application,
		Environment:   facts.Environment,
		Release:       identity,
		Configuration: kernel.Configuration{Replicas: facts.Instance.Replicas, MemoryMiB: facts.Instance.MemoryMiB},
		Grants:        kernel.Grants{Exposure: facts.Instance.Exposure},
		Storage:       kernel.Storage{ClaimName: facts.Claim},
		Calls:         facts.Calls,
		Allows:        facts.Allows,
		Events:        kernel.Events{Key: facts.EventsKey, Subscriptions: facts.Subscriptions},
	}
	app := kernel.AppSpec{SchemaVersion: 1, Application: facts.Application, Team: facts.Instance.Team}
	return release, spec, app, nil
}

// recordFiles is the records a migration writes: one app record and the environment's pair, under the root
// the snapshot exporter reads. An instance with a broker identity also keeps its encrypted seed beside the
// record, before the instance that names its public key, so the record folder is what a later compile reads.
// The documents it publishes are declared inputs of the same folder, so a later compile reads them from the
// record rather than from the compiled copy a prune may remove.
func recordFiles(application, environment string, app, release, instance, eventsSecret []byte,
	offers []kernel.File) []kernel.File {
	folder := recordsRoot + "/" + application + "/" + environment
	records := []kernel.File{
		{Path: recordsRoot + "/" + application + "/app.json", Document: app},
		{Path: folder + "/release.json", Document: release},
	}
	if len(eventsSecret) > 0 {
		records = append(records, kernel.File{Path: folder + "/" + kernel.EventsSecretFile, Document: eventsSecret})
	}
	records = append(records, kernel.File{Path: folder + "/instance.json", Document: instance})
	for _, document := range offers {
		records = append(records, kernel.File{Path: folder + "/offers/" + document.Path,
			Document: document.Document})
	}
	return records
}

// callees is the names a resolution's calls point at: what a predecessor's own declarations are compared
// with.
func callees(connections kernel.Connections) []string {
	names := make([]string, 0, len(connections.Calls))
	for _, call := range connections.Calls {
		names = append(names, call.Callee)
	}
	return names
}

// unmet is the declarations the resolution does not connect. A team grant has no caller of its own — the
// resolver matches it against a caller's team — so there is no one name to look for; the resolution's own
// waiting and refused lists say whether a caller asked and was turned away.
func unmet(declared, resolved []string) []string {
	missing := []string{}
	for _, name := range declared {
		if strings.HasPrefix(name, "team:") || slices.Contains(resolved, name) {
			continue
		}
		missing = append(missing, name)
	}
	return missing
}
