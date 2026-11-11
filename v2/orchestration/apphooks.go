package orchestration

import (
	"context"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// The image webhook on every app repository whose staging release publishes the app's own image: the kernel
// chooses the apps and decides what each repository's webhooks need, and this use case lists them and makes the one
// change through the repository write port. A missing webhook is created, a stale one changed, a correct one left
// alone; none is deleted. One app's problem is its own outcome and the others still run.

// AppHooksInputs is one pass: each app's staging release record, by app, the account the platform images are published under, the
// Receiver's host, the two tokens — GitHubToken lists and writes webhooks, HookToken signs them — and the port.
// DryRun lists and decides, and writes nothing.
type AppHooksInputs struct {
	Staging                map[string][]byte
	Owner, HookHost        string
	GitHubToken, HookToken string
	DryRun                 bool
	Repos                  github.RepositoryWriter
}

// AppHookOutcome is one app's result: the state its repository's webhook was found in, or the problem that stopped
// it. In a dry run a missing or stale state is what would change; otherwise it is what was changed. It holds no
// credential.
type AppHookOutcome struct {
	Application, Repository string
	State                   kernel.ImageHookState
	Problem                 string
}

// AppHooks keeps the image webhook of every chosen app's repository, in name order. It refuses a pass without
// both tokens, the host or the owner before any call.
func AppHooks(ctx context.Context, inputs AppHooksInputs) ([]AppHookOutcome, error) {
	if inputs.GitHubToken == "" || inputs.HookToken == "" || inputs.HookHost == "" || inputs.Owner == "" {
		return nil, &Refusal{"app-hooks", "app-hooks needs the GitHub token, the image webhook token, the hook host and the owner"}
	}
	want := kernel.ImageHookURL(inputs.HookToken, inputs.HookHost)
	outcomes := []AppHookOutcome{}
	for _, app := range kernel.ImageHookApps(inputs.Staging, inputs.Owner) {
		outcome := AppHookOutcome{Application: app.Application, Repository: app.Repository, Problem: app.Problem}
		if outcome.Problem == "" {
			outcome.State, outcome.Problem = keepImageHook(ctx, inputs, app.Repository, want)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

// keepImageHook lists one repository's webhooks and makes the change the kernel decides, unless this is a dry run.
func keepImageHook(ctx context.Context, inputs AppHooksInputs, repository, want string) (kernel.ImageHookState, string) {
	hooks, err := inputs.Repos.Hooks(ctx, repository, inputs.GitHubToken)
	if err != nil {
		return "", "cannot list the webhooks of " + repository + ": " + github.SafeProblem(err)
	}
	listed := make([]kernel.ListedHook, 0, len(hooks))
	for _, hook := range hooks {
		listed = append(listed, kernel.ListedHook{ID: hook.ID, Active: hook.Active, Events: hook.Events, URL: hook.URL})
	}
	plan := kernel.PlanImageHook(listed, want)
	if plan.State == kernel.ImageHookInPlace || inputs.DryRun {
		return plan.State, ""
	}
	hook := github.RepositoryHook{URL: want, Secret: inputs.HookToken, Events: kernel.ImageHookEvents(), Active: true}
	if plan.State == kernel.ImageHookMissing {
		err = inputs.Repos.CreateHook(ctx, repository, inputs.GitHubToken, hook)
	} else {
		err = inputs.Repos.UpdateHook(ctx, repository, inputs.GitHubToken, plan.ID, hook)
	}
	if err != nil {
		return plan.State, "cannot keep the image webhook on " + repository + ": " + github.SafeProblem(err)
	}
	return plan.State, ""
}
