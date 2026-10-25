package files_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// fixtures are the raw documents the contract runs on: manifests as the compiler writes them.
func fixtures(t *testing.T) []kernel.File {
	t.Helper()
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	docs := make([]kernel.File, 0, len(names))
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(content) {
			t.Fatalf("testdata/%s is not JSON", name)
		}
		docs = append(docs, kernel.File{Path: "instances/demo/staging/" + name, Document: content})
	}
	if len(docs) == 0 {
		t.Fatal("no fixtures")
	}
	return docs
}

func TestContract(t *testing.T) {
	filestest.Contract(t, func(t *testing.T) (files.Writer, string) {
		return files.New(), t.TempDir()
	}, fixtures(t))
}

func write(t *testing.T, writer files.Writer, dir string, docs []kernel.File) []files.Outcome {
	t.Helper()
	outcomes, err := writer.Write(dir, docs)
	if err != nil {
		t.Fatal(err)
	}
	return outcomes
}

// A destination is the operator's choice, but nothing inside it may lead somewhere else.
func TestRefusesASymlinkThatLeavesTheDestination(t *testing.T) {
	for _, name := range []string{"directory", "file"} {
		t.Run(name, func(t *testing.T) {
			dir, elsewhere := t.TempDir(), t.TempDir()
			if name == "directory" {
				if err := os.Symlink(elsewhere, filepath.Join(dir, "instances")); err != nil {
					t.Skip("symlinks unavailable:", err)
				}
			} else if err := os.WriteFile(filepath.Join(elsewhere, "target"), []byte("keep"), 0o644); err != nil {
				t.Fatal(err)
			} else if err := os.Symlink(filepath.Join(elsewhere, "target"), filepath.Join(dir, "unit.json")); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
			docs := []kernel.File{{Path: "instances/demo/staging/one.json", Document: []byte(`{}`)}}
			if name == "file" {
				docs = []kernel.File{{Path: "unit.json", Document: []byte(`{}`)}}
			}
			_, err := files.New().Write(dir, docs)
			var refusal *files.Error
			if !errors.As(err, &refusal) || refusal.Code != files.RefusedDestination || refusal.Reason != "symlink_in_path" {
				t.Fatalf("a symlink out of the destination: %v", err)
			}
			if content, err := os.ReadFile(filepath.Join(elsewhere, "target")); name == "file" && (err != nil || string(content) != "keep") {
				t.Fatalf("the file outside the destination changed: %q %v", content, err)
			}
		})
	}
}

// Every file is prepared before anything is written, so a refused path writes nothing.
func TestARefusedPathWritesNothing(t *testing.T) {
	dir := t.TempDir()
	docs := []kernel.File{
		{Path: "instances/demo/staging/one.json", Document: []byte(`{}`)},
		{Path: "../outside.json", Document: []byte(`{}`)},
	}
	_, err := files.New().Write(dir, docs)
	var refusal *files.Error
	if !errors.As(err, &refusal) || refusal.Code != files.RefusedDestination {
		t.Fatalf("want a refused destination, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused compile wrote %d entries", len(entries))
	}
}

// The adapter writes the paths it was given and leaves every other file alone.
func TestLeavesFilesItWasNotGivenAlone(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "instances", "demo", "staging", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("hand-written"), 0o644); err != nil {
		t.Fatal(err)
	}
	docs := []kernel.File{{Path: "instances/demo/staging/one.json", Document: []byte(`{"a":1}`)}}
	write(t, files.New(), dir, docs)
	if content, err := os.ReadFile(keep); err != nil || string(content) != "hand-written" {
		t.Fatalf("an unnamed file changed: %q %v", content, err)
	}
}

func TestRefusesAnAbsolutePath(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "instances/../../outside.json", "instances//one.json", ""} {
		if _, err := files.Target(t.TempDir(), path); err == nil {
			t.Errorf("%q was accepted", path)
		}
	}
}

// A filesystem failure is an I/O error, distinct from a refused destination: the command exits 1,
// not 2, because the destination was sound and the machine was not.
func TestReportsFilesystemFailures(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(dir string) error
	}{
		{"a parent that is a file", func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "instances"), []byte("x"), 0o644)
		}},
		{"a directory where the file goes", func(dir string) error {
			return os.MkdirAll(filepath.Join(dir, "instances", "demo", "staging", "one.json"), 0o755)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tt.setup(dir); err != nil {
				t.Fatal(err)
			}
			docs := []kernel.File{{Path: "instances/demo/staging/one.json", Document: []byte(`{}`)}}
			_, err := files.New().Write(dir, docs)
			var failure *files.Error
			if !errors.As(err, &failure) || failure.Code != files.IO {
				t.Fatalf("want an %s error, got %v", files.IO, err)
			}
		})
	}
}

// A directory the process may not write to is an I/O failure, and no temporary file survives it.
func TestAnUnwritableDirectoryFailsWithoutLitter(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write anywhere")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "instances", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "instances", "demo"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "instances", "demo"), 0o755) })
	docs := []kernel.File{{Path: "instances/demo/staging/one.json", Document: []byte(`{}`)}}
	_, err := files.New().Write(dir, docs)
	var failure *files.Error
	if !errors.As(err, &failure) || failure.Code != files.IO {
		t.Fatalf("want an %s error, got %v", files.IO, err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "instances", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed write left %d entries", len(entries))
	}
}

// A successful write leaves no temporary file behind.
func TestASuccessfulWriteLeavesNoTemporaries(t *testing.T) {
	dir := t.TempDir()
	write(t, files.New(), dir, []kernel.File{{Path: "instances/demo/one.json", Document: []byte(`{"a":1}`)}})
	var names []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			names = append(names, entry.Name())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "one.json" {
		t.Fatalf("files after a write: %v", names)
	}
}
