package github_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
)

// repositoryServer answers the repository write port and keeps every request, so a test can assert what was sent.
func repositoryServer(t *testing.T, scene githubtest.RepositoryFixture) (*httptest.Server, *[]writeRequest) {
	t.Helper()
	kept := &[]writeRequest{}
	record := func(w http.ResponseWriter, r *http.Request, status int, answer string) {
		body, _ := io.ReadAll(r.Body)
		sent := map[string]any{}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Errorf("%s %s sent no JSON: %q", r.Method, r.URL.Path, body)
			}
		}
		*kept = append(*kept, writeRequest{Method: r.Method, Path: r.URL.Path,
			Token: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), Body: sent})
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/samclement/notes", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusOK, `{"full_name":"samclement/notes"}`)
	})
	mux.HandleFunc("/repos/samclement/hello", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusNotFound, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusCreated, `{"html_url":"`+scene.CreatedURL+`"}`)
	})
	mux.HandleFunc("/repos/samclement/notes/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusOK, `{"object":{"sha":"`+scene.Head+`"}}`)
	})
	mux.HandleFunc("/repos/samclement/notes/git/refs/heads/main", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusOK, `{"ref":"refs/heads/main"}`)
	})
	mux.HandleFunc("/repos/samclement/notes/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusCreated, `{"sha":"`+scene.Blob+`"}`)
	})
	mux.HandleFunc("/repos/samclement/notes/git/trees", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusCreated, `{"sha":"`+scene.Tree+`"}`)
	})
	mux.HandleFunc("/repos/samclement/notes/git/commits", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusCreated, `{"sha":"`+scene.Commit+`"}`)
	})
	mux.HandleFunc("/repos/samclement/notes/hooks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			record(w, r, http.StatusOK, `[{"id":7,"active":true,"events":["package","registry_package"],`+
				`"config":{"url":"https://flux-webhook.example.test/hook/abc"}}]`)
			return
		}
		record(w, r, http.StatusCreated, `{"id":8}`)
	})
	mux.HandleFunc("/repos/samclement/notes/hooks/7", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusOK, `{"id":7}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, kept
}

func TestRepositoryContract(t *testing.T) {
	githubtest.RepositoryContract(t, func(t *testing.T) (github.RepositoryWriter, githubtest.RepositoryFixture) {
		scene := githubtest.RepositoryScenario()
		server, _ := repositoryServer(t, scene)
		return github.NewRepositoryWriter(&http.Client{Transport: consoleTransport(t, server)}), scene
	})
}

// What each write sends: a public repository with one initial commit, a first tree over no base, a commit whose one
// parent is main, a ref moved to it, and the image webhook's own body.
func TestRepositoryRequests(t *testing.T) {
	scene := githubtest.RepositoryScenario()
	server, kept := repositoryServer(t, scene)
	writer := github.NewRepositoryWriter(&http.Client{Transport: consoleTransport(t, server)})
	ctx := t.Context()
	if _, err := writer.Create(ctx, "token", "samclement", "notes", "A swhurl app"); err != nil {
		t.Fatal(err)
	}
	head, err := writer.Head(ctx, scene.Repository, "token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.FirstCommit(ctx, scene.Repository, "token", head, "Start notes\n",
		[]github.NewFile{{Path: "README.md", Body: []byte("notes\n")},
			{Path: "scripts/run.sh", Executable: true, Body: []byte("#!/bin/sh\n")}}); err != nil {
		t.Fatal(err)
	}
	hook := github.RepositoryHook{URL: "https://flux-webhook.example.test/hook/credential", Secret: "s3cret",
		Events: []string{"package", "registry_package"}, Active: true}
	if err := writer.CreateHook(ctx, scene.Repository, "token", hook); err != nil {
		t.Fatal(err)
	}
	if err := writer.UpdateHook(ctx, scene.Repository, "token", 7, hook); err != nil {
		t.Fatal(err)
	}
	requests := *kept
	created := requests[0].Body
	if created["name"] != "notes" || created["private"] != false || created["auto_init"] != true {
		t.Fatalf("create sent %#v", created)
	}
	blobs := bodiesOf(requests, "/git/blobs")
	if len(blobs) != 2 || blobs[0]["encoding"] != "base64" ||
		blobs[0]["content"] != base64.StdEncoding.EncodeToString([]byte("notes\n")) {
		t.Fatalf("blobs sent %#v", blobs)
	}
	trees := bodiesOf(requests, "/git/trees")
	if len(trees) != 1 || trees[0]["base_tree"] != nil {
		t.Fatalf("first tree sent %#v (it must replace the initial README)", trees)
	}
	entries := trees[0]["tree"].([]any)
	modes := map[string]string{}
	for _, entry := range entries {
		one := entry.(map[string]any)
		modes[one["path"].(string)] = one["mode"].(string)
	}
	if modes["README.md"] != "100644" || modes["scripts/run.sh"] != "100755" {
		t.Fatalf("tree modes = %v", modes)
	}
	commits := bodiesOf(requests, "/git/commits")
	if len(commits) != 1 || commits[0]["parents"].([]any)[0] != scene.Head {
		t.Fatalf("commit sent %#v", commits)
	}
	if ref := requests[len(requests)-3]; ref.Method != http.MethodPatch || ref.Body["sha"] != scene.Commit {
		t.Fatalf("ref update = %#v", ref)
	}
	for _, request := range requests[len(requests)-2:] {
		sent := request.Body
		if request.Method == http.MethodPost && sent["name"] != "web" {
			t.Fatalf("create hook sent %#v", sent)
		}
		if _, named := sent["name"]; request.Method == http.MethodPatch && named {
			t.Fatalf("update hook sent create-only name: %#v", sent)
		}
		if sent["active"] != true {
			t.Fatalf("hook sent %#v", sent)
		}
		config := sent["config"].(map[string]any)
		if config["content_type"] != "json" || config["insecure_ssl"] != "0" || config["secret"] != "s3cret" {
			t.Fatalf("hook config = %#v", config)
		}
	}
	if requests[len(requests)-1].Method != http.MethodPatch {
		t.Fatalf("last request = %#v", requests[len(requests)-1])
	}
}

// bodiesOf is every request body the server received on a path, in order.
func bodiesOf(requests []writeRequest, suffix string) []map[string]any {
	found := []map[string]any{}
	for _, request := range requests {
		if strings.HasSuffix(request.Path, suffix) {
			found = append(found, request.Body)
		}
	}
	return found
}
