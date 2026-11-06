package observe

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Hook is the read-only webhook evidence. Host is parsed by the adapter, never a secret-bearing URL.
// Message contains only a standard HTTP reason, never an arbitrary server response.
type Hook struct {
	Active  bool
	Events  []string
	Host    string
	Code    *int
	Message string
}

// HookListing distinguishes a missing operator credential from an unsuccessful authorized read.
type HookListing struct {
	Hooks        []Hook
	MissingToken bool
	Problem      string
}

// RepositoryHooks is one staging app's declared source and its hooks.
type RepositoryHooks struct {
	App, Repository, Problem string
	Listing                  HookListing
}

// WebhookEvidence is the installed receiver and settings and the hooks read from GitHub.
type WebhookEvidence struct {
	Receiver                  Read
	Host, Repository, Problem string
	Listing                   HookListing
	Apps                      []RepositoryHooks
}

// TokenEvidence contains no token: only its key/label, absence, or the result of asking GitHub.
type TokenEvidence struct {
	Key, Label        string
	Required, Missing bool
	Problem           string
	Status            int
	Expiry            string
}

// ConsoleTokenEvidence separates a failed Secret/settings read from absent credential keys.
type ConsoleTokenEvidence struct {
	Problem string
	Tokens  []TokenEvidence
}

// GitHubRepository accepts only an owner/repository identity, never a URL or credentials.
func GitHubRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		return false
	}
	owner := regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repo := regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	return owner.MatchString(parts[0]) && repo.MatchString(parts[1]) && parts[1] != "." && parts[1] != ".."
}

// InstalledRepository parses Flux's installed GitHub HTTPS or scp-style SSH source URL.
func InstalledRepository(value string) (string, string) {
	repository := ""
	switch {
	case strings.HasPrefix(value, "https://github.com/"):
		repository = strings.TrimPrefix(value, "https://github.com/")
	case strings.HasPrefix(value, "git@github.com:"):
		repository = strings.TrimPrefix(value, "git@github.com:")
	}
	repository = strings.TrimSuffix(repository, ".git")
	if !GitHubRepository(repository) {
		return "", "installed GitRepository has no valid GitHub repository URL"
	}
	return repository, ""
}

// WebhookHost validates BASE_DOMAIN before deriving the receiver's exact host.
func WebhookHost(domain string) (string, string) {
	if len(domain) > 253 || !strings.Contains(domain, ".") {
		return "", "platform-settings.BASE_DOMAIN is missing or malformed"
	}
	label := regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	for _, part := range strings.Split(domain, ".") {
		if !label.MatchString(part) {
			return "", "platform-settings.BASE_DOMAIN is missing or malformed"
		}
	}
	return "flux-webhook." + strings.ToLower(domain), ""
}

func receiverEntries(read Read) []PlatformEntry {
	status, message := "Unknown", "could not read it"
	if read.Status == Present {
		status, message = readyCondition(read.Object)
	}
	name := read.Ref.Namespace + "/" + read.Ref.Name
	if status == "True" {
		return []PlatformEntry{ok("Flux receiver " + name + " is Ready")}
	}
	return []PlatformEntry{bad("Flux receiver " + name + " is not Ready: " + message + " (make reconcile UNIT=platform-flux-webhook)")}
}

func matchingHook(hooks []Hook, host string) *Hook {
	for _, hook := range hooks {
		if strings.EqualFold(hook.Host, host) {
			return &hook
		}
	}
	return nil
}

func listingProblem(list HookListing, repository, prefix string) []PlatformEntry {
	if list.MissingToken {
		return []PlatformEntry{bad(prefix + "GitHub token not supplied")}
	}
	if list.Problem != "" {
		return []PlatformEntry{warn(prefix + "could not list the GitHub webhooks of " + repository + ": " + list.Problem)}
	}
	return nil
}

func platformPushWebhook(e WebhookEvidence) []PlatformEntry {
	entries := receiverEntries(e.Receiver)
	if e.Listing.MissingToken {
		entries = append(entries, bad("GitHub token not supplied"))
	}
	if e.Problem != "" {
		return append(entries, bad(e.Problem))
	}
	if e.Listing.MissingToken {
		return entries
	}
	if problem := listingProblem(e.Listing, e.Repository, ""); problem != nil {
		return append(entries, problem...)
	}
	hook := matchingHook(e.Listing.Hooks, e.Host)
	switch {
	case hook == nil:
		return append(entries, warn("no GitHub webhook on "+e.Repository+" calls "+e.Host+"; Flux falls back to polling every minute"))
	case !hook.Active:
		return append(entries, warn("the GitHub webhook to "+e.Host+" is disabled; Flux falls back to polling every minute"))
	case hook.Code != nil && *hook.Code == 200:
		return append(entries, ok("GitHub's latest delivery to "+e.Host+" returned 200"))
	default:
		return append(entries, warn(delivery("GitHub's latest delivery to "+e.Host, *hook)+
			"; see Recent Deliveries on the webhook (Flux still polls every minute)"))
	}
}

