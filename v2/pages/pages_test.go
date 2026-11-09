package pages_test

import (
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// An instance is always named <app>/<env> and links to its page, whether it is given as its unit or as an instance;
// anything else is a Flux unit, and a change on the Teams page is a team.
func TestTarget(t *testing.T) {
	for _, tt := range []struct{ name, label, link string }{
		{"app-web-staging", "web/staging", "/apps/web/staging"},
		{"app-my-api-prod", "my-api/prod", "/apps/my-api/prod"},
		{"orders/staging", "orders/staging", "/apps/orders/staging"},
		{"team:payments", "team payments", "/teams#payments"},
		{"platform-nats", "platform-nats", "/units/platform-nats"},
		{"app-web-qa", "app-web-qa", "/units/app-web-qa"},
	} {
		label, link := pages.Target(tt.name)
		if label != tt.label || link != tt.link {
			t.Errorf("Target(%q) = %q, %q; want %q, %q", tt.name, label, link, tt.label, tt.link)
		}
	}
}

func unit(name string, ready, message, reason string, suspend bool, depends ...string) []byte {
	dependencies := ""
	for i, dependency := range depends {
		if i > 0 {
			dependencies += ","
		}
		dependencies += `{"name":"` + dependency + `"}`
	}
	state := `"True"`
	if ready != "" {
		state = `"` + ready + `"`
	}
	return []byte(`{"metadata":{"name":"` + name + `"},"spec":{"dependsOn":[` + dependencies + `],"suspend":` +
		map[bool]string{true: "true", false: "false"}[suspend] + `,"path":"./apps/` + name + `","interval":"1m"},
		"status":{"lastAppliedRevision":"main@sha1:0123456789abcdef0123456789abcdef01234567",
		"conditions":[{"type":"Ready","status":` + state + `,"message":"` + message + `","reason":"` + reason + `"}]}}`)
}

// The units list orders by dependency depth and name, a dependency that is not ready is Updating rather than Failing,
// a suspended unit is Suspended, and each unit says which units need it.
func TestUnits(t *testing.T) {
	found, err := pages.Units([][]byte{
		unit("app-web-staging", "True", "Applied revision: main@sha1:0123456789abcdef", "", false, "platform-oauth2-proxy"),
		unit("platform-oauth2-proxy", "Unknown", "dependency 'infra-base' is not ready", "DependencyNotReady", false, "infra-base"),
		unit("infra-base", "", "", "", false),
		unit("app-old-prod", "False", "Helm upgrade failed", "UpgradeFailed", false),
		unit("app-paused-staging", "False", "suspended", "", true),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name     string
		level    int
		state    string
		revision string
	}{
		{"app-old-prod", 0, "failing", "0123456"},
		{"app-paused-staging", 0, "suspended", "0123456"},
		{"infra-base", 0, "healthy", "0123456"},
		{"platform-oauth2-proxy", 1, "updating", "0123456"},
		{"app-web-staging", 2, "healthy", "0123456"},
	}
	if len(found) != len(want) {
		t.Fatalf("%d units, want %d: %#v", len(found), len(want), found)
	}
	for i, expected := range want {
		got := found[i]
		if got.Name != expected.name || got.Level != expected.level || got.Ready.Key != expected.state || got.Revision != expected.revision {
			t.Errorf("unit %d = %s level %d %s %s; want %v", i, got.Name, got.Level, got.Ready.Key, got.Revision, expected)
		}
	}
	if got := found[3].Ready.Detail; got != "waiting for infra-base" {
		t.Errorf("a dependency's detail = %q", got)
	}
	if got := found[4].NeededBy; len(got) != 0 {
		t.Errorf("web is needed by %q", got)
	}
	if got := found[3].NeededBy; len(got) != 1 || got[0] != "app-web-staging" {
		t.Errorf("oauth2-proxy is needed by %q", got)
	}
}

// A failed unit that is also named by the Flux check is listed once, from the units list the page shows; suspended
// units are listed last, so what is failing comes first.
func TestProblems(t *testing.T) {
	units, err := pages.Units([][]byte{
		unit("app-broken-staging", "False", "Helm upgrade failed", "UpgradeFailed", false),
		unit("infra-paused", "True", "", "", true),
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := pages.ChecksOf([]pages.Check{{Section: pages.FluxSection, Entries: []pages.Entry{
		{Level: "bad", Message: "app-broken-staging is not ready"}, {Level: "ok", Message: "infra-paused is ready"}}},
		{Section: "Ingress", Entries: []pages.Entry{{Level: "bad", Message: "Traefik does not redirect"}}}})
	found := pages.Problems(checks, units)
	levels := []string{}
	for _, problem := range found {
		levels = append(levels, problem.Level)
	}
	if len(found) != 3 || levels[0] != "failing" || levels[1] != "failing" || levels[2] != "suspended" {
		t.Fatalf("problems = %#v", found)
	}
	if found[1].Link != "/apps/broken/staging" {
		t.Errorf("a unit's problem links to %q", found[1].Link)
	}
}

// Ages are counted from the time supplied, and a date alone counts whole days.
func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]string{
		"2026-11-09":           "today",
		"2026-11-08":           "yesterday",
		"2026-11-01":           "8 days ago",
		"2026-11-09T11:59:30Z": "just now",
		"2026-11-09T10:00:00Z": "2 h ago",
		"2026-11-07T12:00:00Z": "2 days ago",
		"not a time":           "not a time",
		"":                     "",
	} {
		if got := pages.Ago(value, now); got != want {
			t.Errorf("Ago(%q) = %q, want %q", value, got, want)
		}
	}
	if got := pages.Took(now, now.Add(95*time.Second)); got != "2 min" {
		t.Errorf("Took = %q", got)
	}
	if got := pages.Took(now, now.Add(42*time.Second)); got != "42 s" {
		t.Errorf("Took = %q", got)
	}
}

// An image check names the instance the way every page does.
func TestCondensed(t *testing.T) {
	found := pages.Condensed([]pages.Entry{{Level: "bad", Message: "web-staging: newest image is not running"}})
	if len(found) != 1 || found[0].Message != "web/staging: newest image is not running" {
		t.Fatalf("condensed = %#v", found)
	}
}
