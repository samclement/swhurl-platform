package kernel

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The platform's image automation is Flux's: one ImageRepository per app repository and one ImagePolicy per
// staging app, both in flux-system, which the staging unit applies and waits for. The tag contract and the
// policy name are the platform's own, so the workflow that starts a delivery and the object Flux watches
// cannot disagree about either.
const (
	// ImageAutomationUnit is the platform unit that holds the Alert starting the Deploy workflow; a staging
	// instance's unit waits for it, as v1's staging units do.
	ImageAutomationUnit = "platform-image-automation"
	// ImagePolicyPattern is the platform's tag contract: the template's workflow publishes `<run>-<commit>`.
	ImagePolicyPattern = `^(?P<run>[0-9]+)-[0-9a-f]{7,40}$`
	// ImagePolicyExtract is the part the policy ranks: the run number, numerically ascending.
	ImagePolicyExtract = `$run`
	// ImageInterval is how often the platform rescans a repository. A webhook makes the first scan sooner.
	ImageInterval = "1h"
	// DeclarationLabel is where an image states what it needs: one JSON document in its config labels, written
	// by the build that produced it. It is the delivery contract, not the whole app declaration.
	DeclarationLabel = "platform.swhurl.com/declaration"
	// RevisionLabel is where an image states the full commit it was built from (the standard OCI label). The
	// platform's tag carries an abbreviation only, and a release states a full commit, so the image states one and
	// the tag has to agree with it.
	RevisionLabel = "org.opencontainers.image.revision"
)

// ImageRepositoryName is the app's repository object in flux-system, as v1 names it.
func ImageRepositoryName(application string) string { return application }

// ImagePolicyName is the staging policy's name. The Deploy workflow names the app from it (`<app>-staging`), so
// the platform's one policy name is the contract between the object and the workflow.
func ImagePolicyName(application string) string { return application + "-staging" }

var imageTagPattern = regexp.MustCompile(ImagePolicyPattern)

// ImageTag is one published tag the platform's contract accepts: the run number it ranks by, and the commit
// its suffix states.
type ImageTag struct {
	Name   string
	Run    int64
	Commit string
}

// NewestTag is the tag the platform's policy picks from the registry's list: the highest run number, and the
// greatest name when two tags share one. A tag outside the contract is not a candidate; false means none is,
// which is not an error about the repository.
func NewestTag(tags []string) (ImageTag, bool) {
	best, found := ImageTag{}, false
	for _, tag := range tags {
		candidate, ok := parseImageTag(tag)
		if !ok {
			continue
		}
		if !found || candidate.Run > best.Run || (candidate.Run == best.Run && candidate.Name > best.Name) {
			best, found = candidate, true
		}
	}
	return best, found
}

// parseImageTag is one tag with the platform's contract applied: the run number it ranks by and the commit its suffix
// abbreviates. A tag outside the contract states neither, so it is no candidate.
func parseImageTag(name string) (ImageTag, bool) {
	match := imageTagPattern.FindStringSubmatch(name)
	if match == nil {
		return ImageTag{}, false
	}
	run, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return ImageTag{}, false // a run number the platform cannot rank is not a candidate
	}
	return ImageTag{Name: name, Run: run, Commit: name[strings.LastIndex(name, "-")+1:]}, true
}

// SplitImage is one image reference as an operator states it, split into the artifact image a record keeps — the
// registry host and the repository path — and the tag it was published with. The tag is what states the commit a
// release needs, so a promise without one, or one whose tag is not the platform's `<run>-<commit>`, states nothing a
// record can be checked against.
func SplitImage(reference string) (string, ImageTag, error) {
	cut := strings.LastIndex(reference, ":")
	if strings.Contains(reference, "@") || cut < 0 || cut < strings.LastIndex(reference, "/") {
		return "", ImageTag{}, refuse(Invalid, "artifact.image", "expected REPO:<run>-<commit>, not a digest")
	}
	image, name := reference[:cut], reference[cut+1:]
	if _, _, err := ImageParts(image); err != nil {
		return "", ImageTag{}, err
	}
	tag, ok := parseImageTag(name)
	if !ok {
		return "", ImageTag{}, refuse(Invalid, "artifact.image", "expected the platform's <run>-<commit> tag")
	}
	return image, tag, nil
}

