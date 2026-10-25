// Package kernel models offline release selection and compilation. It has no I/O; its records hold
// no references, except a PlatformProfile's Delivery.DependsOn slice, which every use copies.
package kernel

import (
	"fmt"
	"regexp"
	"strings"
)

// Code is a stable refusal category; messages name fields, never rejected values.
type Code string

const (
	Invalid          Code = "invalid"
	Unsupported      Code = "unsupported"
	IdentityMismatch Code = "identity_mismatch"
	WrongApplication Code = "wrong_application"
	StaleInstance    Code = "stale_instance"
)

type Error struct {
	Code   Code
	Field  string
	Reason string
}

func (e *Error) Error() string                     { return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Reason) }
func refuse(code Code, field, reason string) error { return &Error{code, field, reason} }

const ManifestMediaType = "application/vnd.oci.image.manifest.v1+json"

type ReleaseID string
type InstanceRevision string

type Source struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}

type Artifact struct {
	Image     string `json:"image"`
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType"`
	Platform  string `json:"platform"`
}

type Runtime struct {
	Kind       string `json:"kind"`
	Port       int32  `json:"port"`
	HealthPath string `json:"healthPath"`
	UID        int32  `json:"uid"`
}

// ReleaseID is an optional assertion on input; DecodeRelease fills it on success.
// Constructing a value does not validate it. ID, encoding and selection all validate.
type ReleaseBundle struct {
	SchemaVersion int32     `json:"schemaVersion"`
	Application   string    `json:"application"`
	Source        Source    `json:"source"`
	Artifact      Artifact  `json:"artifact"`
	Runtime       Runtime   `json:"runtime"`
	ReleaseID     ReleaseID `json:"releaseId"`
}

type Configuration struct {
	Replicas  int32 `json:"replicas"`
	MemoryMiB int32 `json:"memoryMiB"`
}

type Grants struct {
	Exposure string `json:"exposure"`
}

// Bindings has no supported members in phase 1. The codec refuses non-empty objects.
type Bindings struct{}

type InstanceSpec struct {
	SchemaVersion int32         `json:"schemaVersion"`
	Application   string        `json:"application"`
	Environment   string        `json:"environment"`
	Release       ReleaseID     `json:"release"`
	Configuration Configuration `json:"configuration"`
	Grants        Grants        `json:"grants"`
	Bindings      Bindings      `json:"bindings"`
}

var (
	namePattern     = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
	sourcePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	releasePattern  = regexp.MustCompile(`^rel-sha256:[0-9a-f]{64}$`)
	revisionPattern = regexp.MustCompile(`^inst-sha256:[0-9a-f]{64}$`)
	// Deliberately narrow: dotted DNS registry, no port, credentials, tag or digest suffix.
	imagePattern  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?(/[a-z0-9]+([._-][a-z0-9]+)*)+$`)
	healthPattern = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*$`)
)

// validate checks the fields in the order of the field table, so the first broken one decides.
func (r ReleaseBundle) validate() error {
	if r.SchemaVersion != 1 {
		return refuse(Unsupported, "schemaVersion", "only version 1 is supported")
	}
	if !namePattern.MatchString(r.Application) {
		return refuse(Invalid, "application", "invalid format")
	}
	if err := r.Source.validate(); err != nil {
		return err
	}
	if err := r.Artifact.validate(); err != nil {
		return err
	}
	return r.Runtime.validate()
}

func (s Source) validate() error {
	if !sourcePattern.MatchString(s.Repository) {
		return refuse(Invalid, "source.repository", "invalid format")
	}
	if !commitPattern.MatchString(s.Commit) {
		return refuse(Invalid, "source.commit", "invalid format")
	}
	return nil
}

func (a Artifact) validate() error {
	if !imagePattern.MatchString(a.Image) {
		return refuse(Invalid, "artifact.image", "invalid format")
	}
	if !strings.HasPrefix(a.Digest, "sha256:") {
		return refuse(Unsupported, "artifact.digest", "only sha256 is supported")
	}
	if !digestPattern.MatchString(a.Digest) {
		return refuse(Invalid, "artifact.digest", "expected sha256 and 64 lowercase hex characters")
	}
	if a.MediaType != ManifestMediaType {
		return refuse(Unsupported, "artifact.mediaType", "only OCI image manifests are supported")
	}
	if a.Platform != "linux/amd64" {
		return refuse(Unsupported, "artifact.platform", "only linux/amd64 is supported")
	}
	return nil
}

func (r Runtime) validate() error {
	if r.Kind != "web" {
		return refuse(Unsupported, "runtime.kind", "only stateless HTTP web releases are supported")
	}
	if r.Port < 1 || r.Port > 65535 {
		return refuse(Invalid, "runtime.port", "expected an integer from 1 to 65535")
	}
	if !healthPattern.MatchString(r.HealthPath) || strings.HasPrefix(r.HealthPath, "//") {
		return refuse(Invalid, "runtime.healthPath", "expected an absolute ASCII path, without authority, query or fragment")
	}
	if r.UID < 1 {
		return refuse(Invalid, "runtime.uid", "expected a positive non-root signed-32-bit UID")
	}
	return nil
}

func (s InstanceSpec) validate() error {
	if s.SchemaVersion != 1 {
		return refuse(Unsupported, "schemaVersion", "only version 1 is supported")
	}
	if !namePattern.MatchString(s.Application) {
		return refuse(Invalid, "application", "expected an app name")
	}
	if s.Environment != "staging" && s.Environment != "prod" {
		return refuse(Unsupported, "environment", "only staging and prod are supported")
	}
	if !releasePattern.MatchString(string(s.Release)) {
		return refuse(Invalid, "release", "expected a release ID")
	}
	if s.Configuration.Replicas < 0 {
		return refuse(Invalid, "configuration.replicas", "expected a nonnegative signed-32-bit integer")
	}
	if s.Configuration.MemoryMiB < 1 {
		return refuse(Invalid, "configuration.memoryMiB", "expected a positive signed-32-bit integer")
	}
	if s.Grants.Exposure != "private" {
		return refuse(Unsupported, "grants.exposure", "only private exposure is supported")
	}
	return nil
}
