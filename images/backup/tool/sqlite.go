package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// copyName is the job's scratch copy inside the backup directory; it is deleted before publishing.
const copyName = "sqlite-copy.db"

// backupSQLite is the job: copy the database while its app keeps writing, check the copy, encrypt it to the
// recipient, publish the archive and then its metadata, and leave no plaintext behind.
// client is the HTTP client the uploads use; nil is the job's own, with the proxy taken from the environment.
func backupSQLite(ctx context.Context, environ []string, out io.Writer, client *http.Client) error {
	job, err := settingsOf(environ)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	archive := archiveName(now)
	if err := os.MkdirAll(job.Directory, 0o700); err != nil {
		return fmt.Errorf("cannot prepare the backup directory: %v", err)
	}
	copied := filepath.Join(job.Directory, copyName)
	_ = os.Remove(copied) // a previous run's copy is never reused
	defer os.Remove(copied)
	recorded, err := copyDatabase(ctx, job.Database, copied)
	if err != nil {
		return err
	}
	archivePath := filepath.Join(job.Directory, archive)
	_ = os.Remove(archivePath)
	if err := encryptFile(job.Recipient, copied, archivePath); err != nil {
		return err
	}
	// age adds a header and 16 bytes per 64 KiB, so an archive smaller than the copy was cut short.
	if info, err := os.Stat(archivePath); err != nil || info.Size() < recorded.Bytes {
		return fmt.Errorf("the encrypted copy is smaller than the database (%d bytes)", recorded.Bytes)
	}
	if err := os.Remove(copied); err != nil {
		return fmt.Errorf("cannot remove the plaintext copy: %v", err)
	}
	archiveSum, err := sha256Of(archivePath)
	if err != nil {
		return err
	}
	record := metadata{Created: stamp(now), Source: job.Source, Path: job.Database,
		Tables: recorded.Tables, IntegrityCheck: recorded.IntegrityCheck, Bytes: recorded.Bytes,
		PlainSHA256: recorded.PlainSHA256, AgeRecipient: job.Recipient, SHA256: archiveSum}
	metadataPath, err := writeMetadata(job.Directory, record)
	if err != nil {
		return err
	}
	bucket, prefix, err := splitURI(job.URI)
	if err != nil {
		return err
	}
	for _, file := range []string{archivePath, metadataPath} {
		if err := putObject(ctx, job, client, bucket, prefix+filepath.Base(file), file); err != nil {
			return fmt.Errorf("cannot publish %s: %v", filepath.Base(file), err)
		}
	}
	fmt.Fprintf(out, "[OK] %s: %s (%d tables, integrity ok)\n", job.Source, archive, recorded.Tables)
	fmt.Fprintf(out, "[OK] Published %s and its metadata to %s\n", archive, job.URI)
	return nil
}

// copyDatabase is SQLite's online backup: VACUUM INTO writes a consistent, defragmented copy of a database
// whose app is still writing, without touching the original. The copy is then checked as a restore would
// check it, and its facts are what the metadata records.
func copyDatabase(ctx context.Context, source, destination string) (facts, error) {
	if _, err := os.Stat(source); err != nil {
		return facts{}, fmt.Errorf("the database %s is not there", source)
	}
	// mode=rw: never create a database that is missing. The original stays untouched; VACUUM INTO writes only
	// the destination.
	opened, err := sql.Open("sqlite", "file:"+source+"?mode=rw")
	if err != nil {
		return facts{}, fmt.Errorf("cannot open the database: %v", err)
	}
	defer opened.Close()
	quoted := "'" + fileEscape(destination) + "'"
	if _, err := opened.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return facts{}, fmt.Errorf("cannot copy the database: %v", err)
	}
	copied, err := sql.Open("sqlite", "file:"+destination+"?mode=rw")
	if err != nil {
		return facts{}, fmt.Errorf("cannot open the copy: %v", err)
	}
	defer copied.Close()
	var check string
	if err := copied.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		return facts{}, fmt.Errorf("cannot check the copy: %v", err)
	}
	if check != "ok" {
		return facts{}, fmt.Errorf("the copy failed integrity_check: %.200s", check)
	}
	var tables int64
	if err := copied.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil {
		return facts{}, fmt.Errorf("cannot count the tables: %v", err)
	}
	digest, err := sha256Of(destination)
	if err != nil {
		return facts{}, err
	}
	info, err := os.Stat(destination)
	if err != nil {
		return facts{}, fmt.Errorf("cannot read the copy: %v", err)
	}
	return facts{Tables: tables, IntegrityCheck: check, Bytes: info.Size(), PlainSHA256: digest}, nil
}

// fileEscape quotes one path for a SQL string literal: SQLite has no bind parameter in VACUUM INTO's
// destination position on every build, so the path is quoted and its quotes doubled.
func fileEscape(path string) string {
	escaped := make([]byte, 0, len(path))
	for i := 0; i < len(path); i++ {
		if path[i] == '\'' {
			escaped = append(escaped, '\'')
		}
		escaped = append(escaped, path[i])
	}
	return string(escaped)
}
