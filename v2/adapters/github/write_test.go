package github_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
)

// writeRequest is one request the scripted server received.
type writeRequest struct {
	Method, Path, Token string
	Body                map[string]any
}

// writeServer answers the console's writes and keeps every request, so a test can assert what was sent.
func writeServer(t *testing.T, scene githubtest.PublisherFixture) (*httptest.Server, *[]writeRequest) {
	t.Helper()
	kept := &[]writeRequest{}
	record := func(w http.ResponseWriter, r *http.Request, answer string) {
		body, _ := io.ReadAll(r.Body)
		sent := map[string]any{}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Errorf("%s %s sent no JSON: %q", r.Method, r.URL.Path, body)
			}
		}
		*kept = append(*kept, writeRequest{Method: r.Method, Path: r.URL.Path,
			Token: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), Body: sent})
		io.WriteString(w, answer)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"sha":"`+scene.Blob+`"}`)
	})
	mux.HandleFunc("/repos/owner/repo/git/trees", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"sha":"`+scene.Tree+`"}`)
	})
	mux.HandleFunc("/repos/owner/repo/git/commits", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"sha":"`+scene.Commit+`"}`)
	})
	mux.HandleFunc("/repos/owner/repo/git/commits/", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"sha":"`+scene.Commit+`","tree":{"sha":"`+scene.Base+`"}}`)
	})
	mux.HandleFunc("/repos/owner/repo/git/refs", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"ref":"`+scene.Branch+`"}`)
	})
	mux.HandleFunc("/repos/owner/repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"number":`+strconv.Itoa(scene.PullNumber)+`,"html_url":"`+scene.PullURL+`","state":"open"}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, kept
}

func TestPublisherContract(t *testing.T) {
	githubtest.PublisherContract(t, func(t *testing.T) (github.Publisher, githubtest.PublisherFixture) {
		scene := githubtest.PublisherScenario()
		server, _ := writeServer(t, scene)
		return github.NewPublisher(&http.Client{Transport: consoleTransport(t, server)}), scene
	})
}

// What each write sends: one base64 blob, a tree that deletes by stating no SHA, a commit with one parent and the
// console's author, a ref under refs/heads, and a pull request naming its branch, base and body.
func TestPublisherRequests(t *testing.T) {
	scene := githubtest.PublisherScenario()
	server, kept := writeServer(t, scene)
	publisher := github.NewPublisher(&http.Client{Transport: consoleTransport(t, server)})
	ctx := t.Context()
	base, err := publisher.BaseTree(ctx, "owner/repo", "token", scene.Commit)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := publisher.Blob(ctx, "owner/repo", "token", []byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := publisher.Tree(ctx, "owner/repo", "token", base, []github.TreeEntry{
		{Path: "a.json", Mode: "100755", SHA: blob}, {Path: "old.json", Mode: "100644"}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := publisher.Commit(ctx, "owner/repo", "token", github.NewCommit{Message: "message", Tree: tree,
		Parent: scene.Commit, Author: github.Signature{Name: "swhurl console", Email: "console@users.noreply.github.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Branch(ctx, "owner/repo", "token", scene.Branch, commit); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.OpenPull(ctx, "owner/repo", "token", github.NewPull{
		Title: "[console] apps: scale web/staging", Head: scene.Branch, Base: "main", Body: "body\n"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/repos/owner/repo/git/commits/" + scene.Commit, "/repos/owner/repo/git/blobs",
		"/repos/owner/repo/git/trees", "/repos/owner/repo/git/commits", "/repos/owner/repo/git/refs",
		"/repos/owner/repo/pulls"}
	if len(*kept) != len(want) {
		t.Fatalf("%d requests, want %d: %#v", len(*kept), len(want), *kept)
	}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPost, http.MethodPost, http.MethodPost, http.MethodPost}
	for i, one := range *kept {
		if one.Path != want[i] || one.Method != methods[i] || one.Token != "token" {
			t.Errorf("request %d = %s %s with token %q", i, one.Method, one.Path, one.Token)
		}
	}
	kept = shift(kept)
	if got := (*kept)[0].Body["content"]; got != base64.StdEncoding.EncodeToString([]byte("hello\n")) {
		t.Errorf("blob content = %v", got)
	}
	if (*kept)[0].Body["encoding"] != "base64" {
		t.Errorf("blob encoding = %v", (*kept)[0].Body["encoding"])
	}
	entries, _ := (*kept)[1].Body["tree"].([]any)
	if len(entries) != 2 || (*kept)[1].Body["base_tree"] != scene.Base {
		t.Fatalf("tree body = %#v", (*kept)[1].Body)
	}
	written, deleted := entries[0].(map[string]any), entries[1].(map[string]any)
	if written["sha"] != scene.Blob || written["mode"] != "100755" || written["type"] != "blob" {
		t.Errorf("written entry = %#v", written)
	}
	if sha, stated := deleted["sha"]; stated && sha != nil {
		t.Errorf("a deletion stated a SHA: %#v", deleted)
	}
	if (*kept)[2].Body["message"] != "message" || (*kept)[2].Body["tree"] != scene.Tree {
		t.Errorf("commit body = %#v", (*kept)[2].Body)
	}
	parents, _ := (*kept)[2].Body["parents"].([]any)
	author, _ := (*kept)[2].Body["author"].(map[string]any)
	if len(parents) != 1 || parents[0] != scene.Commit || author["email"] != "console@users.noreply.github.com" {
		t.Errorf("commit parents/author = %#v %#v", parents, author)
	}
	if (*kept)[3].Body["ref"] != "refs/heads/"+scene.Branch || (*kept)[3].Body["sha"] != scene.Commit {
		t.Errorf("ref body = %#v", (*kept)[3].Body)
	}
	pull := (*kept)[4].Body
	if pull["head"] != scene.Branch || pull["base"] != "main" || pull["body"] != "body\n" {
		t.Errorf("pull body = %#v", pull)
	}
}

// A refused write names the operation, GitHub's status and its own message — the one thing an operator can act on —
// and it never echoes the token.
func TestPublisherRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("the token did not travel: %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		io.WriteString(w, `{"message":"Reference already exists"}`)
	}))
	t.Cleanup(server.Close)
	publisher := github.NewPublisher(&http.Client{Transport: consoleTransport(t, server)})
	err := publisher.Branch(t.Context(), "owner/repo", "secret-token", "console/v2-scale-web-staging-0123456",
		"3333333333333333333333333333333333333333")
	if err == nil || !strings.Contains(err.Error(), "POST /git/refs: HTTP 422: Reference already exists") {
		t.Fatalf("refusal = %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Error("a refusal echoed the token")
	}
}

// A write is never redirected: the answer is the refusal, so the token cannot travel somewhere the caller did not
// name.
func TestPublisherRefusesRedirects(t *testing.T) {
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		http.Redirect(w, r, "https://elsewhere.test/", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	publisher := github.NewPublisher(&http.Client{Transport: consoleTransport(t, server)})
	if _, err := publisher.Blob(t.Context(), "owner/repo", "token", []byte("x")); err == nil {
		t.Fatal("a redirected write was followed")
	}
	if asked != 1 {
		t.Errorf("%d requests: the redirect was followed", asked)
	}
}

// shift drops the first request, so the assertions below read the writes in order.
func shift(kept *[]writeRequest) *[]writeRequest {
	rest := (*kept)[1:]
	return &rest
}
