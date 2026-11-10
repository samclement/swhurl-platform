package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Console is the read-only GitHub port the console's pages need: one repository at a pinned commit, the paths a
// comparison changed, and the pull requests the console opened. Tokens are explicit per call, never retained.
type Console interface {
	// Revision is the full commit id of the repository's main branch.
	Revision(ctx context.Context, repository, token string) (string, error)
	// Contents is one file at one commit; found is false when the commit has no such file.
	Contents(ctx context.Context, repository, token, revision, path string) (body []byte, found bool, err error)
	// Tarball is the repository at one commit, as GitHub serves it: a tar archive inside a leading folder.
	Tarball(ctx context.Context, repository, token, revision string) ([]byte, error)
	// CommitFiles is the regular files among paths at one commit, read from the commit's complete tree listing and then
	// each blob once, at most four at a time. A path the complete tree does not hold as a regular file (none, a link
	// or a submodule) is absent from the answer; a truncated or malformed listing and any failed read is an error,
	// never absence, and no file is answered then.
	CommitFiles(ctx context.Context, repository, token, revision string, paths []string) (map[string][]byte, error)
	// AppFile is one file at another repository's default branch: an app's swhurl.yaml, which the console reads
	// without a token because app repositories are public. found is false when the repository has no such file.
	AppFile(ctx context.Context, repository, token, path string) ([]byte, bool, error)
	// PublicFile is one file at one commit of a public repository, read without a token: a stack template's
	// copier.yml at the revision the contract pins. found is false when the commit has no such file.
	PublicFile(ctx context.Context, repository, revision, path string) ([]byte, bool, error)
	// Compare reports whether to contains from and, when it does, the paths changed between them. A comparison
	// GitHub truncated is contained but has no path list.
	Compare(ctx context.Context, repository, token, from, to string) (paths []string, contained bool, err error)
	// OpenPulls lists the open console pull requests, newest first, each with how its Validate workflow went.
	OpenPulls(ctx context.Context, repository, token string) ([]Pull, error)
	// Pull is one pull request, however it ended.
	Pull(ctx context.Context, repository, token string, number int) (Pull, error)
}

var _ Console = (*API)(nil)

// Pull is one pull request as the console shows it.
type Pull struct {
	Number      int
	Title       string
	URL         string
	Branch      string
	Opened      string // the date it was opened, YYYY-MM-DD
	Body        string
	Head        string // its head commit
	Checks      string // passed, failed, running, pending, conflict, or empty when not read
	ChecksURL   string
	State       string // open, closed or merged, for a pull request read by number
	MergedAt    string
	ClosedAt    string
	MergeCommit string
}

// The largest answers the console needs: a directory listing, a workflow listing or a tarball.
const (
	maxJSONResponse   = 4 << 20
	maxTarball        = 64 << 20
	comparisonLimit   = 300 // GitHub lists this many files in one answer
	branchPrefix      = "console/"
	checksWorkflow    = "Validate"
	openPullsPageSize = 50
)

// consoleClient is the API's client with redirects only where GitHub sends a tarball: an authorized request keeps
// its token on api.github.com and loses it, deliberately, on a redirect to another host.
func consoleClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	bounded := *base
	bounded.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || next.URL.Scheme != "https" {
			return http.ErrUseLastResponse
		}
		if next.URL.Host != via[0].URL.Host {
			next.Header.Del("Authorization")
		}
		return nil
	}
	return &bounded
}

// consoleAPI is the client the console's reads use.
type consoleAPI struct{ client *http.Client }

func (a *consoleAPI) get(ctx context.Context, repository, path, token, accept string, limit int) ([]byte, int, error) {
	if err := validate(repository, token); err != nil {
		return nil, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+path, nil)
	if err != nil {
		return nil, 0, &Error{"invalid request"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", accept)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "swhurl-console")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, 0, &Error{"request failed"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, 0, &Error{"response unreadable"}
	}
	if len(body) > limit {
		return nil, response.StatusCode, &Error{"response too large"}
	}
	return body, response.StatusCode, nil
}

// json reads one answer and decodes it into target, naming only the status a refusal can show.
func (a *consoleAPI) json(ctx context.Context, repository, path, token string, target any) error {
	body, status, err := a.get(ctx, repository, path, token, "application/vnd.github+json", maxJSONResponse)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return &Error{"HTTP 404"}
	}
	if status >= 300 {
		return &Error{"HTTP " + strconv.Itoa(status)}
	}
	if err := json.Unmarshal(body, target); err != nil {
		return &Error{"response malformed"}
	}
	return nil
}

