package ntfy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy/ntfytest"
	"github.com/samclement/swhurl-platform/v2/notify"
)

// publication is a copy as the outbox sends it.
var publication = notify.Publication{Title: "payments/production: Orders failing",
	Message: "The orders queue has not drained for 15 minutes.", Priority: 4, Tags: []string{"warning"},
	Click: "https://payments.homelab.swhurl.com/orders"}

// request is one publication the server was posted.
type request struct {
	method, path, contentType string
	body                      []byte
}

// server is an ntfy server over TLS that answers from the recorded answers, by the topic a document names: one
// topic is refused, one is moved elsewhere, one never answers, and any other is accepted.
type server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []request
}

func newServer(t *testing.T) *server {
	t.Helper()
	published, refused := fixture(t, "published.json"), fixture(t, "refused.json")
	s := &server{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, request{r.Method, r.URL.Path, r.Header.Get("Content-Type"), body})
		s.mu.Unlock()
		var document struct{ Topic string }
		_ = json.Unmarshal(body, &document)
		switch document.Topic {
		case "refused-topic":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(refused)
		case "moved-topic":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "slow-topic":
			<-r.Context().Done()
		default:
			_, _ = w.Write(published)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) posted() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]request{}, s.requests...)
}

// fixture is one recorded ntfy answer.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil || !json.Valid(data) {
		t.Fatalf("testdata/%s: %v", name, err)
	}
	return data
}

func TestContract(t *testing.T) {
	ntfytest.Contract(t, ntfytest.Scenario{Accepting: "https://placeholder/private-topic-0123",
		Refusing: "https://placeholder/refused-topic", Publication: publication},
		func(t *testing.T, _ ntfytest.Scenario) (ntfy.Publisher, func() [][]byte) {
			s := newServer(t)
			return &rewriting{ntfy.New(s.Client()), strings.TrimPrefix(s.URL, "https://")}, func() [][]byte {
				var bodies [][]byte
				for _, posted := range s.posted() {
					if bytes.Contains(posted.body, []byte(`"topic":"private-topic-0123"`)) {
						bodies = append(bodies, posted.body)
					}
				}
				return bodies
			}
		})
}

// rewriting sends the scenario's addresses to the test server, whose port is known only once it listens.
type rewriting struct {
	ntfy.Publisher
	host string
}

func (r *rewriting) Publish(ctx context.Context, address string, publication notify.Publication) error {
	return r.Publisher.Publish(ctx, strings.Replace(address, "//placeholder/", "//"+r.host+"/", 1), publication)
}

// A publication is a JSON POST of the document, naming the topic, to the root of the address's server; a trailing
// slash does not change the topic.
func TestPublicationIsPostedToTheServerRoot(t *testing.T) {
	s := newServer(t)
	for _, address := range []string{s.URL + "/private-topic-0123", s.URL + "/private-topic-0123/"} {
		if err := ntfy.New(s.Client()).Publish(context.Background(), address, publication); err != nil {
			t.Fatal(err)
		}
	}
	want, err := publication.Document("private-topic-0123")
	if err != nil {
		t.Fatal(err)
	}
	for _, posted := range s.posted() {
		if posted.method != http.MethodPost || posted.path != "/" || posted.contentType != "application/json" ||
			!bytes.Equal(posted.body, want) {
			t.Fatalf("%s %s %q %s", posted.method, posted.path, posted.contentType, posted.body)
		}
	}
}

// A redirect is the server's answer, not a place to send the publication; a refusal, a server that cannot be
// reached and one that does not answer in time are failures. None names the address, its topic or the answer.
func TestFailuresNameNothingTheyWereSent(t *testing.T) {
	s := newServer(t)
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	closed.Close()
	deadline, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	for _, tt := range []struct {
		ctx     context.Context
		address string
		want    string
	}{
		{context.Background(), s.URL + "/moved-topic", "failed: publish: status_302"},
		{context.Background(), s.URL + "/refused-topic", "failed: publish: status_403"},
		{context.Background(), closed.URL + "/private-topic-0123", "failed: publish: unreachable"},
		{deadline, s.URL + "/slow-topic", "failed: publish: timed_out"},
	} {
		err := ntfy.New(s.Client()).Publish(tt.ctx, tt.address, publication)
		if err == nil || err.Error() != tt.want {
			t.Fatalf("%s: %v, want %s", tt.address, err, tt.want)
		}
	}
	for _, posted := range s.posted() {
		if posted.path != "/" {
			t.Fatalf("a redirect was followed to %s", posted.path)
		}
	}
}
