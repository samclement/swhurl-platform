package apps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/provisioning"
)

const (
	createCommit = "dddddddddddddddddddddddddddddddddddddddd"
	createTag    = "43-" + createCommit
	createDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	createImage  = "ghcr.io/samclement/demo-orders"
)

// createLabel is the declaration a web image with a database and one published event writes: the facts a create takes
// from the image and nowhere else, the documents it offers among them.
func createLabel(t *testing.T, change func(*kernel.Declaration)) string {
	t.Helper()
	declaration := kernel.Declaration{SchemaVersion: 2,
		Runtime:   kernel.Runtime{Kind: "web", Port: 8080, HealthPath: "/healthz", UID: 65532},
		Telemetry: true, Database: "sqlite", DatabaseSizeMiB: 1024, Publishes: []string{"created"},
		MemoryMiB: 128, Description: "Takes orders, keeps them in SQLite", APIPath: "/openapi.json",
		Documents: map[string][]byte{
			"api":    []byte(`{"openapi":"3.1.0"}`),
			"events": []byte(`{"asyncapi":"3.0.0"}`),
		}}
	if change != nil {
		change(&declaration)
	}
	document, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	return string(document)
}

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
		Profile: profile, RecordsRoot: "v2-records", Seed: createSeed()}
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

// A create states the release its image publishes, the records an instance is, the documents the label carries byte
// for byte, and the encrypted seed before the record that names its public key.
func TestCreateWritesTheRecordsAnImageStates(t *testing.T) {
	created, writer, encrypter, err := create(t, createLabel(t, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.Tag != createTag || created.Public != kernel.NewUserKey(createSeed()).Public {
		t.Fatalf("created %+v", created)
	}
	want := []string{"v2-records/demo-orders/app.json", "v2-records/demo-orders/staging/release.json",
		"v2-records/demo-orders/staging/events.sops.yaml", "v2-records/demo-orders/staging/instance.json",
		"v2-records/demo-orders/staging/offers/openapi.json", "v2-records/demo-orders/staging/offers/asyncapi.yaml"}
	if len(created.Written) != len(want) {
		t.Fatalf("written %+v", created.Written)
	}
	for index, path := range want {
		if created.Written[index].Path != path {
			t.Errorf("file %d is %s, want %s", index, created.Written[index].Path, path)
		}
	}
	if got := string(writer.Contents["v2-records/demo-orders/staging/offers/openapi.json"]); got != `{"openapi":"3.1.0"}` {
		t.Errorf("the api document is %q, not the label's bytes", got)
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
	if release.Description != "Takes orders, keeps them in SQLite" || release.APIPath != "/openapi.json" {
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
	pair := kernel.NewUserKey(createInputs(t).Seed)
	for path, document := range writer.Contents {
		if strings.Contains(string(document), pair.Seed) {
			t.Fatalf("%s holds the plaintext seed", path)
		}
	}
	if string(writer.Contents["v2-records/demo-orders/staging/events.sops.yaml"]) != string(encrypter.Output) {
		t.Fatal("the seed file is not the encrypter's output")
	}
}

// A reference the registry cannot read, a label without a declaration, a revision the tag does not abbreviate and a
// profile with no recipient to encrypt the seed to are all refused before anything is written.
func TestCreateRefusesBeforeItWrites(t *testing.T) {
	for _, tt := range []struct {
		name, label string
		change      func(*CreateInputs)
		reason      string
	}{
		{name: "an image that states no declaration", label: `{}`, reason: "declaration"},
		{name: "an image at label version 1", label: strings.Replace(createLabel(t, nil), `"schemaVersion":2`,
			`"schemaVersion":1`, 1), reason: "schemaVersion"},
		{name: "an image that needs secrets", label: createLabel(t, func(d *kernel.Declaration) {
			d.Secrets = []string{"API_KEY"}
		}), reason: "bindings_are_not_modelled"},
		{name: "an image with no volume for its database", label: createLabel(t, func(d *kernel.Declaration) {
			d.DatabaseSizeMiB = 0
		}), reason: "databaseSizeMiB"},
		{name: "a publication without its events document", label: createLabel(t, func(d *kernel.Declaration) {
			delete(d.Documents, "events")
		}), reason: "documents.events"},
		{name: "a reference outside the platform's tag contract", label: createLabel(t, nil),
			change: func(i *CreateInputs) { i.Image = createImage + ":latest" }, reason: "artifact.image"},
		{name: "a profile that states no recipient", label: createLabel(t, nil), change: func(i *CreateInputs) {
			i.Profile = regexp.MustCompile(`"recipient": "[^"]*"`).ReplaceAll(i.Profile, []byte(`"recipient": ""`))
		}, reason: "the_profile_states_no_recipient"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			created, writer, encrypter, err := create(t, tt.label, tt.change)
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error = %v, want it to name %q", err, tt.reason)
			}
			if tt.reason == "the_profile_states_no_recipient" {
				if _, ok := err.(*provisioning.Refusal); !ok || err.Error() != "refused: sops: the_profile_states_no_recipient" {
					t.Fatalf("missing recipient must surface provisioning's refusal unchanged: %T %v", err, err)
				}
			}
			if len(encrypter.Calls) != 0 {
				t.Fatal("a refused create encrypted a seed")
			}
			if writer.Writes != 0 || len(created.Written) != 0 {
				t.Errorf("a refused create wrote %+v", created.Written)
			}
		})
	}
}

// Encryption failures and unsafe ciphertext never reach the record writer.
func TestCreateStopsBeforeWritingAnUnsafeSeed(t *testing.T) {
	inputs := createInputs(t)
	failure := errors.New("encryption failed")
	for _, tt := range []struct {
		name   string
		output []byte
		err    error
		reason string
	}{
		{name: "encryption failed", err: failure, reason: failure.Error()},
		{name: "plaintext seed even with encrypted values", output: []byte("ENC[fake] " + kernel.NewUserKey(inputs.Seed).Seed), reason: "plaintext seed"},
		{name: "no encrypted value", output: []byte("not encrypted"), reason: "no encrypted value"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writer, encrypter := filestest.New(), sopstest.New()
			encrypter.Output, encrypter.Err = tt.output, tt.err
			images := createRepository(createLabel(t, nil))
			created, err := Create(context.Background(), inputs, func(string) (registry.Reader, error) { return images, nil },
				t.TempDir(), writer, encrypter)
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error = %v, want %q", err, tt.reason)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Fatalf("encryption error was not propagated: %v", err)
			}
			if len(encrypter.Calls) != 1 || writer.Writes != 0 || len(created.Written) != 0 {
				t.Fatal("an unsafe seed reached the writer or was not encrypted once")
			}
		})
	}
}
