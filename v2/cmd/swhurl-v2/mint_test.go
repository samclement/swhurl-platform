package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// mintRecord is one v2-owned instance with no broker identity, written where the command reads it.
func mintRecord(t *testing.T) string {
	t.Helper()
	record, err := kernel.EncodeInstance(kernel.InstanceSpec{SchemaVersion: 1, Application: "example",
		Environment: "staging", Release: kernel.ReleaseID("rel-sha256:" + strings.Repeat("a", 64)),
		Grants: kernel.Grants{Exposure: "private"}, Configuration: kernel.Configuration{Replicas: 1, MemoryMiB: 128}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "instance.json")
	if err := os.WriteFile(path, record, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// seeds hands out a different seed on each call, so a rotation is visible.
func seeds() func() ([32]byte, error) {
	calls := 0
	return func() ([32]byte, error) {
		calls++
		var seed [32]byte
		for index := range seed {
			seed[index] = byte(calls)
		}
		return seed, nil
	}
}

// mintArguments is one mint's command line: the instance, the profile and a destination.
func mintArguments(t *testing.T, instance, destination string, extra ...string) []string {
	t.Helper()
	args := []string{"mint", "--instance", instance, "--profile", profilePath(t), "--out", destination}
	return append(args, extra...)
}

// A mint reports the new public key and the files it wrote, and never the seed in any form.
func TestMintWritesAndReports(t *testing.T) {
	instance, destination := mintRecord(t), t.TempDir()
	var out, diagnostic bytes.Buffer
	status := runWithEncrypter(mintArguments(t, instance, destination), &out, &diagnostic, os.ReadFile,
		files.New(), sopstest.New(), seeds())
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic.String())
	}
	var document struct {
		Kind        string
		Version     int
		Application string
		Environment string
		Public      string
		Previous    string
		Files       []files.Outcome
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out.String())
	}
	var seed [32]byte
	for index := range seed {
		seed[index] = 1
	}
	pair := kernel.NewUserKey(seed)
	if document.Kind != "v2-mint" || document.Version != 1 || document.Application != "example" ||
		document.Environment != "staging" || document.Public != pair.Public || document.Previous != "" {
		t.Fatalf("the report: %+v", document)
	}
	if len(document.Files) != 2 || document.Files[0].State != files.Written ||
		document.Files[0].Path != "v2-records/example/staging/events.sops.yaml" ||
		document.Files[1].Path != "v2-records/example/staging/instance.json" {
		t.Fatalf("the files: %+v", document.Files)
	}
	for name, text := range map[string]string{"stdout": out.String(), "diagnostic": diagnostic.String()} {
		if strings.Contains(text, pair.Seed) {
			t.Fatalf("%s holds the seed", name)
		}
	}
	if !strings.Contains(diagnostic.String(), "mint: minted example/staging, public key "+pair.Public) {
		t.Fatalf("the summary: %s", diagnostic.String())
	}
	record, err := os.ReadFile(filepath.Join(destination, "v2-records/example/staging/instance.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := kernel.DecodeInstance(record)
	if err != nil || spec.Events.Key != pair.Public {
		t.Fatalf("the record: %+v, %v", spec.Events, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "v2-records/example/staging/events.sops.yaml")); err != nil {
		t.Fatal(err)
	}
}

// A key that exists is replaced only with --rotate, and a refused mint writes nothing. The record the command
// writes is the record it reads next time, as it is in the checkout.
func TestMintRefusesAKeyThatExists(t *testing.T) {
	destination := t.TempDir()
	instance := mintRecord(t)
	next := seeds()
	var out, diagnostic bytes.Buffer
	if status := runWithEncrypter(mintArguments(t, instance, destination), &out, &diagnostic, os.ReadFile,
		files.New(), sopstest.New(), next); status != 0 {
		t.Fatalf("the first mint: exit %d: %s", status, diagnostic.String())
	}
	var minted struct{ Public string }
	if err := json.Unmarshal(out.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(destination, "v2-records/example/staging/instance.json"))
	if err != nil {
		t.Fatal(err)
	}
	written := filepath.Join(t.TempDir(), "instance.json")
	if err := os.WriteFile(written, first, 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, destination)
	out.Reset()
	diagnostic.Reset()
	status := runWithEncrypter(mintArguments(t, written, destination), &out, &diagnostic, os.ReadFile,
		files.New(), sopstest.New(), next)
	if status != 2 || !strings.Contains(diagnostic.String(), "events.key") {
		t.Fatalf("a second mint without --rotate: exit %d, %q", status, diagnostic.String())
	}
	if out.Len() != 0 {
		t.Fatalf("a refusal wrote a report: %s", out.String())
	}
	if after := tree(t, destination); after != before {
		t.Fatal("a refused mint wrote something")
	}
	// With --rotate the record states a new key and reports the one it replaced.
	out.Reset()
	diagnostic.Reset()
	status = runWithEncrypter(mintArguments(t, written, destination, "--rotate"), &out, &diagnostic,
		os.ReadFile, files.New(), sopstest.New(), next)
	if status != 0 {
		t.Fatalf("a rotation: exit %d: %s", status, diagnostic.String())
	}
	var document struct{ Public, Previous string }
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Previous != minted.Public || document.Public == document.Previous {
		t.Fatalf("the rotation's report: %+v", document)
	}
	if !strings.Contains(diagnostic.String(), "mint: rotated example/staging") {
		t.Fatalf("the summary: %s", diagnostic.String())
	}
}

// tree is every file under a destination and its bytes, for a "wrote nothing" assertion.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var out strings.Builder
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out.WriteString(relative + ":" + string(content) + "\n")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
