package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// fakeFacts is the server's facts without a server: a version and two collection counts.
type fakeFacts struct {
	version     string
	collections map[string]int64
	err         error
}

func (f fakeFacts) Facts(context.Context, string, string) (string, map[string]int64, error) {
	return f.version, f.collections, f.err
}

// fakeDump puts a stand-in mongodump first on PATH: it records the configuration it read on stdin and writes
// the bytes the test states, so the job's piping and encryption can be exercised without MongoDB.
func fakeDump(t *testing.T, output string) string {
	t.Helper()
	directory := t.TempDir()
	config := filepath.Join(directory, "config")
	script := fmt.Sprintf("#!/bin/sh\ncat > %q\nprintf '%%s' '%s'\n", config, output)
	path := filepath.Join(directory, "mongodump")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return config
}

// mongoEnviron is a MongoDB job's environment.
func mongoEnviron(directory string) []string {
	return []string{"MONGODB_URI=mongodb://user:pass@mongo:27017/hyperdx", "MONGO_DATABASE=hyperdx",
		"AGE_RECIPIENT=age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p",
		"BACKUP_S3_URI=s3://backups/clickstack-mongodb/", "BACKUP_SOURCE=observability/clickstack-mongodb",
		"BACKUP_DIR=" + directory, "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE", "AWS_SECRET_ACCESS_KEY=secret"}
}

// A MongoDB job pipes the dump into age, records the version and counts with the platform's keys, and
// publishes the archive then its metadata. The connection string reaches the tool on stdin, not on the
// command line.
func TestBackupJobMongoDB(t *testing.T) {
	directory := t.TempDir()
	config := fakeDump(t, "a gzipped dump")
	recorder := &putting{}
	server := recorder.serve(t)
	identity, err := ageIdentity(t)
	if err != nil {
		t.Fatal(err)
	}
	environ := mongoEnviron(directory)
	environ = append(environ, "S3_ENDPOINT="+server.URL)
	environ = replace(environ, "AGE_RECIPIENT", identity.Recipient().String())
	facts := fakeFacts{version: "5.0.32", collections: map[string]int64{"alerts": 22, "users": 1}}
	var out bytes.Buffer
	if err := backupMongoDB(context.Background(), environ, &out, server.Client(), facts); err != nil {
		t.Fatal(err)
	}
	read, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(read), "uri: ") || !strings.Contains(string(read), "mongodb://user:pass@mongo:27017/hyperdx") {
		t.Fatalf("the tool's configuration %q", read)
	}
	if len(recorder.requests) != 2 {
		t.Fatalf("%d uploads", len(recorder.requests))
	}
	archive := recorder.requests[0].URL.Path
	metadataPath := recorder.requests[1].URL.Path
	if !strings.HasSuffix(archive, ".archive.gz.age") ||
		!strings.Contains(archive, "clickstack-mongodb-") ||
		!strings.HasPrefix(metadataPath, strings.TrimSuffix(archive, ".archive.gz.age")) {
		t.Fatalf("objects %s %s", archive, metadataPath)
	}
	recorded, err := os.ReadFile(filepath.Join(directory, filepath.Base(metadataPath)))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(recorded, &document); err != nil {
		t.Fatal(err)
	}
	if len(document) != 7 || document["database"] != "hyperdx" || document["mongodb_version"] != "5.0.32" ||
		document["source"] != "observability/clickstack-mongodb" {
		t.Fatalf("metadata %s", recorded)
	}
	collections := document["collections"].(map[string]any)
	if collections["alerts"] != float64(22) || collections["users"] != float64(1) || len(collections) != 2 {
		t.Fatalf("collections %v", collections)
	}
	// The archive decrypts to exactly what the tool wrote: no plaintext copy was made on the way.
	key := filepath.Join(directory, "age.key")
	if err := os.WriteFile(key, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(directory, "restored.gz")
	if err := decryptFile(key, filepath.Join(directory, filepath.Base(archive)), restored); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(restored); err != nil || string(body) != "a gzipped dump" {
		t.Fatalf("archive body %q %v", body, err)
	}
	if !strings.Contains(out.String(), "[OK] observability/clickstack-mongodb: clickstack-mongodb-") {
		t.Fatalf("output %q", out.String())
	}
}

// A job that cannot read the counts, or whose dump fails, publishes nothing.
func TestBackupJobMongoDBRefusals(t *testing.T) {
	for _, tt := range []struct {
		name  string
		facts mongoFacts
		want  string
	}{
		{"counts", fakeFacts{err: fmt.Errorf("no server")}, "could not read the collection counts"},
		{"settings", fakeFacts{}, "not configured"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			fakeDump(t, "dump")
			recorder := &putting{}
			server := recorder.serve(t)
			environ := append(mongoEnviron(directory), "S3_ENDPOINT="+server.URL)
			if tt.name == "settings" {
				environ = replace(environ, "MONGO_DATABASE", "")
			}
			if err := backupMongoDB(context.Background(), environ, &bytes.Buffer{}, server.Client(), tt.facts); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
			if len(recorder.requests) != 0 {
				t.Fatalf("a refused job published %d objects", len(recorder.requests))
			}
		})
	}
}

// ageIdentity is a fresh identity, so each test's archive has its own key.
func ageIdentity(t *testing.T) (*age.X25519Identity, error) {
	t.Helper()
	return age.GenerateX25519Identity()
}