// RevisionOf is the commit an image was built from, checked against the tag that published it: the revision is a
// full commit, and the tag's own suffix abbreviates it. A tag whose suffix a revision does not start with is another
// build, and an abbreviation is not a commit.
func RevisionOf(tag ImageTag, revision string) (string, error) {
	if !commitPattern.MatchString(revision) {
		return "", refuse(Invalid, "source.revision", "invalid format")
	}
	if !strings.HasPrefix(revision, tag.Commit) {
		return "", refuse(Invalid, "source.revision", "the tag's suffix does not abbreviate the revision")
	}
	return revision, nil
}

// ImageParts splits an artifact image into its registry host and repository path. Both are what the registry
// API addresses; the path is also what the platform's source convention states for an image built from an app
// repository.
func ImageParts(image string) (host, repository string, err error) {
	if !imagePattern.MatchString(image) {
		return "", "", refuse(Invalid, "artifact.image", "invalid format")
	}
	host, repository, _ = strings.Cut(image, "/")
	return host, repository, nil
}

// ImageRepository is the repository path of an artifact image, or "" when the image is not one the platform
// states. Rendering uses it to tell the app's own publication from a pinned third-party image.
func ImageRepository(image string) string {
	_, repository, err := ImageParts(image)
	if err != nil {
		return ""
	}
	return repository
}

// Declaration is what an image states about itself, read from its config labels: the runtime facts the
// platform applies, the needs it cannot apply yet, and the documents the app offers. It is the image's own
// claim, so the platform validates it strictly rather than filling a missing fact with a default: an image
// either states its whole declaration or is not this platform's.
type Declaration struct {
	SchemaVersion int32    `json:"schemaVersion"`
	Runtime       Runtime  `json:"runtime"`
	Telemetry     bool     `json:"telemetry,omitempty"`
	Database      string   `json:"database,omitempty"`
	Publishes     []string `json:"publishes,omitempty"`
	// Secrets is a need v2 does not model yet. It is read so an image that adds one is held rather than
	// silently run without it; a non-empty list is refused.
	Secrets []string `json:"secrets,omitempty"`
	// MemoryMiB is the memory the image runs in: required, because an instance has to run it somewhere, and it
	// is what a later image may hold an instance under.
	MemoryMiB int32 `json:"memoryMiB"`
	// DatabaseSizeMiB is the volume a new database is created with: required with a database, and refused
	// without one, because a volume with nothing to hold is not a decision an image can make.
	DatabaseSizeMiB int32 `json:"databaseSizeMiB,omitempty"`
	// Description is the one line the app says about itself, served to it as SWHURL_DESCRIPTION.
	Description string `json:"description,omitempty"`
	// APIPath is where a web image serves its API description. It is required with the api document and
	// refused without it, so an app that describes itself and one that serves that description cannot disagree.
	APIPath string `json:"apiPath,omitempty"`
	// Documents are the bytes the app offers other apps, by role, base64-encoded in the label so the record's
	// copy is byte-identical to the file the build read.
	Documents map[string][]byte `json:"documents,omitempty"`
}

// DocumentRoles names the fixed file an offered document's role takes in an instance's offers folder: a role the
// label states is a file named here, so no index file says which name is which.
var DocumentRoles = map[string]string{"api": "openapi.json", "events": "asyncapi.yaml", "flags": "flags.json"}

// OfferedFiles is the documents the label states, as the files an instance's offers folder holds: the role's own
// name and the bytes the producer encoded, unchanged. The order is the roles', so a report of them is stable.
func (d Declaration) OfferedFiles() []File {
	files := make([]File, 0, len(d.Documents))
	for _, role := range documentRoles(d) {
		files = append(files, File{Path: DocumentRoles[role], Document: d.Documents[role]})
	}
	return files
}

