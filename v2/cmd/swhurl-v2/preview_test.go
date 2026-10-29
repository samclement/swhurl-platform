package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// legacyFile is one converted legacy record of the hello staging fixture.
func legacyFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "legacy", "testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// previewArgs is the command line for the hello staging preview.
func previewArgs(t *testing.T, committed string) []string {
	t.Helper()
	return []string{"preview",
		"--app", legacyFile(t, "app.json"),
		"--settings", legacyFile(t, "instance.json"),
		"--release", legacyFile(t, "release.json"),
		"--committed", committed,
		"--profile", filepath.Join("..", "..", "testdata", "profile.json"),
	}
}

// runPreview runs the command and returns its exit status and streams.
func runPreview(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := run(args, &out, &diagnostic, os.ReadFile, files.New())
	return status, out.String(), diagnostic.String()
}

// previewOf decodes the report a successful preview writes.
func previewOf(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("the preview is not JSON: %v\n%s", err, stdout)
	}
	return report
}

// findingOf is one comparison finding of a decoded preview.
func findingOf(t *testing.T, report map[string]any, field string) map[string]any {
	t.Helper()
	for _, raw := range report["comparison"].(map[string]any)["findings"].([]any) {
		entry := raw.(map[string]any)
		if entry["field"] == field {
			return entry
		}
	}
	t.Fatalf("no comparison finding for %s", field)
	return nil
}

// The fixture previews as intended: the facts v1 states, the values only its committed output states, the
// gaps it cannot carry, and a comparison that explains the two deliberate changes and nothing else.
func TestPreviewComparesTheCommittedInstance(t *testing.T) {
	status, stdout, stderr := runPreview(t, previewArgs(t, legacyFile(t, "helmrelease.json"))...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	report := previewOf(t, stdout)
	if report["kind"] != "v2-preview" || report["version"] != float64(1) {
		t.Fatalf("envelope %v/%v", report["kind"], report["version"])
	}
	if report["application"] != "hello" || report["environment"] != "staging" || !strings.Contains(report["note"].(string), "not a migration plan") {
		t.Fatalf("identity/note: %v / %q", report["application"], report["note"])
	}
	preview := report["preview"].(map[string]any)
	workload := preview["workload"].(map[string]any)
	if workload["port"] != float64(8080) || workload["healthPath"] != "/" || workload["digest"] == "" {
		t.Fatalf("workload: %v", workload)
	}
	instance := preview["instance"].(map[string]any)
	if instance["replicas"] != float64(1) || instance["memoryMiB"] != float64(128) || instance["exposure"] != "authenticated-web" {
		t.Fatalf("instance: %v", instance)
	}
	sources := preview["sources"].(map[string]any)
	if sources["instance.memory"] != "committed HelmRelease" || sources["workload.digest"] != "release.yaml" {
		t.Fatalf("sources: %v", sources)
	}
	if memory := findingOf(t, report, "resources.memory"); memory["outcome"] != "intentional" || memory["reason"] != "chosen_single_memory" {
		t.Fatalf("memory: %v", memory)
	}
	if image := findingOf(t, report, "image"); image["outcome"] != "intentional" || image["reason"] != "tag_beside_digest" {
		t.Fatalf("image: %v", image)
	}
	for _, field := range []string{"metadata", "chart", "service.http", "probes", "replicas", "env.identity",
		"pod.securityContext", "pod.automountServiceAccountToken", "container.securityContext", "resources.cpuRequest"} {
		if finding := findingOf(t, report, field); finding["outcome"] != "equal" {
			t.Errorf("%s: %v", field, finding)
		}
	}
	comparison := report["comparison"].(map[string]any)
	if len(comparison["unassessed"].([]any)) == 0 {
		t.Fatal("nothing is reported as unassessed")
	}
	// A signed-in route and the team are facts the preview now carries; the image provenance still is.
	if hasGapOf(report, "instance.exposure") || !hasGapOf(report, "image.provenance") || hasGapOf(report, "app.team") {
		t.Fatalf("gaps: %v", report["gaps"])
	}
	if !strings.Contains(stderr, "every assessed field agrees") {
		t.Fatalf("summary %q", stderr)
	}
}

func hasGapOf(report map[string]any, fact string) bool {
	for _, raw := range report["gaps"].([]any) {
		if raw.(map[string]any)["fact"] == fact {
			return true
		}
	}
	return false
}

// A deliberate change in the committed output is visible: the comparison is wired to the documents, not
// to agreement between two copies of one input.
func TestPreviewSeesACommittedDifference(t *testing.T) {
	committed, err := os.ReadFile(legacyFile(t, "helmrelease.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(committed, &document); err != nil {
		t.Fatal(err)
	}
	container := document["spec"].(map[string]any)["values"].(map[string]any)["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)
	container["image"].(map[string]any)["digest"] = "sha256:" + strings.Repeat("c", 64)
	changed, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "committed.json")
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := runPreview(t, previewArgs(t, path)...)
	if status != 0 {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if image := findingOf(t, previewOf(t, stdout), "image"); image["outcome"] != "different" {
		t.Fatalf("image: %v", image)
	}
	if !strings.Contains(stderr, "image different") {
		t.Fatalf("summary %q", stderr)
	}
}

// A layout the preview does not support is refused with a usage/validation status and no report.
func TestPreviewRefusesAnUnsupportedRelease(t *testing.T) {
	release, err := os.ReadFile(legacyFile(t, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(release, &document); err != nil {
		t.Fatal(err)
	}
	// A worker release states neither an HTTP port nor a health path; the preview compares HTTP only, so it
	// is refused for its kind rather than for facts a worker does not have.
	document["kind"] = "worker"
	delete(document, "port")
	delete(document, "healthPath")
	changed, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release.json")
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	args := previewArgs(t, legacyFile(t, "helmrelease.json"))
	for i, arg := range args {
		if arg == "--release" {
			args[i+1] = path
		}
	}
	status, stdout, stderr := runPreview(t, args...)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "only_http_preview") {
		t.Fatalf("exit %d, stdout %q, stderr %q", status, stdout, stderr)
	}
}

// A file the command cannot read is an I/O failure, not a refusal.
func TestPreviewReportsAnUnreadableInput(t *testing.T) {
	args := previewArgs(t, legacyFile(t, "helmrelease.json"))
	for i, arg := range args {
		if arg == "--app" {
			args[i+1] = filepath.Join(t.TempDir(), "missing.json")
		}
	}
	if status, stdout, _ := runPreview(t, args...); status != 1 || stdout != "" {
		t.Fatalf("exit %d, stdout %q", status, stdout)
	}
}
