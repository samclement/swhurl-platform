package orchestration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const (
	createCommit = "dddddddddddddddddddddddddddddddddddddddd"
	createTag    = "43-" + createCommit
	createDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	createImage  = "ghcr.io/samclement/demo-orders"
)

// createDeclaration is the label a web image with a database and one published event writes: the facts a create
// takes from the image and nowhere else.
const createDeclaration = `{"schemaVersion":1,"runtime":{"kind":"web","port":8080,"healthPath":"/healthz",` +
	`"uid":65532},"telemetry":true,"database":"sqlite","publishes":["created"],"memoryMiB":128}`

func createSeed() [32]byte {
	var seed [32]byte
	for index := range seed {
		seed[index] = 7
	}
	return seed
}

// createRepository is a repository answering one published image.
func createRepository(label string) *registrytest.Fake {
	return registrytest.New(registrytest.Scenario{Tags: []string{createTag},
		Images: map[string]registry.Image{createTag: {Digest: createDigest, MediaType: kernel.ManifestMediaType,
			Labels: map[string]string{kernel.DeclarationLabel: label, kernel.RevisionLabel: createCommit}}}})
}

func createInputs(t *testing.T) CreateInputs {
	t.Helper()
	profile, err := os.ReadFile("../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	return CreateInputs{Application: "demo-orders", Team: "demo", Image: createImage + ":" + createTag,
		Description: "Takes orders, keeps them in SQLite", APIPath: "/openapi.json",
		Offers:  []kernel.File{{Path: "openapi.json", Document: []byte(`{"openapi":"3.1.0"}`)}},
		Profile: profile, RecordsRoot: "v2-records", StorageMiB: 1024, Seed: createSeed()}
}

// create runs the use case against a scripted registry and a destination that writes nothing to disk.
func create(t *testing.T, label string, change func(*CreateInputs)) (Created, *filestest.Fake, *sopstest.Fake, error) {
	t.Helper()
	images, writer, encrypter := createRepository(label), filestest.New(), sopstest.New()
	inputs := createInputs(t)
	if change != nil {
		change(&inputs)
	}
	created, err := Create(context.Background(), inputs, func(string) (registry.Reader, error) { return images, nil },
		t.TempDir(), writer, encrypter)
	return created, writer, encrypter, err
}

// A create states the release its image publishes, the records an instance is, and the encrypted seed before the
// record that names its public key.
func TestCreateWritesTheRecordsAnImageStates(t *testing.T) {
	created, writer, encrypter, err := create(t, createDeclaration, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.Tag != createTag || created.Public != kernel.NewUserKey(createSeed()).Public {
		t.Fatalf("created %+v", created)
	}
	want := []string{"v2-records/demo-orders/app.json", "v2-records/demo-orders/staging/release.json",
		"v2-records/demo-orders/staging/events.sops.yaml", "v2-records/demo-orders/staging/instance.json",
		"v2-records/demo-orders/staging/offers/openapi.json"}
	if len(created.Written) != len(want) {
		t.Fatalf("written %+v", created.Written)
	}
	for index, path := range want {
		if created.Written[index].Path != path {
			t.Errorf("file %d is %s, want %s", index, created.Written[index].Path, path)
		}
	}
	release := created.Release
	if release.Source.Repository != "samclement/demo-orders" || release.Source.Commit != createCommit {
		t.Errorf("the source: %+v", release.Source)
	}
	if release.Artifact.Image != createImage || release.Artifact.Digest != createDigest ||
		release.Artifact.Platform != "linux/amd64" {
		t.Errorf("the artifact: %+v", release.Artifact)
	}
	if release.Database != "sqlite" || !release.Telemetry || strings.Join(release.Publishes, ",") != "created" {
		t.Errorf("what the image states: %+v", release)
	}
	if release.Description != "Takes orders, keeps them in SQLite" || release.APIPath != "/openapi.json" ||
		release.Offers != createCommit {
		t.Errorf("what it offers: %+v", release)
	}
	instance, err := kernel.DecodeInstance(writer.Contents["v2-records/demo-orders/staging/instance.json"])
	if err != nil {
		t.Fatal(err)
	}
	if instance.Release != release.ReleaseID || instance.Configuration.MemoryMiB != 128 ||
		instance.Storage.SizeMiB != 1024 || instance.Events.Key != created.Public {
		t.Errorf("the instance: %+v", instance)
	}
	app, err := kernel.DecodeApp(writer.Contents["v2-records/demo-orders/app.json"])
	if err != nil {
		t.Fatal(err)
	}
	if app.Team != "demo" {
		t.Errorf("the app record: %+v", app)
	}
	if len(encrypter.Calls) != 1 || !strings.Contains(string(encrypter.Calls[0].Document), "SWHURL_EVENTS_SEED") {
		t.Errorf("the seed was not encrypted: %+v", encrypter.Calls)
	}
}

// A reference the registry cannot read, a label without a declaration, and a revision the tag does not abbreviate are
// all refused before anything is written.
func TestCreateRefusesBeforeItWrites(t *testing.T) {
	for _, tt := range []struct {
		name, label, image string
		change             func(*CreateInputs)
		reason             string
	}{
		{name: "an image that states no declaration", label: `{}`, reason: "declaration"},
		{name: "an image v2 cannot take", label: strings.Replace(createDeclaration, `"database":"sqlite"`,
			`"database":"sqlite","secrets":["API_KEY"]`, 1), reason: "bindings_are_not_modelled"},
		{name: "an image with no volume for its database", label: createDeclaration,
			change: func(i *CreateInputs) { i.StorageMiB = 0 }, reason: "volume size"},
		{name: "a memory below the image's floor", label: createDeclaration,
			change: func(i *CreateInputs) { i.MemoryMiB = 64 }, reason: "memory"},
		{name: "a reference outside the platform's tag contract", label: createDeclaration,
			image: createImage + ":latest", reason: "artifact.image"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			created, writer, _, err := create(t, tt.label, func(i *CreateInputs) {
				if tt.image != "" {
					i.Image = tt.image
				}
				if tt.change != nil {
					tt.change(i)
				}
			})
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error = %v, want it to name %q", err, tt.reason)
			}
			if writer.Writes != 0 || len(created.Written) != 0 {
				t.Errorf("a refused create wrote %+v", created.Written)
			}
		})
	}
}
