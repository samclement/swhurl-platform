package observe

import (
	"strings"
	"testing"
	"time"
)

func healthyGitHub() (WebhookEvidence, WebhookEvidence, ConsoleTokenEvidence) {
	code := 200
	hook := Hook{Active: true, Events: []string{"push", "package", "registry_package"}, Host: "flux-webhook.example.test", Code: &code, Message: "OK"}
	receiver := func(name string) Read {
		return presentRead(Ref{Namespace: "flux-system", Name: name}, `{"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
	}
	push := WebhookEvidence{Receiver: receiver("github"), Host: "flux-webhook.example.test", Repository: "owner/platform", Listing: HookListing{Hooks: []Hook{hook}}}
	images := WebhookEvidence{Receiver: receiver("app-images"), Host: push.Host, Apps: []RepositoryHooks{{App: "demo-shop", Repository: "samclement/demo-shop", Listing: push.Listing}}}
	tokens := ConsoleTokenEvidence{Tokens: []TokenEvidence{{Key: "GITHUB_TOKEN", Label: "console token", Required: true, Status: 200},
		{Key: "APP_REPOS_TOKEN", Label: "console repository token", Status: 200}}}
	return push, images, tokens
}

func TestPushWebhook(t *testing.T) {
	cases := []struct {
		name   string
		change func(*WebhookEvidence)
		level  CheckLevel
		want   string
	}{
		{"ready", func(*WebhookEvidence) {}, CheckOK, "latest delivery to flux-webhook.example.test returned 200"},
		{"nonready", func(e *WebhookEvidence) {
			e.Receiver.Object = []byte(`{"status":{"conditions":[{"type":"Ready","status":"False","message":"no secret"}]}}`)
		}, CheckBad, "is not Ready: no secret"},
		{"absent", func(e *WebhookEvidence) { e.Receiver.Status = Absent }, CheckBad, "could not read it"},
		{"unavailable", func(e *WebhookEvidence) { e.Receiver.Status = Unavailable }, CheckBad, "could not read it"},
		{"missing ready condition", func(e *WebhookEvidence) { e.Receiver.Object = []byte(`{}`) }, CheckBad, "no Ready condition"},
		{"no hook", func(e *WebhookEvidence) { e.Listing.Hooks = nil }, CheckWarn, "no GitHub webhook on owner/platform calls"},
		{"disabled", func(e *WebhookEvidence) { e.Listing.Hooks[0].Active = false }, CheckWarn, "is disabled; Flux falls back"},
		{"no delivery", func(e *WebhookEvidence) { e.Listing.Hooks[0].Code = nil; e.Listing.Hooks[0].Message = "" }, CheckWarn, "returned None; see Recent Deliveries"},
		{"failure", func(e *WebhookEvidence) {
			code := 502
			e.Listing.Hooks[0].Code = &code
			e.Listing.Hooks[0].Message = "Bad Gateway"
		}, CheckWarn, "returned 502 Bad Gateway"},
		{"denied", func(e *WebhookEvidence) { e.Listing.Problem = "GitHub: HTTP 403" }, CheckWarn, "could not list the GitHub webhooks of owner/platform: GitHub: HTTP 403"},
		{"no operator credential", func(e *WebhookEvidence) { e.Listing = HookListing{MissingToken: true} }, CheckBad, "GitHub token not supplied"},
		{"bad settings", func(e *WebhookEvidence) { e.Problem = "settings unavailable" }, CheckBad, "settings unavailable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			e, _, _ := healthyGitHub()
			test.change(&e)
			assertEntry(t, platformPushWebhook(e), test.level, test.want)
		})
	}
}

func TestImageWebhooks(t *testing.T) {
	cases := []struct {
		name   string
		change func(*WebhookEvidence)
		level  CheckLevel
		want   string
	}{
		{"healthy", func(*WebhookEvidence) {}, CheckOK, "demo-shop: image webhook on samclement/demo-shop, latest delivery returned 200"},
		{"nonready", func(e *WebhookEvidence) {
			e.Receiver.Object = []byte(`{"status":{"conditions":[{"type":"Ready","status":"False","message":"no secret"}]}}`)
		}, CheckBad, "is not Ready: no secret"},
		{"absent", func(e *WebhookEvidence) { e.Receiver.Status = Absent }, CheckBad, "could not read it"},
		{"unavailable", func(e *WebhookEvidence) { e.Receiver.Status = Unavailable }, CheckBad, "could not read it"},
		{"no hook", func(e *WebhookEvidence) { e.Apps[0].Listing.Hooks = nil }, CheckWarn, "no image webhook on samclement/demo-shop; Flux finds its new images within the hour (make app-hooks)"},
		{"no delivery", func(e *WebhookEvidence) { e.Apps[0].Listing.Hooks[0].Code = nil }, CheckOK, "no delivery yet"},
		{"failure", func(e *WebhookEvidence) {
			code := 404
			e.Apps[0].Listing.Hooks[0].Code = &code
			e.Apps[0].Listing.Hooks[0].Message = ""
		}, CheckWarn, "latest image webhook delivery from samclement/demo-shop returned 404; Flux finds"},
		{"wrong events", func(e *WebhookEvidence) { e.Apps[0].Listing.Hooks[0].Events = []string{"push"} }, CheckWarn, "is disabled or lacks its events"},
		{"disabled", func(e *WebhookEvidence) { e.Apps[0].Listing.Hooks[0].Active = false }, CheckWarn, "is disabled or lacks its events"},
		{"denied", func(e *WebhookEvidence) { e.Apps[0].Listing.Problem = "GitHub: HTTP 403" }, CheckWarn, "demo-shop: could not list"},
		{"missing token even without apps", func(e *WebhookEvidence) { e.Listing.MissingToken = true; e.Apps = nil }, CheckBad, "GitHub token not supplied"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, e, _ := healthyGitHub()
			test.change(&e)
			assertEntry(t, platformImageWebhooks(e), test.level, test.want)
		})
	}
}

func assertEntry(t *testing.T, entries []PlatformEntry, level CheckLevel, want string) {
	t.Helper()
	for _, entry := range entries {
		if entry.Level == level && strings.Contains(entry.Message, want) {
			return
		}
	}
	t.Fatalf("want %s %q in %+v", level, want, entries)
}

func TestExactWebhookHost(t *testing.T) {
	for _, host := range []string{"flux-webhook.example.test.evil", "evil", "flux-webhook.example.test:443", ""} {
		push, images, _ := healthyGitHub()
		push.Listing.Hooks[0].Host = host
		images.Apps[0].Listing.Hooks[0].Host = host
		assertEntry(t, platformPushWebhook(push), CheckWarn, "no GitHub webhook")
		assertEntry(t, platformImageWebhooks(images), CheckWarn, "no image webhook")
	}
}

func TestConsoleTokenExpiryAndRejection(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		expiry  string
		status  int
		problem string
		level   CheckLevel
		want    string
	}{
		{"", 200, "", CheckOK, "(no expiry date)"},
		{"bad-private-expiry", 200, "", CheckWarn, "could not read its expiry"},
		{now.Add(5 * 24 * time.Hour).Format("2006-01-02 15:04:05 UTC"), 200, "", CheckWarn, "(5 days); create a new one"},
		{now.Add(14 * 24 * time.Hour).Format("2006-01-02 15:04:05 UTC"), 200, "", CheckOK, "(14 days)"},
		{now.Add(14*24*time.Hour - time.Second).Format("2006-01-02 15:04:05 UTC"), 200, "", CheckWarn, "create a new one"},
		{"2099-01-01 00:00:00 UTC", 200, "", CheckOK, "expires 2099-01-01"},
		{"", 401, "", CheckBad, "did not accept the console token (HTTP 401)"},
		{"", 0, "GitHub: request failed", CheckBad, "did not accept the console token (GitHub: request failed)"},
	} {
		entry := consoleToken(TokenEvidence{Label: "console token", Status: test.status, Expiry: test.expiry, Problem: test.problem}, now)
		assertEntry(t, []PlatformEntry{entry}, test.level, test.want)
		if strings.Contains(entry.Message, "private") {
			t.Fatal("expiry leaked")
		}
	}
	// Supplied time is converted to UTC, not interpreted as local wall-clock time.
	zone := time.FixedZone("offset", 2*60*60)
	entry := consoleToken(TokenEvidence{Label: "console token", Status: 200, Expiry: "2026-11-20 12:00:00 UTC"}, now.In(zone))
	if entry.Level != CheckOK {
		t.Fatal(entry)
	}
}

func TestMissingConsoleCredentials(t *testing.T) {
	_, _, e := healthyGitHub()
	for i := range e.Tokens {
		e.Tokens[i].Missing = true
	}
	entries := platformConsoleTokens(e, platformNow)
	assertEntry(t, entries, CheckBad, "console/console-github.GITHUB_TOKEN is not set")
	assertEntry(t, entries, CheckWarn, "console/console-github.APP_REPOS_TOKEN is not set")
	assertEntry(t, entries, CheckWarn, "the console's New app page is off until then")
	assertEntry(t, platformConsoleTokens(ConsoleTokenEvidence{Problem: "cannot read console/console-github (forbidden)"}, platformNow), CheckBad, "cannot read")
}

func TestInstalledGitHubSettings(t *testing.T) {
	for _, value := range []string{"https://github.com/owner/repo.git", "https://github.com/owner/repo", "git@github.com:owner/repo.git"} {
		repo, problem := InstalledRepository(value)
		if repo != "owner/repo" || problem != "" {
			t.Fatalf("%s %s", repo, problem)
		}
	}
	for _, value := range []string{"", "https://github.com.evil/owner/repo", "https://user:secret@github.com/owner/repo",
		"https://github.com/owner/repo?secret=x", "https://github.com/owner/../repo", "git@evil:owner/repo", "https://github.com/owner/repo#token"} {
		repo, problem := InstalledRepository(value)
		if repo != "" || problem == "" || strings.Contains(problem, value) && value != "" {
			t.Fatalf("unsafe setting result: %q %q", repo, problem)
		}
	}
	for _, value := range []string{"", "evil/path", "a..test", "user:secret@example.test", "https://example.test", "-bad.test"} {
		if host, problem := WebhookHost(value); host != "" || problem == "" {
			t.Fatalf("accepted %q", value)
		}
	}
	if host, problem := WebhookHost("Example.Test"); host != "flux-webhook.example.test" || problem != "" {
		t.Fatal(host, problem)
	}
}
