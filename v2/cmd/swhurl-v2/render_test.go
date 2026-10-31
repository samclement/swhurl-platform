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
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const renderRevision = "dddddddddddddddddddddddddddddddddddddddd"

// renderRelease is one app's release record, with the identity its content gives it.
func renderRelease(t *testing.T, application, database string) kernel.ReleaseBundle {
	t.Helper()
	release := kernel.ReleaseBundle{SchemaVersion: 1, Application: application,
		Source: kernel.Source{Repository: "samclement/" + application, Commit: strings.Repeat("b", 40)},
		Artifact: kernel.Artifact{Image: "ghcr.io/samclement/" + application, Digest: "sha256:" + strings.Repeat("b", 64),
			MediaType: "application/vnd.oci.image.manifest.v1+json", Platform: "linux/amd64"},
		Runtime:  kernel.Runtime{Kind: "web", Port: 8080, HealthPath: "/healthz", UID: 65532},
		Database: database, Telemetry: true}
	id, err := release.ID()
	if err != nil {
		t.Fatal(err)
	}
	release.ReleaseID = id
	return release
}

// writeRecord writes one record as JSON under the records tree.
func writeRecord(t *testing.T, path string, encode func() ([]byte, error)) {
	t.Helper()
	data, err := encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// renderInstance writes one instance's record pair, and the app record it shares with the app's other instances.
func renderInstance(t *testing.T, records string, spec kernel.InstanceSpec, release kernel.ReleaseBundle) {
	t.Helper()
	folder := filepath.Join(records, spec.Application, spec.Environment)
	spec.SchemaVersion, spec.Release = 1, release.ReleaseID
	writeRecord(t, filepath.Join(folder, instanceFile), func() ([]byte, error) { return kernel.EncodeInstance(spec) })
	writeRecord(t, filepath.Join(folder, releaseFile), func() ([]byte, error) { return kernel.EncodeRelease(release) })
	writeRecord(t, filepath.Join(records, spec.Application, appFile), func() ([]byte, error) {
		return kernel.EncodeApp(kernel.AppSpec{SchemaVersion: 1, Application: spec.Application, Team: "demo"})
	})
}

// renderTree is a checkout with three instances: demo-orders in staging and production, which admits its team, and
// demo-shop in staging, which calls it. It returns the checkout, which is the destination, and its records tree.
func renderTree(t *testing.T) (string, string) {
	t.Helper()
	out := t.TempDir()
	records := filepath.Join(out, "v2-records")
	orders, shop := renderRelease(t, "demo-orders", ""), renderRelease(t, "demo-shop", "")
	for _, environment := range []string{"staging", "prod"} {
		renderInstance(t, records, kernel.InstanceSpec{Application: "demo-orders", Environment: environment,
			Configuration: kernel.Configuration{Replicas: 1, MemoryMiB: 128},
			Grants:        kernel.Grants{Exposure: "private"}, Allows: []string{"team:demo"}}, orders)
	}
	renderInstance(t, records, kernel.InstanceSpec{Application: "demo-shop", Environment: "staging",
		Configuration: kernel.Configuration{Replicas: 1, MemoryMiB: 128},
		Grants:        kernel.Grants{Exposure: "private"}, Calls: []string{"demo-orders"}}, shop)
	return out, records
}

// renderRun runs one render with the writer given, and reads its report.
func renderRun(t *testing.T, writer files.Writer, args ...string) (int, renderDocument, string) {
	t.Helper()
	var stdout, diagnostic bytes.Buffer
	status := run(append([]string{"render"}, args...), &stdout, &diagnostic, os.ReadFile, writer)
	var document renderDocument
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
			t.Fatalf("the report is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return status, document, diagnostic.String()
}

// renderArgs is a render of a records tree into its checkout.
func renderArgs(out, records string, extra ...string) []string {
	return append([]string{"--records", records, "--profile", "../../testdata/profile.json", "--out", out}, extra...)
}

// treeContents is every file under a folder and its bytes, so a test can say a run changed nothing.
func treeContents(t *testing.T, root string) map[string]string {
	t.Helper()
	contents := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		relative, _ := filepath.Rel(root, path)
		contents[filepath.ToSlash(relative)] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return contents
}

// renderedTree renders the fixture tree on disk and returns the checkout and its records.
func renderedTree(t *testing.T) (string, string) {
	t.Helper()
	out, records := renderTree(t)
	if status, _, diagnostic := renderRun(t, files.New(), renderArgs(out, records, "--revision", renderRevision)...); status != 0 {
		t.Fatalf("render exited %d: %s", status, diagnostic)
	}
	return out, records
}

// A render writes every file v2 derives from the records, production as well as staging, and the resolution it
// derives from them connects the pair the records declare.
func TestRenderWritesEveryDerivedFile(t *testing.T) {
	out, records := renderTree(t)
	before := treeContents(t, out)
	status, document, diagnostic := renderRun(t, files.New(), renderArgs(out, records, "--revision", renderRevision)...)
	if status != 0 {
		t.Fatalf("render exited %d: %s", status, diagnostic)
	}
	instance := func(application, environment string, names ...string) []string {
		paths := []string{"clusters/home/app-" + application + "-" + environment + ".json"}
		for _, name := range names {
			paths = append(paths, "v2-apps/instances/"+application+"/"+environment+"/"+name)
		}
		return paths
	}
	manifests := []string{"namespace.json", "helmrelease.json", "resourcequota.json", "networkpolicy.json",
		"kustomization.yaml"}
	want := []string{"v2-records/calls.json", "platform/egress/allocation.json", "platform/egress/configmap.json",
		"platform/egress/networkpolicy.json", "platform/auth/kustomization.yaml",
		"platform/auth/swhurl-auth-demo-orders-prod.json", "platform/auth/swhurl-auth-demo-orders-staging.json",
		"platform/auth/swhurl-auth-demo-shop-staging.json", "platform/nats/users.json", "platform/reloader/values.json"}
	// Nothing calls production, so its folder renders no callers.
	want = append(want, instance("demo-orders", "prod", manifests...)...)
	want = append(want, instance("demo-orders", "staging", append(slices.Clone(manifests), "callers.json",
		"image-repository.json", "image-policy.json")...)...)
	want = append(want, instance("demo-shop", "staging", append(slices.Clone(manifests), "image-repository.json",
		"image-policy.json")...)...)
	written := []string{}
	for path := range treeContents(t, out) {
		if _, record := before[path]; !record {
			written = append(written, path)
		}
	}
	slices.Sort(want)
	slices.Sort(written)
	if !slices.Equal(written, want) {
		t.Fatalf("the render wrote\n%s\nwant\n%s", strings.Join(written, "\n"), strings.Join(want, "\n"))
	}
	if document.Kind != "v2-render" || document.Revision != renderRevision || document.Check ||
		len(document.Files) != len(want) || len(document.Differences) != 0 {
		t.Errorf("the report is %+v", document)
	}
	data, err := os.ReadFile(filepath.Join(records, callsFile))
	if err != nil {
		t.Fatal(err)
	}
	connections, err := kernel.ConnectionsOf(data, "demo-shop", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections.Calls) != 1 || connections.Calls[0] != (kernel.Call{Callee: "demo-orders", Port: 8080}) {
		t.Errorf("the resolution connects %+v for demo-shop/staging", connections.Calls)
	}
	var resolution resolutionDocument
	if err := json.Unmarshal(data, &resolution); err != nil || resolution.Revision != renderRevision ||
		len(resolution.Pairs) != 1 || resolution.Pairs[0] != (pairDocument{"demo-shop", "demo-orders", "staging", 8080}) {
		t.Errorf("calls.json is %s (%v)", data, err)
	}
}

// A check of a freshly rendered tree finds nothing to change and writes nothing, and a second render reports every
// file unchanged.
func TestRenderCheckPassesOnARenderedTree(t *testing.T) {
	out, records := renderedTree(t)
	before := treeContents(t, out)
	writer := filestest.New()
	status, document, diagnostic := renderRun(t, writer, renderArgs(out, records, "--revision", renderRevision, "--check")...)
	if status != 0 || len(document.Differences) != 0 || !document.Check || len(document.Files) != 0 {
		t.Fatalf("check exited %d: %s\n%+v", status, diagnostic, document)
	}
	if writer.Writes != 0 || writer.Prunes != 0 || writer.Removals != 0 {
		t.Errorf("the check used the writer: %d writes, %d prunes, %d removals", writer.Writes, writer.Prunes, writer.Removals)
	}
	if !strings.Contains(diagnostic, "0 differ") {
		t.Errorf("the summary is %q", diagnostic)
	}
	if after := treeContents(t, out); !mapsEqual(before, after) {
		t.Error("the check changed the tree")
	}
	status, document, diagnostic = renderRun(t, files.New(), renderArgs(out, records, "--revision", renderRevision)...)
	if status != 0 {
		t.Fatalf("a second render exited %d: %s", status, diagnostic)
	}
	for _, outcome := range document.Files {
		if outcome.State != files.Unchanged {
			t.Errorf("a second render of the same records: %s %s", outcome.State, outcome.Path)
		}
	}
}

// A changed record is a difference the check names, and the check still writes nothing.
func TestRenderCheckNamesTheFileARecordChangeMoves(t *testing.T) {
	out, records := renderedTree(t)
	path := filepath.Join(records, "demo-shop", "staging", instanceFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := kernel.DecodeInstance(data)
	if err != nil {
		t.Fatal(err)
	}
	if spec, err = kernel.WithReplicas(spec, 3); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, path, func() ([]byte, error) { return kernel.EncodeInstance(spec) })
	before := treeContents(t, out)
	status, document, diagnostic := renderRun(t, filestest.New(), renderArgs(out, records, "--check")...)
	want := []string{"v2-apps/instances/demo-shop/staging/helmrelease.json"}
	if status != 1 || !slices.Equal(document.Differences, want) {
		t.Fatalf("check exited %d with differences %v: %s", status, document.Differences, diagnostic)
	}
	if !strings.Contains(diagnostic, "1 differ: "+want[0]) {
		t.Errorf("stderr does not name the file: %q", diagnostic)
	}
	if after := treeContents(t, out); !mapsEqual(before, after) {
		t.Error("the check changed the tree")
	}
}

// A check needs no revision: the two documents that record one are compared without it.
func TestRenderCheckIgnoresTheRecordedRevision(t *testing.T) {
	out, records := renderedTree(t)
	status, document, diagnostic := renderRun(t, filestest.New(), renderArgs(out, records, "--check")...)
	if status != 0 || len(document.Differences) != 0 || document.Revision != "" {
		t.Fatalf("check without a revision exited %d: %s\n%+v", status, diagnostic, document)
	}
	other := strings.Repeat("e", 40)
	status, _, diagnostic = renderRun(t, filestest.New(), renderArgs(out, records, "--check", "--revision", other)...)
	if status != 0 {
		t.Fatalf("check at another revision exited %d: %s", status, diagnostic)
	}
}

// A record the decoder refuses stops the whole render before anything is written, and so does a write that names no
// revision or a malformed one.
func TestRenderRefusesAndWritesNothing(t *testing.T) {
	out, records := renderTree(t)
	if err := os.WriteFile(filepath.Join(records, "demo-shop", "staging", instanceFile), []byte(`{"schemaVersion": 1`),
		0o644); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"malformed record":   renderArgs(out, records, "--revision", renderRevision),
		"malformed on check": renderArgs(out, records, "--check"),
		"no revision":        renderArgs(out, records),
		"short revision":     renderArgs(out, records, "--revision", "abc1234"),
	} {
		writer := filestest.New()
		status, _, diagnostic := renderRun(t, writer, args...)
		if status != 2 {
			t.Errorf("%s: exit %d: %s", name, status, diagnostic)
		}
		if writer.Writes != 0 || writer.Prunes != 0 {
			t.Errorf("%s: %d writes and %d prunes", name, writer.Writes, writer.Prunes)
		}
	}
	_, _, diagnostic := renderRun(t, filestest.New(), renderArgs(out, records)...)
	if strings.TrimSpace(diagnostic) != "invalid: revision: required" {
		t.Errorf("a write without a revision: %q", diagnostic)
	}
}

// A database instance's allocation grants it the profile's backup host, so it is compiled behind the proxy. With no
// backup in the profile the allocation could grant it nothing, and the compiler refuses that instance first, by its
// own reason: the render names the instance and writes nothing.
func TestRenderGrantsADatabaseItsBackupOrRefusesIt(t *testing.T) {
	out, records := renderTree(t)
	renderInstance(t, records, kernel.InstanceSpec{Application: "demo-notes", Environment: "staging",
		Configuration: kernel.Configuration{Replicas: 1, MemoryMiB: 128}, Grants: kernel.Grants{Exposure: "private"},
		Storage: kernel.Storage{SizeMiB: 1024}}, renderRelease(t, "demo-notes", "sqlite"))
	writer := filestest.New()
	if status, _, diagnostic := renderRun(t, writer, renderArgs(out, records, "--revision", renderRevision)...); status != 0 {
		t.Fatalf("render exited %d: %s", status, diagnostic)
	}
	allocation, err := kernel.DecodeEgressDocument(writer.Contents[allocationPath])
	if err != nil {
		t.Fatal(err)
	}
	granted, err := allocation.EgressOf("demo-notes", "staging")
	if err != nil || len(granted.Grants) != 1 || !strings.HasSuffix(granted.Grants[0], ".amazonaws.com:443") {
		t.Fatalf("the database is allocated %+v (%v)", granted, err)
	}
	if _, compiled := writer.Contents["v2-apps/instances/demo-notes/staging/backup-cronjob.json"]; !compiled {
		t.Error("the database was not compiled with its backup")
	}

	var profile map[string]any
	if err := json.Unmarshal(fixture(t, "profile.json"), &profile); err != nil {
		t.Fatal(err)
	}
	delete(profile, "backup")
	profilePath := filepath.Join(t.TempDir(), "profile.json")
	writeRecord(t, profilePath, func() ([]byte, error) { return json.Marshal(profile) })
	writer = filestest.New()
	status, _, diagnostic := renderRun(t, writer, "--records", records, "--profile", profilePath, "--out", out,
		"--revision", renderRevision)
	if status != 2 || strings.TrimSpace(diagnostic) != "demo-notes/staging: unsupported: backup: the profile states no backup" {
		t.Fatalf("exit %d: %q", status, diagnostic)
	}
	if writer.Writes != 0 || writer.Prunes != 0 {
		t.Errorf("%d writes and %d prunes", writer.Writes, writer.Prunes)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, found := b[key]; !found || other != value {
			return false
		}
	}
	return true
}
