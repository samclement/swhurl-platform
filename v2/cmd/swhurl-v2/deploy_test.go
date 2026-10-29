package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const (
	newDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	newCommit = "dddddddddddddddddddddddddddddddddddddddd"
	newTag    = "43-" + newCommit
)

// deployedImage is one image a test's registry answers, with the declaration its build wrote.
func deployedImage(digest string) registry.Image {
	label := `{"schemaVersion":1,"runtime":{"kind":"web","port":8080,"healthPath":"/healthz","uid":65532},"telemetry":true}`
	return registry.Image{Digest: digest, MediaType: kernel.ManifestMediaType,
		Labels: map[string]string{kernel.DeclarationLabel: label}}
}

// deploy runs the command against a scripted registry and a destination that writes nothing to disk.
func deploy(t *testing.T, answered *registrytest.Fake) (int, string, string, *filestest.Fake) {
	t.Helper()
	spec, err := kernel.DecodeInstance(fixture(t, "staging.json"))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := spec.Revision()
	if err != nil {
		t.Fatal(err)
	}
	writer := filestest.New()
	var out, diagnostic bytes.Buffer
	status := runWithRegistry([]string{"deploy", "--instance", "../../testdata/staging.json", "--release",
		"../../testdata/r42.json", "--app", "../../testdata/app.json", "--profile", "../../testdata/profile.json",
		"--expect", string(revision), "--out", t.TempDir()}, &out, &diagnostic, os.ReadFile, writer,
		func(string) (registry.Reader, error) { return answered, nil })
	return status, out.String(), diagnostic.String(), writer
}

func TestDeployCommandWritesTheRecordsAndFiles(t *testing.T) {
	images := registrytest.New(registrytest.Scenario{Tags: []string{"42-" + strings.Repeat("b", 40), newTag},
		Images: map[string]registry.Image{newTag: deployedImage(newDigest)}})
	status, out, diagnostic, writer := deploy(t, images)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out)
	}
	if document["kind"] != "v2-deploy" || document["version"] != float64(1) || document["changed"] != true ||
		document["tag"] != newTag {
		t.Fatalf("report %v", document)
	}
	image := document["image"].(map[string]any)
	if image["digest"] != newDigest || image["previous"] != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("image %v", image)
	}
	if len(document["files"].([]any)) == 0 {
		t.Fatal("the report names no files")
	}
	if !strings.Contains(diagnostic, "deploy: demo-orders/staging: image "+newTag) {
		t.Fatalf("diagnostic %q", diagnostic)
	}
	for _, path := range []string{"v2-records/demo-orders/staging/release.json", "v2-records/demo-orders/staging/instance.json",
		"v2-apps/instances/demo-orders/staging/image-repository.json", "clusters/home/app-demo-orders-staging.json"} {
		if _, found := writer.Contents[path]; !found {
			t.Errorf("the delivery did not write %s", path)
		}
	}
	release, err := kernel.DecodeRelease(writer.Contents["v2-records/demo-orders/staging/release.json"])
	if err != nil {
		t.Fatal(err)
	}
	instance, err := kernel.DecodeInstance(writer.Contents["v2-records/demo-orders/staging/instance.json"])
	if err != nil {
		t.Fatal(err)
	}
	if instance.Release != release.ReleaseID {
		t.Fatal("the instance does not select the release it was written with")
	}
}

// The image the instance runs is a no-op: the report says so, and no file is written.
func TestDeployCommandNoOp(t *testing.T) {
	current, err := kernel.DecodeRelease(fixture(t, "r42.json"))
	if err != nil {
		t.Fatal(err)
	}
	images := registrytest.New(registrytest.Scenario{Tags: []string{newTag},
		Images: map[string]registry.Image{newTag: deployedImage(current.Artifact.Digest)}})
	status, out, diagnostic, writer := deploy(t, images)
	if status != 0 || writer.Writes != 0 {
		t.Fatalf("exit %d, %d writes: %s", status, writer.Writes, diagnostic)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatal(err)
	}
	if document["changed"] != false || document["files"] != nil {
		t.Fatalf("a no-op report: %v", document)
	}
	if !strings.Contains(diagnostic, "runs its newest image") {
		t.Fatalf("diagnostic %q", diagnostic)
	}
}