// documentRoles are the roles a declaration states, in the order the platform names them.
func documentRoles(d Declaration) []string {
	roles := make([]string, 0, len(d.Documents))
	for _, role := range []string{"api", "events", "flags"} {
		if _, stated := d.Documents[role]; stated {
			roles = append(roles, role)
		}
	}
	return roles
}

// DecodeDeclaration reads and validates one image declaration. The document is strict: unknown fields and
// nulls are refused, and the runtime is the release's own rule, so a web image that states no port and a
// worker that states one are both invalid.
func DecodeDeclaration(data []byte) (Declaration, error) {
	if err := document(data); err != nil {
		return Declaration{}, err
	}
	root, err := fields(data, "declaration", []string{"schemaVersion", "runtime", "memoryMiB"},
		"telemetry", "database", "publishes", "secrets", "databaseSizeMiB", "description", "apiPath", "documents")
	if err != nil {
		return Declaration{}, err
	}
	if _, err := fields(root["runtime"], "runtime", []string{"kind", "uid"}, "port", "healthPath"); err != nil {
		return Declaration{}, err
	}
	var declaration Declaration
	if err := json.Unmarshal(data, &declaration); err != nil {
		return Declaration{}, refuse(Invalid, "declaration", "a field has the wrong type")
	}
	if declaration.SchemaVersion != 2 {
		return Declaration{}, refuse(Unsupported, "schemaVersion", "only version 2 is supported")
	}
	if err := declaration.validate(); err != nil {
		return Declaration{}, err
	}
	return declaration, nil
}

// validate applies the label's own rules in the field table's order, so the first broken fact decides: what the
// image needs, what it may not add, and what its offered documents have to agree with.
func (d Declaration) validate() error {
	if err := d.Runtime.validate(); err != nil {
		return err
	}
	if len(d.Secrets) > 0 {
		return refuse(Unsupported, "declaration.secrets", "bindings_are_not_modelled")
	}
	if d.MemoryMiB < 1 {
		return refuse(Invalid, "declaration.memoryMiB", "expected the memory the image runs in, at least 1 MiB")
	}
	if err := d.validateDocuments(); err != nil {
		return err
	}
	if err := validateEventNames("publishes", d.Publishes); err != nil {
		return err
	}
	if len(d.Publishes) > 0 && d.Documents["events"] == nil {
		return refuse(Invalid, "declaration.documents.events", "an image that publishes states its events document")
	}
	return d.validateDatabase()
}

// validateDocuments checks what the label states about the app's offered documents: the roles it may name, the one
// line it says about itself, and the path that has to agree with the api document.
func (d Declaration) validateDocuments() error {
	if err := d.validateDocumentRoles(); err != nil {
		return err
	}
	if d.Description != "" && !descriptionPattern.MatchString(d.Description) {
		return refuse(Invalid, "declaration.description", "expected one line of at most 200 printable characters")
	}
	return d.validateDocumentPath()
}

// validateDocumentRoles refuses a role the platform has no fixed name for, and a document that is a name rather
// than the bytes it offers.
func (d Declaration) validateDocumentRoles() error {
	for role, document := range d.Documents {
		if _, known := DocumentRoles[role]; !known {
			return refuse(Invalid, "declaration.documents", "expected api, events and flags only")
		}
		if len(document) == 0 {
			return refuse(Invalid, "declaration.documents."+role, "expected the document's bytes")
		}
	}
	return nil
}

// validateDocumentPath ties the api document to the path it is served on: one without the other describes nothing,
// and only a web image serves a path.
func (d Declaration) validateDocumentPath() error {
	switch {
	case d.APIPath != "" && d.Documents["api"] == nil:
		return refuse(Invalid, "declaration.apiPath", "a path is served only with the api document it describes")
	case d.Documents["api"] != nil && d.APIPath == "":
		return refuse(Invalid, "declaration.documents.api", "the api document needs the path it is served on")
	case d.APIPath == "":
		return nil
	}
	if d.Runtime.Kind != "web" {
		return refuse(Invalid, "declaration.apiPath", "a worker serves no API description")
	}
	if !apiPathPattern.MatchString(d.APIPath) {
		return refuse(Invalid, "declaration.apiPath", "expected an absolute path")
	}
	return nil
}

