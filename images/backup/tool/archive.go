package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// The archive names are the platform's, not this program's: an archive written here is the one
// `make restore-sqlite` and `verify-platform` already understand.
const (
	archivePrefix = "sqlite-"
	archiveSuffix = ".db.age"
)

// archiveName is one backup's file name: sqlite-<UTC>.db.age.
func archiveName(now time.Time) string { return archivePrefix + stamp(now) + archiveSuffix }

// metadataName is the name of the record published beside an archive, which S3 receives last: a backup counts
// only once its metadata is there.
func metadataName(archive string) string {
	return strings.TrimSuffix(archive, archiveSuffix) + ".json"
}

// facts is what the job records about its copy, keyed as the Python job keys it.
type facts struct {
	Tables         int64  `json:"tables"`
	IntegrityCheck string `json:"integrity_check"`
	Bytes          int64  `json:"bytes"`
	PlainSHA256    string `json:"plain_sha256"`
}

// metadata is the record beside an archive: the job's facts, the archive's own checksum, and where it came
// from. The keys are the contract a restore and the verification read.
type metadata struct {
	Created        string `json:"created"`
	Source         string `json:"source"`
	Path           string `json:"path"`
	Tables         int64  `json:"tables"`
	IntegrityCheck string `json:"integrity_check"`
	Bytes          int64  `json:"bytes"`
	PlainSHA256    string `json:"plain_sha256"`
	AgeRecipient   string `json:"age_recipient"`
	SHA256         string `json:"sha256"`
}

// encryptFile writes one archive: the plaintext encrypted to the recipient, and nothing left behind.
func encryptFile(recipient, plaintext, archive string) error {
	parsed, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return fmt.Errorf("not configured: AGE_RECIPIENT is not an age public key")
	}
	source, err := os.Open(plaintext)
	if err != nil {
		return fmt.Errorf("cannot read the copy: %v", err)
	}
	defer source.Close()
	destination, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot create the archive: %v", err)
	}
	writer, err := age.Encrypt(destination, parsed)
	if err != nil {
		destination.Close()
		return fmt.Errorf("cannot encrypt: %v", err)
	}
	if _, err := io.Copy(writer, source); err != nil {
		destination.Close()
		return fmt.Errorf("cannot encrypt: %v", err)
	}
	if err := writer.Close(); err != nil {
		destination.Close()
		return fmt.Errorf("cannot encrypt: %v", err)
	}
	return destination.Close()
}

// decryptFile writes the plaintext of one archive, for the restore that streams it into the database's pod.
func decryptFile(identityFile, archive, plaintext string) error {
	identities, err := ageIdentities(identityFile)
	if err != nil {
		return err
	}
	source, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("cannot read the archive: %v", err)
	}
	defer source.Close()
	reader, err := age.Decrypt(source, identities...)
	if err != nil {
		return fmt.Errorf("cannot decrypt: %v", err)
	}
	destination, err := os.OpenFile(plaintext, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot create the copy: %v", err)
	}
	if _, err := io.Copy(destination, reader); err != nil {
		destination.Close()
		return fmt.Errorf("cannot decrypt: %v", err)
	}
	return destination.Close()
}

// ageIdentities reads one age identity file: one or more private keys, as `age-keygen` writes them.
func ageIdentities(path string) ([]age.Identity, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the age key: %v", err)
	}
	defer file.Close()
	identities, err := age.ParseIdentities(file)
	if err != nil {
		return nil, fmt.Errorf("not configured: the age key is not an age identity")
	}
	return identities, nil
}

// writeMetadata records one archive, with the same keys and the same trailing newline the Python job writes.
func writeMetadata(directory string, record metadata) (string, error) {
	body, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("cannot record the metadata: %v", err)
	}
	path := filepath.Join(directory, metadataName(archivePrefix+record.Created+archiveSuffix))
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("cannot record the metadata: %v", err)
	}
	return path, nil
}

// sha256Of is a file's checksum in hex, as the metadata records it.
func sha256Of(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the file: %v", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("cannot read the file: %v", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
