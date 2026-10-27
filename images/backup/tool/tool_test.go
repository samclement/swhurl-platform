package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crypto/sha256"
	"encoding/hex"
	"filippo.io/age"
	"time"
)

// environ is a job's environment, without the values a test does not care about.
func timeAt(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func environ(extra ...string) []string {
	base := []string{"AGE_RECIPIENT=age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p",
		"BACKUP_S3_URI=s3://backups/app-sqlite/hello-staging/", "BACKUP_SOURCE=hello-staging/hello",
		"AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE", "AWS_SECRET_ACCESS_KEY=secret", "DATABASE_PATH=/data/app.db"}
	return append(base, extra...)
}

// The job's settings: the required facts are refused when missing, and the rest have the image's defaults.
func TestSettings(t *testing.T) {
	job, err := settingsOf(environ())
	if err != nil {
		t.Fatal(err)
	}
	if job.Directory != defaultDirectory || job.Region != defaultRegion || job.URI == "" {
		t.Fatalf("settings %+v", job)
	}
	if job, err := settingsOf(environ("BACKUP_DIR=/tmp/b", "AWS_DEFAULT_REGION=eu-west-1")); err != nil ||
		job.Directory != "/tmp/b" || job.Region != "eu-west-1" {
		t.Fatalf("overrides %+v %v", job, err)
	}
	for _, tt := range []struct {
		env  []string
		want string
	}{
		{[]string{}, "AGE_RECIPIENT is not set"},
		{environ("BACKUP_S3_URI="), "BACKUP_S3_URI is not set"},
		{environ("DATABASE_PATH="), "DATABASE_PATH is not set"},
		{environ("BACKUP_S3_URI=s3://bucket/no-slash"), "BACKUP_S3_URI needs a bucket and a prefix ending in /"},
		{environ("AWS_SECRET_ACCESS_KEY="), "the AWS credentials are not set"},
	} {
		if _, err := settingsOf(tt.env); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: %v", tt.env, err)
		}
	}
}

// The archive's names are the platform's: sqlite-<UTC>.db.age and the .json published beside it.
func TestArchiveNames(t *testing.T) {
	if got := archiveName(timeAt(t, "2026-10-27T03:58:07Z")); got != "sqlite-20261027T035807Z.db.age" {
		t.Fatalf("name %q", got)
	}
	if got := metadataName("sqlite-20261027T035807Z.db.age"); got != "sqlite-20261027T035807Z.json" {
		t.Fatalf("metadata %q", got)
	}
}

