package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/adapters/s3/s3test"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// recorded is the recorded listing answer from testdata.
func recorded(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "list-objects.xml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// listedObjects is what the recorded answer holds, as the adapter reads it.
func listedObjects(t *testing.T) []observe.Archive {
	t.Helper()
	archive, err := time.Parse(time.RFC3339, "2026-10-26T08:21:21Z")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := time.Parse(time.RFC3339, "2026-10-26T08:21:22Z")
	if err != nil {
		t.Fatal(err)
	}
	return []observe.Archive{
		{Key: "app-sqlite/hello-staging/sqlite-20261026T082103Z.db.age", LastModified: archive},
		{Key: "app-sqlite/hello-staging/sqlite-20261026T082103Z.json", LastModified: metadata},
	}
}

// answers is the contract's scenario over the recorded answer: one prefix with objects, one the server
// refuses, and one the adapter itself refuses.
func answers(t *testing.T) s3test.Scenario {
	t.Helper()
	return s3test.Scenario{
		Query: observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694",
			Prefix: "app-sqlite/hello-staging/"},
		Objects: listedObjects(t),
		Forbidden: observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694",
			Prefix: "app-sqlite/refused/"},
		Empty:   observe.ArchiveQuery{},
		Key:     "app-sqlite/hello-staging/sqlite-20261026T082103Z.json",
		Body:    `{"sha256":"` + strings.Repeat("a", 64) + `","tables":2,"source":"hello-staging"}`,
		Missing: "app-sqlite/hello-staging/sqlite-missing.db.age.json",
	}
}

// server answers a listing, a refusal and whatever else a test states, and records what it was asked.
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
		if r.URL.Query().Get("list-type") == "" { // one object, not a listing
			switch {
			case strings.HasSuffix(r.URL.Path, "/sqlite-20261026T082103Z.json"):
				write(t, w, []byte(`{"sha256":"`+strings.Repeat("a", 64)+`","tables":2,"source":"hello-staging"}`))
			case strings.HasSuffix(r.URL.Path, "/sqlite-missing.db.age.json"):
				w.WriteHeader(http.StatusNotFound)
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
			return
		}
		switch r.URL.Query().Get("prefix") {
		case "app-sqlite/hello-staging/":
			write(t, w, recorded(t))
		case "app-sqlite/refused/":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(instance.Close)
	return instance
}

// rerouted sends every request to the test server, whatever host the adapter signed for: the server does not
// verify a signature, and its certificate names the test address rather than the bucket.
type rerouted struct {
	inner  http.RoundTripper
	target *url.URL
}

func (r rerouted) RoundTrip(request *http.Request) (*http.Response, error) {
	copied := request.Clone(request.Context())
	copied.URL.Scheme, copied.URL.Host = r.target.Scheme, r.target.Host
	return r.inner.RoundTrip(copied)
}

// client is the test server's client, sending bucket-hosted requests to it.
func client(t *testing.T, instance *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(instance.URL)
	if err != nil {
		t.Fatal(err)
	}
	trusting := instance.Client()
	trusting.Transport = rerouted{inner: trusting.Transport, target: target}
	trusting.Timeout = 5 * time.Second
	return trusting
}

// reader is the real adapter over a test server.
func reader(t *testing.T, instance *httptest.Server) s3.Reader {
	t.Helper()
	return s3.New("eu-west-2", s3.Credentials{AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "secret"}, client(t, instance))
}

// The real adapter runs the shared contract over the recorded answer.
func TestContract(t *testing.T) {
	s3test.Contract(t, answers(t), func(t *testing.T, _ s3test.Scenario) s3.Reader {
		return reader(t, (&server{}).serve(t))
	})
}

// A listing follows its pages, and every request is signed for the stated region and service.
func TestListFollowsPages(t *testing.T) {
	answering := &server{answer: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("continuation-token") {
		case "":
			write(t, w, listing([]object{{Key: "p/a.db.age", LastModified: "2026-10-26T08:00:00Z"}}, "next"))
		case "next":
			if authorization := r.Header.Get("Authorization"); !strings.Contains(authorization,
				"Credential=AKIAIOSFODNN7EXAMPLE/") || !strings.Contains(authorization, "/eu-west-2/s3/aws4_request") {
				t.Errorf("authorization %q", authorization)
			}
			write(t, w, listing([]object{{Key: "p/b.db.age", LastModified: "2026-10-26T09:00:00Z"}}, ""))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}}
	instance := answering.serve(t)
	listed, err := reader(t, instance).List(context.Background(),
		observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: "p/"})
	if err != nil || listed.Status != observe.Present || len(listed.Objects) != 2 {
		t.Fatalf("listing: %+v %v", listed, err)
	}
	if len(answering.requests) != 2 || answering.requests[0].URL.Query().Get("list-type") != "2" ||
		answering.requests[1].URL.Query().Get("continuation-token") != "next" {
		t.Fatalf("%d requests: %v", len(answering.requests), answering.requests)
	}
}

