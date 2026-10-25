package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// runProxy runs the proxy command with the files it was given.
func runProxy(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic strings.Builder
	status := run(append([]string{"proxy"}, extra...), &out, &diagnostic, os.ReadFile, files.New())
	return status, out.String(), diagnostic.String()
}

// allocationFile writes an allocation over a snapshot with one granted instance and returns its path.
func allocationFile(t *testing.T) string {
	t.Helper()
	status, stdout, stderr := runEgress(t, writeSnapshot(t, snapshotWithEgress(t, []string{"api.stripe.com"})))
	if status != 0 {
		t.Fatalf("egress exit %d: %s", status, stderr)
	}
	path := filepath.Join(t.TempDir(), "egress.json")
	if err := os.WriteFile(path, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// proxyOf parses the document the command prints.
func proxyOf(t *testing.T, data string) map[string]any {
	t.Helper()
	document := map[string]any{}
	if err := json.Unmarshal([]byte(data), &document); err != nil {
		t.Fatalf("the proxy document is not JSON: %v", err)
	}
	return document
}

// The command renders the proxy's objects from the allocation and writes them where it is told: the
// document says what it was rendered from, and the files are the same bytes.
func TestProxyRendersAndWrites(t *testing.T) {
	allocation := allocationFile(t)
	status, stdout, stderr := runProxy(t, "--allocation", allocation, "--profile", "../../testdata/profile.json")
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	document := proxyOf(t, stdout)
	if document["kind"] != "v2-proxy" || document["version"] != float64(1) {
		t.Fatalf("envelope: %v", document)
	}
	if revision, _ := document["revision"].(string); len(revision) != 40 {
		t.Fatalf("revision: %v", document["revision"])
	}
	inputs := document["inputs"].(map[string]any)
	if profileID, _ := inputs["profile"].(string); !strings.HasPrefix(profileID, "prof-sha256:") {
		t.Fatalf("inputs: %v", inputs)
	}
	items := document["files"].([]any)
	if len(items) != 2 {
		t.Fatalf("files: %v", items)
	}
	paths := []string{}
	for _, raw := range items {
		paths = append(paths, raw.(map[string]any)["path"].(string))
	}
	if paths[0] != "egress/configmap.json" || paths[1] != "egress/networkpolicy.json" {
		t.Fatalf("paths: %v", paths)
	}
	inventory := document["inventory"].([]any)
	if len(inventory) != 2 || inventory[0].(map[string]any)["owner"] != "platform" {
		t.Fatalf("inventory: %v", inventory)
	}

	destination := t.TempDir()
	status, stdout, stderr = runProxy(t, "--allocation", allocation, "--profile", "../../testdata/profile.json", "--out", destination)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if !strings.Contains(stdout, "written egress/configmap.json") || !strings.Contains(stdout, "written egress/networkpolicy.json") {
		t.Fatalf("outcomes: %q", stdout)
	}
	for _, name := range []string{"configmap.json", "networkpolicy.json"} {
		data, err := os.ReadFile(filepath.Join(destination, "egress", name))
		if err != nil {
			t.Fatal(err)
		}
		var object struct{ Kind string }
		if err := json.Unmarshal(data, &object); err != nil || object.Kind == "" {
			t.Fatalf("%s is not an object document: %v", name, err)
		}
	}
	// A second run changes nothing.
	status, stdout, stderr = runProxy(t, "--allocation", allocation, "--profile", "../../testdata/profile.json", "--out", destination)
	if status != 0 || !strings.Contains(stdout, "unchanged egress/configmap.json") {
		t.Fatalf("exit %d: %s%s", status, stdout, stderr)
	}
}

// Nothing is rendered from a document this command did not write, or from one that names no commit,
// and a destination inside v1's app tree is refused as it is for a compilation.
func TestProxyRefusals(t *testing.T) {
	allocation := allocationFile(t)
	data, err := os.ReadFile(allocation)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(map[string]any)
		status int
		want   string
	}{
		{"another document", func(document map[string]any) { document["kind"] = "v2-calls" }, 2, "unsupported: egress"},
		{"no revision", func(document map[string]any) { document["revision"] = "main" }, 2, "invalid: egress"},
		{"shared port", func(document map[string]any) {
			instances := document["instances"].([]any)
			instances[1].(map[string]any)["port"] = instances[0].(map[string]any)["port"]
		}, 2, "invalid: egress.port"},
		{"not a host", func(document map[string]any) {
			document["instances"].([]any)[0].(map[string]any)["grants"] = []any{"10.0.0.1"}
		}, 2, "invalid: egress.grant"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			document := map[string]any{}
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			tt.change(document)
			edited, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "egress.json")
			if err := os.WriteFile(path, edited, 0o600); err != nil {
				t.Fatal(err)
			}
			status, _, stderr := runProxy(t, "--allocation", path, "--profile", "../../testdata/profile.json")
			if status != tt.status || !strings.Contains(stderr, tt.want) {
				t.Fatalf("exit %d: %s, want %d saying %q", status, stderr, tt.status, tt.want)
			}
		})
	}
	// A profile without the platform's proxy cannot render one.
	profile := map[string]any{}
	profileData, err := os.ReadFile("../../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(profileData, &profile); err != nil {
		t.Fatal(err)
	}
	delete(profile, "egress")
	edited, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(profilePath, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := runProxy(t, "--allocation", allocation, "--profile", profilePath)
	if status != 2 || !strings.Contains(stderr, "unsupported: egress") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	// The v1 app tree is never a destination, however it is spelled.
	checkout := t.TempDir()
	for _, marker := range []string{".git", "apps"} {
		if err := os.MkdirAll(filepath.Join(checkout, marker), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	status, _, stderr = runProxy(t, "--allocation", allocation, "--profile", "../../testdata/profile.json",
		"--out", filepath.Join(checkout, "apps"))
	if status != 2 || !strings.Contains(stderr, files.RefusedDestination) {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	// A repeated or missing flag is a usage error, not the last value winning.
	if status, _, _ := runProxy(t, "--allocation", allocation, "--allocation", allocation, "--profile", "../../testdata/profile.json"); status != 2 {
		t.Fatalf("a repeated flag: exit %d", status)
	}
	if status, _, _ := runProxy(t, "--allocation", allocation); status != 2 {
		t.Fatalf("a missing profile: exit %d", status)
	}
}
