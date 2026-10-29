// Package kernel models offline release selection and resource generation with no I/O.
// Resource results own their slices and document bytes; profile dependencies are copied on use.
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
	SchemaVersion int32    `json:"schemaVersion"`
	Application   string   `json:"application"`
	Source        Source   `json:"source"`
	Artifact      Artifact `json:"artifact"`
	Runtime       Runtime  `json:"runtime"`
	// Database is what the image needs to keep state: "sqlite", or empty for none. It is a release fact
	// because the image is what depends on it, and an instance without a volume for it cannot run.
	Database string `json:"database,omitempty"`
	// Telemetry is whether the image has an SDK that should report to the platform's collector. It is a
	// release fact for the same reason: the image, not the environment, decides whether it has one.
	Telemetry bool `json:"telemetry,omitempty"`
	// Publishes is the events the image publishes, each an `<event>` name under the app's own subject. Its
	// AsyncAPI document says so; the platform reads it at the image's commit and states the list here, so a
	// new image cannot publish something the platform has not read.
	Publishes []string  `json:"publishes,omitempty"`
	ReleaseID ReleaseID `json:"releaseId"`
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
	// Storage is the volume the instance asks for: a size in MiB, and nothing else. The class belongs to
	// the platform; a size is a decision, so there is no default.
	Storage Storage `json:"storage,omitempty"`
	// Calls and Allows are the instance's own declarations — the apps it calls, and the apps whose calls it
	// accepts, by app name (teams are not modelled). The resolver reads them to decide consent; the
	// compiler renders only the connected pairs a resolution gives it, so these never reach a resource.
	Calls  []string `json:"calls,omitempty"`
	Allows []string `json:"allows,omitempty"`
	// Events is the identity an instance signs in to the event service with: the public key, and the
	// subscriptions it grants itself. The pair's private half is never in a record — it is an encrypted
	// Secret in the instance's own folder — so a record can state the key and can never leak the seed. Every
	// instance that has one is a user of the broker, whether or not it declares events, because the same
	// identity reads its feature-flag overrides.
	Events Events `json:"events,omitempty"`
}

// Events is one instance's identity at the broker and what it grants itself: the public nkey it signs in
// with, and the events it receives, each `<publisher>.<event>`.
type Events struct {
	Key           string   `json:"key,omitempty"`
	Subscriptions []string `json:"subscriptions,omitempty"`
}

// configured is whether an instance has a broker identity at all.
func (e Events) configured() bool { return e.Key != "" || len(e.Subscriptions) > 0 }

// Storage is one instance's data volume: a size to create, or the claim of a volume that already exists —
// the shape a migrated instance takes, so its data is the same bytes before and after the handover. A size
// of zero with no claim means the instance asks for none.
type Storage struct {
	SizeMiB   int32  `json:"sizeMiB,omitempty"`
	ClaimName string `json:"claimName,omitempty"`
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
	if err := r.Runtime.validate(); err != nil {
		return err
	}
	if r.Database != "" && r.Database != "sqlite" {
		return refuse(Unsupported, "database", "only sqlite is supported")
	}
	return validateEventNames("publishes", r.Publishes)
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
	if err := validateImage(a.Image, a.Digest); err != nil {
		return err
	}
	if a.MediaType != ManifestMediaType {
		return refuse(Unsupported, "artifact.mediaType", "only OCI image manifests are supported")
	}
	if a.Platform != "linux/amd64" {
		return refuse(Unsupported, "artifact.platform", "only linux/amd64 is supported")
	}
	return nil
}

func (r Runtime) validate() error { return validateWorkload(r.Kind, r.Port, r.HealthPath, r.UID) }

// validateWorkload checks a kind and the runtime facts it has. It is the one definition of what a kind
// serves, so a record and a renderer cannot disagree about one: a web release serves HTTP on its port and
// path, a worker serves nothing and therefore states neither. A uid is both kinds'.
func validateWorkload(kind string, port int32, healthPath string, uid int32) error {
	switch kind {
	case "web":
		return validateHTTP(port, healthPath, uid)
	case "worker":
		// A port or a path would describe a Service and probes the instance does not have.
		if port != 0 {
			return refuse(Invalid, "runtime.port", "a worker serves no HTTP port")
		}
		if healthPath != "" {
			return refuse(Invalid, "runtime.healthPath", "a worker serves no health path")
		}
		return validateUID(uid)
	}
	return refuse(Unsupported, "runtime.kind", "only web and worker releases are supported")
}

