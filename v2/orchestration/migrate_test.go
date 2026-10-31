package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/legacy"
)

// The image pair each fixture's predecessor states and the platform manifest it resolves to, as the registry
// published them: the hello fixture is nginx's index and its linux/amd64 entry, the notifier the frozen
// demo-notifier predecessor.
const (
	declaredDigest = "sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0"
	platformDigest = "sha256:28d91bdce70ad09025ea901458fdd149259d8e05982ade79d4ef2c0d9470eb48"
	notifierDigest = "sha256:578f4aa722861ac50fcc67805e9e82bb0cfb67c2503216c276b84e9dd09a09be"
	notifierAmd64  = "sha256:2bd56b2cd701083520983b847b7e26609f55465253b421483a362fafe14115d5"
)

// resolved answers the fixtures' declared digests with the platform manifests they resolve to, as the registry
// does, and names itself for no image in particular: what a migration asks for is asserted where it matters.
func resolved(t *testing.T) func(string) (registry.Reader, error) {
	t.Helper()
	reader := registrytest.New(registrytest.Scenario{Images: map[string]registry.Image{
		declaredDigest: {Digest: platformDigest, MediaType: kernel.ManifestMediaType},
		notifierDigest: {Digest: notifierAmd64, MediaType: kernel.ManifestMediaType},
	}})
	return func(string) (registry.Reader, error) { return reader, nil }
}

// migrate runs the use case with the fixtures' registry answers.
func migrate(t *testing.T, inputs MigrationInputs, writer files.Writer) (Migration, error) {
	t.Helper()
	return Migrate(context.Background(), inputs, resolved(t), "/checkout", writer)
}

// predecessor is one instance's legacy documents as the snapshot carries them: JSON objects, not YAML.
func predecessor(t *testing.T) legacy.Records {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile("../legacy/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return legacy.Records{App: read("app.json"), Instance: read("instance.json"),
		Release: read("release.json"), Committed: read("helmrelease.json")}
}

// migrationInputs is the fixture with the source only the operator knows.
func migrationInputs(t *testing.T) MigrationInputs {
	t.Helper()
	profile, err := os.ReadFile("../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	return MigrationInputs{Records: predecessor(t),
		Supplied: legacy.Supplied{Repository: "nginxinc/nginx-unprivileged", Commit: revision},
		Profile:  profile}
}

// A migration writes v2's record pair and then exactly what compiling that pair renders: the use case adds
// the translation and the effect, not a second set of rules.
func TestMigrateWritesThePairAndTheCompilation(t *testing.T) {
	inputs := migrationInputs(t)
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	migrated, err := migrate(t, inputs, writer)
	if err != nil {
		t.Fatal(err)
	}
	app, ok := writer.Contents["v2-records/hello/app.json"]
	if !ok {
		t.Fatalf("no app record was written: %v", writer.Contents)
	}
	release, ok := writer.Contents["v2-records/hello/staging/release.json"]
	if !ok {
		t.Fatalf("no release record was written: %v", writer.Contents)
	}
	instance, ok := writer.Contents["v2-records/hello/staging/instance.json"]
	if !ok {
		t.Fatalf("no instance record was written: %v", writer.Contents)
	}
	// The pair is what the compile use case renders from, so the two agree file for file.
	compiled, err := Compile(Inputs{Instance: instance, Release: release, App: app, Profile: inputs.Profile})
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated.Written) != len(compiled.Files)+3 {
		t.Fatalf("%d written, want the app record, the pair and %d compiled files", len(migrated.Written), len(compiled.Files))
	}
	if migrated.Written[0].State != files.Written || migrated.Written[0].Path != "v2-records/hello/app.json" {
		t.Fatalf("the app record is not written first: %+v", migrated.Written[:1])
	}
	for _, outcome := range migrated.Written[1:3] {
		if outcome.State != files.Written || !strings.HasPrefix(outcome.Path, "v2-records/hello/staging/") {
			t.Fatalf("the environment pair follows the app record: %+v", migrated.Written[:3])
		}
	}
	// The record pair's own identities are the ones the facts give, and the report repeats them.
	spec, err := kernel.DecodeInstance(instance)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := spec.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Release != migrated.Release.ReleaseID || migrated.Instance.Release != spec.Release {
		t.Fatalf("the instance record names %s, the release is %s", spec.Release, migrated.Release.ReleaseID)
	}
	if migrated.Facts.Application != "hello" || len(migrated.Facts.Gaps) == 0 {
		t.Fatalf("facts %+v", migrated.Facts)
	}
	if migrated.Facts.Source.Repository != "nginxinc/nginx-unprivileged" || revision == "" {
		t.Fatalf("source %+v, revision %s", migrated.Facts.Source, revision)
	}
}

