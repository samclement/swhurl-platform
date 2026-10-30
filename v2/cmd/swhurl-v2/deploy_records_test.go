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
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const (
	recordsFixture = "demo-orders"
	nextCommit     = "cccccccccccccccccccccccccccccccccccccccc"
	// The platform publishes the tag's commit abbreviated: the image states the whole one.
	nextTag    = "43-ccccccc"
	nextDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// recordsTree lays the fixture instance's records out as a v2 checkout keeps them: one folder per app, its own
// record beside it, and the instance's pair under staging.
func recordsTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "v2-records")
	folder := filepath.Join(root, recordsFixture, "staging")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{"instance.json": "staging.json", "release.json": "r42.json"} {
		if err := os.WriteFile(filepath.Join(folder, name), fixture(t, source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, recordsFixture, "app.json"), fixture(t, "app.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// recordsDeploy runs the records form against one tree and a registry that answers from a script.
func recordsDeploy(t *testing.T, root string, extra []string,
	images func(string) (registry.Reader, error)) (int, deployRecordsDocument, string, *filestest.Fake) {
	t.Helper()
	writer := filestest.New()
	var stdout, diagnostic bytes.Buffer
	args := append([]string{"deploy", "--records", root, "--profile", "../../testdata/profile.json",
		"--out", t.TempDir()}, extra...)
	status := runWithRegistry(args, &stdout, &diagnostic, os.ReadFile, writer, images)
	var document deployRecordsDocument
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
			t.Fatalf("the report is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return status, document, diagnostic.String(), writer
}

// answering is a registry factory for one script, whatever image a delivery names.
func answering(answers registrytest.Scenario) func(string) (registry.Reader, error) {
	return func(string) (registry.Reader, error) { return registrytest.New(answers), nil }
}

// currentTag is the tag the fixture release was published as.
func currentTag(t *testing.T) (string, kernel.ReleaseBundle) {
	t.Helper()
	release, err := kernel.DecodeRelease(fixture(t, "r42.json"))
	if err != nil {
		t.Fatal(err)
	}
	return "42-" + release.Source.Commit, release
}

func TestRecordsReportsAnInstanceThatAlreadyRunsItsNewestImage(t *testing.T) {
	tag, release := currentTag(t)
	status, document, diagnostic, writer := recordsDeploy(t, recordsTree(t), nil, answering(registrytest.Scenario{
		Tags:   []string{tag},
		Images: map[string]registry.Image{tag: {Digest: release.Artifact.Digest, MediaType: kernel.ManifestMediaType}},
	}))
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	if document.Kind != recordsKind || document.Version != 1 || len(document.Instances) != 1 {
		t.Fatalf("not one delivery: %+v", document)
	}
	entry := document.Instances[0]
	if entry.Outcome != outcomeUnchanged || entry.Tag != tag || entry.Application != recordsFixture {
		t.Fatalf("outcome %q, tag %q, app %q", entry.Outcome, entry.Tag, entry.Application)
	}
	if writer.Writes != 0 {
		t.Fatalf("an unchanged instance wrote %d times", writer.Writes)
	}
}

func TestRecordsWritesTheSelectedReleaseAndItsFiles(t *testing.T) {
	status, document, diagnostic, writer := recordsDeploy(t, recordsTree(t), nil, answering(registrytest.Scenario{
		Tags:   []string{nextTag},
		Images: map[string]registry.Image{nextTag: deployedImage(nextDigest, nextCommit)},
	}))
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	if outcome := document.Instances[0].Outcome; outcome != outcomeChanged {
		t.Fatalf("outcome %q", outcome)
	}
	release, err := kernel.DecodeRelease(writer.Contents["v2-records/demo-orders/staging/release.json"])
	if err != nil {
		t.Fatal(err)
	}
	if release.Artifact.Digest != nextDigest || release.Source.Commit != nextCommit {
		t.Fatalf("release states %s at %s", release.Artifact.Digest, release.Source.Commit)
	}
	instance, err := kernel.DecodeInstance(writer.Contents["v2-records/demo-orders/staging/instance.json"])
	if err != nil {
		t.Fatal(err)
	}
	if instance.Release != release.ReleaseID {
		t.Fatalf("the instance selects %s, the release is %s", instance.Release, release.ReleaseID)
	}
	if _, found := writer.Contents["v2-apps/instances/demo-orders/staging/helmrelease.json"]; !found {
		t.Fatalf("no compiled files were written: %v", keys(writer.Contents))
	}
	if strings.Contains(string(writer.Contents["v2-apps/instances/demo-orders/staging/helmrelease.json"]), "HTTP_PROXY") {
		t.Fatal("an instance whose allocation grants nothing was put behind the proxy")
	}
}

func TestRecordsRefusesAnImageThatStatesNoDeclaration(t *testing.T) {
	status, document, diagnostic, writer := recordsDeploy(t, recordsTree(t), nil, answering(registrytest.Scenario{
		Tags:   []string{nextTag},
		Images: map[string]registry.Image{nextTag: {Digest: nextDigest, MediaType: kernel.ManifestMediaType}},
	}))
	if status != 2 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	entry := document.Instances[0]
	if entry.Outcome != outcomeRefused || !strings.Contains(entry.Reason, "declaration: absent") {
		t.Fatalf("outcome %q, reason %q", entry.Outcome, entry.Reason)
	}
	if writer.Writes != 0 {
		t.Fatalf("a refused instance wrote %d times", writer.Writes)
	}
}

func TestRecordsFailsWhenTheRegistryCannotBeRead(t *testing.T) {
	unavailable := &registry.Error{Code: registry.Unavailable, Subject: "tags", Reason: "no answer"}
	status, document, diagnostic, writer := recordsDeploy(t, recordsTree(t), nil,
		func(string) (registry.Reader, error) {
			fake := registrytest.New(registrytest.Scenario{})
			fake.Fail = unavailable
			return fake, nil
		})
	if status != 1 {
		t.Fatalf("exit %d: %s", status, diagnostic)
	}
	if outcome := document.Instances[0].Outcome; outcome != outcomeFailed {
		t.Fatalf("outcome %q", outcome)
	}
	if writer.Writes != 0 {
		t.Fatalf("a failed delivery wrote %d times", writer.Writes)
	}
}

func TestRecordsRefusesAHalfReadTreeAndDeliversNothing(t *testing.T) {
	half := filepath.Join(t.TempDir(), "v2-records")
	folder := filepath.Join(half, recordsFixture, "staging")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "instance.json"), fixture(t, "staging.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(half, recordsFixture, "app.json"), fixture(t, "app.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	writer := filestest.New()
	var stdout, diagnostic bytes.Buffer
	status := runWithRegistry([]string{"deploy", "--records", half, "--profile", "../../testdata/profile.json",
		"--out", t.TempDir()}, &stdout, &diagnostic, os.ReadFile, writer,
		answering(registrytest.Scenario{Tags: []string{nextTag}}))
	if status != 1 || !strings.Contains(diagnostic.String(), "release") {
		t.Fatalf("a tree without a release record: exit %d, %q", status, diagnostic.String())
	}
	if writer.Writes != 0 || stdout.Len() != 0 {
		t.Fatalf("a half-read tree delivered something: %d writes", writer.Writes)
	}
}

func TestRecordsGivesAnInstanceItsAllocationOnlyWhenItGrantsSomething(t *testing.T) {
	allocation := func(grants []string) string {
		document := map[string]any{"kind": "v2-egress", "version": 1, "revision": nextCommit,
			"instances": []map[string]any{{"application": recordsFixture, "environment": "staging",
				"port": 30101, "grants": grants}}}
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "allocation.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, test := range []struct {
		name      string
		grants    []string
		expectApp bool
	}{
		{name: "a port with nothing to reach stays unused", grants: []string{}, expectApp: false},
		{name: "a granted host puts the instance behind the proxy", grants: []string{"example.com:443"}, expectApp: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, _, diagnostic, writer := recordsDeploy(t, recordsTree(t),
				[]string{"--egress", allocation(test.grants)}, answering(registrytest.Scenario{
					Tags: []string{nextTag}, Images: map[string]registry.Image{nextTag: deployedImage(nextDigest, nextCommit)},
				}))
			if status != 0 {
				t.Fatalf("exit %d: %s", status, diagnostic)
			}
			rendered := string(writer.Contents["v2-apps/instances/demo-orders/staging/helmrelease.json"])
			if got := strings.Contains(rendered, "HTTP_PROXY"); got != test.expectApp {
				t.Fatalf("HTTP_PROXY present: %v, want %v", got, test.expectApp)
			}
		})
	}
}

func TestRecordsIgnoresAnAppFolderWithNoStagingInstance(t *testing.T) {
	root := recordsTree(t)
	if err := os.MkdirAll(filepath.Join(root, "another"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "another", "app.json"), fixture(t, "app.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag, release := currentTag(t)
	status, document, diagnostic, _ := recordsDeploy(t, root, nil, answering(registrytest.Scenario{
		Tags:   []string{tag},
		Images: map[string]registry.Image{tag: {Digest: release.Artifact.Digest, MediaType: kernel.ManifestMediaType}},
	}))
	if status != 0 || len(document.Instances) != 1 {
		t.Fatalf("exit %d, %d instances: %s", status, len(document.Instances), diagnostic)
	}
}

func keys(content map[string][]byte) []string {
	names := make([]string, 0, len(content))
	for name := range content {
		names = append(names, name)
	}
	return names
}
