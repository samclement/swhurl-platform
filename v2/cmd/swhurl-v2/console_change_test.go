package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
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
