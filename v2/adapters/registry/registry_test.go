package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const (
	repository = "samclement/demo-orders"
	indexTag   = "42-cccccccccccccccccccccccccccccccccccccccc"
	directTag  = "41-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	token      = "test-token"
	indexHash  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	platform   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	direct     = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	configHash = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

func recorded(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// label is the declaration the recorded config states.
func label(t *testing.T) string {
	t.Helper()
	var document struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(recorded(t, "config.json"), &document); err != nil {
		t.Fatal(err)
	}
	return document.Config.Labels[kernel.DeclarationLabel]
}

// answers is the contract's scenario over the recorded repository. The index is keyed by its own digest too,
// so the contract proves a digest reference resolves to the platform's manifest whichever reader answers.
func answers(t *testing.T) registrytest.Scenario {
	t.Helper()
	declaration := label(t)
	return registrytest.Scenario{
		Tags: []string{"9-eeeeeeee", directTag, indexTag},
		Images: map[string]registry.Image{
			indexTag:  {Digest: platform, MediaType: kernel.ManifestMediaType, Labels: map[string]string{kernel.DeclarationLabel: declaration}},
			indexHash: {Digest: platform, MediaType: kernel.ManifestMediaType, Labels: map[string]string{kernel.DeclarationLabel: declaration}},
			directTag: {Digest: direct, MediaType: kernel.ManifestMediaType, Labels: map[string]string{kernel.DeclarationLabel: declaration}},
		},
		Missing: "43-dddddddddddddddddddddddddddddddddddddddd",
	}
}

// server answers the recorded registry: a token, the tag list, an index and a direct manifest, and the
// config blob. It records what it was asked and refuses an unauthenticated registry read.
type server struct {
	requests []*http.Request
	answer   func(t *testing.T, w http.ResponseWriter, r *http.Request)
}

func (s *server) serve(t *testing.T) *httptest.Server {
	t.Helper()
	instance := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests = append(s.requests, r)
		if s.answer != nil {
			s.answer(t, w, r)
			return
		}
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("scope") != "repository:"+repository+":pull" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			write(w, "application/json", "application/json", recorded(t, "token.json"))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			write(w, "application/json", "application/json", recorded(t, "tags.json"))
		case strings.HasSuffix(r.URL.Path, "/manifests/"+indexTag):
			write(w, "application/vnd.oci.image.index.v1+json", indexHash, recorded(t, "index.json"))
		case strings.HasSuffix(r.URL.Path, "/manifests/"+indexHash):
			// The index is reachable by its own digest, as a record that states one reads it.
			write(w, "application/vnd.oci.image.index.v1+json", indexHash, recorded(t, "index.json"))
		case strings.HasSuffix(r.URL.Path, "/manifests/"+directTag):
			write(w, "application/vnd.oci.image.manifest.v1+json", direct, recorded(t, "manifest.json"))
		case strings.HasSuffix(r.URL.Path, "/manifests/"+platform):
			write(w, "application/vnd.oci.image.manifest.v1+json", platform, recorded(t, "manifest.json"))
		case strings.HasSuffix(r.URL.Path, "/blobs/"+configHash):
			write(w, "application/octet-stream", "", recorded(t, "config.json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(instance.Close)
	return instance
}

func write(w http.ResponseWriter, contentType, digest string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	if digest != "" {
		w.Header().Set("Docker-Content-Digest", digest)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// rerouted sends every request to the test server, whatever host the adapter addressed: the image names
// ghcr.io, and the server's certificate names the test address.
type rerouted struct {
	inner  http.RoundTripper
	target *url.URL
}

func (r rerouted) RoundTrip(request *http.Request) (*http.Response, error) {
	copied := request.Clone(request.Context())
	copied.URL.Scheme, copied.URL.Host = r.target.Scheme, r.target.Host
	return r.inner.RoundTrip(copied)
}

func client(t *testing.T, instance *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(instance.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: rerouted{inner: instance.Client().Transport, target: target}}
}

// open is the real adapter over the recorded registry.
func open(t *testing.T, _ registrytest.Scenario) registry.Reader {
	t.Helper()
	reader, err := registry.New("ghcr.io/"+repository, client(t, (&server{}).serve(t)))
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func TestContract(t *testing.T) {
	registrytest.Contract(t, answers(t), open)
}

// TestDescribeAuthorizesItsOwnRead: a reader asked about a reference without listing tags first — a migration
// holding the digest a record states — obtains its own token. The ordering deploy happens to have hides this, so
// the test reads with a fresh reader and asserts the token came first.
func TestDescribeAuthorizesItsOwnRead(t *testing.T) {
	s := &server{}
	reader, err := registry.New("ghcr.io/"+repository, client(t, s.serve(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Describe(context.Background(), indexHash); err != nil {
		t.Fatal(err)
	}
	if len(s.requests) < 2 || s.requests[0].URL.Path != "/token" {
		t.Fatalf("the first request was not the token: %v", s.requests)
	}
	if got := s.requests[1].URL.Path; !strings.HasSuffix(got, "/manifests/"+indexHash) {
		t.Fatalf("the digest reference was read as %q", got)
	}
}

// TestRecordedRequests: the adapter asks the registry's own paths, with the token, and reads the index's
// platform manifest and that manifest's config.
func TestRecordedRequests(t *testing.T) {
	s := &server{}
	reader, err := registry.New("ghcr.io/"+repository, client(t, s.serve(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Describe(context.Background(), indexTag); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, request := range s.requests {
		paths = append(paths, request.URL.Path)
	}
	for _, want := range []string{"/token", "/v2/" + repository + "/tags/list", "/v2/" + repository + "/manifests/" + indexTag,
		"/v2/" + repository + "/manifests/" + platform, "/v2/" + repository + "/blobs/" + configHash} {
		found := false
		for _, path := range paths {
			found = found || path == want
		}
		if !found {
			t.Errorf("the adapter never asked for %s: %v", want, paths)
		}
	}
}

// TestNoPlatformManifest: an index with no linux/amd64 manifest is a refusal about the image, not a read
// failure and not another platform's image.
func TestNoPlatformManifest(t *testing.T) {
	s := &server{}
	s.answer = func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			write(w, "application/json", "", recorded(t, "token.json"))
		case strings.HasSuffix(r.URL.Path, "/manifests/"+indexTag):
			body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"` + direct + `","platform":{"architecture":"arm64","os":"linux"}}]}`)
			write(w, "application/vnd.oci.image.index.v1+json", indexHash, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
	reader, err := registry.New("ghcr.io/"+repository, client(t, s.serve(t)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Describe(context.Background(), indexTag)
	refusal(t, err, registry.NoPlatformManifest)
}

// TestFailures: only a 200 is evidence; every other answer is a failed read with a fixed reason, never an
// empty repository.
func TestFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		answer func(t *testing.T, w http.ResponseWriter, r *http.Request)
		code   string
	}{
		{"forbidden", func(_ *testing.T, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		}, registry.Unavailable},
		{"not found", func(_ *testing.T, w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}, registry.Unavailable},
		{"unreadable token", func(_ *testing.T, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				_, _ = w.Write([]byte(`{"token":`))
				return
			}
			w.WriteHeader(http.StatusOK)
		}, registry.Unavailable},
		{"unreadable tags", func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				write(w, "application/json", "", recorded(t, "token.json"))
				return
			}
			_, _ = w.Write([]byte(`{"tags":[`))
		}, registry.Unavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &server{answer: tt.answer}
			reader, err := registry.New("ghcr.io/"+repository, client(t, s.serve(t)))
			if err != nil {
				t.Fatal(err)
			}
			_, err = reader.Tags(context.Background())
			refusal(t, err, tt.code)
		})
	}
}

// TestNewRefusesAnInvalidImage: the adapter addresses an artifact image, so it refuses what is not one
// before it asks any registry.
func TestNewRefusesAnInvalidImage(t *testing.T) {
	for _, image := range []string{"", "ghcr.io", "ghcr.io/owner/app:tag"} {
		if _, err := registry.New(image, nil); err == nil {
			t.Errorf("%q was accepted", image)
		}
	}
}

// refusal names the error's code, which is the machine contract.
func refusal(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got no error", code)
	}
	var failure *registry.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("%v, want code %s", err, code)
	}
}