// commitID is the only revision these reads name, exactly as the console pins it.
func commitID(revision string) bool {
	if len(revision) != 40 {
		return false
	}
	for _, c := range revision {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func (a *API) Revision(ctx context.Context, repository, token string) (string, error) {
	api := &consoleAPI{a.client}
	var found struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := api.json(ctx, repository, "/git/ref/heads/main", token, &found); err != nil {
		return "", err
	}
	if !commitID(found.Object.SHA) {
		return "", &Error{"main did not name a commit"}
	}
	return found.Object.SHA, nil
}

// contentsPath escapes each path segment, so a name from a record cannot reshape the request.
func contentsPath(path, revision string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/contents/" + strings.Join(segments, "/") + "?ref=" + url.QueryEscape(revision)
}

func (a *API) Contents(ctx context.Context, repository, token, revision, path string) ([]byte, bool, error) {
	if !commitID(revision) || path == "" {
		return nil, false, &Error{"invalid contents reference"}
	}
	api := &consoleAPI{a.client}
	body, status, err := api.get(ctx, repository, contentsPath(path, revision), token, "application/vnd.github.raw+json", maxJSONResponse)
	if err != nil {
		return nil, false, err
	}
	switch {
	case status == http.StatusNotFound:
		return nil, false, nil
	case status >= 300:
		return nil, false, &Error{"HTTP " + strconv.Itoa(status)}
	}
	return body, true, nil
}

// appFilePath escapes each segment of a path in another repository, so a name from a record cannot reshape the
// request.
func appFilePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func (a *API) AppFile(ctx context.Context, repository, token, file string) ([]byte, bool, error) {
	if !observe.GitHubRepository(repository) || file == "" || strings.Contains(file, "..") {
		return nil, false, &Error{"invalid app file reference"}
	}
	body, status, err := a.getAppFile(ctx, repository, token, file)
	if err != nil {
		return nil, false, err
	}
	if status == http.StatusNotFound {
		return nil, false, nil
	}
	if status >= 300 {
		return nil, false, &Error{"HTTP " + strconv.Itoa(status)}
	}
	return body, true, nil
}

// PublicFile reads one file at a commit of a public repository: a template's copier.yml is the platform's input,
// and the template is public, so this read carries no credential at all.
func (a *API) PublicFile(ctx context.Context, repository, revision, file string) ([]byte, bool, error) {
	if !observe.GitHubRepository(repository) || !commitID(revision) || file == "" || strings.Contains(file, "..") {
		return nil, false, &Error{"invalid public file reference"}
	}
	address := "https://api.github.com/repos/" + repository + "/contents/" + appFilePath(file) + "?ref=" + url.QueryEscape(revision)
	body, status, err := a.getPublic(ctx, address)
	if err != nil {
		return nil, false, err
	}
	switch {
	case status == http.StatusNotFound:
		return nil, false, nil
	case status < 200 || status >= 300:
		return nil, false, &Error{"HTTP " + strconv.Itoa(status)}
	}
	return body, true, nil
}

// getPublic is one anonymous read: the template repositories are public, so no token is sent or needed.
func (a *API) getPublic(ctx context.Context, address string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, 0, &Error{"invalid request"}
	}
	request.Header.Set("Accept", "application/vnd.github.raw+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "swhurl-console")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, 0, &Error{"request failed"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponse+1))
	if err != nil {
		return nil, response.StatusCode, &Error{"response unreadable"}
	}
	if len(body) > maxJSONResponse {
		return nil, response.StatusCode, &Error{"response too large"}
	}
	return body, response.StatusCode, nil
}

// getAppFile is one file at another repository's default branch, without a token: app repositories are public.
func (a *API) getAppFile(ctx context.Context, repository, token, file string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repository+"/contents/"+appFilePath(file), nil)
	if err != nil {
		return nil, 0, &Error{"invalid request"}
	}
	if strings.TrimSpace(token) != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Accept", "application/vnd.github.raw+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "swhurl-console")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, 0, &Error{"request failed"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponse+1))
	if err != nil {
		return nil, response.StatusCode, &Error{"response unreadable"}
	}
	if len(body) > maxJSONResponse {
		return nil, response.StatusCode, &Error{"response too large"}
	}
	return body, response.StatusCode, nil
}

func (a *API) Tarball(ctx context.Context, repository, token, revision string) ([]byte, error) {
	if !commitID(revision) {
		return nil, &Error{"invalid tarball reference"}
	}
	api := &consoleAPI{a.client}
	body, status, err := api.get(ctx, repository, "/tarball/"+revision, token, "application/vnd.github+json", maxTarball)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, &Error{"HTTP " + strconv.Itoa(status)}
	}
	return body, nil
}

// comparison is GitHub's compare answer, with only the fields the console reads.
type comparison struct {
	Status string `json:"status"`
	Files  []struct {
		Filename         string `json:"filename"`
		PreviousFilename string `json:"previous_filename"`
	} `json:"files"`
}

