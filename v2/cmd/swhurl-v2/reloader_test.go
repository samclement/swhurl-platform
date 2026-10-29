package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// reloaderInstances is a snapshot with a v1 instance that signs in, one whose release declares a Secret, a
// v2-owned instance with a broker key, and one that declares nothing and must be left out.
func reloaderInstances(t *testing.T) map[string]any {
	t.Helper()
	instance := func(application, environment, format string, settings, release map[string]any) map[string]any {
		return map[string]any{"application": application, "environment": environment, "format": format,
			"missing": []any{}, "objects": []any{},
			"records": map[string]any{"instance": settings, "release": release}}
	}
	key := func(letter string) string { return "U" + strings.Repeat(letter, 55) }
	return map[string]any{"version": 1, "revision": strings.Repeat("a", 40), "residual": []any{},
		"instances": []any{
			instance("orders", "staging", "v1", map[string]any{"allocations": map[string]any{"eventsKey": key("A")}}, map[string]any{}),
			instance("shop", "prod", "v1", map[string]any{}, map[string]any{"secrets": []any{"API_TOKEN"}}),
			instance("notifier", "staging", "v2", map[string]any{"events": map[string]any{"key": key("B")}}, map[string]any{}),
			instance("quiet", "staging", "v2", map[string]any{}, map[string]any{}),
		}}
}

// The command renders the namespaces Reloader watches from both record shapes, the platform's own first.
func TestReloaderRendersTheWatchList(t *testing.T) {
	snapshot, profile := writeSnapshot(t, reloaderInstances(t)), profilePath(t)
	var out, diagnostic bytes.Buffer
	status := run([]string{"reloader", "--snapshot", snapshot, "--profile", profile}, &out, &diagnostic,
		os.ReadFile, files.New())
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic.String())
	}
	var document struct {
		Kind       string
		Version    int
		Revision   string
		Namespaces int
		Files      []struct {
			Path     string
			Document json.RawMessage
		}
		Inventory []map[string]any
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out.String())
	}
	// The platform's three, then staging's two instances by app name, then production's.
	want := []string{"ingress", "logging", "console", "notifier-staging", "orders-staging", "shop-prod"}
	if document.Kind != "v2-reloader" || document.Version != 1 || document.Revision != strings.Repeat("a", 40) {
		t.Fatalf("envelope: %+v", document)
	}
	if document.Namespaces != len(want) || len(document.Files) != 1 ||
		document.Files[0].Path != "reloader/values.json" {
		t.Fatalf("report: %+v", document)
	}
	var configMap struct {
		Metadata struct{ Name, Namespace string }
		Data     map[string]string
	}
	if err := json.Unmarshal(document.Files[0].Document, &configMap); err != nil {
		t.Fatal(err)
	}
	if configMap.Metadata.Name != "reloader-values" || configMap.Metadata.Namespace != "platform-system" {
		t.Fatalf("the object: %+v", configMap.Metadata)
	}
	var values struct {
		Reloader struct{ Namespaces []string }
	}
	if err := json.Unmarshal([]byte(configMap.Data["values.yaml"]), &values); err != nil {
		t.Fatalf("the data key is not JSON: %v", err)
	}
	if !slices.Equal(values.Reloader.Namespaces, want) {
		t.Fatalf("watch list %v, want %v", values.Reloader.Namespaces, want)
	}
	if len(document.Inventory) != 1 {
		t.Fatalf("the claim: %+v", document.Inventory)
	}
	if !strings.Contains(diagnostic.String(), "reloader: 6 namespace(s), 1 file(s)") {
		t.Errorf("the summary: %s", diagnostic.String())
	}
}

// --out writes the rendered file and nothing else, and a second run leaves it unchanged.
func TestReloaderWritesItsFile(t *testing.T) {
	snapshot, profile := writeSnapshot(t, reloaderInstances(t)), profilePath(t)
	destination := t.TempDir()
	for range 2 {
		var out, diagnostic bytes.Buffer
		status := run([]string{"reloader", "--snapshot", snapshot, "--profile", profile, "--out", destination},
			&out, &diagnostic, os.ReadFile, files.New())
		if status != 0 {
			t.Fatalf("exit %d: %s", status, diagnostic.String())
		}
		if diagnostic.String() != "reloader: 6 namespace(s), 1 file(s)\n" ||
			(out.String() != "written reloader/values.json\n" && out.String() != "unchanged reloader/values.json\n") {
			t.Fatalf("stdout %q, diagnostic %q", out.String(), diagnostic.String())
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "reloader", "values.json")); err != nil {
		t.Fatal(err)
	}
}

// An instance whose name or environment could not spell a namespace is refused, not skipped.
func TestReloaderRefusesAnUnspellableInstance(t *testing.T) {
	document := reloaderInstances(t)
	document["instances"].([]any)[1].(map[string]any)["environment"] = "test"
	snapshot, profile := writeSnapshot(t, document), profilePath(t)
	var out, diagnostic bytes.Buffer
	status := run([]string{"reloader", "--snapshot", snapshot, "--profile", profile}, &out, &diagnostic,
		os.ReadFile, files.New())
	if status != 2 || !strings.Contains(diagnostic.String(), "reloader.instance") {
		t.Fatalf("exit %d, diagnostic %q", status, diagnostic.String())
	}
	if out.Len() != 0 {
		t.Fatalf("a refusal wrote a report: %s", out.String())
	}
}
