package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// restoreSettings is one restore's configuration: the database to replace, the archive to restore it from,
// the facts its metadata states, and where the age key is mounted. The archive is fetched from a URL — a
// presigned read for one object — so a restore pod never holds a standing bucket credential.
type restoreSettings struct {
	Database    string // DATABASE_PATH: the SQLite file inside the claim
	Source      string // BACKUP_SOURCE: the instance the archive must have come from
	ArchiveURL  string // ARCHIVE_URL: a read URL for the encrypted archive
	ArchiveHash string // ARCHIVE_SHA256: the archive's own checksum, from the metadata
	PlainHash   string // PLAIN_SHA256: the plaintext's checksum, from the metadata
	Tables      int64  // TABLES: the table count the metadata records
	AgeKey      string // AGE_KEY_FILE (default /keys/age.key)
	Directory   string // BACKUP_DIR scratch (default /scratch)
}

// restoreSettingsOf reads a restore's settings, refusing one that cannot be verified: every fact a check
// needs is required, because a restore that cannot check what it writes is not a restore.
func restoreSettingsOf(environ []string) (restoreSettings, error) {
	values := map[string]string{}
	for _, entry := range environ {
		if name, value, found := strings.Cut(entry, "="); found {
			values[name] = value
		}
	}
	read := func(name, fallback string) string {
		if value := values[name]; value != "" {
			return value
		}
		return fallback
	}
	job := restoreSettings{
		Database: values["DATABASE_PATH"], Source: values["BACKUP_SOURCE"], ArchiveURL: values["ARCHIVE_URL"],
		ArchiveHash: values["ARCHIVE_SHA256"], PlainHash: values["PLAIN_SHA256"],
		AgeKey: read("AGE_KEY_FILE", "/keys/age.key"), Directory: read("BACKUP_DIR", defaultDirectory),
	}
	for _, required := range []struct{ name, value string }{
		{"DATABASE_PATH", job.Database},
		{"BACKUP_SOURCE", job.Source},
		{"ARCHIVE_URL", job.ArchiveURL},
		{"ARCHIVE_SHA256", job.ArchiveHash},
		{"PLAIN_SHA256", job.PlainHash},
		{"TABLES", values["TABLES"]},
	} {
		if required.value == "" {
			return restoreSettings{}, fmt.Errorf("not configured: %s is not set", required.name)
		}
	}
	tables, err := parseInt(values["TABLES"])
	if err != nil || tables < 1 {
		return restoreSettings{}, fmt.Errorf("not configured: TABLES is not a table count")
	}
	job.Tables = tables
	return job, nil
}

// databaseInPlace is the line a failed restore prints when the live database is where it was, unchanged: v2's
// restore (v2/recovery) scales the app back up after a failed job only when its log carries this exact line.
const databaseInPlace = "[OK] The live database is in place, unchanged"

// errNotInPlace marks a swap that could not be reversed: the live database may be missing or partial, so the
// job prints no databaseInPlace line and keeps every file a recovery needs.
var errNotInPlace = errors.New("the live database is not in place")

// rename is os.Rename, replaced only by tests that need one move of the swap to fail.
var rename = os.Rename

// restoreSQLite is the job: fetch the archive, decrypt and check it, and only then replace the database —
// keeping what was there in before-restore-<UTC>/ beside it. A copy that fails any check never replaces the
// live database, and every failure that leaves the database in place says so.
func restoreSQLite(ctx context.Context, environ []string, out io.Writer, client *http.Client) error {
	err := restoreChecked(ctx, environ, out, client)
	if err != nil && !errors.Is(err, errNotInPlace) {
		fmt.Fprintln(out, databaseInPlace)
	}
	return err
}

