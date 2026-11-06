// Package githubtest supplies read-only GitHub facts and the shared Reader contract.
package githubtest

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// Call records a read's operation, repository and explicit credential for boundary assertions.
type Call struct{ Operation, Repository, Token string }

// Fake answers hooks per repository and acceptance per token. Fail applies to both reads.
type Fake struct {
	Repositories map[string][]observe.Hook
	Tokens       map[string]github.Acceptance
	Denied       map[string]bool
	Fail         error
	Calls        []Call
}

var _ github.Reader = (*Fake)(nil)

// New returns an empty read-only GitHub inventory.
func New() *Fake {
	return &Fake{Repositories: map[string][]observe.Hook{}, Tokens: map[string]github.Acceptance{}}
}

func (f *Fake) read(operation, repository, token string) error {
	if !observe.GitHubRepository(repository) {
		return &github.Error{Reason: "invalid repository"}
	}
	if strings.TrimSpace(token) == "" {
		return &github.Error{Reason: "token not supplied"}
	}
	f.Calls = append(f.Calls, Call{operation, repository, token})
	return f.Fail
}

// Hooks returns owned facts, without ever writing the repository.
func (f *Fake) Hooks(_ context.Context, repository, token string) ([]observe.Hook, error) {
	if err := f.read("hooks", repository, token); err != nil {
		return nil, err
	}
	if f.Denied[token] {
		return nil, &github.Error{Reason: "HTTP 401"}
	}
	found := []observe.Hook{}
	for _, hook := range f.Repositories[repository] {
		hook.Events = slices.Clone(hook.Events)
		if hook.Code != nil {
			code := *hook.Code
			hook.Code = &code
		}
		found = append(found, hook)
	}
	return found, nil
}

// Token reports acceptance, or HTTP401 for an unknown credential.
func (f *Fake) Token(_ context.Context, repository, token string) (github.Acceptance, error) {
	if err := f.read("token", repository, token); err != nil {
		return github.Acceptance{}, err
	}
	if answer, found := f.Tokens[token]; found {
		return answer, nil
	}
	return github.Acceptance{Status: 401}, nil
}

// Contract exercises the same acceptance, rejection, absence and invalid-request behavior against real and fake.
// open provides owner/repo's single hook, accepted/expired credentials and a rejected credential.
func Contract(t *testing.T, open func(*testing.T) github.Reader) {
	t.Helper()
	reader := open(t)
	ctx := context.Background()
	hooks, err := reader.Hooks(ctx, "owner/repo", "accepted")
	if err != nil || len(hooks) != 1 || !hooks[0].Active || hooks[0].Host != "hooks.example" ||
		hooks[0].Code == nil || *hooks[0].Code != 200 || !slices.Contains(hooks[0].Events, "package") {
		t.Fatalf("hooks: %+v %v", hooks, err)
	}
	empty, err := reader.Hooks(ctx, "owner/empty", "accepted")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	contractTokens(t, reader)
	contractRefusals(t, reader)
}

func contractTokens(t *testing.T, reader github.Reader) {
	t.Helper()
	ctx := context.Background()
	for token, expiry := range map[string]string{"accepted": "", "expired": "2026-11-10 00:00:00 UTC"} {
		fact, err := reader.Token(ctx, "owner/repo", token)
		if err != nil || fact.Status != 200 || fact.Expiry != expiry {
			t.Fatalf("token: %+v %v", fact, err)
		}
	}
	if _, err := reader.Hooks(ctx, "owner/repo", "rejected"); err == nil {
		t.Fatal("denied hooks accepted")
	}
	rejected, err := reader.Token(ctx, "owner/repo", "rejected")
	if err != nil || rejected.Status != 401 {
		t.Fatalf("rejected: %+v %v", rejected, err)
	}
}

func contractRefusals(t *testing.T, reader github.Reader) {
	t.Helper()
	ctx := context.Background()
	for _, repository := range []string{"https://evil.example/token", "owner/../repo", "owner/repo?token=x", "owner", "x/y/z"} {
		if _, err := reader.Hooks(ctx, repository, "accepted"); err == nil {
			t.Fatalf("accepted invalid repository")
		}
		if _, err := reader.Token(ctx, repository, "accepted"); err == nil {
			t.Fatalf("accepted invalid repository")
		}
	}
	for _, token := range []string{"", " \t\n"} {
		if _, err := reader.Hooks(ctx, "owner/repo", token); err == nil {
			t.Fatal("missing hook token accepted")
		}
		if _, err := reader.Token(ctx, "owner/repo", token); err == nil {
			t.Fatal("missing acceptance token accepted")
		}
	}
}
