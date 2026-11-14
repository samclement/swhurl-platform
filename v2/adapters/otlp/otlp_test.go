package otlp_test

import (
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

	"github.com/samclement/swhurl-platform/v2/adapters/otlp"
	"github.com/samclement/swhurl-platform/v2/adapters/otlp/otlptest"
)

// collector is an OTLP/HTTP receiver answering from the recorded answers: 503 when it refuses, otherwise what its
// answers map holds for the path (the accepted answer by default).
type collector struct {
	*httptest.Server
	mu       sync.Mutex
	received []otlptest.Sent
	types    []string
}

func newCollector(t *testing.T, refusing bool, answers map[string]string) *collector {
	t.Helper()
	c := &collector{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.received = append(c.received, otlptest.Sent{Path: r.URL.Path, Document: body})
		c.types = append(c.types, r.Header.Get("Content-Type"))
		c.mu.Unlock()
		switch {
		case refusing:
			w.WriteHeader(http.StatusServiceUnavailable)
		case answers[r.URL.Path] == "redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case answers[r.URL.Path] == "slow":
			<-r.Context().Done()
		default:
			name := answers[r.URL.Path]
			if name == "" {
				name = "accepted.json"
			}
			_, _ = w.Write(fixture(t, name))
		}
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *collector) posted() ([]otlptest.Sent, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]otlptest.Sent{}, c.received...), append([]string{}, c.types...)
}

// fixture is one recorded collector answer.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil || !json.Valid(data) {
		t.Fatalf("testdata/%s: %v", name, err)
	}
	return data
}

func exporter(t *testing.T, endpoint string) *otlp.Collector {
	t.Helper()
	e, err := otlp.New(nil, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestContract(t *testing.T) {
	otlptest.Contract(t, func(t *testing.T, refusing bool) (otlp.Exporter, func() []otlptest.Sent) {
		c := newCollector(t, refusing, nil)
		return exporter(t, c.URL), func() []otlptest.Sent { sent, _ := c.posted(); return sent }
	})
}

// Every export is a JSON POST under the endpoint's own path, whether or not the endpoint ends in a slash.
func TestExportsArePostedAsJSONUnderTheEndpoint(t *testing.T) {
	c := newCollector(t, false, nil)
	source, spans, _ := otlptest.Scenario()
	for _, endpoint := range []string{c.URL + "/otlp", c.URL + "/otlp/"} {
		if err := exporter(t, endpoint).Traces(context.Background(), source, spans); err != nil {
			t.Fatal(err)
		}
	}
	sent, types := c.posted()
	for i := range sent {
		if sent[i].Path != "/otlp/v1/traces" || types[i] != "application/json" {
			t.Fatalf("%s %s", sent[i].Path, types[i])
		}
	}
}

// A partial success that rejected anything, a redirect, a collector that cannot be reached and one that does not
// answer in time are failures; none repeats the endpoint or the collector's message.
func TestFailuresNameNothingTheyWereSent(t *testing.T) {
	c := newCollector(t, false, map[string]string{"/rejecting/v1/traces": "rejected-spans.json",
		"/rejecting/v1/metrics": "rejected-points.json", "/moved/v1/traces": "redirect", "/slow/v1/traces": "slow"})
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	deadline, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	source, spans, metrics := otlptest.Scenario()
	for _, tt := range []struct {
		name string
		call func() error
		want string
	}{
		{"rejected spans", func() error { return exporter(t, c.URL+"/rejecting").Traces(context.Background(), source, spans) },
			"failed: traces: partly_rejected"},
		{"rejected points", func() error { return exporter(t, c.URL+"/rejecting").Metrics(context.Background(), source, metrics) },
			"failed: metrics: partly_rejected"},
		{"redirect", func() error { return exporter(t, c.URL+"/moved").Traces(context.Background(), source, spans) },
			"failed: traces: status_302"},
		{"unreachable", func() error { return exporter(t, closed.URL).Traces(context.Background(), source, spans) },
			"failed: traces: unreachable"},
		{"slow", func() error { return exporter(t, c.URL+"/slow").Traces(deadline, source, spans) },
			"failed: traces: timed_out"},
	} {
		err := tt.call()
		if err == nil || err.Error() != tt.want || strings.Contains(err.Error(), "invalid") {
			t.Fatalf("%s: %v, want %s", tt.name, err, tt.want)
		}
	}
	sent, _ := c.posted()
	for _, s := range sent {
		if s.Path == "/elsewhere" {
			t.Fatal("a redirect was followed")
		}
	}
}

// Only an http or https base URL with a host is an endpoint.
func TestEndpointsThatAreNotBaseURLsAreRefused(t *testing.T) {
	for _, endpoint := range []string{"", "localhost:4318", "ftp://collector:4318", "http://", "http://user:pass@collector:4318",
		"http://collector:4318?x=1", "http://collector:4318#x", "http://%zz"} {
		if _, err := otlp.New(nil, endpoint); err == nil || err.Error() != "refused_request: endpoint: not_an_http_base_url" {
			t.Fatalf("%q: %v", endpoint, err)
		}
	}
}