// restoreChecked is the restore's steps; restoreSQLite states what a failure left.
func restoreChecked(ctx context.Context, environ []string, out io.Writer, client *http.Client) error {
	job, err := restoreSettingsOf(environ)
	if err != nil {
		return err
	}
	if _, err := os.Stat(job.AgeKey); err != nil {
		return fmt.Errorf("not configured: the age key is not mounted")
	}
	if err := os.MkdirAll(job.Directory, 0o700); err != nil {
		return fmt.Errorf("cannot prepare the scratch directory: %v", err)
	}
	archive := filepath.Join(job.Directory, "restored.db.age")
	// The decrypted copy is staged beside the database, not in the scratch directory: the swap is three
	// renames inside the claim's own filesystem, and a rename across filesystems fails (EXDEV).
	plaintext := stagingPath(job.Database)
	for _, stale := range []string{archive, plaintext} {
		_ = os.Remove(stale)
	}
	if err := download(ctx, client, job.ArchiveURL, archive); err != nil {
		return err
	}
	hash, err := sha256Of(archive)
	if err != nil {
		return err
	}
	if hash != job.ArchiveHash {
		return fmt.Errorf("the archive's SHA-256 differs from the metadata's")
	}
	if err := decryptFile(job.AgeKey, archive, plaintext); err != nil {
		return err
	}
	hash, err = sha256Of(plaintext)
	if err != nil {
		return err
	}
	if hash != job.PlainHash {
		return fmt.Errorf("the restored copy's SHA-256 differs from the backup's")
	}
	tables, err := checkDatabase(ctx, plaintext)
	if err != nil {
		return err
	}
	if tables != job.Tables {
		return fmt.Errorf("%d tables, expected %d", tables, job.Tables)
	}
	replaced, err := replaceDatabase(job.Database, plaintext, time.Now().UTC())
	if err != nil {
		if !errors.Is(err, errNotInPlace) {
			_ = os.Remove(plaintext) // the checked copy is the operator's to put in place when the swap is half-done
		}
		return err
	}
	fmt.Fprintf(out, "[OK] Restored the archive into %s (%d tables, integrity ok); the replaced files are in %s\n",
		job.Database, tables, replaced)
	_ = os.Remove(archive)
	return nil
}

// download fetches one archive to a file.
func download(ctx context.Context, client *http.Client, address, destination string) error {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("cannot build the download")
	}
	response, err := doWithRetry(client, request)
	if err != nil {
		return fmt.Errorf("the archive could not be downloaded (%v)", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the archive could not be downloaded (%d)", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write the archive")
	}
	if _, err := io.Copy(file, response.Body); err != nil {
		file.Close()
		return fmt.Errorf("the archive could not be downloaded")
	}
	return file.Close()
}

// checkDatabase runs the checks a restore must pass: integrity, then the table count the metadata states.
func checkDatabase(ctx context.Context, path string) (int64, error) {
	opened, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		return 0, fmt.Errorf("cannot open the copy: %v", err)
	}
	defer opened.Close()
	var check string
	if err := opened.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		return 0, fmt.Errorf("cannot check the copy: %v", err)
	}
	if check != "ok" {
		return 0, fmt.Errorf("the copy failed integrity_check: %.200s", check)
	}
	var tables int64
	if err := opened.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil {
		return 0, fmt.Errorf("cannot count the tables: %v", err)
	}
	return tables, nil
}

// stagingPath is where the decrypted copy is written: beside the database, on the same filesystem, so the
// final move is a rename rather than a copy across devices. The plaintext is on the claim for that moment,
// exactly as the operator's own restore procedure stages it.
func stagingPath(database string) string { return database + ".restore" }

// replaceDatabase puts the checked copy in place, keeping the database and its -wal/-shm files in
// before-restore-<UTC>/ beside it. The directory is created only when the copy is ready to be moved. A move
// that fails reverses the moves already made, newest first; a reversal that fails too is errNotInPlace, and
// the kept files and the checked copy stay where the error says.
func replaceDatabase(database, copy string, now time.Time) (string, error) {
	folder := filepath.Join(filepath.Dir(database), "before-restore-"+stamp(now))
	if err := os.Mkdir(folder, 0o700); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("cannot keep the replaced database: %v", err)
	}
	var moved []string
	err := func() error {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			current := database + suffix
			if _, err := os.Stat(current); err != nil {
				continue // a WAL file that is not there is not an error; the database itself must be
			}
			if err := rename(current, filepath.Join(folder, filepath.Base(current))); err != nil {
				return fmt.Errorf("cannot keep the replaced database: %v", err)
			}
			moved = append(moved, current)
		}
		if err := rename(copy, database); err != nil {
			return fmt.Errorf("cannot put the restored database in place: %v", err)
		}
		return nil
	}()
	if err == nil {
		return folder, nil
	}
	for i := len(moved) - 1; i >= 0; i-- {
		if undo := rename(filepath.Join(folder, filepath.Base(moved[i])), moved[i]); undo != nil {
			return "", fmt.Errorf("%v; %w: putting %s back failed (%v); the replaced files are in %s and the "+
				"checked copy is %s", err, errNotInPlace, moved[i], undo, folder, copy)
		}
	}
	_ = os.Remove(folder) // empty again; Remove leaves one that already held an earlier run's files
	return "", fmt.Errorf("%v; the replaced files were put back", err)
}

// parseInt reads a non-negative decimal integer.
func parseInt(value string) (int64, error) {
	var parsed int64
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, fmt.Errorf("not a number")
		}
		parsed = parsed*10 + int64(value[i]-'0')
	}
	if value == "" {
		return 0, fmt.Errorf("not a number")
	}
	return parsed, nil
}

// sha256Bytes is a byte slice's checksum in hex.
func sha256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
