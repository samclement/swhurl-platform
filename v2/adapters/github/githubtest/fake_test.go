package githubtest

import (
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/observe"
	"testing"
)

func TestFakeContract(t *testing.T) {
	Contract(t, func(*testing.T) github.Reader {
		fake := New()
		fake.Denied = map[string]bool{"rejected": true}
		code := 200
		fake.Repositories["owner/repo"] = []observe.Hook{{Active: true, Events: []string{"package", "registry_package"}, Host: "hooks.example", Code: &code, Message: "OK"}}
		fake.Tokens["accepted"] = github.Acceptance{Status: 200}
		fake.Tokens["expired"] = github.Acceptance{Status: 200, Expiry: "2026-11-10 00:00:00 UTC"}
		return fake
	})
}