// The image is the one fact a migration reads outside the records. A predecessor states the digest it selected,
// which for a multi-platform build is its index's, so the migration asks the registry about that reference —
// through the reader built for the image the release names — and states the platform manifest it answers with.
func TestMigrateStatesThePlatformManifestDigest(t *testing.T) {
	inputs := migrationInputs(t)
	reader := registrytest.New(registrytest.Scenario{Images: map[string]registry.Image{
		declaredDigest: {Digest: platformDigest, MediaType: kernel.ManifestMediaType},
	}})
	named := []string{}
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	migrated, err := Migrate(context.Background(), inputs, func(image string) (registry.Reader, error) {
		named = append(named, image)
		return reader, nil
	}, "/checkout", writer)
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 1 || named[0] != "docker.io/nginxinc/nginx-unprivileged" {
		t.Fatalf("the reader was built for %v, not the image the release names", named)
	}
	if asked := reader.Described; len(asked) != 1 || asked[0] != declaredDigest {
		t.Fatalf("the registry was asked about %v, not the digest the record states", asked)
	}
	if migrated.Release.Artifact.Digest != platformDigest ||
		migrated.Release.Artifact.MediaType != kernel.ManifestMediaType ||
		migrated.Release.Artifact.Platform != artifactPlatform {
		t.Fatalf("the release states %+v", migrated.Release.Artifact)
	}
	if source := migrated.Facts.Sources["artifact.digest"]; source != "registry" {
		t.Fatalf("the report says the digest came from %q", source)
	}
	written, err := kernel.DecodeRelease(writer.Contents["v2-records/hello/staging/release.json"])
	if err != nil {
		t.Fatal(err)
	}
	if written.Artifact.Digest != platformDigest {
		t.Fatalf("the record states %s, not the platform manifest %s", written.Artifact.Digest, platformDigest)
	}
}

// A registry that cannot be read fails the migration before anything is written: the record would otherwise
// state a digest the migration never checked.
func TestMigrateRefusesAnUnreadableRegistry(t *testing.T) {
	inputs := migrationInputs(t)
	failure := &registry.Error{Code: registry.Unavailable, Subject: "registry", Reason: "forbidden"}
	reader := registrytest.New(registrytest.Scenario{})
	reader.Fail = failure
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	_, err := Migrate(context.Background(), inputs, func(string) (registry.Reader, error) { return reader, nil },
		"/checkout", writer)
	if !errors.Is(err, failure) {
		t.Fatalf("a failed registry read gave %v", err)
	}
	if len(writer.Contents) != 0 {
		t.Fatalf("a failed read wrote %d files", len(writer.Contents))
	}
}

