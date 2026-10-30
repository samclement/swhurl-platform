package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// twoInstances is a snapshot with a caller and its callee, in the exporter's shape: the caller declares the
// call, the callee consents by team, and the callee's release states the port the caller must reach.
func twoInstances(t *testing.T) map[string]any {
	t.Helper()
	record := func(role string) map[string]any {
		data, err := os.ReadFile(legacyFile(t, role+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		return document.(map[string]any)
	}
	instance := func(application string, settings, release map[string]any) map[string]any {
		return map[string]any{
			"application": application, "environment": "staging", "missing": []any{}, "objects": []any{},
			"records": map[string]any{"app": record("app"), "instance": settings, "release": release,
				"helmrelease": record("helmrelease")},
		}
	}
	callerSettings := map[string]any{"version": 1, "exposure": "private", "replicas": 1, "calls": []any{"orders"},
		"egress": []any{"api.example.com:443"}}
	calleeSettings := map[string]any{"version": 1, "exposure": "private", "replicas": 1, "allows": []any{"team:demo"},
		"egress": []any{"api.example.com:443"}}
	release := record("release")
	release["port"] = 8080
	return map[string]any{"version": 1, "revision": strings.Repeat("a", 40), "residual": []any{},
		"instances": []any{instance("shop", callerSettings, release), instance("orders", calleeSettings, release)}}
}

// resolve writes the connected pair; compile --calls renders only the instance's side of it.
func TestResolveThenCompileACalledInstance(t *testing.T) {
	snapshotPath := writeSnapshot(t, twoInstances(t))
	var resolved, diagnostic bytes.Buffer
	status := run([]string{"resolve", "--snapshot", snapshotPath}, &resolved, &diagnostic, os.ReadFile, files.New())
	if status != 0 {
		t.Fatalf("resolve exit %d: %s", status, diagnostic.String())
	}
	document := map[string]any{}
	if err := json.Unmarshal(resolved.Bytes(), &document); err != nil {
		t.Fatalf("the resolution is not JSON: %v", err)
	}
	if document["kind"] != "v2-calls" || document["version"] != float64(1) || document["revision"] != strings.Repeat("a", 40) {
		t.Fatalf("envelope: %v", document)
	}
	pairs := document["pairs"].([]any)
	if len(pairs) != 1 {
		t.Fatalf("pairs: %v", pairs)
	}
	pair := pairs[0].(map[string]any)
	if pair["caller"] != "shop" || pair["callee"] != "orders" || pair["port"] != float64(8080) {
		t.Fatalf("the pair: %v", pair)
	}
	callsPath := filepath.Join(t.TempDir(), "calls.json")
	if err := os.WriteFile(callsPath, resolved.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// The caller compiles with its side of the pair: the callee's address and a token for its audience.
	compiled := compileWithCalls(t, callsPath, "shop", callerRelease(t))
	release := renderedHelmReleaseOf(t, compiled)
	values := release["spec"].(map[string]any)["values"].(map[string]any)
	container := values["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)
	if env := container["env"].(map[string]any); env["SWHURL_SERVICE_ORDERS_URL"] != "http://orders.orders-staging.svc.cluster.local:8080" {
		t.Fatalf("the caller's address: %v", env["SWHURL_SERVICE_ORDERS_URL"])
	}
	if compiled.Inputs.Calls == "" {
		t.Fatal("the compilation does not record the resolution it used")
	}
	// The callee compiles with its side: it knows who may call it.
	calleeCompiled := compileWithCalls(t, callsPath, "orders", calleeRelease(t))
	calleeRelease := renderedHelmReleaseOf(t, calleeCompiled)
	calleeContainer := calleeRelease["spec"].(map[string]any)["values"].(map[string]any)["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)
	if env := calleeContainer["env"].(map[string]any); env["SWHURL_ALLOWED_CALLERS_FILE"] == nil {
		t.Fatalf("the callee does not name its callers file: %v", env)
	}
	allowed := ""
	for _, file := range calleeCompiled.Files {
		if strings.HasSuffix(file.Path, "/callers.json") {
			document := map[string]any{}
			if err := json.Unmarshal(file.Document, &document); err != nil {
				t.Fatal(err)
			}
			allowed, _ = document["data"].(map[string]any)["SWHURL_ALLOWED_CALLERS"].(string)
		}
	}
	if allowed != "shop" {
		t.Fatalf("the callee's callers: %q", allowed)
	}
	if calleeCompiled.Inputs.Calls == compiled.Inputs.Calls {
		t.Fatal("two different sides of one pair share a connections identity")
	}
}

// A v2-owned instance's declarations come from its own pair, and its runtime facts from the nested runtime
// object: the resolution must be the same one a predecessor's records give, or a migrated pair would silently
// lose or invent a relationship.
func TestResolveReadsAV2OwnedPair(t *testing.T) {
	snapshotPath := writeSnapshot(t, twoInstances(t))
	var fromRecords bytes.Buffer
	if status := run([]string{"resolve", "--snapshot", snapshotPath}, &fromRecords, &bytes.Buffer{}, os.ReadFile,
		files.New()); status != 0 {
		t.Fatalf("resolve over v1 records: exit %d", status)
	}

	// The same two instances as v2 owns them: the declarations in the instance record, the runtime nested.
	document := twoInstances(t)
	for _, raw := range document["instances"].([]any) {
		instance := raw.(map[string]any)
		instance["format"] = "v2"
		instance["records"].(map[string]any)["instance"] = map[string]any{
			"schemaVersion": 1, "application": instance["application"], "environment": "staging",
			"calls":  instance["records"].(map[string]any)["instance"].(map[string]any)["calls"],
			"allows": instance["records"].(map[string]any)["instance"].(map[string]any)["allows"],
		}
		instance["records"].(map[string]any)["release"] = map[string]any{
			"schemaVersion": 1, "runtime": map[string]any{"kind": "web", "port": 8080},
		}
	}
	snapshotPath = writeSnapshot(t, document)
	var fromPair bytes.Buffer
	if status := run([]string{"resolve", "--snapshot", snapshotPath}, &fromPair, &bytes.Buffer{}, os.ReadFile,
		files.New()); status != 0 {
		t.Fatalf("resolve over v2 pairs: exit %d", status)
	}
	if fromPair.String() != fromRecords.String() {
		t.Fatalf("a v2 pair resolves differently from the records it replaced:\n%s\n%s", fromPair.String(),
			fromRecords.String())
	}
}

// A v2 record states a team grant, never a member list: the resolution matches it against the caller's own app
// record, so the callee renders a concrete caller name and a caller on another team is refused by the grant
// itself rather than quietly connected.
func TestAV2RecordConsentsByTeam(t *testing.T) {
	owned := func(callerTeam string) string {
		document := twoInstances(t)
		for _, raw := range document["instances"].([]any) {
			instance := raw.(map[string]any)
			records := instance["records"].(map[string]any)
			settings := records["instance"].(map[string]any)
			team := "demo"
			if instance["application"] == "shop" {
				team = callerTeam
			}
			records["app"] = map[string]any{"schemaVersion": 1, "application": instance["application"], "team": team}
			instance["format"] = "v2"
			records["instance"] = map[string]any{
				"schemaVersion": 1, "application": instance["application"], "environment": "staging",
				"calls": settings["calls"], "allows": settings["allows"],
			}
			records["release"] = map[string]any{"schemaVersion": 1, "runtime": map[string]any{"kind": "web", "port": 8080}}
		}
		return writeSnapshot(t, document)
	}
	resolve := func(snapshot string) []byte {
		t.Helper()
		var resolved, diagnostic bytes.Buffer
		if status := run([]string{"resolve", "--snapshot", snapshot}, &resolved, &diagnostic, os.ReadFile,
			files.New()); status != 0 {
			t.Fatalf("resolve exit %d: %s", status, diagnostic.String())
		}
		return resolved.Bytes()
	}
	documentOf := func(raw []byte) map[string]any {
		t.Helper()
		document := map[string]any{}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("the resolution is not JSON: %v", err)
		}
		return document
	}
	resolved := resolve(owned("demo"))
	document := documentOf(resolved)
	pairs := document["pairs"].([]any)
	if len(pairs) != 1 || pairs[0].(map[string]any)["caller"] != "shop" {
		t.Fatalf("the team grant's pair: %v", pairs)
	}
	// The callee's own side names the caller it admitted: the grant decided consent, not the rendered list.
	callsPath := filepath.Join(t.TempDir(), "calls.json")
	if err := os.WriteFile(callsPath, resolved, 0o600); err != nil {
		t.Fatal(err)
	}
	allowed := ""
	for _, file := range compileWithCalls(t, callsPath, "orders", calleeRelease(t)).Files {
		if strings.HasSuffix(file.Path, "/callers.json") {
			callers := map[string]any{}
			if err := json.Unmarshal(file.Document, &callers); err != nil {
				t.Fatal(err)
			}
			allowed, _ = callers["data"].(map[string]any)["SWHURL_ALLOWED_CALLERS"].(string)
		}
	}
	if allowed != "shop" {
		t.Fatalf("the callee's callers: %q", allowed)
	}
	document = documentOf(resolve(owned("payments")))
	if pairs := document["pairs"].([]any); len(pairs) != 0 {
		t.Fatalf("a caller outside the team: %v", pairs)
	}
	if waiting := document["waiting"].([]any); len(waiting) != 1 ||
		waiting[0].(map[string]any)["reason"] != "no_consent" {
		t.Fatalf("the outside caller's declaration: %v", waiting)
	}
}

// compileWithCalls builds one instance's files from the fixture profile and the given resolution.
func compileWithCalls(t *testing.T, callsPath, application string, release kernel.ReleaseBundle) kernel.Compilation {
	t.Helper()
	data, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	connections, err := kernel.ConnectionsOf(data, application, "staging")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := kernel.DecodeProfile(fixture(t, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := kernel.CompileWith(instanceFor(t, application, release), release, kernel.AppSpec{}, profile, connections, nil)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// instanceFor is a private staging instance of one app that selects the given release.
func instanceFor(t *testing.T, application string, release kernel.ReleaseBundle) kernel.InstanceSpec {
	t.Helper()
	identifier, err := release.ID()
	if err != nil {
		t.Fatal(err)
	}
	return kernel.InstanceSpec{SchemaVersion: 1, Application: application, Environment: "staging", Release: identifier,
		Configuration: kernel.Configuration{Replicas: 1, MemoryMiB: 128}, Grants: kernel.Grants{Exposure: "private"}}
}

// callerRelease and calleeRelease are the same artifact under the two applications.
func callerRelease(t *testing.T) kernel.ReleaseBundle { return releaseFor(t, "shop") }
func calleeRelease(t *testing.T) kernel.ReleaseBundle { return releaseFor(t, "orders") }

// mutate is a fixture document with one visible change, re-encoded.
func mutate(t *testing.T, data []byte, change func(map[string]any)) []byte {
	t.Helper()
	document := map[string]any{}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	change(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func releaseFor(t *testing.T, application string) kernel.ReleaseBundle {
	t.Helper()
	release, err := kernel.DecodeRelease(mutate(t, fixture(t, "r42.json"), func(document map[string]any) {
		document["application"] = application
	}))
	if err != nil {
		t.Fatal(err)
	}
	return release
}

// renderedHelmReleaseOf is the HelmRelease among a compilation's files, decoded.
func renderedHelmReleaseOf(t *testing.T, compiled kernel.Compilation) map[string]any {
	t.Helper()
	for _, file := range compiled.Files {
		if strings.HasSuffix(file.Path, "helmrelease.json") {
			document := map[string]any{}
			if err := json.Unmarshal(file.Document, &document); err != nil {
				t.Fatal(err)
			}
			return document
		}
	}
	t.Fatal("no HelmRelease among the compiled files")
	return nil
}

// The platform's keys are rendered once for every managed namespace, from one pinned snapshot: the document
// names them, the files carry them, and writing them puts them where the platform unit applies from.
func TestAuthRendersTheKeysForEveryInstanceNamespace(t *testing.T) {
	snapshotPath := writeSnapshot(t, twoInstances(t))
	var out, diagnostic bytes.Buffer
	status := run([]string{"auth", "--snapshot", snapshotPath, "--profile", filepath.Join("..", "..", "testdata", "profile.json")},
		&out, &diagnostic, os.ReadFile, files.New())
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic.String())
	}
	document := map[string]any{}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["kind"] != "v2-auth" || document["version"] != float64(1) {
		t.Fatalf("envelope %v/%v", document["kind"], document["version"])
	}
	namespaces := document["namespaces"].([]any)
	if len(namespaces) != 2 || namespaces[0] != "shop-staging" || namespaces[1] != "orders-staging" {
		t.Fatalf("the namespaces are %v", namespaces)
	}
	renderedFiles := document["files"].([]any)
	if len(renderedFiles) != 3 {
		t.Fatalf("the files are %v", renderedFiles)
	}
	for _, raw := range renderedFiles {
		file := raw.(map[string]any)
		if file["path"] == "auth/swhurl-auth-shop-staging.json" &&
			!strings.Contains(string(file["document"].(map[string]any)["data"].(map[string]any)["SWHURL_AUTH_KEYS"].(string)), `"keys"`) {
			t.Fatalf("the keys are not in the rendered file: %v", file["document"])
		}
	}
	destination := t.TempDir()
	if status := run([]string{"auth", "--snapshot", snapshotPath, "--profile", filepath.Join("..", "..", "testdata", "profile.json"),
		"--out", destination}, &bytes.Buffer{}, &diagnostic, os.ReadFile, files.New()); status != 0 {
		t.Fatalf("writing: exit %d: %s", status, diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(destination, "auth", "kustomization.yaml")); err != nil {
		t.Fatalf("the folder was not written: %v", err)
	}
}
