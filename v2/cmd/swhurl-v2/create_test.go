package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
)

const (
	createCommit = "dddddddddddddddddddddddddddddddddddddddd"
	createTag    = "43-" + createCommit
	createDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	createRepo   = "ghcr.io/samclement/example"
)

// createLabel is the declaration an image build writes: a web app with a database, the volume it is created with, a
// floor of 128 MiB it runs in, and the documents it offers.
const createLabel = `{"schemaVersion":2,"runtime":{"kind":"web","port":8080,"healthPath":"/healthz",` +
	`"uid":65532},"database":"sqlite","databaseSizeMiB":1024,"memoryMiB":128,` +
	`"description":"An example","apiPath":"/openapi.json",` +
	`"documents":{"api":"e30=","events":"e30="}}`

// createCommand runs the create command, with the registry, the encrypter and the seed a test supplies, in a fresh
// destination: --records is the tree under it, as every writing command names it.
func createCommand(t *testing.T, arguments []string) (int, string, string, *filestest.Fake, *sopstest.Fake) {
	t.Helper()
	return createCommandLabel(t, createLabel, arguments)
}

// createCommandLabel is createCommand with the declaration label the image states, so a refusal about the label itself
// is exercised as well.
func createCommandLabel(t *testing.T, label string, arguments []string) (int, string, string, *filestest.Fake, *sopstest.Fake) {
	t.Helper()
	out := t.TempDir()
	writer, encrypter := filestest.New(), sopstest.New()
	images := registrytest.New(registrytest.Scenario{Tags: []string{createTag},
		Images: map[string]registry.Image{createTag: {Digest: createDigest, MediaType: "application/vnd.oci.image.manifest.v1+json",
			Labels: map[string]string{"platform.swhurl.com/declaration": label,
				"org.opencontainers.image.revision": createCommit}}}})
	args := append([]string{"create", "--records", filepath.Join(out, "v2-records"), "--out", out}, arguments...)
	var stdout, diagnostic bytes.Buffer
	status := runWith(args, &stdout, &diagnostic, cli{readFile: os.ReadFile, writer: writer, archives: archiveReader,
		registries: func(string) (registry.Reader, error) { return images, nil },
		seed:       func() ([32]byte, error) { return [32]byte{3}, nil }, encrypter: encrypter})
	return status, stdout.String(), diagnostic.String(), writer, encrypter
}

func createArguments(extra ...string) []string {
	return append([]string{"--app", "example", "--team", "demo", "--image", createRepo + ":" + createTag,
		"--profile", "../../testdata/profile.json"}, extra...)
}

func TestCreateCommandWritesTheRecords(t *testing.T) {
	status, out, diagnostic, writer, encrypter := createCommand(t, createArguments())
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out)
	}
	if document["kind"] != "v2-create" || document["version"] != float64(1) || document["application"] != "example" ||
		document["environment"] != "staging" || document["public"] == "" {
		t.Fatalf("report %v", document)
	}
	identities := document["identities"].(map[string]any)
	if !strings.HasPrefix(identities["app"].(string), "app-sha256:") ||
		!strings.HasPrefix(identities["release"].(string), "rel-sha256:") ||
		!strings.HasPrefix(identities["instance"].(string), "inst-sha256:") {
		t.Errorf("identities %v", identities)
	}
	if !strings.Contains(document["note"].(string), "make v2-render") {
		t.Errorf("the report does not say what runs next: %v", document["note"])
	}
	if !strings.Contains(diagnostic, "create: example/staging wrote 6 file(s)") {
		t.Fatalf("diagnostic %q", diagnostic)
	}
	for _, name := range []string{"app.json", "staging/release.json", "staging/instance.json",
		"staging/events.sops.yaml", "staging/offers/openapi.json", "staging/offers/asyncapi.yaml"} {
		if _, found := writer.Contents["v2-records/example/"+name]; !found {
			t.Errorf("the create did not write v2-records/example/%s", name)
		}
	}
	if len(encrypter.Calls) != 1 {
		t.Fatalf("the seed was encrypted %d times", len(encrypter.Calls))
	}
}

// A create writes records only: the compiled files, the unit and the allocation are the whole tree's, which the next
// render decides, so nothing under the destination but the records is written.
func TestCreateCommandWritesNoCompiledFiles(t *testing.T) {
	status, _, diagnostic, writer, _ := createCommand(t, createArguments())
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	for path := range writer.Contents {
		if !strings.HasPrefix(path, "v2-records/") {
			t.Errorf("the create wrote %s, which is not a record", path)
		}
	}
}

func TestCreateCommandRefusals(t *testing.T) {
	for _, tt := range []struct {
		name      string
		label     string
		arguments []string
		status    int
		reason    string
	}{
		{"no team", "", []string{"--app", "example", "--image", createRepo + ":" + createTag, "--profile",
			"../../testdata/profile.json"}, 2, "usage"},
		{"a tag the platform does not publish", "", []string{"--app", "example", "--team", "demo", "--image",
			createRepo + ":latest", "--profile", "../../testdata/profile.json"}, 2, "artifact.image"},
		{"an image at label version 1", strings.Replace(createLabel, `"schemaVersion":2`, `"schemaVersion":1`, 1),
			createArguments(), 2, "schemaVersion"},
		{"a database with no volume", strings.Replace(createLabel, `,"databaseSizeMiB":1024`, ``, 1),
			createArguments(), 2, "databaseSizeMiB"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			label := tt.label
			if label == "" {
				label = createLabel
			}
			status, out, diagnostic, writer, _ := createCommandLabel(t, label, tt.arguments)
			if status != tt.status {
				t.Fatalf("exit %d, want %d: %s", status, tt.status, diagnostic)
			}
			if out != "" {
				t.Errorf("a refused create reported %q on stdout", out)
			}
			if !strings.Contains(diagnostic, tt.reason) {
				t.Errorf("diagnostic %q does not name %q", diagnostic, tt.reason)
			}
			if len(writer.Contents) != 0 {
				t.Errorf("a refused create wrote %v", writer.Contents)
			}
		})
	}
}

// An app that already has records is refused: a create states what an app starts with, and a second one would overwrite
// a release an instance may be selecting.
func TestCreateCommandRefusesAnAppThatExists(t *testing.T) {
	out := t.TempDir()
	records := filepath.Join(out, "v2-records")
	if err := os.MkdirAll(filepath.Join(records, "example", "staging"), 0o755); err != nil {
		t.Fatal(err)
	}
	writer := filestest.New()
	var stdout, diagnostic bytes.Buffer
	status := runWith(append([]string{"create", "--records", records, "--out", out}, createArguments()...),
		&stdout, &diagnostic, cli{readFile: os.ReadFile, writer: writer,
			registries: func(string) (registry.Reader, error) { return registrytest.New(registrytest.Scenario{}), nil },
			seed:       randomSeed, encrypter: sopstest.New()})
	if status != 2 || !strings.Contains(diagnostic.String(), "the_app_already_has_records") {
		t.Fatalf("exit %d: %s", status, diagnostic.String())
	}
	if len(writer.Contents) != 0 {
		t.Errorf("a refused create wrote %v", writer.Contents)
	}
}
