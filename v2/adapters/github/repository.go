package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The repository write port: create one public repository, write its first commit and keep its webhooks. It is a
// separate interface from Publisher because its token is a different, wider one — it may create repositories — so a
// use case that only asks for a pull request cannot reach it. There is no delete and no call on an existing
// repository's files: the first commit replaces the tree of a repository the caller just created.
//
// Tokens are explicit per call and never retained. A call names its refusal with the operation and GitHub's own
// message, so an operator is told what GitHub said rather than what v2 assumed.

// ErrNoMainBranch is a repository GitHub has not finished making: its main branch does not exist yet.
var ErrNoMainBranch = errors.New("GitHub: no main branch yet")

// NewFile is one file a first commit writes: its repository path, whether it is executable, and its bytes.
type NewFile struct {
	Path       string
	Executable bool
	Body       []byte
}

// Hook is one repository webhook as GitHub holds it.
type Hook struct {
	ID     int
	Active bool
	Events []string
	URL    string
}

// RepositoryHook is the webhook a repository is given: where it posts, what signs it, and which events it takes.
type RepositoryHook struct {
	URL    string
	Secret string
	Events []string
	Active bool
}

// RepositoryWriter creates one public repository and keeps its webhooks.
type RepositoryWriter interface {
	// Exists asks whether the repository is there at all.
	Exists(ctx context.Context, repository, token string) (bool, error)
	// Create makes the public repository, with one initial commit, and returns its page.
	Create(ctx context.Context, token, owner, name, description string) (string, error)
	// Head is the commit main names, so the first commit has a parent. ErrNoMainBranch means GitHub has not
	// finished creating the repository.
	Head(ctx context.Context, repository, token string) (string, error)
	// FirstCommit replaces the initial commit's tree with every file and moves main to the new commit. It is only
	// for a repository the caller just created.
	FirstCommit(ctx context.Context, repository, token, parent, message string, files []NewFile) (string, error)
	// Hooks lists the repository's webhooks.
	Hooks(ctx context.Context, repository, token string) ([]Hook, error)
	// CreateHook adds one webhook.
	CreateHook(ctx context.Context, repository, token string, hook RepositoryHook) error
	// UpdateHook changes the webhook with this id.
	UpdateHook(ctx context.Context, repository, token string, id int, hook RepositoryHook) error
}

var _ RepositoryWriter = (*repositoryAPI)(nil)

// repositoryAPI is the repository write port's client. It refuses every redirect, as the publisher's does.
type repositoryAPI struct{ client *http.Client }

// NewRepositoryWriter is the repository write client: the same fixed origin, and no redirect at all.
func NewRepositoryWriter(client *http.Client) RepositoryWriter {
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &repositoryAPI{client: &bounded}
}

// call performs one request and returns the answer's body and status. A transport failure is the only error it
// returns: a refusal is the caller's to interpret, because some statuses are answers (404: no such repository).
func (a *repositoryAPI) call(ctx context.Context, method, path, token string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, &Error{"invalid request"}
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "https://api.github.com"+path, reader)
	if err != nil {
		return nil, 0, &Error{"invalid request"}
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "swhurl-console")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(request)
	if err != nil {
		return nil, 0, &Error{"request failed"}
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponse+1))
	if err != nil {
		return nil, response.StatusCode, &Error{"response unreadable"}
	}
	if len(answer) > maxJSONResponse {
		return nil, response.StatusCode, &Error{path + ": response too large"}
	}
	return answer, response.StatusCode, nil
}

// answer performs one request and decodes a 2xx answer into target, or names the refusal.
func (a *repositoryAPI) answer(ctx context.Context, method, path, token string, body, target any) error {
	payload, status, err := a.call(ctx, method, path, token, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &Error{refusalOf(method, path, status, payload)}
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return &Error{path + ": response malformed"}
	}
	return nil
}

func (a *repositoryAPI) Exists(ctx context.Context, repository, token string) (bool, error) {
	if err := validate(repository, token); err != nil {
		return false, err
	}
	path := "/repos/" + repository
	payload, status, err := a.call(ctx, http.MethodGet, path, token, nil)
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusNotFound:
		return false, nil
	case status < 200 || status >= 300:
		return false, &Error{refusalOf(http.MethodGet, path, status, payload)}
	}
	return true, nil
}