// validateDatabase checks the database and the volume it states together: one without the other is a record that
// cannot be run, and only sqlite is modelled.
func (d Declaration) validateDatabase() error {
	switch {
	case d.Database == "" && d.DatabaseSizeMiB > 0:
		return refuse(Invalid, "declaration.databaseSizeMiB", "an image that keeps no database has nothing to put in a volume")
	case d.Database != "" && d.Database != "sqlite":
		return refuse(Unsupported, "declaration.database", "only sqlite is supported")
	case d.Database != "" && d.DatabaseSizeMiB < 1:
		return refuse(Invalid, "declaration.databaseSizeMiB", "a database needs the volume it is created with")
	}
	return nil
}

// Release turns one declaration into the release an image publishes. The artifact's digest and media type are
// what the registry answered; the platform is the one the platform runs. The source commit is the tag's own
// suffix, checked by the release's identity like every other fact.
func (d Declaration) Release(application, repository, commit, image, digest, mediaType string) (ReleaseBundle, error) {
	release := ReleaseBundle{
		SchemaVersion: 1,
		Application:   application,
		Source:        Source{Repository: repository, Commit: commit},
		Artifact:      Artifact{Image: image, Digest: digest, MediaType: mediaType, Platform: "linux/amd64"},
		Runtime:       d.Runtime,
		Database:      d.Database,
		Telemetry:     d.Telemetry,
		Publishes:     slices.Clone(d.Publishes),
		Description:   d.Description,
		APIPath:       d.APIPath,
		// The documents travel with the image, so the release states which roles it carries; the commit they are
		// named by is the source's.
		Documents: documentRoles(d),
	}
	id, err := release.ID()
	if err != nil {
		return ReleaseBundle{}, err
	}
	release.ReleaseID = id
	return release, nil
}

// AcceptDeclaration is where a new image meets the instance that would run it. It refuses a fact v2 cannot
// apply (new secrets, a larger memory floor, a different kind, a database or a user it cannot change), so a
// refused image is held and the running one stays. Absence of a fact is not permission: nothing here is
// defaulted.
func AcceptDeclaration(declaration Declaration, spec InstanceSpec, current ReleaseBundle) error {
	if err := declarationUnmodelled(declaration, spec); err != nil {
		return err
	}
	return declarationWorkload(declaration, spec, current)
}

// declarationUnmodelled refuses the needs v2 reads but cannot apply. They are read so an image that adds one is
// held, rather than run without it.
func declarationUnmodelled(declaration Declaration, spec InstanceSpec) error {
	if len(declaration.Secrets) > 0 {
		return refuse(Unsupported, "declaration.secrets", "bindings_are_not_modelled")
	}
	if declaration.MemoryMiB > spec.Configuration.MemoryMiB {
		return refuse(Unsupported, "declaration.memoryMiB", "the image needs more memory than the instance has")
	}
	return nil
}

// declarationWorkload refuses a workload fact an existing instance cannot change: its kind, its database, and
// the user that owns the files on a volume it already has.
func declarationWorkload(declaration Declaration, spec InstanceSpec, current ReleaseBundle) error {
	if declaration.Runtime.Kind != current.Runtime.Kind {
		return refuse(Unsupported, "runtime.kind", "the kind is fixed; create a new app for another kind")
	}
	if declaration.Database != current.Database {
		return refuse(Unsupported, "declaration.database", "an image cannot add or remove the volume a database needs")
	}
	if (spec.Storage.ClaimName != "" || spec.Storage.SizeMiB > 0) && declaration.Runtime.UID != current.Runtime.UID {
		return refuse(Unsupported, "runtime.uid", "the user cannot change on an instance with a volume")
	}
	return nil
}
