package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// defaults are the image's own: the scratch volume, the region the bucket lives in, and the age recipient.
const (
	defaultDirectory = "/scratch"
	defaultRegion    = "eu-west-2"
	stampLayout      = "20060102T150405Z"
)

// settings is one job's configuration, read from the pod's environment: what the job protects, where it
// publishes, and how it encrypts. Nothing is defaulted that a job must state.
type settings struct {
	Recipient string // AGE_RECIPIENT: the public key the archive is encrypted to
	URI       string // BACKUP_S3_URI: s3://bucket/prefix/ for this database
	Source    string // BACKUP_SOURCE: <namespace>/<application>, recorded in the metadata
	Directory string // BACKUP_DIR: the scratch directory (default /scratch)
	Database  string // DATABASE_PATH: the SQLite file inside the claim
	Region    string // AWS_DEFAULT_REGION (default eu-west-2)
	Endpoint  string // S3_ENDPOINT: an override for a compatible store; empty for AWS
	AccessKey string
	SecretKey string
	Token     string
}

// settingsOf reads a job's settings from an environment, and refuses a job that cannot run: a missing fact is
// named, never defaulted into a job that would publish somewhere unintended.
func settingsOf(environ []string) (settings, error) {
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
	job := settings{
		Recipient: values["AGE_RECIPIENT"],
		URI:       values["BACKUP_S3_URI"],
		Source:    values["BACKUP_SOURCE"],
		Directory: read("BACKUP_DIR", defaultDirectory),
		Database:  values["DATABASE_PATH"],
		Region:    read("AWS_DEFAULT_REGION", defaultRegion),
		Endpoint:  values["S3_ENDPOINT"],
		AccessKey: values["AWS_ACCESS_KEY_ID"],
		SecretKey: values["AWS_SECRET_ACCESS_KEY"],
		Token:     values["AWS_SESSION_TOKEN"],
	}
	for _, required := range []struct{ name, value string }{
		{"AGE_RECIPIENT", job.Recipient},
		{"BACKUP_S3_URI", job.URI},
		{"BACKUP_SOURCE", job.Source},
		{"DATABASE_PATH", job.Database},
	} {
		if required.value == "" {
			return settings{}, fmt.Errorf("not configured: %s is not set", required.name)
		}
	}
	if _, _, err := splitURI(job.URI); err != nil {
		return settings{}, err
	}
	if job.AccessKey == "" || job.SecretKey == "" {
		return settings{}, errors.New("not configured: the AWS credentials are not set")
	}
	return job, nil
}

// splitURI is the bucket and prefix an s3:// URI names.
func splitURI(uri string) (bucket, prefix string, err error) {
	rest, found := strings.CutPrefix(uri, "s3://")
	if !found {
		return "", "", fmt.Errorf("not configured: BACKUP_S3_URI is not an s3:// URI")
	}
	bucket, prefix, _ = strings.Cut(rest, "/")
	if bucket == "" || !strings.HasSuffix(prefix, "/") {
		return "", "", fmt.Errorf("not configured: BACKUP_S3_URI needs a bucket and a prefix ending in /")
	}
	return bucket, prefix, nil
}

// stamp is a backup's identity in its file name: UTC, to the second.
func stamp(now time.Time) string { return now.UTC().Format(stampLayout) }
