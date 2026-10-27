package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// mongodbPrefix is the archive's name for the platform's MongoDB backups, as the tooling that reads them
// states it: clickstack-mongodb-<UTC>.archive.gz.age.
const mongodbPrefix = "clickstack-mongodb"

// mongodbSettings is a MongoDB job's configuration: the same records and bucket as a SQLite job, plus the
// connection and the one database it dumps.
type mongodbSettings struct {
	settings
	Connection string // MONGODB_URI: the connection string, kept out of any command line
	Database   string // MONGO_DATABASE: the database to dump
	Dump       string // MONGODUMP: the dump tool's path (default: mongodump)
}

// mongodbSettingsOf reads a MongoDB job's settings. The connection string is required and never printed.
func mongodbSettingsOf(environ []string) (mongodbSettings, error) {
	values := map[string]string{}
	for _, entry := range environ {
		if name, value, found := strings.Cut(entry, "="); found {
			values[name] = value
		}
	}
	common, err := settingsOf(append(environ, "DATABASE_PATH="+values["MONGO_DATABASE"]))
	if err != nil {
		return mongodbSettings{}, err
	}
	job := mongodbSettings{settings: common, Connection: values["MONGODB_URI"],
		Database: values["MONGO_DATABASE"], Dump: values["MONGODUMP"]}
	if job.Connection == "" {
		return mongodbSettings{}, fmt.Errorf("not configured: MONGODB_URI is not set")
	}
	if job.Database == "" {
		return mongodbSettings{}, fmt.Errorf("not configured: MONGO_DATABASE is not set")
	}
	if job.Dump == "" {
		job.Dump = "mongodump"
	}
	return job, nil
}

// mongoFacts is the server's version and each collection's document count, which a restore test compares.
type mongoFacts interface {
	Facts(ctx context.Context, uri, database string) (string, map[string]int64, error)
}

// backupMongoDB is the job: dump one database straight into age (the tool's output is never written to
// plaintext disk), record its version and counts, and publish the archive and then its metadata.
func backupMongoDB(ctx context.Context, environ []string, out io.Writer, client *http.Client,
	facts mongoFacts) error {
	job, err := mongodbSettingsOf(environ)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	archive := mongodbPrefix + "-" + stamp(now) + ".archive.gz.age"
	if err := os.MkdirAll(job.Directory, 0o700); err != nil {
		return fmt.Errorf("cannot prepare the backup directory: %v", err)
	}
	archivePath := filepath.Join(job.Directory, archive)
	_ = os.Remove(archivePath)
	if err := dumpToArchive(ctx, job, archivePath); err != nil {
		return err
	}
	info, err := os.Stat(archivePath)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("the backup archive is empty")
	}
	version, collections, err := facts.Facts(ctx, job.Connection, job.Database)
	if err != nil {
		return fmt.Errorf("could not read the collection counts: %v", err)
	}
	archiveSum, err := sha256Of(archivePath)
	if err != nil {
		return err
	}
	record := metadata{Created: stamp(now), Source: job.Source, Path: "",
		Tables: int64(len(collections)), IntegrityCheck: "", Bytes: info.Size(), PlainSHA256: "",
		AgeRecipient: job.Recipient, SHA256: archiveSum}
	metadataPath, err := writeMongoMetadata(job.Directory, record, job.Database, version, collections)
	if err != nil {
		return err
	}
	bucket, prefix, err := splitURI(job.URI)
	if err != nil {
		return err
	}
	for _, file := range []string{archivePath, metadataPath} {
		if err := putObject(ctx, job.settings, client, bucket, prefix+filepath.Base(file), file); err != nil {
			return fmt.Errorf("cannot publish %s: %v", filepath.Base(file), err)
		}
	}
	fmt.Fprintf(out, "[OK] %s: %s (%d collections)\n", job.Source, archive, len(collections))
	fmt.Fprintf(out, "[OK] Published %s and its metadata to %s\n", archive, job.URI)
	return nil
}

// dumpToArchive runs the dump tool with the connection string on stdin — never on a command line — and
// encrypts its output as it streams: plaintext never reaches a file.
func dumpToArchive(ctx context.Context, job mongodbSettings, archive string) error {
	parsed, err := age.ParseX25519Recipient(job.Recipient)
	if err != nil {
		return fmt.Errorf("not configured: AGE_RECIPIENT is not an age public key")
	}
	destination, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot create the archive: %v", err)
	}
	defer destination.Close()
	writer, err := age.Encrypt(destination, parsed)
	if err != nil {
		return fmt.Errorf("cannot encrypt: %v", err)
	}
	command := exec.CommandContext(ctx, job.Dump, "--quiet", "--config=/dev/stdin", "--db", job.Database,
		"--archive", "--gzip")
	configuration, err := json.Marshal(job.Connection)
	if err != nil {
		return fmt.Errorf("cannot prepare the connection")
	}
	command.Stdin = strings.NewReader("uri: " + string(configuration) + "\n")
	command.Stdout = writer
	var problems bytes.Buffer
	command.Stderr = &problems
	if err := command.Run(); err != nil {
		writer.Close()
		return fmt.Errorf("the dump failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("cannot encrypt: %v", err)
	}
	return nil
}

// writeMongoMetadata records a MongoDB archive: the summary keys every archive has, plus the database, the
// server's version and each collection's count.
func writeMongoMetadata(directory string, record metadata, database, version string,
	collections map[string]int64) (string, error) {
	document := map[string]any{
		"created": record.Created, "source": record.Source, "database": database,
		"mongodb_version": version, "age_recipient": record.AgeRecipient,
		"collections": collections, "sha256": record.SHA256,
	}
	body, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("cannot record the metadata: %v", err)
	}
	path := filepath.Join(directory, mongodbPrefix+"-"+record.Created+".json")
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("cannot record the metadata: %v", err)
	}
	return path, nil
}
