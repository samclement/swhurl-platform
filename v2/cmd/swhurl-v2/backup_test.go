package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// compiledDatabase is the CLI's compilation of the SQLite fixture, allocated at v1's port for that
// instance, with each file's document.
func compiledDatabase(t *testing.T) map[string]map[string]any {
	t.Helper()
	allocation := filepath.Join(t.TempDir(), "egress.json")
	document := `{"kind":"v2-egress","version":1,"revision":"` + strings.Repeat("b", 40) +
		`","instances":[{"application":"demo-orders","environment":"staging","port":30102,"grants":[]}]}`
	if err := os.WriteFile(allocation, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic strings.Builder
	status := run([]string{"compile", "--instance", "../../testdata/sqlite.json", "--release", "../../testdata/r43.json",
		"--profile", "../../testdata/profile.json", "--egress", allocation}, &out, &diagnostic, os.ReadFile, nil)
	if status != 0 {
		t.Fatalf("compile exit %d: %s", status, diagnostic.String())
	}
	var compiled struct {
		Files []struct {
			Path     string
			Document json.RawMessage
		}
	}
	if err := json.Unmarshal([]byte(out.String()), &compiled); err != nil {
		t.Fatal(err)
	}
	files := map[string]map[string]any{}
	for _, file := range compiled.Files {
		object := map[string]any{}
		if err := json.Unmarshal(file.Document, &object); err != nil {
			t.Fatalf("%s: %v", file.Path, err)
		}
		files[filepath.Base(file.Path)] = object
	}
	return files
}

// committedBackup is v1's rendered backup for the same instance, read as the tools read it: the file is
// YAML and v2 reads none, so kubectl parses it offline.
func committedBackup(t *testing.T) (secret, job map[string]any) {
	t.Helper()
	items := yamlDocuments(t, "../../../apps/demo-orders/staging/backup.yaml")
	if len(items) != 2 {
		t.Fatalf("v1's backup holds %d objects", len(items))
	}
	for _, item := range items {
		// The tools add fields a manifest does not carry.
		metadata, _ := item["metadata"].(map[string]any)
		delete(metadata, "creationTimestamp")
		delete(item, "status")
		switch item["kind"] {
		case "Secret":
			secret = item
		case "CronJob":
			job = item
		}
	}
	return secret, job
}

// TestBackupMatchesTheCommittedJob ties what v2 renders to what v1 runs today for the same instance: the
// same Secret, the same job, field for field. A migration then changes the owner of the file and nothing
// about the backup — the tie that says so, rather than an assertion that it looks about right.
func TestBackupMatchesTheCommittedJob(t *testing.T) {
	if _, err := os.Stat("../../../apps/demo-orders/staging/backup.yaml"); err != nil {
		need(t, "v1's committed backup job")
	}
	files := compiledDatabase(t)
	committedSecret, committedJob := committedBackup(t)
	renderedSecret, renderedJob := files["backup-secret.json"], files["backup-cronjob.json"]
	if renderedSecret == nil || renderedJob == nil {
		t.Fatalf("the compilation holds %d files", len(files))
	}
	if !reflect.DeepEqual(renderedSecret, committedSecret) {
		t.Fatalf("the Secret differs:\nv2 %v\nv1 %v", renderedSecret, committedSecret)
	}
	// v2 pins an image by digest alone; v1 names the tag beside it. Nothing else may differ.
	digestOf := func(object map[string]any) (string, string) {
		container := object["spec"].(map[string]any)["jobTemplate"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		image := container["image"].(string)
		delete(container, "image")
		return image, container["name"].(string)
	}
	renderedImage, _ := digestOf(renderedJob)
	committedImage, _ := digestOf(committedJob)
	if !reflect.DeepEqual(renderedJob, committedJob) {
		t.Fatalf("the CronJob differs:\nv2 %v\nv1 %v", renderedJob, committedJob)
	}
	if renderedImage != "ghcr.io/samclement/swhurl-backup@sha256:b730422b4789b6377ca1b39705cedef61d0c64a0a8161ccb64652dbc7b1db74a" {
		t.Fatalf("v2 renders the image %q", renderedImage)
	}
	if !strings.HasPrefix(committedImage, "ghcr.io/samclement/swhurl-backup:") || committedImage[strings.Index(committedImage, "@"):] != renderedImage[strings.Index(renderedImage, "@"):] {
		t.Fatalf("v1 runs %q, which is not the same artifact as %q", committedImage, renderedImage)
	}
}

// committedFile is a file of this repository, read as the operator's tree has it.
func committedFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../" + path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return data
}

// TestCommittedProxyIsWhatTheAllocationRenders keeps the platform's live proxy configuration truthful: the
// committed objects are exactly what the committed allocation and the profile render. v1 used to compare its
// own generator's output here; from the cutover, v2's render is the authority, so a changed grant, port or
// profile fact that is not re-rendered fails this check instead of waiting to be noticed in the cluster.
func TestCommittedProxyIsWhatTheAllocationRenders(t *testing.T) {
	allocation, err := kernel.DecodeEgressDocument(committedFile(t, "platform/egress/allocation.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := kernel.DecodeProfile(committedFile(t, "v2/testdata/profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := kernel.RenderProxy(allocation.Instances, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Files) != 2 {
		t.Fatalf("the render holds %d files", len(rendered.Files))
	}
	for _, file := range rendered.Files {
		committed := committedFile(t, "platform/"+file.Path)
		if string(committed) != string(file.Document) {
			t.Errorf("platform/%s is not what the allocation renders; run `make v2-egress`", file.Path)
		}
	}
}
