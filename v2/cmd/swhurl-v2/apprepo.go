package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/copier"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// A new app repository is one command: render the stack's template, create the public repository with its first
// commit, add the image webhook and wait for the first image, then the ordinary publication for `create`, each
// `edit` and `render`. The console reads the lines as the job's log and takes the last one as the pull request it
// opened, so every line of progress goes to standard output and only a refusal goes to standard error.
//
// The credentials are the environment's, never arguments: CONSOLE_TOKEN opens the staging pull request,
// APP_REPOS_TOKEN creates the repository and writes its first commit, and IMAGE_WEBHOOK_TOKEN signs the webhook.

const (
	appReposTokenEnv   = "APP_REPOS_TOKEN"
	appWebhookTokenEnv = "IMAGE_WEBHOOK_TOKEN"
)

// appRepo makes the repository, waits for its first image and opens the staging pull request.
func (c cli) appRepo(r request) int {
	spec := r.spec
	if spec == nil || spec.app == "" || r.appType == "" || spec.requestedBy == "" {
		return fail(c.diagnostic, errors.New("invalid: app-repo needs --app, --type and --requested-by"))
	}
	contract, err := c.readFile(r.contractPath)
	if err != nil {
		return fail(c.diagnostic, fmt.Errorf("invalid: --contract: %s", r.contractPath))
	}
	repository, status := c.changeRepository(spec)
	if status != 0 {
		return status
	}
	inputs, status := c.appRepoInputs(r, contract, repository)
	if status != 0 {
		return status
	}
	// Copier renders into this folder, and the rendered tree is read from it before the first commit is written, so
	// it lives exactly as long as this command.
	defer os.RemoveAll(inputs.Work)
	found, err := orchestration.AppRepo(context.Background(), inputs)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if spec.dryRun {
		fmt.Fprintf(c.out, "Dry run: would create https://github.com/%s and open its staging pull request; nothing was written\n",
			found.Repository)
		return 0
	}
	name := spec.app
	r.spec = &changeSpec{kind: "new", app: name, env: "staging", settings: spec.settings, image: found.Image,
		preface: appPreface(found), requestedBy: spec.requestedBy, repository: repository}
	return c.change(r)
}

// appRepoInputs is what the use case needs: the contract, the tokens, the work folder, and the effects it uses.
func (c cli) appRepoInputs(r request, contract []byte, repository string) (orchestration.AppRepoInputs, int) {
	reposToken := c.environment(appReposTokenEnv)
	if reposToken == "" || reposToken == "REPLACE_ME" {
		return orchestration.AppRepoInputs{}, fail(c.diagnostic,
			errors.New("invalid: token: "+appReposTokenEnv+" is not set"))
	}
	owner := r.owner
	if owner == "" {
		owner, _, _ = strings.Cut(repository, "/")
	}
	work, err := os.MkdirTemp("", "app-repo-")
	if err != nil {
		return orchestration.AppRepoInputs{}, fail(c.diagnostic, errors.New("io: working folder: cannot create one"))
	}
	inputs := orchestration.AppRepoInputs{
		Name: r.spec.app, Type: r.appType, Stack: r.stack, Description: r.spec.description,
		Owner: owner, Identity: r.spec.requestedBy, PlatformRepository: repository, Settings: r.spec.settings,
		Resume: r.resume, DryRun: r.spec.dryRun, Contract: contract, Work: work,
		ReposToken: reposToken, HookToken: c.environment(appWebhookTokenEnv), HookHost: r.hookHost,
		Source: c.appRepoSource(), Renderer: c.appRepoRenderer(contract), Repos: c.appRepoWriter(),
		Images: c.appRepoImages(), Now: c.appRepoNow(), Sleep: c.sleeper(),
		Note: func(line string) { fmt.Fprintln(c.out, line) },
	}
	return inputs, 0
}

// appRepoSource is the GitHub reader a template's copier.yml is read through.
func (c cli) appRepoSource() github.Console {
	if c.consoleSource != nil {
		return c.consoleSource
	}
	return github.NewConsole(nil)
}

// appRepoRenderer is Copier, pinned by the stack catalogue's own version.
func (c cli) appRepoRenderer(contract []byte) copier.Renderer {
	if c.renderer != nil {
		return c.renderer
	}
	specifier := ""
	if found, err := orchestration.ReadStackContract(contract); err == nil {
		specifier = found.Copier
	}
	return copier.New(specifier)
}

// appRepoWriter is the GitHub repository write port.
func (c cli) appRepoWriter() github.RepositoryWriter {
	if c.repositoryWriter != nil {
		return c.repositoryWriter
	}
	return github.NewRepositoryWriter(nil)
}

// appRepoImages is the registry the first image is read from.
func (c cli) appRepoImages() func(string) (registry.Reader, error) {
	if c.registries != nil {
		return c.registries
	}
	return func(image string) (registry.Reader, error) { return registry.New(image, nil) }
}

// appRepoNow is the clock the wait for the first image is measured by.
func (c cli) appRepoNow() func() time.Time {
	if c.clock != nil {
		return c.clock
	}
	return time.Now
}

// sleeper is the wait between polls; a test supplies one that does not wait.
func (c cli) sleeper() func(time.Duration) {
	if c.sleep != nil {
		return c.sleep
	}
	return time.Sleep
}

// registerAppRepoFlags adds what a new app repository takes: its name and type, where it comes from, and the
// settings its staging instance starts with.
func (r *request) registerAppRepoFlags(flags *flag.FlagSet) {
	spec := &changeSpec{}
	r.spec = spec
	r.contractPath = "v2/contract/stacks.json"
	flags.Var(&once{value: &spec.app}, "app", "the new app's name")
	flags.Var(&once{value: &r.appType}, "type", "the app type: web-app, api or worker")
	flags.Var(&once{value: &r.stack}, "stack", "the stack (default: the type's first)")
	flags.Var(&once{value: &spec.description}, "description", "the repository's description")
	flags.Var(repeated{&spec.settings}, "set", "one setting for staging, name=value; team is required")
	flags.Var(&once{value: &spec.requestedBy}, "requested-by", "who asked for it, as the commit states them")
	flags.Var(&once{value: &spec.repository}, "repository", "owner/repository for the pull request (default: $CONSOLE_REPO)")
	flags.Var(&once{value: &r.owner}, "owner", "the account the repository is created under (default: the repository's owner)")
	flags.Var(&once{value: &r.hookHost}, "hook-host", "the host Flux's app-images Receiver listens on (default: no webhook)")
	flags.Var(&once{value: &r.contractPath}, "contract", "the stack catalogue (default: v2/contract/stacks.json)")
	flags.Var(&onceBool{value: &r.resume}, "resume", "find a repository an earlier job created; create nothing")
	flags.Var(&onceBool{value: &spec.dryRun}, "dry-run", "check and report the plan; write nothing to GitHub")
}

// appRepoNamed is what creating a new app's repository needs: the app's name and type, and who asked for it.
func (r *request) appRepoNamed() []string {
	if r.spec == nil || r.spec.app == "" || r.appType == "" || r.spec.requestedBy == "" {
		return []string{""}
	}
	return []string{r.spec.app, r.appType, r.spec.requestedBy}
}

// appPreface is what the staging pull request says before its commands: which repository it came from.
func appPreface(found orchestration.AppRepository) string {
	return "The repository " + found.URL + " was created from [the " + found.Stack + " template](https://github.com/" +
		found.Template + ") and published its first image " + found.Image + "@" + found.Digest + ".\n\n"
}
