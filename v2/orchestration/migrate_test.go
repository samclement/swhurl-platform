package orchestration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/legacy"
)

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
	migrated, err := Migrate(inputs, "/checkout", writer)
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

// A translation refusal writes nothing: the pair is only written once every fact is sound.
func TestMigrateRefusesBeforeWriting(t *testing.T) {
	inputs := migrationInputs(t)
	inputs.Supplied.Repository = "nginxinc/other"
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	if _, err := Migrate(inputs, "/checkout", writer); err == nil {
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
	if _, err := Migrate(inputs, "/checkout", writer); err == nil {
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
	migrated, err := Migrate(inputs, "/checkout", writer)
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