// An instance with a broker identity keeps its seed beside its record, not only beside its manifests, so the
// record folder alone is what a later compile reads. The migration copies the encrypted file as it found it.
func TestMigrateWritesTheSeedBesideTheRecord(t *testing.T) {
	read := func(name string) []byte {
		data, err := os.ReadFile("../testdata/legacy/demo-notifier/staging/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	profile, err := os.ReadFile("../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	seed := []byte("a ciphertext v2 never reads\n")
	documents := offered(t, "../testdata/legacy/demo-notifier/staging/offers")
	inputs := MigrationInputs{
		Records: legacy.Records{App: read("app.json"), Instance: read("instance.json"),
			Release: read("release.json"), Committed: read("helmrelease.json")},
		Supplied:     legacy.Supplied{Repository: "samclement/demo-notifier", Commit: "eab35a3" + strings.Repeat("0", 33)},
		Profile:      profile,
		EventsSecret: seed,
		Offers:       documents,
	}
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	if _, err := migrate(t, inputs, writer); err != nil {
		t.Fatal(err)
	}
	kept, ok := writer.Contents["v2-records/demo-notifier/staging/events.sops.yaml"]
	if !ok || string(kept) != string(seed) {
		t.Fatalf("the record folder keeps %q, not the supplied seed", kept)
	}
	replay := &filestest.Fake{Contents: map[string][]byte{}}
	if _, err := CompileInto(Inputs{
		Instance: writer.Contents["v2-records/demo-notifier/staging/instance.json"],
		Release:  writer.Contents["v2-records/demo-notifier/staging/release.json"],
		App:      writer.Contents["v2-records/demo-notifier/app.json"],
		Profile:  profile, EventsSecret: kept, Offers: documents,
	}, "/checkout", replay); err != nil {
		t.Fatal(err)
	}
	for path, content := range replay.Contents {
		// The records themselves are inputs to the replay, not its output.
		if strings.HasPrefix(path, "v2-records/") {
			continue
		}
		if got := writer.Contents[path]; string(got) != string(content) {
			t.Errorf("%s: the record folder renders %q, the migration wrote %q", path, content, got)
		}
	}
	for _, document := range documents {
		path := "v2-records/demo-notifier/staging/offers/" + document.Path
		if got := writer.Contents[path]; string(got) != string(document.Document) {
			t.Errorf("%s: the record folder keeps %q, not %q", path, got, document.Document)
		}
	}
}

// A translation refusal writes nothing: the pair is only written once every fact is sound.
func TestMigrateRefusesBeforeWriting(t *testing.T) {
	inputs := migrationInputs(t)
	inputs.Supplied.Repository = "nginxinc/other"
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	if _, err := migrate(t, inputs, writer); err == nil {
		t.Fatal("a source the records contradict was accepted")
	}
	if len(writer.Contents) != 0 {
		t.Fatalf("a refusal wrote %d files", len(writer.Contents))
	}
}

// The resolution is read by the same helper the compilation used, so a malformed one is refused before
// anything is written, even when the instance needs nothing from it.
func TestMigrateRefusesAMalformedResolution(t *testing.T) {
	inputs := migrationInputs(t)
	inputs.Calls = []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `"}`)
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	if _, err := migrate(t, inputs, writer); err == nil {
		t.Fatal("a document that is not a resolution was accepted")
	}
	if len(writer.Contents) != 0 {
		t.Fatalf("a refusal wrote %d files", len(writer.Contents))
	}
}

// edited decodes one legacy document, changes it and encodes it again: what the snapshot would carry.
func edited(t *testing.T, document []byte, change func(map[string]any)) []byte {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatal(err)
	}
	change(decoded)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// workerInputs is the fixture as a worker: a kind that serves nothing, so the release states neither an HTTP
// port nor a health path, and the instance cannot be exposed.
func workerInputs(t *testing.T) MigrationInputs {
	t.Helper()
	inputs := migrationInputs(t)
	inputs.Records.Release = edited(t, inputs.Records.Release, func(document map[string]any) {
		document["kind"] = "worker"
		delete(document, "port")
		delete(document, "healthPath")
	})
	inputs.Records.Instance = edited(t, inputs.Records.Instance, func(document map[string]any) {
		document["exposure"] = "private"
	})
	return inputs
}

// A worker migrates to the same record pair a web instance gets, without the HTTP facts, and its files serve
// nothing: the migration carries a kind rather than assuming one.
func TestMigrateAWorker(t *testing.T) {
	inputs := workerInputs(t)
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	migrated, err := migrate(t, inputs, writer)
	if err != nil {
		t.Fatal(err)
	}
	release, err := kernel.DecodeRelease(writer.Contents["v2-records/hello/staging/release.json"])
	if err != nil {
		t.Fatal(err)
	}
	if release.Runtime.Kind != "worker" || release.Runtime.Port != 0 || release.Runtime.HealthPath != "" {
		t.Fatalf("the release's runtime: %+v", release.Runtime)
	}
	instance, err := kernel.DecodeInstance(writer.Contents["v2-records/hello/staging/instance.json"])
	if err != nil {
		t.Fatal(err)
	}
	if instance.Grants.Exposure != "private" {
		t.Fatalf("a worker's exposure: %q", instance.Grants.Exposure)
	}
	if migrated.Facts.Workload.Kind != "worker" {
		t.Fatalf("the translated workload: %+v", migrated.Facts.Workload)
	}
	for _, file := range migrated.Compilation.Files {
		if !strings.HasSuffix(file.Path, "/helmrelease.json") {
			continue
		}
		var document struct {
			Spec struct {
				Values map[string]any
			}
		}
		if err := json.Unmarshal(file.Document, &document); err != nil {
			t.Fatal(err)
		}
		if service, found := document.Spec.Values["service"]; found {
			t.Fatalf("a migrated worker serves a port: %v", service)
		}
	}
	for _, claim := range migrated.Compilation.Inventory {
		if claim.Kind == "Ingress" {
			t.Fatal("a migrated worker claims a route")
		}
	}
}

// offered reads a predecessor's published documents out of a frozen fixture folder, as the operator supplies them:
// each file by its name, in name order.
func offered(t *testing.T, folder string) []kernel.File {
	t.Helper()
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	documents := make([]kernel.File, 0, len(entries))
	for _, entry := range entries {
		data, err := os.ReadFile(folder + "/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, kernel.File{Path: entry.Name(), Document: data})
	}
	return documents
}

// The documents and the commit they were read at travel together: a record that states one without the other is a
// console link to nothing, so the migration refuses either half before it writes anything.
func TestMigrateRefusesDocumentsWithoutProvenance(t *testing.T) {
	withDocuments := migrationInputs(t)
	withDocuments.Offers = []kernel.File{{Path: "asyncapi.yaml", Document: []byte("events: {}\n")}}
	if _, err := migrate(t, withDocuments, &filestest.Fake{Contents: map[string][]byte{}}); err == nil ||
		!strings.Contains(err.Error(), "documents_without_offers") {
		t.Fatalf("documents with no provenance: %v", err)
	}
	withoutDocuments := migrationInputs(t)
	withoutDocuments.Records.Release = edited(t, withoutDocuments.Records.Release, func(document map[string]any) {
		document["offers"] = revision[:7]
	})
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	if _, err := migrate(t, withoutDocuments, writer); err == nil ||
		!strings.Contains(err.Error(), "offers_without_documents") {
		t.Fatalf("provenance with no documents: %v", err)
	}
	if len(writer.Contents) != 0 {
		t.Fatalf("a refusal wrote %d files", len(writer.Contents))
	}
}
