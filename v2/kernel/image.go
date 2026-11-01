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
// platform applies and the needs it cannot apply yet. It is the image's own claim, so the platform validates
// it strictly rather than filling a missing fact with a default.
type Declaration struct {
	SchemaVersion int32    `json:"schemaVersion"`
	Runtime       Runtime  `json:"runtime"`
	Telemetry     bool     `json:"telemetry,omitempty"`
	Database      string   `json:"database,omitempty"`
	Publishes     []string `json:"publishes,omitempty"`
	// Secrets and MemoryMiB are needs v2 does not model yet. They are read so an image that adds one is held
	// rather than silently run without it; an empty list and zero mean the image needs neither.
	Secrets   []string `json:"secrets,omitempty"`
	MemoryMiB int32    `json:"memoryMiB,omitempty"`
}

// DecodeDeclaration reads and validates one image declaration. The document is strict: unknown fields and
// nulls are refused, and the runtime is the release's own rule, so a web image that states no port and a
// worker that states one are both invalid.
func DecodeDeclaration(data []byte) (Declaration, error) {
	if err := document(data); err != nil {
		return Declaration{}, err
	}
	root, err := fields(data, "declaration", []string{"schemaVersion", "runtime"},
		"telemetry", "database", "publishes", "secrets", "memoryMiB")
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
	if declaration.SchemaVersion != 1 {
		return Declaration{}, refuse(Unsupported, "schemaVersion", "only version 1 is supported")
	}
	if err := declaration.Runtime.validate(); err != nil {
		return Declaration{}, err
	}
	if err := validateEventNames("publishes", declaration.Publishes); err != nil {
		return Declaration{}, err
	}
	if declaration.MemoryMiB < 0 {
		return Declaration{}, refuse(Invalid, "declaration.memoryMiB", "expected a nonnegative signed-32-bit integer")
	}
	return declaration, nil
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
