package github_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRealContract(t *testing.T) {
	githubtest.Contract(t, func(t *testing.T) github.Reader {
		hooks, repository := fixture(t, "hooks"), fixture(t, "repository")
		return github.New(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.URL.Host != "api.github.com" || r.URL.Scheme != "https" {
				t.Fatal("not a fixed-origin read")
			}
			body, status, header := repository, 200, http.Header{}
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			switch {
			case token == "rejected":
				status, body = 401, "untrusted response"
			case strings.Contains(r.URL.Path, "/hooks"):
				body = hooks
				if r.URL.Path == "/repos/owner/empty/hooks" {
					body = "[]"
				}
			case token == "expired":
				header.Set("github-authentication-token-expiration", "2026-11-10 00:00:00 UTC")
			}
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
	})
}

func scripted(body string, status int, header http.Header) github.Reader {
	return github.New(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
}

func TestFailuresNeverExposeCredentialURLOrBody(t *testing.T) {
	secret := "fixture-private-token"
	for name, reader := range map[string]github.Reader{
		"denial": scripted(secret, 403, nil), "server": scripted(secret, 503, nil),
		"malformed": scripted(secret, 200, nil), "null": scripted("null", 200, nil),
		"oversize":  scripted(strings.Repeat(secret, 200000), 200, nil),
		"transport": github.New(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return nil, errors.New(secret) })}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := reader.Hooks(context.Background(), "owner/repo", secret)
			assertSafeError(t, err, secret)
			result, err := reader.Token(context.Background(), "owner/repo", secret)
			if name == "denial" || name == "server" {
				if err != nil || result.Status == 200 {
					t.Fatalf("%+v %v", result, err)
				}
			} else {
				assertSafeError(t, err, secret)
			}
		})
	}
	for _, body := range []string{`{}`, `[{"active":true}]`, `[{"active":true,"events":[],"config":{"url":"x"},"last_response":{"code":"bad"}}]`} {
		_, err := scripted(body, 200, nil).Hooks(context.Background(), "owner/repo", secret)
		assertSafeError(t, err, secret)
	}
}

func assertSafeError(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "https://") {
		t.Fatalf("unsafe or missing error: %v", err)
	}
}

func TestPagination(t *testing.T) {
	for _, link := range []string{"<https://api.github.com/repos/owner/repo/hooks?per_page=100&page=2>; rel=\"next\"",
		"<https://evil.example/private>; rel=\"next\"", "<https://api.github.com/repos/other/repo/hooks?per_page=100&page=2>; rel=\"next\"",
		"<https://api.github.com/repos/owner/repo/hooks?per_page=100&page=2>; rel=next",
		"<https://api.github.com/repos/owner/repo/hooks?per_page=100&page=2>; rel=\"next\"; type=\"application/json\""} {
		calls := 0
		reader := github.New(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			calls++
			header, body := http.Header{}, "[]"
			if calls == 1 {
				header.Set("Link", link)
			} else {
				body = fixture(t, "hooks")
			}
			if r.URL.Query().Get("page") != fmt.Sprint(calls) {
				t.Fatal("wrong page")
			}
			return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
		hooks, err := reader.Hooks(context.Background(), "owner/repo", "accepted")
		if strings.Contains(link, "/repos/owner/repo/") && strings.HasSuffix(link, `; rel="next"`) {
			if err != nil || len(hooks) != 1 || calls != 2 {
				t.Fatalf("pagination: %v %d %v", err, calls, hooks)
			}
		} else if err == nil || calls != 1 {
			t.Fatal("unsafe pagination followed")
		}
	}
}

func TestPaginationBoundAndFullPages(t *testing.T) {
	one := strings.TrimSpace(fixture(t, "hooks"))
	hook := one[1 : len(one)-1]
	full := "[" + strings.TrimSuffix(strings.Repeat(hook+",", 100), ",") + "]"
	calls := 0
	reader := github.New(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(full))}, nil
	})})
	hooks, err := reader.Hooks(context.Background(), "owner/repo", "accepted")
	if err == nil || hooks != nil || calls != 100 {
		t.Fatalf("bound: %d %v", calls, err)
	}
}

func TestRedirectDoesNotForwardAuthorization(t *testing.T) {
	networkCalls, requests := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { networkCalls++ }))
	defer server.Close()
	reader := github.New(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "api.github.com" {
			t.Error("redirect request left GitHub origin")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{server.URL + "/private"}},
			Body: io.NopCloser(strings.NewReader(""))}, nil
	})})
	_, err := reader.Hooks(context.Background(), "owner/repo", "private-token")
	if err == nil || requests != 1 || networkCalls != 0 {
		t.Fatalf("redirect followed: requests=%d network=%d error=%v", requests, networkCalls, err)
	}
}

func TestDeliveryMessageCannotEchoSecrets(t *testing.T) {
	body := `[{"active":true,"events":[],"config":{"url":"https://hooks.example/private"},"last_response":{"code":500,"message":"https://hooks.example/private fixture-token"}}]`
	hooks, err := scripted(body, 200, nil).Hooks(context.Background(), "owner/repo", "fixture-token")
	if err != nil || len(hooks) != 1 || hooks[0].Message != "" {
		t.Fatalf("untrusted message retained: %+v %v", hooks, err)
	}
}

func TestHostParsingRejectsImpostorsAndCredentials(t *testing.T) {
	for _, value := range []string{"https://hooks.example.evil/hook/private", "https://evil/hooks.example/hook/private",
		"https://hooks.example@evil/hook/private", "https://evil/?next=https://hooks.example/", "https://%zz/private"} {
		body := fmt.Sprintf(`[{"active":true,"events":[],"config":{"url":%q}}]`, value)
		hooks, err := scripted(body, 200, nil).Hooks(context.Background(), "owner/repo", "fixture-token")
		if err != nil || len(hooks) != 1 || hooks[0].Host == "hooks.example" {
			t.Fatalf("impostor matched: %+v %v", hooks, err)
		}
	}
}

type unreadable struct{}

func (unreadable) Read([]byte) (int, error) {
	return 0, errors.New("fixture-token https://private/path")
}
func (unreadable) Close() error { return nil }

func TestUnreadableResponseAndMalformedExpiry(t *testing.T) {
	reader := github.New(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: unreadable{}}, nil
	})})
	_, err := reader.Token(context.Background(), "owner/repo", "fixture-token")
	assertSafeError(t, err, "fixture-token")
	_, err = reader.Hooks(context.Background(), "owner/repo", "fixture-token")
	assertSafeError(t, err, "fixture-token")
	reader = scripted(fixture(t, "repository"), 200, http.Header{"Github-Authentication-Token-Expiration": []string{"bad-expiry"}})
	fact, err := reader.Token(context.Background(), "owner/repo", "fixture-token")
	if err != nil || fact.Status != 200 || fact.Expiry != "bad-expiry" {
		t.Fatalf("%+v %v", fact, err)
	}
}