// SQLite's online backup gives a consistent copy of a database whose app is writing, and the facts a restore
// checks: the table count, the integrity result, the plaintext's size and its SHA-256.
func TestCopyDatabase(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "app.db")
	create := func() *sql.DB {
		db, err := sql.Open("sqlite", "file:"+source+"?mode=rwc")
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := create()
	if _, err := db.Exec(`CREATE TABLE visits (id INTEGER PRIMARY KEY, at TEXT); INSERT INTO visits (at) VALUES ('now'), ('then')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	copied := filepath.Join(directory, "copy.db")
	found, err := copyDatabase(context.Background(), source, copied)
	if err != nil {
		t.Fatal(err)
	}
	if found.Tables != 1 || found.IntegrityCheck != "ok" || found.Bytes < 1 || len(found.PlainSHA256) != 64 {
		t.Fatalf("facts %+v", found)
	}
	digest, err := sha256Of(copied)
	if err != nil || digest != found.PlainSHA256 {
		t.Fatalf("plain %q %v", digest, err)
	}
	if _, err := copyDatabase(context.Background(), filepath.Join(directory, "missing.db"), filepath.Join(directory, "x.db")); err == nil ||
		!strings.Contains(err.Error(), "is not there") {
		t.Fatalf("a missing database: %v", err)
	}
}

// An archive is age-encrypted to the recipient the profile states, and the job's own identity reads it back.
func TestArchiveRoundTrip(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	plaintext := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(plaintext, []byte("a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := plaintext + ".age"
	if err := encryptFile(identity.Recipient().String(), plaintext, archive); err != nil {
		t.Fatal(err)
	}
	key := plaintext + ".key"
	if err := os.WriteFile(key, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := plaintext + ".restored"
	if err := decryptFile(key, archive, restored); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(restored); !bytes.Equal(got, []byte("a database")) {
		t.Fatalf("round trip %q", got)
	}
	if err := encryptFile("not-a-recipient", plaintext, filepath.Join(t.TempDir(), "x.age")); err == nil {
		t.Fatal("a malformed recipient was accepted")
	}
}

// putting is a server that answers one upload and records what it was asked.
type putting struct {
	requests []*http.Request
	status   int
}

func (p *putting) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.requests = append(p.requests, r)
		_, _ = io.Copy(io.Discard, r.Body)
		if p.status == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(p.status)
	}))
	t.Cleanup(server.Close)
	return server
}

// The upload carries what the bucket checks: the payload's own SHA-256, the same checksum S3 verifies, a
// refusal to replace an existing object, and a signature that names the region and the service.
func TestPutObject(t *testing.T) {
	recorder := &putting{}
	server := recorder.serve(t)
	job, err := settingsOf(environ("S3_ENDPOINT=" + server.URL))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "sqlite-1.db.age")
	if err := os.WriteFile(file, []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := putObject(context.Background(), job, server.Client(), "backups", "app-sqlite/hello-staging/sqlite-1.db.age", file); err != nil {
		t.Fatal(err)
	}
	request := recorder.requests[0]
	if request.Method != http.MethodPut || request.URL.Path != "/backups/app-sqlite/hello-staging/sqlite-1.db.age" {
		t.Fatalf("request %s %s", request.Method, request.URL.Path)
	}
	sum := sha256.Sum256([]byte("archive bytes"))
	if got := request.Header.Get("x-amz-checksum-sha256"); got != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatalf("checksum %q", got)
	}
	if got := request.Header.Get("x-amz-content-sha256"); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("payload hash %q", got)
	}
	if request.Header.Get("If-None-Match") != "*" {
		t.Fatalf("if-none-match %q", request.Header.Get("If-None-Match"))
	}
	authorization := request.Header.Get("Authorization")
	for _, want := range []string{"Credential=AKIAIOSFODNN7EXAMPLE/", "/eu-west-2/s3/aws4_request",
		"SignedHeaders=host;if-none-match;x-amz-checksum-sha256;x-amz-content-sha256;x-amz-date"} {
		if !strings.Contains(authorization, want) {
			t.Errorf("authorization lacks %q: %s", want, authorization)
		}
	}
	// An object that already exists is refused, and so is an upload the bucket forbids.
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusPreconditionFailed, "already exists"},
		{http.StatusForbidden, "refused"},
		{http.StatusInternalServerError, "refused"},
	} {
		refusing := (&putting{status: tt.status}).serve(t)
		job.Endpoint = refusing.URL
		if err := putObject(context.Background(), job, refusing.Client(), "backups", "k", file); err == nil ||
			!strings.Contains(err.Error(), tt.want) {
			t.Errorf("status %d: %v", tt.status, err)
		}
	}
}

// The whole job: a real database is copied, encrypted and published as the two objects a restore reads, and
// the metadata carries exactly the keys the platform's tools read.
func TestBackupJob(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "app.db")
	db, err := sql.Open("sqlite", "file:"+source+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE visits (id INTEGER PRIMARY KEY); INSERT INTO visits DEFAULT VALUES`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	recorder := &putting{}
	server := recorder.serve(t)
	var out bytes.Buffer
	err = backupSQLite(context.Background(), environ("BACKUP_DIR="+directory, "DATABASE_PATH="+source,
		"S3_ENDPOINT="+server.URL, "AGE_RECIPIENT="+identity.Recipient().String()), &out, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 2 {
		t.Fatalf("%d uploads", len(recorder.requests))
	}
	archive := recorder.requests[0].URL.Path
	metadataPath := recorder.requests[1].URL.Path
	if !strings.HasSuffix(archive, ".db.age") || !strings.HasSuffix(metadataPath, ".json") ||
		!strings.HasPrefix(metadataPath, strings.TrimSuffix(archive, ".db.age")) {
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
	for _, key := range []string{"created", "source", "path", "tables", "integrity_check", "bytes",
		"plain_sha256", "age_recipient", "sha256"} {
		if _, found := document[key]; !found {
			t.Errorf("metadata lacks %q: %s", key, recorded)
		}
	}
	if len(document) != 9 || document["source"] != "hello-staging/hello" || document["tables"] != float64(1) {
		t.Fatalf("metadata %s", recorded)
	}
	// The archive is the backup: decrypting it gives the copy the metadata describes.
	key := filepath.Join(directory, "age.key")
	if err := os.WriteFile(key, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(directory, "restored.db")
	if err := decryptFile(key, filepath.Join(directory, filepath.Base(archive)), restored); err != nil {
		t.Fatal(err)
	}
	if digest, err := sha256Of(restored); err != nil || digest != document["plain_sha256"] {
		t.Fatalf("the archive does not decrypt to the recorded copy: %q %v", digest, err)
	}
	if !strings.Contains(out.String(), "[OK] hello-staging/hello: sqlite-") ||
		!strings.Contains(out.String(), "Published") {
		t.Fatalf("output %q", out.String())
	}
}