// A status the server answers with is evidence: only 200 is a listing, and 404 is not an empty prefix.
func TestListFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		reason string
	}{
		{"forbidden", http.StatusForbidden, observe.ReasonForbidden},
		{"missing", http.StatusNotFound, observe.ReasonNotFound},
		{"status", http.StatusInternalServerError, observe.ReasonStatus},
		{"limited", http.StatusTooManyRequests, observe.ReasonStatus},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instance := (&server{answer: func(t *testing.T, w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}}).serve(t)
			listed, err := reader(t, instance).List(context.Background(),
				observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: "p/"})
			if err != nil || listed.Status != observe.Unavailable || listed.Reason != tt.reason ||
				len(listed.Objects) != 0 {
				t.Fatalf("status %d: %+v %v", tt.status, listed, err)
			}
		})
	}
	for _, tt := range []struct {
		name string
		body []byte
	}{
		{"malformed", []byte("not xml")},
		{"oversized", []byte(strings.Repeat(" ", 2<<20))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instance := (&server{answer: func(t *testing.T, w http.ResponseWriter, _ *http.Request) {
				write(t, w, tt.body)
			}}).serve(t)
			listed, err := reader(t, instance).List(context.Background(),
				observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: "p/"})
			want := observe.ReasonMalformed
			if tt.name == "oversized" {
				want = observe.ReasonTooLarge
			}
			if err != nil || listed.Status != observe.Unavailable || listed.Reason != want {
				t.Fatalf("%s: %+v %v", tt.name, listed, err)
			}
		})
	}
}

// A server that does not answer in time is a timed-out read, not an empty prefix.
func TestListTimesOut(t *testing.T) {
	instance := (&server{answer: func(t *testing.T, _ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			// The client has already timed out. Keep the handler from writing an empty 200 that could race
			// the timeout error the adapter is about to classify.
			time.Sleep(200 * time.Millisecond)
		case <-time.After(2 * time.Second):
		}
	}}).serve(t)
	expiring := client(t, instance)
	expiring.Timeout = 100 * time.Millisecond
	listed, err := s3.New("eu-west-2", s3.Credentials{AccessKey: "k", SecretKey: "s"}, expiring).
		List(context.Background(), observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: "p/"})
	if err != nil || listed.Status != observe.Unavailable || listed.Reason != observe.ReasonTimedOut {
		t.Fatalf("timeout: %+v %v", listed, err)
	}
}

// The region comes from the bucket itself, so the profile keeps no region field.
func TestDiscoverRegion(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		header  string
		want    string
		wantErr bool
	}{
		{"redirected", http.StatusMovedPermanently, "eu-west-2", "eu-west-2", false},
		{"global", http.StatusOK, "", "us-east-1", false},
		{"failed", http.StatusInternalServerError, "", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instance := (&server{answer: func(t *testing.T, w http.ResponseWriter, _ *http.Request) {
				if tt.header != "" {
					w.Header().Set("x-amz-bucket-region", tt.header)
				}
				w.WriteHeader(tt.status)
			}}).serve(t)
			region, err := s3.DiscoverRegion(context.Background(), "swhurl-platform-backups-110927251694",
				client(t, instance))
			if (err != nil) != tt.wantErr || region != tt.want {
				t.Fatalf("region %q, %v", region, err)
			}
		})
	}
}

