package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// line is one JSON line of a log, decoded.
func line(t *testing.T, text string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &decoded); err != nil {
		t.Fatalf("not one JSON line: %v\n%s", err, text)
	}
	return decoded
}

// requestLogger is a logger over a buffer, with a fixed clock.
func requestLogger(out *bytes.Buffer, name string) *jsonLog {
	fixed := time.Date(2026, time.November, 14, 10, 0, 0, 0, time.UTC)
	return &jsonLog{out: out, now: func() time.Time { return fixed }, name: name}
}

// One line per answered request: the method, the route pattern the mux matched, the status and how long it took, and
// none of the path, query or body, where a token or a form value would be.
func TestARequestLineCarriesItsRouteAndStatusAndNothingElse(t *testing.T) {
	var out bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apps/{app}/{env}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /broken", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	})
	handler := logRequests(requestLogger(&out, "console"), mux)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/apps/hello/staging?token=s3cret", nil))
	got := line(t, out.String())
	if got["level"] != "INFO" || got["logger"] != "console" || got["message"] != requestLine {
		t.Fatalf("line %v", got)
	}
	if got["event"] != "request" || got["method"] != "GET" || got["route"] != "GET /apps/{app}/{env}" ||
		got["status"] != float64(200) || got["duration_ms"] == nil {
		t.Fatalf("fields %v", got)
	}
	if strings.Contains(out.String(), "s3cret") || strings.Contains(out.String(), "hello") ||
		strings.Contains(strings.ToLower(out.String()), "staging") {
		t.Fatalf("the line names what was asked for: %s", out.String())
	}

	out.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/broken", nil))
	if got := line(t, out.String()); got["level"] != "ERROR" || got["status"] != float64(500) {
		t.Fatalf("a 5xx is the service's failure: %v", got)
	}
}

// A request the mux answers itself, and one it never matched, are still one line and are named as such.
func TestAnUnmatchedRequestIsOneLine(t *testing.T) {
	var out bytes.Buffer
	handler := logRequests(requestLogger(&out, "console"), http.NewServeMux())
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/nope", nil))
	if got := line(t, out.String()); got["route"] != "unmatched" || got["method"] != "POST" || got["status"] != float64(404) {
		t.Fatalf("line %v", got)
	}
}

// Probes and assets write no line: they say nothing about the service and would bury the requests an operator made.
func TestProbesAndAssetsAreNotLogged(t *testing.T) {
	var out bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, _ *http.Request) {})
	handler := logRequests(requestLogger(&out, "console"), mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/static/app.css", nil))
	if out.Len() != 0 {
		t.Fatalf("logged %s", out.String())
	}
}

// A handler that streams still streams through the line's status recorder.
func TestAStreamFlushesThroughTheRecorder(t *testing.T) {
	var out bytes.Buffer
	mux := http.NewServeMux()
	flushed := false
	mux.HandleFunc("GET /jobs/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "cannot stream", http.StatusInternalServerError)
			return
		}
		flusher.Flush()
		flushed = true
	})
	handler := logRequests(requestLogger(&out, "console"), mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/jobs/1/events", nil))
	if !flushed {
		t.Fatal("the handler could not flush")
	}
	if got := line(t, out.String()); got["status"] != float64(200) {
		t.Fatalf("line %v", got)
	}
}

// The console logs every request it answers, including the one it refuses for want of an identity, and nothing for
// its health probe.
func TestTheConsoleLogsTheRequestsItAnswers(t *testing.T) {
	var out bytes.Buffer
	server, err := newConsoleServer(consoleConfig{Log: &out})
	if err != nil {
		t.Fatal(err)
	}
	handler := logRequests(server.requests, server.identityMiddleware(server.routes()))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/apps", nil))
	got := line(t, out.String())
	if got["status"] != float64(401) || got["logger"] != "console" {
		t.Fatalf("refused request %v", got)
	}
	out.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	if out.Len() != 0 {
		t.Fatalf("the probe was logged: %s", out.String())
	}

	// An answered request names the route the mux matched, which the middleware that attaches the identity must not
	// copy away first.
	overview, err := newConsoleServer(consoleConfig{Log: &out})
	if err != nil {
		t.Fatal(err)
	}
	overview.identity = devIdentity
	handler = logRequests(overview.requests, overview.identityMiddleware(overview.routes()))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/services", nil))
	if got := line(t, out.String()); got["route"] != "GET /services" || got["status"] != float64(301) {
		t.Fatalf("answered request %v", got)
	}
}

// The notify service logs its own requests the same way.
func TestNotifyLogsItsRequests(t *testing.T) {
	h := newNotifyHarness(t, defaultScenario())
	if status, _ := h.post(t, "/v1/notify", "app-token", notifyBody); status != http.StatusAccepted {
		t.Fatalf("status %d", status)
	}
	request := ""
	for _, text := range strings.Split(h.log.String(), "\n") {
		if strings.Contains(text, `"event":"request"`) {
			request = text
		}
	}
	got := line(t, request)
	if got["logger"] != "notify" || got["method"] != "POST" || got["route"] != "POST /v1/notify" ||
		got["status"] != float64(http.StatusAccepted) {
		t.Fatalf("line %v", got)
	}
}