func (s InstanceSpec) validate() error {
	if s.SchemaVersion != 1 {
		return refuse(Unsupported, "schemaVersion", "only version 1 is supported")
	}
	if err := validateNames(s.Application, s.Environment); err != nil {
		return err
	}
	if !releasePattern.MatchString(string(s.Release)) {
		return refuse(Invalid, "release", "expected a release ID")
	}
	if err := s.Configuration.validate(); err != nil {
		return err
	}
	if s.Storage.SizeMiB < 0 {
		return refuse(Invalid, "storage.sizeMiB", "expected a nonnegative signed-32-bit integer")
	}
	if s.Storage.ClaimName != "" && !labelPattern.MatchString(s.Storage.ClaimName) {
		return refuse(Invalid, "storage.claimName", "expected a DNS label")
	}
	if s.Grants.Exposure != "private" && s.Grants.Exposure != "authenticated-web" {
		return refuse(Unsupported, "grants.exposure", "only private and authenticated-web exposure are supported")
	}
	return s.validateEventsAndDeclarations()
}

// validateEventsAndDeclarations checks an instance's own lists: its broker identity, then the apps it calls and
// the apps whose calls it accepts, each app name at most once.
func (s InstanceSpec) validateEventsAndDeclarations() error {
	if err := s.Events.validate(); err != nil {
		return err
	}
	return s.validateDeclarations()
}

// validateDeclarations checks both sides of an instance's declarations: app names, each at most once. A team
// grant is v1's spelling and not v2's: the record states app names, and migration refuses a team grant rather
// than quietly changing what the instance accepts.
func (s InstanceSpec) validateDeclarations() error {
	for _, declared := range []struct {
		field string
		names []string
	}{{"calls", s.Calls}, {"allows", s.Allows}} {
		seen := map[string]bool{}
		for _, name := range declared.names {
			if !namePattern.MatchString(name) {
				return refuse(Invalid, declared.field, "expected an app name")
			}
			if seen[name] {
				return refuse(Invalid, declared.field, "repeated")
			}
			seen[name] = true
		}
	}
	return nil
}

func validateImage(image, digest string) error {
	if !imagePattern.MatchString(image) {
		return refuse(Invalid, "artifact.image", "invalid format")
	}
	if !strings.HasPrefix(digest, "sha256:") {
		return refuse(Unsupported, "artifact.digest", "only sha256 is supported")
	}
	if !digestPattern.MatchString(digest) {
		return refuse(Invalid, "artifact.digest", "expected sha256 and 64 lowercase hex characters")
	}
	return nil
}

func validateHTTP(port int32, healthPath string, uid int32) error {
	if port < 1 || port > 65535 {
		return refuse(Invalid, "runtime.port", "expected an integer from 1 to 65535")
	}
	if !healthPattern.MatchString(healthPath) || strings.HasPrefix(healthPath, "//") {
		return refuse(Invalid, "runtime.healthPath", "expected an absolute ASCII path, without authority, query or fragment")
	}
	return validateUID(uid)
}

func validateUID(uid int32) error {
	if uid < 1 {
		return refuse(Invalid, "runtime.uid", "expected a positive non-root signed-32-bit UID")
	}
	return nil
}

func validateNames(application, environment string) error {
	if !namePattern.MatchString(application) {
		return refuse(Invalid, "application", "expected an app name")
	}
	if environment != "staging" && environment != "prod" {
		return refuse(Unsupported, "environment", "only staging and prod are supported")
	}
	return nil
}

func (c Configuration) validate() error {
	if c.Replicas < 0 {
		return refuse(Invalid, "configuration.replicas", "expected a nonnegative signed-32-bit integer")
	}
	if c.MemoryMiB < 1 {
		return refuse(Invalid, "configuration.memoryMiB", "expected a positive signed-32-bit integer")
	}
	return nil
}

// validate checks an instance's broker identity: a public user nkey, and subscriptions that are an app name and
// an event name. An instance that receives events without a key is refused, because it could not sign in.
func (e Events) validate() error {
	if e.Key != "" && !nkeyPattern().MatchString(e.Key) {
		return refuse(Invalid, "events.key", "expected a user public nkey")
	}
	if e.Key == "" && len(e.Subscriptions) > 0 {
		return refuse(Invalid, "events.key", "an instance that receives events needs a key")
	}
	for _, subscription := range e.Subscriptions {
		publisher, event, split := strings.Cut(subscription, ".")
		if !split || !namePattern.MatchString(publisher) || !namePattern.MatchString(event) {
			return refuse(Invalid, "events.subscriptions", "expected an app name and an event name")
		}
	}
	return nil
}

// validateEventNames checks a list of event names: each one an app-name-style segment, each at most once.
func validateEventNames(field string, names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if !namePattern.MatchString(name) {
			return refuse(Invalid, field, "expected an event name")
		}
		if seen[name] {
			return refuse(Invalid, field, "repeated")
		}
		seen[name] = true
	}
	return nil
}