func (a *repositoryAPI) Create(ctx context.Context, token, owner, name, description string) (string, error) {
	if err := validate(owner+"/"+name, token); err != nil {
		return "", err
	}
	if len(description) > maxDescription {
		return "", &Error{"invalid description: too long"}
	}
	var created struct {
		URL string `json:"html_url"`
	}
	body := struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Private     bool   `json:"private"`
		AutoInit    bool   `json:"auto_init"`
		Wiki        bool   `json:"has_wiki"`
		Projects    bool   `json:"has_projects"`
	}{Name: name, Description: description, AutoInit: true}
	if err := a.answer(ctx, http.MethodPost, "/user/repos", token, body, &created); err != nil {
		return "", err
	}
	if created.URL == "" {
		return "", &Error{"/user/repos: the answer named no repository"}
	}
	return created.URL, nil
}

// maxDescription bounds a repository description, which GitHub shows beside the name.
const maxDescription = 350

func (a *repositoryAPI) Head(ctx context.Context, repository, token string) (string, error) {
	if err := validate(repository, token); err != nil {
		return "", err
	}
	path := "/repos/" + repository + "/git/ref/heads/main"
	payload, status, err := a.call(ctx, http.MethodGet, path, token, nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound || status == http.StatusConflict {
		return "", ErrNoMainBranch
	}
	var found struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if status < 200 || status >= 300 {
		return "", &Error{refusalOf(http.MethodGet, path, status, payload)}
	}
	if err := json.Unmarshal(payload, &found); err != nil || !commitID(found.Object.SHA) {
		return "", &Error{path + ": main did not name a commit"}
	}
	return found.Object.SHA, nil
}

func (a *repositoryAPI) FirstCommit(ctx context.Context, repository, token, parent, message string,
	files []NewFile) (string, error) {
	if err := validate(repository, token); err != nil {
		return "", err
	}
	if !commitID(parent) || strings.TrimSpace(message) == "" {
		return "", &Error{"invalid commit: it names a parent and a message"}
	}
	entries, err := a.fileEntries(ctx, repository, token, files)
	if err != nil {
		return "", err
	}
	tree, err := a.firstTree(ctx, repository, token, entries)
	if err != nil {
		return "", err
	}
	commit, err := a.commit(ctx, repository, token, message, tree, parent)
	if err != nil {
		return "", err
	}
	ref := "/repos/" + repository + "/git/refs/heads/main"
	body := struct {
		SHA string `json:"sha"`
	}{SHA: commit}
	if err := a.answer(ctx, http.MethodPatch, ref, token, body, nil); err != nil {
		return "", err
	}
	return commit, nil
}

// fileEntries stores every file's bytes and returns the tree's entries, in path order.
func (a *repositoryAPI) fileEntries(ctx context.Context, repository, token string, files []NewFile) ([]TreeEntry, error) {
	if len(files) == 0 || len(files) > maxTreeEntries {
		return nil, &Error{"invalid commit: it states no file or too many"}
	}
	entries := make([]TreeEntry, 0, len(files))
	for _, file := range files {
		if !plainPath(file.Path) {
			return nil, &Error{"invalid commit: every file names a path"}
		}
		blob, err := a.blob(ctx, repository, token, file.Body)
		if err != nil {
			return nil, err
		}
		mode := "100644"
		if file.Executable {
			mode = "100755"
		}
		entries = append(entries, TreeEntry{Path: file.Path, Mode: mode, SHA: blob})
	}
	return entries, nil
}

// blob stores one file's bytes and returns their identity.
func (a *repositoryAPI) blob(ctx context.Context, repository, token string, content []byte) (string, error) {
	body := struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}{Content: base64.StdEncoding.EncodeToString(content), Encoding: "base64"}
	var created struct {
		SHA string `json:"sha"`
	}
	if err := a.answer(ctx, http.MethodPost, "/repos/"+repository+"/git/blobs", token, body, &created); err != nil {
		return "", err
	}
	if !commitID(created.SHA) {
		return "", &Error{"git/blobs: the blob did not name a SHA"}
	}
	return created.SHA, nil
}

