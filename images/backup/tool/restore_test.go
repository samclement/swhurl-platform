package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// database writes one SQLite file with the statements a test states, and returns its path.
func database(t *testing.T, path string, statements ...string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// tablesIn is how many tables one SQLite file holds.
func tablesIn(t *testing.T, path string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables int64
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	return tables
}

// served is a read URL for one encrypted archive, and a bucket that does not have it.
func served(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/archive.age" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	return server
}

// restoreFixture is one claim with a live database, and an archive of a different state to restore from.
type restoreFixture struct {
	env     []string
	current string
	scratch string
	server  *httptest.Server
}

func fixture(t *testing.T, tables int64, plainHash string) restoreFixture {
	t.Helper()
	claim := t.TempDir()
	current := database(t, filepath.Join(claim, "app.db"),
		`CREATE TABLE visits (id INTEGER PRIMARY KEY)`, `INSERT INTO visits DEFAULT VALUES`)
	archived := database(t, filepath.Join(t.TempDir(), "archived.db"),
		`CREATE TABLE visits (id INTEGER PRIMARY KEY)`, `INSERT INTO visits DEFAULT VALUES`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY)`, `INSERT INTO orders DEFAULT VALUES`)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "age.key")
	if err := os.WriteFile(key, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "archive.age")
	if err := encryptFile(identity.Recipient().String(), archived, archive); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveHash, err := sha256Of(archive)
	if err != nil {
		t.Fatal(err)
	}
	if plainHash == "" {
		plainHash, err = sha256Of(archived)
		if err != nil {
			t.Fatal(err)
		}
	}
	scratch := t.TempDir()
	return restoreFixture{
		env: []string{"DATABASE_PATH=" + current, "BACKUP_SOURCE=hello-staging/hello",
			"ARCHIVE_URL=" + "REPLACED", "ARCHIVE_SHA256=" + archiveHash, "PLAIN_SHA256=" + plainHash,
			"TABLES=" + itoa(tables), "AGE_KEY_FILE=" + key, "BACKUP_DIR=" + scratch},
		current: current, scratch: scratch, server: served(t, body),
	}
}

func itoa(tables int64) string {
	if tables == 0 {
		return "0"
	}
	out := ""
	for ; tables > 0; tables /= 10 {
		out = string(rune('0'+tables%10)) + out
	}
	return out
}

// withURL is the fixture's environment pointing at its served archive.
func (f restoreFixture) withURL() []string {
	env := make([]string, 0, len(f.env))
	for _, entry := range f.env {
		env = append(env, strings.Replace(entry, "REPLACED", f.server.URL+"/archive.age", 1))
	}
	return env
}

// The restore replaces the database with the archive's, keeps what was there beside it, and says what it did.
func TestRestoreJob(t *testing.T) {
	f := fixture(t, 2, "")
	var out strings.Builder
	if err := restoreSQLite(context.Background(), f.withURL(), &out, f.server.Client()); err != nil {
		t.Fatal(err)
	}
	if tables := tablesIn(t, f.current); tables != 2 {
		t.Fatalf("%d tables after the restore", tables)
	}
	folders, err := filepath.Glob(filepath.Join(filepath.Dir(f.current), "before-restore-*"))
	if err != nil || len(folders) != 1 {
		t.Fatalf("the replaced database is not kept: %v %v", folders, err)
	}
	if kept := filepath.Join(folders[0], "app.db"); tablesIn(t, kept) != 1 {
		t.Fatal("the kept database is not the one that was replaced")
	}
	if !strings.Contains(out.String(), "2 tables, integrity ok") {
		t.Fatalf("output %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(f.scratch, "restored.db")); err == nil {
		t.Fatal("a plaintext copy was left behind")
	}
}

// Every check refuses before the live database is touched: a checksum, a table count, a download, and a
// missing archive that is not this instance's.
func TestRestoreRefusesWhatItCannotVerify(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*restoreFixture)
		want   string
	}{
		{"plaintext checksum", func(f *restoreFixture) { f.env = replace(f.env, "PLAIN_SHA256", strings.Repeat("a", 64)) },
			"the restored copy's SHA-256 differs"},
		{"table count", func(f *restoreFixture) { f.env = replace(f.env, "TABLES", "3") }, "3"},
		{"archive checksum", func(f *restoreFixture) { f.env = replace(f.env, "ARCHIVE_SHA256", strings.Repeat("b", 64)) },
			"the archive's SHA-256 differs"},
		{"source", func(f *restoreFixture) { f.env = replace(f.env, "BACKUP_SOURCE", "") }, "BACKUP_SOURCE is not set"},
		{"missing archive", func(f *restoreFixture) {
			f.env = replace(f.env, "ARCHIVE_URL", "https://127.0.0.1:1/archive.age")
		}, "could not be downloaded"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := fixture(t, 2, "")
			tt.change(&f)
			var out strings.Builder
			if err := restoreSQLite(context.Background(), f.withURL(), &out, f.server.Client()); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
			if tables := tablesIn(t, f.current); tables != 1 {
				t.Fatalf("the live database was touched: %d tables", tables)
			}
			if folders, _ := filepath.Glob(filepath.Join(filepath.Dir(f.current), "before-restore-*")); len(folders) != 0 {
				t.Fatalf("a refused restore kept a folder: %v", folders)
			}
		})
	}
}

// replace changes one setting in an environment.
func replace(env []string, name, value string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, name+"=") {
			out = append(out, name+"="+value)
			continue
		}
		out = append(out, entry)
	}
	return out
}
