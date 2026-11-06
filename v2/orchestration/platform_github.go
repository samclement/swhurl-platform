package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// readGitHub reads installed settings and receivers, then hooks with the operator's token and acceptance
// with each console token. Credential bytes never enter the observation evidence.
func readGitHub(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	host, domainProblem, err := installedWebhookHost(ctx, inputs.Data)
	if err != nil {
		return err
	}
	repository, repoProblem, err := installedPlatformRepository(ctx, inputs)
	if err != nil {
		return err
	}
	push := observe.WebhookEvidence{Host: host, Repository: repository, Problem: domainProblem,
		Listing: observe.HookListing{MissingToken: inputs.GitHubToken == ""}}
	if push.Problem == "" {
		push.Problem = repoProblem
	}
	images := observe.WebhookEvidence{Host: host, Problem: domainProblem,
		Listing: observe.HookListing{MissingToken: inputs.GitHubToken == ""}}
	if push.Receiver, err = inputs.Reader.Read(ctx, receiverRef("github")); err != nil {
		return err
	}
	if images.Receiver, err = inputs.Reader.Read(ctx, receiverRef("app-images")); err != nil {
		return err
	}
	if push.Problem == "" {
		push.Listing = readHooks(ctx, inputs, repository)
	}
	images.Apps = readAppHooks(ctx, inputs, images.Problem)
	scope.PushWebhook, scope.ImageWebhooks = push, images
	scope.ConsoleTokens, err = readConsoleTokens(ctx, inputs, repository, repoProblem)
	return err
}

func receiverRef(name string) observe.Ref {
	return observe.Ref{APIVersion: "notification.toolkit.fluxcd.io/v1", Kind: "Receiver", Namespace: "flux-system", Name: name}
}

func installedWebhookHost(ctx context.Context, data kube.Data) (string, string, error) {
	found, err := data.ConfigMaps(ctx, "flux-system", kube.Selector{Name: "platform-settings"})
	if unread, err := failedRead(err); err != nil || unread != "" {
		return "", "cannot read flux-system/platform-settings (" + unread + ")", err
	}
	if len(found) != 1 {
		return "", "flux-system/platform-settings is missing", nil
	}
	host, problem := observe.WebhookHost(found[0].Data["BASE_DOMAIN"])
	return host, problem, nil
}

func installedPlatformRepository(ctx context.Context, inputs PlatformAllInputs) (string, string, error) {
	profile, problem := profileOf(inputs.Checkout.Profile)
	if problem != "" {
		return "", "cannot read delivery.source from the checkout profile", nil
	}
	ref := observe.Ref{APIVersion: "source.toolkit.fluxcd.io/v1", Kind: "GitRepository", Namespace: "flux-system", Name: profile.Delivery.Source}
	found, err := inputs.Reader.Read(ctx, ref)
	if err != nil {
		return "", "", err
	}
	if found.Status != observe.Present {
		return "", "cannot read installed platform GitRepository (" + found.Status + ")", nil
	}
	var document struct {
		Spec struct {
			URL string `json:"url"`
		} `json:"spec"`
	}
	if json.Unmarshal(found.Object, &document) != nil {
		return "", "installed platform GitRepository is malformed", nil
	}
	repository, problem := observe.InstalledRepository(document.Spec.URL)
	return repository, problem, nil
}

func readHooks(ctx context.Context, inputs PlatformAllInputs, repository string) observe.HookListing {
	if inputs.GitHubToken == "" {
		return observe.HookListing{MissingToken: true}
	}
	hooks, err := inputs.GitHub.Hooks(ctx, repository, inputs.GitHubToken)
	return observe.HookListing{Hooks: hooks, Problem: github.SafeProblem(err)}
}

// appHookRepositories reuses the release decoder: only staging and the platform image exactly qualify.
// Invalid records remain failed evidence, not a fabricated owner/repository or a silently empty catalogue.
func appHookRepositories(releases map[string][]byte) []observe.RepositoryHooks {
	found := []observe.RepositoryHooks{}
	for _, key := range sortedKeys(releases) {
		app, environment, _ := strings.Cut(key, "/")
		if environment != observe.StagingEnvironment {
			continue
		}
		release, err := kernel.DecodeRelease(releases[key])
		fact := observe.RepositoryHooks{App: app}
		if err != nil {
			fact.Problem = observe.CheckoutRecords + "/" + key + "/release.json: cannot read release"
		} else if release.Artifact.Image != "ghcr.io/samclement/"+app {
			continue
		} else if !observe.GitHubRepository(release.Source.Repository) {
			fact.Problem = app + ": release has no valid source repository"
		} else {
			fact.Repository = release.Source.Repository
		}
		found = append(found, fact)
	}
	return found
}

func readConsoleTokens(ctx context.Context, inputs PlatformAllInputs, repository, repoProblem string) (observe.ConsoleTokenEvidence, error) {
	found, err := inputs.Data.Secrets(ctx, "console", kube.Selector{Name: "console-github"})
	if unread, err := failedRead(err); err != nil || unread != "" {
		return observe.ConsoleTokenEvidence{Problem: "cannot read console/console-github (" + unread + ")"}, err
	}
	if repoProblem != "" {
		return observe.ConsoleTokenEvidence{Problem: repoProblem}, nil
	}
	data := map[string][]byte{}
	if len(found) == 1 {
		data = found[0].Data
	}
	evidence := observe.ConsoleTokenEvidence{}
	for _, fact := range []observe.TokenEvidence{{Key: "GITHUB_TOKEN", Label: "console token", Required: true},
		{Key: "APP_REPOS_TOKEN", Label: "console repository token"}} {
		token := strings.TrimSpace(string(data[fact.Key]))
		fact.Missing = token == "" || token == "REPLACE_ME" || !utf8.Valid(data[fact.Key])
		if !fact.Missing {
			acceptance, err := inputs.GitHub.Token(ctx, repository, token)
			fact.Status, fact.Expiry, fact.Problem = acceptance.Status, acceptance.Expiry, github.SafeProblem(err)
		}
		evidence.Tokens = append(evidence.Tokens, fact)
	}
	return evidence, nil
}

func readAppHooks(ctx context.Context, inputs PlatformAllInputs, problem string) []observe.RepositoryHooks {
	apps := appHookRepositories(inputs.Checkout.Releases)
	if problem != "" || inputs.GitHubToken == "" {
		return apps
	}
	for i := range apps {
		if apps[i].Problem == "" {
			apps[i].Listing = readHooks(ctx, inputs, apps[i].Repository)
		}
	}
	return apps
}
