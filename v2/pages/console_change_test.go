package pages

import "testing"

// Every kind of console pull request, as its branch, its target, its title and the paths it may carry read: the one
// table the command that opens a pull request and the pages that follow it share.
func TestConsoleChanges(t *testing.T) {
	cases := []struct {
		kind, name, env string
		branch, target  string
		heading         string
		owns            []string
		foreign         []string
	}{
		{
			kind: "v2-scale", name: "web", env: "staging",
			branch: "console/v2-scale-web-staging-0123456", target: "web/staging", heading: "apps: scale web/staging",
			owns: []string{"v2-records/web/app.json", "v2-records/web/staging/instance.json",
				"v2-apps/instances/web/staging/helmrelease.json", "clusters/home/app-web-staging.json",
				"v2-records/calls.json", "platform/egress/allocation.json", "platform/nats/users.json",
				"platform/auth/web-staging.json"},
			foreign: []string{"v2-records/web/prod/instance.json", "v2-records/other/staging/instance.json",
				"clusters/home/app-web-prod.json", "apps/web/staging/helmrelease.json",
				"platform/egress/helmrelease.yaml", "v2-extra/web/staging/instance.json"},
		},
		{
			kind: "v2-team", name: "web", env: "staging",
			branch: "console/v2-team-web-staging-0123456", target: "web/staging",
			heading: "apps: change the team of web",
			// the app-level change rewrites both environments, whichever page asked for it
			owns: []string{"v2-records/web/app.json", "v2-records/web/staging/instance.json",
				"v2-records/web/prod/instance.json"},
		},
		{
			kind: "v2-promote", name: "web", env: "staging",
			branch: "console/v2-promote-web-staging-0123456", target: "web/prod",
			heading: "apps: promote web/staging to web/prod",
			owns:    []string{"v2-records/web/staging/release.json", "v2-records/web/prod/release.json"},
		},
		{
			kind: "team-channel-set", name: "payments", env: "",
			branch: "console/team-channel-set-payments-0123456", target: "team:payments",
			heading: "teams: set the notification channel of payments",
			owns: []string{"v2-records/teams.json", "platform/notify/routes.json",
				"platform/notify/channels/channel-payments.sops.yaml"},
			foreign: []string{"platform/notify/channels/nested/channel.sops.yaml", "v2-records/teams/app.json",
				"v2-records/payments/staging/instance.json", "platform/notify/kustomization.yaml",
				"platform/notify/channels"},
		},
	}
	for _, test := range cases {
		t.Run(test.kind, func(t *testing.T) {
			change, found := ConsoleChangeNamed(test.kind)
			if !found {
				t.Fatalf("%s is not a kind", test.kind)
			}
			if got := change.Target(test.name, test.env); got != test.target {
				t.Errorf("target = %q, want %q", got, test.target)
			}
			if got := change.Heading(test.name, test.env); got != test.heading {
				t.Errorf("heading = %q, want %q", got, test.heading)
			}
			made, err := change.Branch(test.name, test.env, "0123456")
			if err != nil || made != test.branch {
				t.Fatalf("branch = %q, %v; want %q", made, err, test.branch)
			}
			read, name, env, ok := ParseConsoleBranch(made)
			if !ok || read.Name != change.Name || name != test.name || env != test.env {
				t.Fatalf("ParseConsoleBranch(%q) = %q %q %q %v", made, read.Name, name, env, ok)
			}
			if action, target := BranchTarget(made); action != test.kind || target != test.target {
				t.Errorf("BranchTarget = %q %q", action, target)
			}
			for _, file := range test.owns {
				if !change.Owns(test.name, test.env, file) {
					t.Errorf("%s does not own %s", test.kind, file)
				}
			}
			for _, file := range test.foreign {
				if change.Owns(test.name, test.env, file) {
					t.Errorf("%s owns %s, which is not its", test.kind, file)
				}
			}
		})
	}
}

// A promotion's branch names staging, but the change it makes is production's.
func TestConsolePromotionTargetsProduction(t *testing.T) {
	promote, _ := ConsoleChangeNamed("v2-promote")
	if promote.Allows("prod") {
		t.Error("a promotion's branch may not name production")
	}
	if got := promote.Target("web", "staging"); got != "web/prod" {
		t.Errorf("target = %q", got)
	}
	scale, _ := ConsoleChangeNamed("v2-scale")
	if !scale.Allows("prod") || !scale.Allows("staging") || scale.Allows("") {
		t.Error("a scale's branch names one of the two instances")
	}
}

