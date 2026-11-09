package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// The console's write port: one commit on one new branch, and the pull request that asks for it. It is a separate
// interface from Console so that a use case which only reads cannot reach it, and it writes no ref but a branch
// under console/.
//
// Tokens are explicit per call and never retained, as the read port's are. A call names its refusal with the
// operation and GitHub's own message, so an operator is told what GitHub said rather than what v2 assumed.

// TreeEntry is one path a commit states: a file it writes with the blob that holds it, or — with no SHA — one it
// deletes. Mode is "100644" or "100755": a written file's mode comes from the tree it was taken from.
type TreeEntry struct {
	Path string
	Mode string
	SHA  string
}

// Signature is the name and address a commit is authored by.
type Signature struct {
	Name  string
	Email string
}

// NewCommit is one commit: its message, the tree it writes, its one parent and its author.
type NewCommit struct {
	Message string
	Tree    string
	Parent  string
	Author  Signature
}

// NewPull is the pull request that asks for a commit.
type NewPull struct {
	Title string
	Head  string
	Base  string
	Body  string
}

// Publisher is the console's GitHub write port.
type Publisher interface {
	// Blob stores one file's bytes and returns their identity.
	Blob(ctx context.Context, repository, token string, content []byte) (string, error)
	// BaseTree is the tree one commit names: what a new tree is written over.
	BaseTree(ctx context.Context, repository, token, commit string) (string, error)
	// Tree writes entries over a base tree and returns the new tree's identity.
	Tree(ctx context.Context, repository, token, base string, entries []TreeEntry) (string, error)
	// Commit creates one commit and returns its identity.
	Commit(ctx context.Context, repository, token string, commit NewCommit) (string, error)
	// Branch creates one reference, and refuses any name outside console/: nothing else is the console's to write.
	Branch(ctx context.Context, repository, token, name, commit string) error
	// OpenPull opens the pull request and returns it as the console shows it.
	OpenPull(ctx context.Context, repository, token string, pull NewPull) (Pull, error)
}

var _ Publisher = (*writeAPI)(nil)

// writeAPI is the write port's client. It refuses every redirect: a write is addressed at api.github.com and a
// redirect would carry the token somewhere this port did not name.
type writeAPI struct{ client *http.Client }

// NewPublisher is the console's write client: the same fixed origin, and no redirect at all.
func NewPublisher(client *http.Client) Publisher {
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &writeAPI{client: &bounded}
}

// read fetches one object of the write port's own inputs.
func (a *writeAPI) read(ctx context.Context, repository, path, token string, target any) error {
	if err := validate(repository, token); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+path, nil)
	if err != nil {
		return &Error{"invalid request"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "swhurl-console")
	response, err := a.client.Do(request)
	if err != nil {
		return &Error{"request failed"}
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponse+1))
	if err != nil {
		return &Error{"response unreadable"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &Error{refusalOf("GET", path, response.StatusCode, answer)}
	}
	if err := json.Unmarshal(answer, target); err != nil {
		return &Error{path + ": response malformed"}
	}
	return nil
}

// post sends one request and decodes its answer into target.
func (a *writeAPI) post(ctx context.Context, repository, path, token string, body, target any) error {
	if err := validate(repository, token); err != nil {
		return err
	}
	answer, failure := a.exchange(ctx, repository, path, token, body)
	if failure != nil {
		return failure
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(answer, target); err != nil {
		return &Error{path + ": response malformed"}
	}
	return nil
}

// exchange performs one write and returns the answer's body, or a fixed failure.
func (a *writeAPI) exchange(ctx context.Context, repository, path, token string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{"invalid request"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.github.com/repos/"+repository+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, &Error{"invalid request"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "swhurl-console")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, &Error{"request failed"}
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponse+1))
	if err != nil {
		return nil, &Error{"response unreadable"}
	}
	if len(answer) > maxJSONResponse {
		return nil, &Error{path + ": response too large"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &Error{refusalOf("POST", path, response.StatusCode, answer)}
	}
	return answer, nil
}

// refusalOf names a refused write: the operation, GitHub's status, and its own message when it sent one. The message
// is bounded and kept to one line, because it is shown to the operator.
func refusalOf(method, path string, status int, answer []byte) string {
	reason := method + " " + path + ": HTTP " + strconv.Itoa(status)
	var sent struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(answer, &sent) == nil && sent.Message != "" {
		message := strings.Join(strings.Fields(sent.Message), " ")
		if len(message) > maxRefusalMessage {
			message = message[:maxRefusalMessage] + "…"
		}
		reason += ": " + message
	}
	return reason
}

// maxRefusalMessage bounds what GitHub's message may add to a refusal.
const maxRefusalMessage = 200

// maxTreeEntries bounds one commit: a change carries the files its kind derives, far fewer than this.
const maxTreeEntries = 4000

func (a *writeAPI) BaseTree(ctx context.Context, repository, token, commit string) (string, error) {
	if !commitID(commit) {
		return "", &Error{"invalid commit reference"}
	}
	var found struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := a.read(ctx, repository, "/git/commits/"+commit, token, &found); err != nil {
		return "", err
	}
	if !commitID(found.Tree.SHA) {
		return "", &Error{"/git/commits: the commit named no tree"}
	}
	return found.Tree.SHA, nil
}

func (a *writeAPI) Blob(ctx context.Context, repository, token string, content []byte) (string, error) {
	var created struct {
		SHA string `json:"sha"`
	}
	body := struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}{Content: base64.StdEncoding.EncodeToString(content), Encoding: "base64"}
	if err := a.post(ctx, repository, "/git/blobs", token, body, &created); err != nil {
		return "", err
	}
	if !commitID(created.SHA) {
		return "", &Error{"/git/blobs: the blob did not name a SHA"}
	}
	return created.SHA, nil
}