func (a *API) Compare(ctx context.Context, repository, token, from, to string) ([]string, bool, error) {
	if !commitID(from) || !commitID(to) {
		return nil, false, &Error{"invalid comparison"}
	}
	api := &consoleAPI{a.client}
	var found comparison
	if err := api.json(ctx, repository, "/compare/"+from+"..."+to, token, &found); err != nil {
		return nil, false, err
	}
	if found.Status != "ahead" && found.Status != "identical" {
		return nil, false, nil
	}
	if len(found.Files) >= comparisonLimit {
		return nil, true, nil
	}
	paths := []string{}
	for _, file := range found.Files {
		if file.Filename != "" {
			paths = append(paths, file.Filename)
		}
		if file.PreviousFilename != "" {
			paths = append(paths, file.PreviousFilename)
		}
	}
	return paths, true, nil
}

// wirePull is the pull request GitHub returns, with only the fields the console reads.
type wirePull struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	HTMLURL  string `json:"html_url"`
	Body     string `json:"body"`
	State    string `json:"state"`
	Created  string `json:"created_at"`
	Merged   string `json:"merged_at"`
	Closed   string `json:"closed_at"`
	MergeSHA string `json:"merge_commit_sha"`
	MergedSt string `json:"mergeable_state"`
	Head     struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

func pullOf(wire wirePull, checks, checksURL string) Pull {
	return Pull{Number: wire.Number, Title: wire.Title, URL: wire.HTMLURL, Branch: wire.Head.Ref,
		Opened: openedDate(wire.Created), Body: wire.Body, Head: wire.Head.SHA, Checks: checks,
		ChecksURL: checksURL, State: wire.State, MergedAt: wire.Merged, ClosedAt: wire.Closed,
		MergeCommit: wire.MergeSHA}
}

// openedDate is the day a pull request was opened, which is all a list row shows.
func openedDate(created string) string {
	if len(created) < 10 {
		return ""
	}
	return created[:10]
}

// checks reads how Validate went on one head commit, and whether GitHub says the pull request conflicts. A read
// that fails leaves both empty: the row says "Checks on GitHub" rather than claiming a verdict.
func (a *consoleAPI) checks(ctx context.Context, repository, token string, pull wirePull) (string, string) {
	checksURL := pull.HTMLURL + "/checks"
	if pull.Head.SHA == "" {
		return "", checksURL
	}
	var runs struct {
		Runs []struct {
			Name       string `json:"name"`
			Head       string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := a.json(ctx, repository, "/actions/runs?head_sha="+url.QueryEscape(pull.Head.SHA)+
		"&event=pull_request&per_page=30", token, &runs); err != nil {
		return "", ""
	}
	checks := "pending"
	for _, run := range runs.Runs {
		if run.Name != checksWorkflow || run.Head != pull.Head.SHA {
			continue
		}
		if run.HTMLURL != "" {
			checksURL = run.HTMLURL
		}
		checks = verdict(run.Status, run.Conclusion)
		break
	}
	var state struct {
		Mergeable string `json:"mergeable_state"`
	}
	if err := a.json(ctx, repository, "/pulls/"+strconv.Itoa(pull.Number), token, &state); err == nil && state.Mergeable == "dirty" {
		checks = "conflict"
	}
	return checks, checksURL
}

// verdict is one workflow run as the word a row shows.
func verdict(status, conclusion string) string {
	switch {
	case conclusion == "success":
		return "passed"
	case conclusion == "failure" || conclusion == "cancelled" || conclusion == "timed_out" ||
		conclusion == "action_required":
		return "failed"
	case status == "in_progress":
		return "running"
	}
	return "pending"
}

func (a *API) OpenPulls(ctx context.Context, repository, token string) ([]Pull, error) {
	api := &consoleAPI{a.client}
	var found []wirePull
	if err := api.json(ctx, repository, "/pulls?state=open&sort=created&direction=desc&per_page="+strconv.Itoa(openPullsPageSize), token, &found); err != nil {
		return nil, err
	}
	pulls := []Pull{}
	for _, wire := range found {
		if !strings.HasPrefix(wire.Head.Ref, branchPrefix) {
			continue
		}
		checks, checksURL := api.checks(ctx, repository, token, wire)
		pulls = append(pulls, pullOf(wire, checks, checksURL))
	}
	return pulls, nil
}

func (a *API) Pull(ctx context.Context, repository, token string, number int) (Pull, error) {
	if number <= 0 {
		return Pull{}, &Error{"invalid pull request"}
	}
	api := &consoleAPI{a.client}
	var wire wirePull
	if err := api.json(ctx, repository, "/pulls/"+strconv.Itoa(number), token, &wire); err != nil {
		return Pull{}, err
	}
	found := pullOf(wire, "", "")
	if wire.Merged != "" {
		found.State = "merged"
	} else if wire.State == "closed" {
		found.State = "closed"
	} else {
		found.State = "open"
	}
	return found, nil
}

// NewConsole is the console's read client: the same fixed origin, with the token kept to api.github.com.
func NewConsole(client *http.Client) Console {
	return &API{client: consoleClient(client)}
}
