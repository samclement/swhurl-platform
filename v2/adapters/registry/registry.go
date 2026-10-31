// Package registry reads one image repository's published tags and what one manifest reference resolves to,
// over the registry's read-only API. A reference is a tag or an image digest: the registry's own API accepts
// both, which is how a record that states a multi-platform index's digest reaches the manifest it names. The
// platform's tag policy is the kernel's; this adapter answers only what the registry publishes, for the one
// platform it runs on. It takes no credential: the platform reads its app packages anonymously, and a package
// it may not read is a failed read rather than an empty answer.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// The kinds of answer this adapter gives: a request it cannot make, a registry it could not read, and a
// well-formed image that has no manifest for the platform the cluster runs.
const (
	RefusedRequest     = "refused_request"
	Unavailable        = "unavailable"
	NoPlatformManifest = "no_platform_manifest"
)

// Error names a code, the subject it is about and a fixed reason. It never carries a response body.
type Error struct{ Code, Subject, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Code, e.Subject, e.Reason) }

func refuse(subject, reason string) error { return &Error{RefusedRequest, subject, reason} }
func unavailable(subject, reason string) error {
	return &Error{Unavailable, subject, reason}
}

// Image is what one published reference resolves to for the platform: the digest and media type of its
// linux/amd64 manifest, and the labels its image config carries.
type Image struct {
	Digest    string
	MediaType string
	Labels    map[string]string
}

// Reader answers one repository's tags and what one reference resolves to. A reference is a tag or an image
// digest: the answer is the same manifest either way, so a caller holding only the digest a record states reads
// the platform's fact about it. A tag outside the platform's contract is the kernel's to rank, so this adapter
// does not filter.
type Reader interface {
	Tags(ctx context.Context) ([]string, error)
	Describe(ctx context.Context, reference string) (Image, error)
}

// The registry protocol's names, and the bounds this adapter reads within.
const (
	indexMediaType  = "application/vnd.oci.image.index.v1+json"
	listMediaType   = "application/vnd.docker.distribution.manifest.list.v2+json"
	manifestType    = "application/vnd.oci.image.manifest.v1+json"
	dockerManifest  = "application/vnd.docker.distribution.manifest.v2+json"
	acceptManifests = indexMediaType + ", " + listMediaType + ", " + manifestType + ", " + dockerManifest
	pageSize        = 1000
	maxPages        = 10
	maxBody         = 1 << 20
)

// Remote reads one image repository on one registry host.
type Remote struct {
	host, repository string
	http             *http.Client
	token            string
}

// New reads one artifact image, as a release states it (`ghcr.io/owner/app`). The registry host comes from
// the image; no profile field repeats it.
func New(image string, client *http.Client) (Reader, error) {
	host, repository, err := kernel.ImageParts(image)
	if err != nil {
		return nil, refuse("image", "invalid_format")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Remote{host: host, repository: repository, http: client}, nil
}

// Tags lists every tag the repository publishes, page by page. An empty repository is an empty list, not a
// failure.
func (r *Remote) Tags(ctx context.Context) ([]string, error) {
	if _, err := r.authorize(ctx); err != nil {
		return nil, err
	}
	tags, last := []string{}, ""
	for page := 0; page < maxPages; page++ {
		address := fmt.Sprintf("https://%s/v2/%s/tags/list?n=%d", r.host, r.repository, pageSize)
		if last != "" {
			address += "&last=" + url.QueryEscape(last)
		}
		body, err := r.get(ctx, address, "")
		if err != nil {
			return nil, err
		}
		var listing struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &listing); err != nil {
			return nil, unavailable("tags", "malformed")
		}
		tags = append(tags, listing.Tags...)
		if len(listing.Tags) < pageSize {
			return tags, nil
		}
		last = listing.Tags[len(listing.Tags)-1]
	}
	return nil, unavailable("tags", "too_many_pages")
}

// Describe resolves one reference — a tag, or an image digest — to the platform's manifest and reads the labels
// its config states. A reference that names an index is resolved to its linux/amd64 manifest; an index without
// one is refused, not guessed. A digest is its own reference, so a record that states one is read the same way a
// tag is.
func (r *Remote) Describe(ctx context.Context, reference string) (Image, error) {
	if reference == "" {
		return Image{}, refuse("reference", "required")
	}
	// The token is this reader's, not a caller's: a caller that asks about a reference without listing tags first
	// — a migration holding a record's digest — reads the same registry as one that does.
	if _, err := r.authorize(ctx); err != nil {
		return Image{}, err
	}
	manifest, digest, mediaType, err := r.manifest(ctx, reference)
	if err != nil {
		return Image{}, err
	}
	return r.forPlatform(ctx, manifest, digest, mediaType)
}

