package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// actionConsole is a console that runs jobs over a record and a cluster it never really reaches: its unit actions are
// performed against a fake, which the root units are refused before touching.
func actionConsole(t *testing.T, records map[string]string) *consoleServer {
	t.Helper()
	fake := jobsFake(records)
	server, err := newConsoleServer(consoleConfig{Dev: true, Data: fake, Reader: fake, Writer: fake,
		Owner: "console-new",
		Now:   func() time.Time { return time.Date(2026, 11, 9, 10, 2, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// post is one form's request to the console, as a browser on the console's own page sends it.
func post(handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	request.Host = "console.example.com"
	request.Header.Set("Origin", "https://console.example.com")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// TestAUnitActionStartsAJob keeps the browser's answer as it always was: a plain form gets the job's page, and a button
// in a list gets the started fragment where it was, which re-reads that page shortly.
func TestAUnitActionStartsAJob(t *testing.T) {
	server := actionConsole(t, nil)
	handler := server.identityMiddleware(server.routes())
	plain := post(handler, "/units/platform-console/reconcile", nil)
	if plain.Code != http.StatusSeeOther || plain.Header().Get("Location") != "/jobs/1" {
		t.Fatalf("a plain form answered %d %q, want 303 /jobs/1", plain.Code, plain.Header().Get("Location"))
	}
	inPlace := post(handler, "/units/console-dashboards/suspend", map[string]string{
		"HX-Request": "true", "HX-Current-URL": "https://console.example.com/platform"})
	want := `<a class="started" href="/jobs/2" hx-get="/platform" hx-trigger="load delay:10s" ` +
		`hx-select="main" hx-target="main" hx-swap="outerHTML">` +
		`<span class="mark updating" aria-hidden="true"></span> suspend started · job #2</a>`
	if inPlace.Code != http.StatusOK || inPlace.Body.String() != want {
		t.Fatalf("a button answered %d %q, want %q", inPlace.Code, inPlace.Body.String(), want)
	}
}

// TestAUnitActionsRefusalIsTheJobsLine keeps the refusal where it always was: Flux's own words end the job, and the
// browser still gets the job to read.
func TestAUnitActionsRefusalIsTheJobsLine(t *testing.T) {
	server := actionConsole(t, nil)
	handler := server.identityMiddleware(server.routes())
	post(handler, "/units/cluster-stack/reconcile", nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, found := server.jobs.Get(context.Background(), 1)
		if found && !job.Running() {
			if job.State != "failed" {
				t.Fatalf("the job ended %s, want failed", job.State)
			}
			if last := job.Lines[len(job.Lines)-1]; !strings.Contains(last, "cluster-stack is applied by make flux-bootstrap") {
				t.Fatalf("the job's last line = %q", last)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the job never ended")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAChangeMustComeFromAConsolePage keeps another site's page from posting one: the Origin must be this console's
// host, and a request without one is refused too.
func TestAChangeMustComeFromAConsolePage(t *testing.T) {
	server := actionConsole(t, nil)
	handler := server.identityMiddleware(server.routes())
	for _, origin := range []string{"", "https://evil.example.com", "null"} {
		request := httptest.NewRequest(http.MethodPost, "/units/platform-console/reconcile", nil)
		request.Host = "console.example.com"
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("Origin %q answered %d, want 403", origin, response.Code)
		}
	}
	// A read needs no Origin: a bookmark, a link and a page's own refresh are all GETs.
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("a GET answered %d", response.Code)
	}
}

// TestAUnitPageResolvesItsName keeps the address a page is reached at: the name in the path is the unit the page reads,
// not a prefix match that answers "no such unit" for every unit.
func TestAUnitPageResolvesItsName(t *testing.T) {
	server := actionConsole(t, nil)
	mux := server.routes()
	request := httptest.NewRequest(http.MethodGet, "/units/platform-console", nil)
	_, pattern := mux.Handler(request)
	if pattern != "GET /units/{unit}" {
		t.Fatalf("the unit page is served by %q", pattern)
	}
}

// TestARootConsoleWithoutJobsRefusesAForm keeps --dev honest: a console with no record runs nothing and says why,
// rather than starting a job nothing would remember.
func TestARootConsoleWithoutJobsRefusesAForm(t *testing.T) {
	server, err := newConsoleServer(consoleConfig{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.identityMiddleware(server.routes())
	response := post(handler, "/units/platform-console/reconcile", nil)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "runs no jobs") {
		t.Fatalf("a form with no job registry answered %d %q", response.Code, response.Body.String())
	}
}
