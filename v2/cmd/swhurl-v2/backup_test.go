package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	kubectl := tool(t, "kubectl")
	parsed := output(t, kubectl, "create", "--dry-run=client", "-o", "json", "--validate=false",
		"-f", "../../../apps/demo-orders/staging/backup.yaml")
	// A multi-document file prints one JSON object per document, not a list.
	decoder := json.NewDecoder(strings.NewReader(parsed))
	var items []map[string]any
	for decoder.More() {
		item := map[string]any{}
		if err := decoder.Decode(&item); err != nil {
			t.Fatalf("v1's backup is not JSON: %v\n%s", err, parsed)
		}
		items = append(items, item)
	}
	if len(items) != 2 {
		t.Fatalf("v1's backup holds %d objects: %s", len(items), parsed)
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
	if renderedImage != "ghcr.io/samclement/swhurl-backup@sha256:f1637924110353f687277141adde4114a461622f7de19fe41098f1a1a5eea7b5" {
		t.Fatalf("v2 renders the image %q", renderedImage)
	}
	if !strings.HasPrefix(committedImage, "ghcr.io/samclement/swhurl-backup:") || committedImage[strings.Index(committedImage, "@"):] != renderedImage[strings.Index(renderedImage, "@"):] {
		t.Fatalf("v1 runs %q, which is not the same artifact as %q", committedImage, renderedImage)
	}
}