func TestDeployCommandRefusals(t *testing.T) {
	for _, tt := range []struct {
		name   string
		images *registrytest.Fake
		status int
		want   string
	}{
		{"no tag matches", registrytest.New(registrytest.Scenario{Tags: []string{"latest"}}), 2, "no_tag_matches"},
		{"the registry cannot be read", func() *registrytest.Fake {
			fake := registrytest.New(registrytest.Scenario{})
			fake.Fail = &registry.Error{Code: registry.Unavailable, Subject: "registry", Reason: "transport"}
			return fake
		}(), 1, "unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, out, diagnostic, writer := deploy(t, tt.images)
			if status != tt.status {
				t.Fatalf("exit %d, want %d: %s", status, tt.status, diagnostic)
			}
			if out != "" {
				t.Fatalf("a refusal printed a document: %s", out)
			}
			if writer.Writes != 0 {
				t.Fatalf("a refusal wrote %d times", writer.Writes)
			}
			if !strings.Contains(diagnostic, tt.want) {
				t.Fatalf("diagnostic %q lacks %q", diagnostic, tt.want)
			}
		})
	}
}

// A delivery needs the revision the caller reviewed, and a destination to write.
func TestDeployCommandUsage(t *testing.T) {
	for _, args := range [][]string{
		{"deploy", "--instance", "i", "--release", "r", "--app", "a", "--profile", "p", "--out", "o"},
		{"deploy", "--instance", "i", "--release", "r", "--app", "a", "--profile", "p", "--expect", "e"},
		{"deploy", "--instance", "i", "--release", "r", "--app", "a", "--profile", "p", "--expect", "e", "--out", "o",
			"--out", "o"},
	} {
		var out, diagnostic bytes.Buffer
		status := runWithRegistry(args, &out, &diagnostic, os.ReadFile, filestest.New(),
			func(string) (registry.Reader, error) { return registrytest.New(registrytest.Scenario{}), nil })
		if status != 2 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "usage") {
			t.Fatalf("%v: exit %d, out %q, diagnostic %q", args, status, out.String(), diagnostic.String())
		}
	}
}

// A bad artifact image is refused before any registry is asked.
func TestDeployCommandRegistryBuildFailure(t *testing.T) {
	spec, err := kernel.DecodeInstance(fixture(t, "staging.json"))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := spec.Revision()
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	status := runWithRegistry([]string{"deploy", "--instance", "../../testdata/staging.json", "--release",
		"../../testdata/r42.json", "--app", "../../testdata/app.json", "--profile", "../../testdata/profile.json",
		"--expect", string(revision), "--out", t.TempDir()}, &out, &diagnostic, os.ReadFile, filestest.New(),
		func(string) (registry.Reader, error) {
			return nil, &registry.Error{Code: registry.RefusedRequest, Subject: "image", Reason: "invalid_format"}
		})
	if status != 2 || !strings.Contains(diagnostic.String(), "refused_request") {
		t.Fatalf("exit %d: %s", status, diagnostic.String())
	}
}

// The pair v2 renders is v1's committed pair, field for field, and both units wait for the same platform
// unit: one contract, read by the workflow and by Flux, not two opinions.
func TestTheImagePolicyIsV1sCommittedObjects(t *testing.T) {
	spec, err := kernel.DecodeInstance(fixture(t, "staging.json"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := kernel.DecodeRelease(fixture(t, "r42.json"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := kernel.DecodeApp(fixture(t, "app.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := kernel.DecodeProfile(fixture(t, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := kernel.CompileWith(spec, release, app, profile, kernel.Connections{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	documents := map[string]map[string]any{}
	for _, file := range compiled.Files {
		var document map[string]any
		if err := json.Unmarshal(file.Document, &document); err != nil {
			t.Fatal(err)
		}
		documents[file.Path] = document
	}
	v1 := yamlDocuments(t, "../../../apps/demo-orders/staging/image-automation.yaml")
	if len(v1) != 2 {
		t.Fatalf("%d v1 objects", len(v1))
	}
	for i, path := range []string{"v2-apps/instances/demo-orders/staging/image-repository.json",
		"v2-apps/instances/demo-orders/staging/image-policy.json"} {
		if !reflect.DeepEqual(documents[path], v1[i]) {
			t.Errorf("%s is not v1's committed object:\nv2 %v\nv1 %v", path, documents[path], v1[i])
		}
	}
	unit := yamlDocuments(t, "../../../clusters/home/app-demo-orders-staging.yaml")[0]
	if !hasDependency(unit, kernel.ImageAutomationUnit) {
		t.Fatalf("v1's unit does not wait for %s", kernel.ImageAutomationUnit)
	}
	if !hasDependency(documents["clusters/home/app-demo-orders-staging.json"], kernel.ImageAutomationUnit) {
		t.Fatalf("v2's unit does not wait for %s", kernel.ImageAutomationUnit)
	}
}

// hasDependency is whether a unit's spec.dependsOn names one unit.
func hasDependency(unit map[string]any, name string) bool {
	spec, _ := unit["spec"].(map[string]any)
	dependsOn, _ := spec["dependsOn"].([]any)
	for _, dependency := range dependsOn {
		if named, ok := dependency.(map[string]any); ok && named["name"] == name {
			return true
		}
	}
	return false
}
