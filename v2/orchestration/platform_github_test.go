package orchestration

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

func gitHubInputs(t *testing.T) (PlatformAllInputs, *githubtest.Fake) {
	t.Helper()
	cluster, inputs := scopeCluster(t)
	profile, problem := profileOf(inputs.Checkout.Profile)
	if problem != "" {
		t.Fatal(problem)
	}
	ref := observe.Ref{APIVersion: "source.toolkit.fluxcd.io/v1", Kind: "GitRepository", Namespace: "flux-system", Name: profile.Delivery.Source}
	cluster.Objects[ref] = []byte(`{"spec":{"url":"git@github.com:installed/platform.git"}}`)
	cluster.ConfigMapData = map[string][]kube.ConfigMap{"flux-system": {{Name: "platform-settings", Data: map[string]string{"BASE_DOMAIN": "installed.example"}}}}
	cluster.SecretData["console"] = []kube.Secret{{Name: "console-github", Data: map[string][]byte{"GITHUB_TOKEN": []byte("  console-fixture\n"), "APP_REPOS_TOKEN": []byte(" repo-fixture ")}}}
	for _, name := range []string{"github", "app-images"} {
		cluster.Objects[receiverRef(name)] = []byte(`{"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
	}
	release, err := os.ReadFile("../testdata/r42.json")
	if err != nil {
		t.Fatal(err)
	}
	inputs.Checkout.Releases = map[string][]byte{"demo-orders/staging": release, "demo-orders/prod": release}
	fake := githubtest.New()
	fake.Tokens["console-fixture"] = github.Acceptance{Status: 200}
	fake.Tokens["repo-fixture"] = github.Acceptance{Status: 200}
	inputs.GitHub, inputs.GitHubToken = fake, "operator-fixture"
	return inputs, fake
}

func TestGitHubUsesInstalledSettingsAndExplicitSeparateCredentials(t *testing.T) {
	inputs, fake := gitHubInputs(t)
	scope := observe.ScopeEvidence{}
	if err := readGitHub(context.Background(), inputs, &scope); err != nil {
		t.Fatal(err)
	}
	if scope.PushWebhook.Repository != "installed/platform" || scope.PushWebhook.Host != "flux-webhook.installed.example" {
		t.Fatalf("%+v", scope.PushWebhook)
	}
	want := []githubtest.Call{{Operation: "hooks", Repository: "installed/platform", Token: "operator-fixture"},
		{Operation: "hooks", Repository: "samclement/demo-orders", Token: "operator-fixture"},
		{Operation: "token", Repository: "installed/platform", Token: "console-fixture"},
		{Operation: "token", Repository: "installed/platform", Token: "repo-fixture"}}
	if !slices.Equal(fake.Calls, want) {
		t.Fatalf("calls %+v", fake.Calls)
	}
	// No token bytes enter ScopeEvidence or the rendered report.
	text := observe.PlatformText(observe.PlatformReport{Checks: observe.PlatformScopeChecks(observe.PlatformEvidence{}, scope)})
	for _, secret := range []string{"operator-fixture", "console-fixture", "repo-fixture"} {
		if strings.Contains(text, secret) {
			t.Fatal("credential leaked")
		}
	}
}

func TestGitHubMissingOperatorTokenDoesNotSkipOtherReads(t *testing.T) {
	inputs, fake := gitHubInputs(t)
	inputs.GitHubToken = ""
	report, err := PlatformAll(context.Background(), inputs)
	if err != nil || report.Passed() || len(report.Checks) != 22 {
		t.Fatalf("%+v %v", report, err)
	}
	for _, check := range report.Checks[15:17] {
		if !slices.ContainsFunc(check.Entries, func(e observe.PlatformEntry) bool {
			return e.Level == observe.CheckBad && e.Message == "GitHub token not supplied"
		}) {
			t.Fatalf("%+v", check)
		}
	}
	if len(fake.Calls) != 2 || fake.Calls[0].Operation != "token" || fake.Calls[1].Operation != "token" {
		t.Fatalf("%+v", fake.Calls)
	}
	if report.Checks[17].Entries[0].Level != observe.CheckOK {
		t.Fatal("console acceptance stopped")
	}
}

func TestGitHubFailedSecretReadIsBadNotAbsence(t *testing.T) {
	inputs, fake := gitHubInputs(t)
	data := failingConsoleData{Data: inputs.Data, err: &kube.Error{Code: kube.Failed, Reason: observe.ReasonForbidden}}
	inputs.Data = data
	scope := observe.ScopeEvidence{}
	if err := readGitHub(context.Background(), inputs, &scope); err != nil {
		t.Fatal(err)
	}
	if scope.ConsoleTokens.Problem != "cannot read console/console-github (forbidden)" || len(fake.Calls) != 2 {
		t.Fatalf("%+v %+v", scope.ConsoleTokens, fake.Calls)
	}
	refusal := &kube.Error{Code: kube.RefusedRequest, Reason: "invalid_selector"}
	inputs.Data = failingConsoleData{Data: data.Data, err: refusal}
	if err := readGitHub(context.Background(), inputs, &scope); !errors.Is(err, refusal) {
		t.Fatalf("refusal %v", err)
	}
}

type failingConsoleData struct {
	kube.Data
	err error
}

func (f failingConsoleData) Secrets(ctx context.Context, namespace string, selector kube.Selector) ([]kube.Secret, error) {
	if namespace == "console" {
		return nil, f.err
	}
	return f.Data.Secrets(ctx, namespace, selector)
}

func TestConsoleCredentialAbsenceAndPlaceholders(t *testing.T) {
	for _, value := range []string{"", "REPLACE_ME", "  REPLACE_ME\n", string([]byte{255})} {
		inputs, fake := gitHubInputs(t)
		inputs.Data = consoleData{Data: inputs.Data, value: []byte(value)}
		evidence, err := readConsoleTokens(context.Background(), inputs, "installed/platform", "")
		if err != nil || len(evidence.Tokens) != 2 || !evidence.Tokens[0].Missing || !evidence.Tokens[1].Missing || len(fake.Calls) != 0 {
			t.Fatalf("%+v %v", evidence, err)
		}
	}
}

type consoleData struct {
	kube.Data
	value []byte
}

func (c consoleData) Secrets(context.Context, string, kube.Selector) ([]kube.Secret, error) {
	return []kube.Secret{{Name: "console-github", Data: map[string][]byte{"GITHUB_TOKEN": c.value, "APP_REPOS_TOKEN": c.value}}}, nil
}

func TestAppHookRepositoriesReuseStagingReleaseRecords(t *testing.T) {
	release, err := os.ReadFile("../testdata/r42.json")
	if err != nil {
		t.Fatal(err)
	}
	other := []byte(strings.Replace(string(release), "ghcr.io/samclement/demo-orders", "ghcr.io/other/demo-orders", 1))
	found := appHookRepositories(map[string][]byte{"demo-orders/staging": release, "demo-orders/prod": release, "third/staging": other, "broken/staging": []byte(`{}`)})
	if len(found) != 2 || found[0].App != "broken" || found[0].Problem == "" || found[1].App != "demo-orders" || found[1].Repository != "samclement/demo-orders" {
		t.Fatalf("%+v", found)
	}
}

func TestInstalledSettingsFailuresNeverInventRepositoryOrDomain(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*kubetest.Fake, PlatformAllInputs)
	}{
		{"absent domain", func(cluster *kubetest.Fake, _ PlatformAllInputs) { cluster.ConfigMapData = nil }},
		{"malformed domain", func(cluster *kubetest.Fake, _ PlatformAllInputs) {
			cluster.ConfigMapData["flux-system"][0].Data["BASE_DOMAIN"] = "https://private-token@evil"
		}},
		{"unreadable domain", func(cluster *kubetest.Fake, _ PlatformAllInputs) {
			cluster.Failing = map[string]error{"configmaps platform-settings": &kube.Error{Code: kube.Failed, Reason: observe.ReasonForbidden}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inputs, fake := gitHubInputs(t)
			test.change(inputs.Data.(*kubetest.Fake), inputs)
			scope := observe.ScopeEvidence{}
			if err := readGitHub(context.Background(), inputs, &scope); err != nil {
				t.Fatal(err)
			}
			if scope.PushWebhook.Problem == "" || scope.ImageWebhooks.Problem == "" || scope.PushWebhook.Host != "" {
				t.Fatalf("%+v", scope.PushWebhook)
			}
			for _, call := range fake.Calls {
				if call.Operation == "hooks" {
					t.Fatal("listed with invalid domain")
				}
			}
		})
	}
	for _, body := range []string{"", `{}`, `{"spec":{"url":"https://github.com.evil/owner/repo"}}`, `{"spec":{"url":"https://private-token@github.com/owner/repo"}}`} {
		inputs, fake := gitHubInputs(t)
		cluster := inputs.Data.(*kubetest.Fake)
		profile, _ := profileOf(inputs.Checkout.Profile)
		ref := observe.Ref{APIVersion: "source.toolkit.fluxcd.io/v1", Kind: "GitRepository", Namespace: "flux-system", Name: profile.Delivery.Source}
		if body == "" {
			delete(cluster.Objects, ref)
		} else {
			cluster.Objects[ref] = []byte(body)
		}
		scope := observe.ScopeEvidence{}
		if err := readGitHub(context.Background(), inputs, &scope); err != nil {
			t.Fatal(err)
		}
		if scope.PushWebhook.Problem == "" || scope.ConsoleTokens.Problem == "" || scope.PushWebhook.Repository != "" {
			t.Fatalf("%+v", scope.PushWebhook)
		}
		if len(fake.Calls) != 1 || fake.Calls[0].Repository != "samclement/demo-orders" {
			t.Fatalf("only app hooks can be read without the platform source: %+v", fake.Calls)
		}
	}
}