func delivery(prefix string, hook Hook) string {
	code := "None"
	if hook.Code != nil {
		code = fmt.Sprint(*hook.Code)
	}
	return strings.TrimSpace(prefix + " returned " + code + " " + hook.Message)
}

func platformImageWebhooks(e WebhookEvidence) []PlatformEntry {
	entries := receiverEntries(e.Receiver)
	if e.Listing.MissingToken {
		entries = append(entries, bad("GitHub token not supplied"))
	}
	if e.Problem != "" {
		return append(entries, bad(e.Problem))
	}
	for _, app := range e.Apps {
		if app.Problem != "" {
			entries = append(entries, bad(app.Problem))
			continue
		}
		if e.Listing.MissingToken {
			continue
		}
		entries = append(entries, imageHookEntries(app, e.Host)...)
	}
	return entries
}

func imageHookEntries(app RepositoryHooks, host string) []PlatformEntry {
	prefix := app.App + ": "
	if problem := listingProblem(app.Listing, app.Repository, prefix); problem != nil {
		return problem
	}
	hook := matchingHook(app.Listing.Hooks, host)
	fallback := "; Flux finds its new images within the hour"
	switch {
	case hook == nil:
		return []PlatformEntry{warn(prefix + "no image webhook on " + app.Repository + fallback + " (make app-hooks)")}
	case !hook.Active || !slices.Contains(hook.Events, "package") || !slices.Contains(hook.Events, "registry_package"):
		return []PlatformEntry{warn(prefix + "the image webhook on " + app.Repository + " is disabled or lacks its events" + fallback + " (make app-hooks)")}
	case hook.Code == nil:
		return []PlatformEntry{ok(prefix + "image webhook on " + app.Repository + ", no delivery yet")}
	case *hook.Code == 200:
		return []PlatformEntry{ok(prefix + "image webhook on " + app.Repository + ", latest delivery returned 200")}
	default:
		return []PlatformEntry{warn(delivery(prefix+"the latest image webhook delivery from "+app.Repository, *hook) + fallback +
			" (after a token rotation: make app-hooks)")}
	}
}

func platformConsoleTokens(e ConsoleTokenEvidence, now time.Time) []PlatformEntry {
	if e.Problem != "" {
		return []PlatformEntry{bad(e.Problem)}
	}
	entries := []PlatformEntry{}
	for _, token := range e.Tokens {
		entries = append(entries, consoleToken(token, now))
	}
	return entries
}

func consoleToken(token TokenEvidence, now time.Time) PlatformEntry {
	if token.Missing {
		message := "console/console-github." + token.Key + " is not set; set it with: sops platform/console/secret.sops.yaml"
		if token.Required {
			return bad(message)
		}
		return warn(message + " (the console's New app page is off until then)")
	}
	if token.Problem != "" {
		return bad("GitHub did not accept the " + token.Label + " (" + token.Problem + ")" + tokenFix())
	}
	if token.Status != 200 {
		return bad(fmt.Sprintf("GitHub did not accept the %s (HTTP %d)", token.Label, token.Status) + tokenFix())
	}
	prefix := "GitHub accepts the " + token.Label
	if token.Expiry == "" {
		return ok(prefix + " (no expiry date)")
	}
	when, err := tokenExpiry(token.Expiry)
	if err != nil {
		return warn(prefix + "; could not read its expiry")
	}
	days := when.Sub(now.UTC()).Hours() / 24
	message := fmt.Sprintf("%s; it expires %s (%.0f days)", prefix, when.Format("2006-01-02"), days)
	if days < 14 {
		return warn(message + "; create a new one and set it as docs/operations.md#secrets says")
	}
	return ok(message)
}

func tokenExpiry(value string) (time.Time, error) {
	if len(value) > 19 {
		value = value[:19]
	}
	return time.ParseInLocation("2006-01-02 15:04:05", value, time.UTC)
}

func tokenFix() string {
	return ", so the console cannot open pull requests: create a new one and set it as docs/operations.md#secrets says"
}