// The profile's URI is parsed into the query a listing takes, and a request the adapter cannot make is a
// refusal rather than a listing.
func TestParseURL(t *testing.T) {
	query, err := s3.ParseURL("s3://swhurl-platform-backups-110927251694/app-sqlite/")
	if err != nil || query.Bucket != "swhurl-platform-backups-110927251694" || query.Prefix != "app-sqlite/" {
		t.Fatalf("query %+v %v", query, err)
	}
	for _, uri := range []string{"", "https://example.com/x", "s3:///prefix"} {
		if _, err := s3.ParseURL(uri); err == nil {
			t.Fatalf("%q was accepted", uri)
		}
	}
	if _, err := s3.New("eu-west-2", s3.Credentials{}, http.DefaultClient).
		List(context.Background(), observe.ArchiveQuery{}); err == nil {
		t.Fatal("a listing without a bucket was answered")
	}
}

// A prefix with a space travels as one unambiguous, percent-encoded query, so the signature covers what the
// server reads.
func TestPrefixIsEncodedOnce(t *testing.T) {
	instance := (&server{answer: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("prefix"); got != "app-sqlite/hello staging/" {
			t.Errorf("prefix %q", got)
		}
		if raw := r.URL.RawQuery; !strings.Contains(raw, "prefix=app-sqlite%2Fhello+staging%2F") &&
			!strings.Contains(raw, "prefix=app-sqlite%2Fhello%20staging%2F") {
			t.Errorf("raw query %q", raw)
		}
		write(t, w, listing(nil, ""))
	}}).serve(t)
	if _, err := reader(t, instance).List(context.Background(),
		observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: "app-sqlite/hello staging/"}); err != nil {
		t.Fatal(err)
	}
}

// write answers with one XML body. A write error is ignored: the client may stop reading at its own bound,
// which is a case these tests exercise.
func write(t *testing.T, w http.ResponseWriter, body []byte) {
	t.Helper()
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write(body)
}

// object is one listed object in a test listing.
type object struct{ Key, LastModified string }

// listing is a ListBucketResult document with the objects a test states.
func listing(objects []object, token string) []byte {
	var out strings.Builder
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	if token != "" {
		out.WriteString("<IsTruncated>true</IsTruncated><NextContinuationToken>" + token + "</NextContinuationToken>")
	} else {
		out.WriteString("<IsTruncated>false</IsTruncated>")
	}
	for _, entry := range objects {
		out.WriteString("<Contents><Key>" + entry.Key + "</Key><LastModified>" + entry.LastModified +
			"</LastModified></Contents>")
	}
	out.WriteString("</ListBucketResult>")
	return []byte(out.String())
}

// A reader built without a region asks the bucket which one it is in, and a bucket that cannot name one is a
// failed read rather than an empty prefix.
func TestListDiscoversTheRegion(t *testing.T) {
	named := "eu-west-2"
	answering := &server{answer: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			if named != "" {
				w.Header().Set("x-amz-bucket-region", named)
			}
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		if r.URL.Query().Get("prefix") != "p/" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		write(t, w, listing(nil, ""))
	}}
	instance := answering.serve(t)
	query := observe.ArchiveQuery{Bucket: "swhurl-platform-backups-110927251694", Prefix: "p/"}
	if listed, err := s3.New("", s3.Credentials{AccessKey: "k", SecretKey: "s"}, client(t, instance)).
		List(context.Background(), query); err != nil || listed.Status != observe.Present {
		t.Fatalf("discovered listing: %+v %v", listed, err)
	}
	named = ""
	if listed, err := s3.New("", s3.Credentials{AccessKey: "k", SecretKey: "s"}, client(t, instance)).
		List(context.Background(), query); err != nil || listed.Status != observe.Unavailable ||
		listed.Reason != observe.ReasonStatus {
		t.Fatalf("a bucket without a region: %+v %v", listed, err)
	}
}
