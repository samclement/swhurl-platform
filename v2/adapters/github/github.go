// Package github reads GitHub repository hooks and credential acceptance. It never writes GitHub state.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// Reader is the read-only GitHub port. Tokens are explicit per request, never retained as client state.
type Reader interface {
	Hooks(context.Context, string, string) ([]observe.Hook, error)
	Token(context.Context, string, string) (Acceptance, error)
}

// Acceptance is GitHub's repository response status and optional token expiration header.
type Acceptance struct {
	Status int
	Expiry string
}

// Error contains only a safe reason/status: never a token, URL, transport error or response body.
type Error struct{ Reason string }

func (e *Error) Error() string { return "GitHub: " + e.Reason }

// API is a bounded, read-only client for api.github.com.
type API struct{ client *http.Client }

var _ Reader = (*API)(nil)

const maxResponse = 2 << 20
const maxPages = 100

// New copies the supplied HTTP client (nil uses the default transport), bounds reads to ten seconds per
// request and refuses redirects. Production always uses https://api.github.com; tests script its transport.
func New(client *http.Client) Reader {
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = 10 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &API{client: &bounded}
}

func validate(repository, token string) error {
	if !observe.GitHubRepository(repository) {
		return &Error{"invalid repository"}
	}
	if strings.TrimSpace(token) == "" {
		return &Error{"token not supplied"}
	}
	return nil
}

// get never propagates the http package's errors, because they can carry credential-bearing URLs.
func (a *API) get(ctx context.Context, path, token string) ([]byte, http.Header, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return nil, nil, 0, &Error{"invalid request"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, nil, 0, &Error{"request failed"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, nil, 0, &Error{"unreadable response"}
	}
	if len(body) > maxResponse {
		return nil, nil, 0, &Error{"response too large"}
	}
	return body, response.Header, response.StatusCode, nil
}

// Hooks lists every page; reaching the bound or receiving an unexpected pagination link is an error,
// never a partial result that could conceal a later hook.
func (a *API) Hooks(ctx context.Context, repository, token string) ([]observe.Hook, error) {
	if err := validate(repository, token); err != nil {
		return nil, err
	}
	found := []observe.Hook{}
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/hooks?per_page=100&page=%d", repository, page)
		body, headers, status, err := a.get(ctx, path, token)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, &Error{"HTTP " + strconv.Itoa(status)}
		}
		hooks, err := decodeHooks(body)
		if err != nil {
			return nil, err
		}
		found = append(found, hooks...)
		next, err := nextPage(headers.Get("Link"), repository, page+1)
		if err != nil {
			return nil, err
		}
		if !next && len(hooks) < 100 {
			return found, nil
		}
	}
	return nil, &Error{"too many hook pages"}
}

func nextPage(link, repository string, next int) (bool, error) {
	found := false
	for _, part := range strings.Split(link, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		target, relation, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok {
			return false, &Error{"invalid pagination"}
		}
		isNext, err := paginationRelation(strings.TrimSpace(relation))
		if err != nil {
			return false, err
		}
		if !isNext {
			continue
		}
		expected := fmt.Sprintf("<https://api.github.com/repos/%s/hooks?per_page=100&page=%d>", repository, next)
		alternative := fmt.Sprintf("<https://api.github.com/repos/%s/hooks?page=%d&per_page=100>", repository, next)
		if target != expected && target != alternative || found {
			return false, &Error{"invalid pagination"}
		}
		found = true
	}
	return found, nil
}

// Unknown/malformed relation syntax cannot silently hide another page.
func paginationRelation(relation string) (bool, error) {
	switch relation {
	case `rel="next"`:
		return true, nil
	case `rel="prev"`, `rel="first"`, `rel="last"`:
		return false, nil
	default:
		return false, &Error{"invalid pagination"}
	}
}

type wireHook struct {
	Active *bool    `json:"active"`
	Events []string `json:"events"`
	Config *struct {
		URL string `json:"url"`
	} `json:"config"`
	Last *struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
	} `json:"last_response"`
}

func decodeHooks(body []byte) ([]observe.Hook, error) {
	var wire []wireHook
	if json.Unmarshal(body, &wire) != nil || wire == nil {
		return nil, &Error{"malformed hooks response"}
	}
	found := []observe.Hook{}
	for _, hook := range wire {
		if hook.Active == nil || hook.Config == nil || hook.Config.URL == "" || hook.Events == nil {
			return nil, &Error{"malformed hook"}
		}
		fact := observe.Hook{Active: *hook.Active, Events: hook.Events, Host: hookHost(hook.Config.URL)}
		fact.Code, fact.Message = deliveryResponse(hook)
		found = append(found, fact)
	}
	return found, nil
}

// Token asks whether this credential can read the installed platform repository. A non-200 response is
// returned as a status; transport, oversized or malformed successful responses are errors, never acceptance.
func (a *API) Token(ctx context.Context, repository, token string) (Acceptance, error) {
	if err := validate(repository, token); err != nil {
		return Acceptance{}, err
	}
	body, headers, status, err := a.get(ctx, "/repos/"+repository, token)
	if err != nil {
		return Acceptance{}, err
	}
	if status != 200 {
		return Acceptance{Status: status}, nil
	}
	var document struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(body, &document) != nil || document.ID <= 0 {
		return Acceptance{}, &Error{"malformed repository response"}
	}
	return Acceptance{Status: status, Expiry: headers.Get("github-authentication-token-expiration")}, nil
}

// SafeProblem keeps arbitrary errors supplied by another Reader from entering a report.
func SafeProblem(err error) string {
	if err == nil {
		return ""
	}
	var failure *Error
	if errors.As(err, &failure) {
		return failure.Error()
	}
	return "GitHub: request failed"
}

// hookHost discards userinfo, paths, queries and fragments at the integration boundary.
func hookHost(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return parsed.Host
}

func deliveryResponse(hook wireHook) (*int, string) {
	if hook.Last == nil {
		return nil, ""
	}
	message := ""
	// Only standard HTTP reason text may cross into the report: arbitrary delivery text can echo secrets.
	if hook.Last.Code != nil && hook.Last.Message == http.StatusText(*hook.Last.Code) {
		message = hook.Last.Message
	}
	return hook.Last.Code, message
}