// A branch the console did not open is never read as one: another prefix, another kind's environments, a name that
// is not a DNS label, or no commit.
func TestParseConsoleBranchRefusals(t *testing.T) {
	for _, branch := range []string{
		"main", "console/merge-gate-web-0123456", "console/v2-scale-web-0123456",
		"console/v2-scale-web-test-0123456", "console/v2-scale-Web-staging-0123456",
		"console/v2-scale-web-staging-0123", "console/v2-scale-web-staging-0123456-extra",
		"console/v2-promote-web-prod-0123456", "console/v2-team--staging-0123456", "console/-web-staging-0123456",
	} {
		if change, name, env, ok := ParseConsoleBranch(branch); ok {
			t.Errorf("%q read as %q %q %q", branch, change.Name, name, env)
		}
		if action, target := BranchTarget(branch); action != "" || target != "" {
			t.Errorf("%q is a change: %q %q", branch, action, target)
		}
	}
}

// Branch refuses to build a name it cannot read back, so the console cannot push a branch it would not recognise.
func TestConsoleBranchRefusesWhatItCannotReadBack(t *testing.T) {
	scale, _ := ConsoleChangeNamed("v2-scale")
	for _, test := range []struct{ name, env string }{
		{"Web", "staging"}, // not a DNS label
		{"", "staging"},    // no name at all
		{"web", ""},        // an instance kind needs its environment
		{"web", "test"},    // not an environment the kind states
	} {
		if made, err := scale.Branch(test.name, test.env, "0123456"); err == nil {
			t.Errorf("%q/%q made %q", test.name, test.env, made)
		}
	}
	// A name that ends in an environment's word is still one name: console/v2-scale-web-staging-staging-0123456.
	if made, err := scale.Branch("web-staging", "staging", "0123456"); err != nil {
		t.Errorf("web-staging refused: %v", err)
	} else if _, name, env, _ := ParseConsoleBranch(made); name != "web-staging" || env != "staging" {
		t.Errorf("%q read back as %q %q", made, name, env)
	}
	team, _ := ConsoleChangeNamed("team-add")
	if made, err := team.Branch("payments", "", "0123456"); err != nil || made != "console/team-add-payments-0123456" {
		t.Errorf("team branch = %q, %v", made, err)
	}
}

// ConsoleChanges is every kind, each reachable by the name its branch states, and each kind's own name is a kind.
func TestConsoleChangesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, change := range ConsoleChanges() {
		if seen[change.Name] || change.Name == "" || change.Title == "" {
			t.Fatalf("kind %q is stated twice or says nothing", change.Name)
		}
		seen[change.Name] = true
		if found, ok := ConsoleChangeNamed(change.Name); !ok || found.Title != change.Title {
			t.Errorf("%q is not readable by name", change.Name)
		}
	}
	for _, branch := range []string{"console/v2-egress-web-staging-0123456", "console/v2-schedule-web-staging-0123456"} {
		if change, _, _, ok := ParseConsoleBranch(branch); ok {
			t.Errorf("%q is a kind the console does not open: %q", branch, change.Name)
		}
	}
	if _, ok := ConsoleChangeNamed("v2-egress"); ok {
		t.Error("a kind the console does not open is readable")
	}
}

// A change's merge plan comes from the paths it added or changed: an encrypted input needs values a merge cannot
// supply, and only a new app's seed is exempt.
func TestMergePlanFor(t *testing.T) {
	cases := []struct {
		kind    string
		changed []string
		plan    string
	}{
		{"v2-scale", []string{"v2-records/web/staging/instance.json"}, ""},
		{"v2-scale", nil, ""},
		{"team-channel-set", []string{"platform/notify/channels/channel-payments.sops.yaml"},
			"needs Secret values to be set on the pull request branch"},
		{"v2-connect", []string{"v2-records/web/staging/events.sops.yaml"},
			"adds or changes the app's generated encrypted events key"},
		{"v2-connect", []string{"v2-records/web/staging/events.sops.yaml", "v2-records/web/staging/secret.sops.yaml"},
			"needs Secret values to be set on the pull request branch"},
		{"new", []string{"v2-records/web/staging/secret.sops.yaml"}, ""},
	}
	for _, test := range cases {
		if got := MergePlanFor(test.kind, test.changed); got != test.plan {
			t.Errorf("%s %v: plan = %q, want %q", test.kind, test.changed, got, test.plan)
		}
	}
}