func (a *writeAPI) Tree(ctx context.Context, repository, token, base string, entries []TreeEntry) (string, error) {
	if !commitID(base) {
		return "", &Error{"invalid tree reference"}
	}
	stated, err := wireEntries(entries)
	if err != nil {
		return "", err
	}
	var created struct {
		SHA string `json:"sha"`
	}
	body := struct {
		Base  string      `json:"base_tree"`
		Entry []wireEntry `json:"tree"`
	}{Base: base, Entry: stated}
	if err := a.post(ctx, repository, "/git/trees", token, body, &created); err != nil {
		return "", err
	}
	if !commitID(created.SHA) {
		return "", &Error{"/git/trees: the tree did not name a SHA"}
	}
	return created.SHA, nil
}

// wireEntry is one path a tree states, as GitHub reads it.
type wireEntry struct {
	Path string  `json:"path"`
	Mode string  `json:"mode"`
	Type string  `json:"type"`
	SHA  *string `json:"sha"`
}

// wireEntries checks every entry before anything is sent: each names a path and one of the two modes a repository
// holds, and a written one names a blob.
func wireEntries(entries []TreeEntry) ([]wireEntry, error) {
	if len(entries) == 0 || len(entries) > maxTreeEntries {
		return nil, &Error{"invalid tree: it states no entry or too many"}
	}
	stated := make([]wireEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Path == "" || (entry.Mode != "100644" && entry.Mode != "100755") {
			return nil, &Error{"invalid tree: every entry names a path and a mode"}
		}
		one := wireEntry{Path: entry.Path, Mode: entry.Mode, Type: "blob"}
		if entry.SHA != "" {
			if !commitID(entry.SHA) {
				return nil, &Error{"invalid tree: an entry's SHA is not a blob"}
			}
			sha := entry.SHA
			one.SHA = &sha
		}
		stated = append(stated, one)
	}
	return stated, nil
}

func (a *writeAPI) Commit(ctx context.Context, repository, token string, commit NewCommit) (string, error) {
	if !commitID(commit.Tree) || !commitID(commit.Parent) || commit.Message == "" {
		return "", &Error{"invalid commit: it names a tree, one parent and a message"}
	}
	var created struct {
		SHA string `json:"sha"`
	}
	body := struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"author"`
	}{Message: commit.Message, Tree: commit.Tree, Parents: []string{commit.Parent}}
	body.Author.Name, body.Author.Email = commit.Author.Name, commit.Author.Email
	if err := a.post(ctx, repository, "/git/commits", token, body, &created); err != nil {
		return "", err
	}
	if !commitID(created.SHA) {
		return "", &Error{"/git/commits: the commit did not name a SHA"}
	}
	return created.SHA, nil
}

func (a *writeAPI) Branch(ctx context.Context, repository, token, name, commit string) error {
	if !strings.HasPrefix(name, branchPrefix) || len(name) == len(branchPrefix) {
		return &Error{"refusing to push " + strconv.Quote(name) + ": the console pushes only its own branches"}
	}
	if !commitID(commit) {
		return &Error{"invalid branch: it names no commit"}
	}
	body := struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}{Ref: "refs/heads/" + name, SHA: commit}
	return a.post(ctx, repository, "/git/refs", token, body, nil)
}

func (a *writeAPI) OpenPull(ctx context.Context, repository, token string, pull NewPull) (Pull, error) {
	if pull.Title == "" || pull.Head == "" || pull.Base == "" {
		return Pull{}, &Error{"invalid pull request: it names a title, a head and a base"}
	}
	var wire wirePull
	body := struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}{Title: pull.Title, Head: pull.Head, Base: pull.Base, Body: pull.Body}
	if err := a.post(ctx, repository, "/pulls", token, body, &wire); err != nil {
		return Pull{}, err
	}
	if wire.HTMLURL == "" || wire.Number <= 0 {
		return Pull{}, &Error{"/pulls: the answer named no pull request"}
	}
	return pullOf(wire, "", ""), nil
}
