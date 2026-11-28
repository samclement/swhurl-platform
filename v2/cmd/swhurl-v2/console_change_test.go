package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	healthrules "github.com/samclement/swhurl-platform/v2/health/rules"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// TestAppFormsAnswerTheirRefusals keeps the first answer each app form gives: the change it does not know, the token it
// does not have, and a page that cannot be posted to at all.
func TestAppFormsAnswerTheirRefusals(t *testing.T) {
	server := actionConsole(t, nil)
	handler := server.identityMiddleware(server.routes())
	cases := []struct {
		name, path, want string
		status           int
	}{
		{name: "no such change", path: "/apps/demo/staging/wibble", status: http.StatusNotFound,
			want: "No such app instance or change."},
		{name: "egress is no form", path: "/apps/demo/staging/egress", status: http.StatusConflict,
			want: "No GitHub token is configured"},
		{name: "a schedule is not modelled", path: "/apps/demo/staging/schedule", status: http.StatusConflict,
			want: "No GitHub token is configured"},
		{name: "no token", path: "/apps/demo/prod/scale", status: http.StatusConflict,
			want: "No GitHub token is configured"},
		{name: "a page cannot be posted to", path: "/platform", status: http.StatusMethodNotAllowed, want: ""},
		{name: "noSuchInstance", path: "/apps/demo/nope/flags/f", status: http.StatusNotFound, want: "No such app instance."},
		{name: "the catalogue needs an app", path: "/catalogue/staging/use", status: http.StatusBadRequest,
			want: "Choose the app that will use it."},
		{name: "a team needs its token", path: "/teams/add", status: http.StatusConflict,
			want: "No GitHub token is configured"},
		{name: "so does a channel", path: "/team-channel/set", status: http.StatusConflict,
			want: "No GitHub token is configured"},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			response := post(handler, one.path, nil)
			if response.Code != one.status {
				t.Fatalf("%s answered %d, want %d", one.path, response.Code, one.status)
			}
			if one.want != "" && !strings.Contains(response.Body.String(), one.want) {
				t.Fatalf("%s answered %q, want %q", one.path, response.Body.String(), one.want)
			}
		})
	}
}

// TestJobResumeIsGuarded keeps the resume form honest: a job that is gone, still running, or that never kept a new
// app's choices is not one to finish, and the console says so rather than creating a second repository.
func TestJobResumeIsGuarded(t *testing.T) {
	fake := jobsFake(map[string]string{
		"job-9": `{"id":9,"action":"new app and repository","unit":"demo/staging","identity":"sam@swhurl.com",` +
			`"started":"2026-11-09T10:01:00Z","finished":null,"state":"running","link":"","owner":"console-gone",` +
			`"resume":{"kind":"new-app","name":"demo"},"reached":"","lines":[]}`,
		"job-8": `{"id":8,"action":"a scale","unit":"demo/staging","identity":"sam@swhurl.com",` +
			`"started":"2026-11-09T10:00:00Z","finished":"2026-11-09T10:00:20Z","state":"failed","link":"",` +
			`"owner":"console-new","resume":null,"reached":"","lines":[]}`,
	})
	server, err := newConsoleServer(consoleConfig{Dev: true, Data: fake, Reader: fake, Owner: "console-new",
		Now: func() time.Time { return time.Date(2026, 11, 9, 10, 2, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.identityMiddleware(server.routes())
	// Job 9 is a new-app job whose pod is gone, so it is the one job worth finishing; without tokens it is refused.
	if response := post(handler, "/jobs/9/resume", nil); response.Code != http.StatusConflict {
		t.Fatalf("/jobs/9/resume answered %d, want 409", response.Code)
	}
	for _, path := range []string{"/jobs/8/resume", "/jobs/1/resume", "/jobs/not-a-number/resume"} {
		response := post(handler, path, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404", path, response.Code)
		}
	}
}

// TestTheCatalogueUseStartsTheConnection keeps the catalogue's Use a connection: the form is read once, so the chosen
// app and what it connects to reach the change together, and the job starts rather than a refusal of an empty form.
func TestTheCatalogueUseStartsTheConnection(t *testing.T) {
	files := map[string]string{}
	for _, path := range []string{"v2/testdata/profile.json", "v2-records/calls.json", "v2-records/teams.json",
		"platform/egress/allocation.json"} {
		files[path] = repositoryFile(t, path)
	}
	for _, app := range []string{"demo-orders", "demo-notifier"} {
		for _, path := range []string{"app.json", "staging/instance.json", "staging/release.json"} {
			files["v2-records/"+app+"/"+path] = repositoryFile(t, "v2-records/"+app+"/"+path)
		}
	}
	fake := jobsFake(nil)
	fake.Collections = map[observe.Ref][][]byte{healthrules.PlatformUnits: {}}
	server, err := newConsoleServer(consoleConfig{Dev: true, Data: fake, Reader: emptyCluster{fake}, Writer: fake,
		Collector: fake,
		Owner:     "console-new", Snapshots: &checkoutSnapshots{head: mainCommit,
			archives: map[string][]byte{mainCommit: tarOf(files, "")}},
		Now: func() time.Time { return time.Date(2026, 11, 9, 10, 2, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	server.hasGitHub = true
	handler := server.identityMiddleware(server.routes())
	request := httptest.NewRequest(http.MethodPost, "/catalogue/staging/use",
		strings.NewReader("calls=demo-orders&app=demo-notifier"))
	request.Host = "console.example.com"
	request.Header.Set("Origin", "https://console.example.com")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/jobs/1" {
		t.Fatalf("the catalogue answered %d %q %q, want 303 /jobs/1", response.Code, response.Header().Get("Location"),
			response.Body.String())
	}
	// The console has no repository here, so the command refuses after the change's flags were accepted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, found := server.jobs.Get(context.Background(), 1)
		if found && !job.Running() {
			if job.Action != "v2-connect" || job.Unit != "demo-notifier/staging" {
				t.Fatalf("the job is %s on %s, want v2-connect on demo-notifier/staging", job.Action, job.Unit)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the job never ended")
		}
		time.Sleep(time.Millisecond)
	}
}

// repositoryFile is one file of this checkout, read as the pinned commit would hold it.
func repositoryFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// emptyCluster is a cluster where nothing is deployed yet: every list is present and empty.
type emptyCluster struct{ *kubetest.Fake }

func (emptyCluster) List(_ context.Context, ref observe.Ref, selector string) (observe.List, error) {
	return observe.List{Ref: ref, Selector: selector, Status: observe.Present}, nil
}

func (emptyCluster) ListAllNamespaces(_ context.Context, ref observe.Ref, selector string) (observe.List, error) {
	return observe.List{Ref: ref, Selector: selector, Status: observe.Present}, nil
}
