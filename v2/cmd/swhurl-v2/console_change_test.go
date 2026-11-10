package main

import (
	"net/http"
	"strings"
	"testing"
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