// firstTree writes a tree over no base: the initial commit's README is replaced, not kept.
func (a *repositoryAPI) firstTree(ctx context.Context, repository, token string, entries []TreeEntry) (string, error) {
	stated, err := wireEntries(entries)
	if err != nil {
		return "", err
	}
	var created struct {
		SHA string `json:"sha"`
	}
	body := struct {
		Entry []wireEntry `json:"tree"`
	}{Entry: stated}
	if err := a.answer(ctx, http.MethodPost, "/repos/"+repository+"/git/trees", token, body, &created); err != nil {
		return "", err
	}
	if !commitID(created.SHA) {
		return "", &Error{"git/trees: the tree did not name a SHA"}
	}
	return created.SHA, nil
}

// commit makes one commit with one parent.
func (a *repositoryAPI) commit(ctx context.Context, repository, token, message, tree, parent string) (string, error) {
	var created struct {
		SHA string `json:"sha"`
	}
	body := struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}{Message: message, Tree: tree, Parents: []string{parent}}
	if err := a.answer(ctx, http.MethodPost, "/repos/"+repository+"/git/commits", token, body, &created); err != nil {
		return "", err
	}
	if !commitID(created.SHA) {
		return "", &Error{"git/commits: the commit did not name a SHA"}
	}
	return created.SHA, nil
}

// plainPath is a repository path a commit may state: no separator tricks and nothing absolute.
func plainPath(path string) bool {
	return path != "" && !strings.HasPrefix(path, "/") && !strings.Contains(path, "..") &&
		!strings.ContainsAny(path, "\\\x00")
}

func (a *repositoryAPI) Hooks(ctx context.Context, repository, token string) ([]Hook, error) {
	if err := validate(repository, token); err != nil {
		return nil, err
	}
	var listed []struct {
		ID     int      `json:"id"`
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	path := "/repos/" + repository + "/hooks?per_page=100"
	if err := a.answer(ctx, http.MethodGet, path, token, nil, &listed); err != nil {
		return nil, err
	}
	hooks := make([]Hook, 0, len(listed))
	for _, found := range listed {
		hooks = append(hooks, Hook{ID: found.ID, Active: found.Active, Events: found.Events, URL: found.Config.URL})
	}
	return hooks, nil
}

func (a *repositoryAPI) CreateHook(ctx context.Context, repository, token string, hook RepositoryHook) error {
	return a.hookCall(ctx, http.MethodPost, "/repos/"+repository+"/hooks", repository, token, hook)
}

func (a *repositoryAPI) UpdateHook(ctx context.Context, repository, token string, id int, hook RepositoryHook) error {
	if id <= 0 {
		return &Error{"invalid hook: no id"}
	}
	return a.hookCall(ctx, http.MethodPatch, "/repos/"+repository+"/hooks/"+strconv.Itoa(id), repository, token, hook)
}

// hookAddress is the URL a webhook may post to: https, an absolute host and no user information.
func hookAddress(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// hookCall creates or changes one webhook. Only creation names the hook's type.
func (a *repositoryAPI) hookCall(ctx context.Context, method, path, repository, token string, hook RepositoryHook) error {
	if err := validate(repository, token); err != nil {
		return err
	}
	if !hookAddress(hook.URL) || len(hook.Events) == 0 {
		return &Error{"invalid hook: it names no URL or no event"}
	}
	body := struct {
		Name   string   `json:"name,omitempty"`
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
			Secret      string `json:"secret"`
			Insecure    string `json:"insecure_ssl"`
		} `json:"config"`
	}{Active: hook.Active, Events: hook.Events}
	if method == http.MethodPost {
		body.Name = "web"
	}
	body.Config.URL, body.Config.ContentType, body.Config.Secret, body.Config.Insecure = hook.URL, "json", hook.Secret, "0"
	return a.answer(ctx, method, path, token, body, nil)
}
