package hoststate_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/hoststate"
	"github.com/samclement/swhurl-platform/v2/adapters/hoststate/hoststatetest"
)

// python is a state file as Python's heartbeat wrote it.
func python(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "notification-heartbeat.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestContract(t *testing.T) {
	dir := t.TempDir()
	scenario := hoststatetest.Scenario{Existing: filepath.Join(dir, "state", "notification-heartbeat.json"),
		Held: python(t), Absent: filepath.Join(dir, "new", "deeper", "notification-heartbeat.json"),
		Unreadable: filepath.Join(dir, "state", "a-directory"),
		// The parent is a file, so its directory cannot be made.
		Unwritable: filepath.Join(dir, "state", "notification-heartbeat.json", "child.json")}
	hoststatetest.Contract(t, scenario, func(t *testing.T, s hoststatetest.Scenario) hoststate.Store {
		if err := os.MkdirAll(s.Unreadable, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.Existing, s.Held, 0o600); err != nil {
			t.Fatal(err)
		}
		return hoststate.New()
	})
}

// A replacement makes the directory 0700 (an existing one included) and the file 0600 whatever the umask allows, and
// leaves no temporary file beside it.
func TestReplaceIsPrivateAndLeavesNothingBehind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "swhurl-platform")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "notification-heartbeat.json")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := hoststate.New().Replace(path, python(t)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		path string
		mode os.FileMode
	}{{dir, 0o700}, {path, 0o600}} {
		info, err := os.Stat(want.path)
		if err != nil || info.Mode().Perm() != want.mode {
			t.Fatalf("%s: %v %v, want %v", want.path, info.Mode(), err, want.mode)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("left beside the file: %v %v", entries, err)
	}
}

// A replacement that cannot rename (the path is a directory) leaves the directory as it was and removes its temporary
// file; the error names a fixed reason, not the path or the bytes.
func TestFailedReplaceRemovesItsTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "occupied")
	if err := os.MkdirAll(filepath.Join(path, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := hoststate.New().Replace(path, []byte("secret-bytes\n"))
	if err == nil || err.Error() != "unwritable: cannot rename" {
		t.Fatalf("replacing a directory: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "occupied" {
		t.Fatalf("left behind: %v", entries)
	}
}

// A file over MaxBytes is unreadable, not truncated; an unreadable file's error does not repeat its content or path.
func TestReadRefusesALargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), hoststate.MaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	data, found, err := hoststate.New().Read(path)
	if err == nil || found || data != nil || strings.Contains(err.Error(), path) {
		t.Fatalf("%d bytes, %t, %v", len(data), found, err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, found, err := hoststate.New().Read(path); err == nil || found || err.Error() != "unreadable: cannot open" {
			t.Fatalf("a file without permission read %t, %v", found, err)
		}
	}
}