// forPlatform reads the manifest a reference resolves to for the cluster's own platform: an index's linux/amd64
// entry when the reference names an index, and the manifest itself when it names one.
func (r *Remote) forPlatform(ctx context.Context, manifest manifest, digest, mediaType string) (Image, error) {
	if mediaType != indexMediaType && mediaType != listMediaType && len(manifest.Manifests) == 0 {
		return r.image(ctx, manifest, digest, mediaType)
	}
	for _, entry := range manifest.Manifests {
		if entry.Platform.OS == "linux" && entry.Platform.Architecture == "amd64" {
			platform, platformDigest, platformType, err := r.manifest(ctx, entry.Digest)
			if err != nil {
				return Image{}, err
			}
			return r.image(ctx, platform, platformDigest, platformType)
		}
	}
	return Image{}, &Error{NoPlatformManifest, "manifest", "linux/amd64"}
}

// manifest reads one manifest reference, returning its references, its digest and its media type.
func (r *Remote) manifest(ctx context.Context, reference string) (manifest, string, string, error) {
	address := fmt.Sprintf("https://%s/v2/%s/manifests/%s", r.host, r.repository, reference)
	body, digest, mediaType, err := r.fetch(ctx, address, acceptManifests)
	if err != nil {
		return manifest{}, "", "", err
	}
	var parsed manifest
	if err := json.Unmarshal(body, &parsed); err != nil {
		return manifest{}, "", "", unavailable("manifest", "malformed")
	}
	if mediaType == "" {
		mediaType = parsed.MediaType
	}
	if mediaType == "" {
		return manifest{}, "", "", unavailable("manifest", "malformed")
	}
	return parsed, digest, mediaType, nil
}

// image reads one manifest's config labels. The config is a blob the platform may read; its labels are the
// image's own declaration.
func (r *Remote) image(ctx context.Context, manifest manifest, digest, mediaType string) (Image, error) {
	if manifest.Config.Digest == "" {
		return Image{}, unavailable("config", "malformed")
	}
	address := fmt.Sprintf("https://%s/v2/%s/blobs/%s", r.host, r.repository, manifest.Config.Digest)
	body, err := r.get(ctx, address, "")
	if err != nil {
		return Image{}, err
	}
	var config struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return Image{}, unavailable("config", "malformed")
	}
	labels := make(map[string]string, len(config.Config.Labels))
	for key, value := range config.Config.Labels {
		labels[key] = value
	}
	return Image{Digest: digest, MediaType: mediaType, Labels: labels}, nil
}

// manifest is the part of a manifest or index this adapter reads.
type manifest struct {
	MediaType string `json:"mediaType"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
}

// authorize obtains the anonymous pull token for this repository. A registry that will not issue one is a
// failed read: the platform cannot tell a private package from a missing one.
func (r *Remote) authorize(ctx context.Context) (string, error) {
	if r.token != "" {
		return r.token, nil
	}
	address := fmt.Sprintf("https://%s/token?service=%s&scope=repository:%s:pull", r.host, r.host, r.repository)
	body, err := r.get(ctx, address, "")
	if err != nil {
		return "", err
	}
	var answer struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Token == "" {
		return "", unavailable("token", "malformed")
	}
	r.token = answer.Token
	return r.token, nil
}

// get reads one body, with the token when the registry requires it.
func (r *Remote) get(ctx context.Context, address, accept string) ([]byte, error) {
	body, _, _, err := r.fetch(ctx, address, accept)
	return body, err
}

// statusFailure maps one answer's status: only a 200 says anything about the repository, and every other
// status is a failed read with a fixed reason.
func statusFailure(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return unavailable("registry", "forbidden")
	case status == http.StatusNotFound:
		return unavailable("registry", "not_found")
	case status != http.StatusOK:
		return unavailable("registry", "status")
	}
	return nil
}

// fetch reads one answer: its body, the digest the registry states (or the body's own), and its media type.
func (r *Remote) fetch(ctx context.Context, address, accept string) ([]byte, string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, "", "", refuse("registry", "invalid_url")
	}
	request.Header.Set("User-Agent", "swhurl-v2")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if r.token != "" {
		request.Header.Set("Authorization", "Bearer "+r.token)
	}
	response, err := r.http.Do(request)
	if err != nil {
		return nil, "", "", unavailable("registry", "transport")
	}
	defer response.Body.Close()
	if err := statusFailure(response.StatusCode); err != nil {
		return nil, "", "", err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil, "", "", unavailable("registry", "too_large")
	}
	digest := response.Header.Get("Docker-Content-Digest")
	if digest == "" {
		sum := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	mediaType, _, _ := strings.Cut(response.Header.Get("Content-Type"), ";")
	return body, digest, strings.TrimSpace(mediaType), nil
}
