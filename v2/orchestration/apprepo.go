package orchestration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/copier"
	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// A new app's repository: render one stack template into a folder of our own, create the public repository with the
// rendered tree as its first commit, add the image webhook, and wait for the first image the repository publishes.
// The publication that adds the app to staging is the ordinary `change` after this, which the command composes.
//
// The stack catalogue and each template's own copier.yml decide what a type renders; this use case decides only that
// the answers are the template's, that the name is free (or, resuming, that it is taken) and which image is first.

const (
	// appImageHost is where an app repository's workflow publishes: GHCR, as the cluster pulls anonymously.
	appImageHost = "ghcr.io"
	// firstImageTimeout and imagePoll bound the wait for the first image: a build is about two minutes.
	firstImageTimeout = 10 * time.Minute
	imagePoll         = 5 * time.Second
	// headAttempts and headPoll bound the wait for the initial commit GitHub makes for a new repository.
	headAttempts = 10
	headPoll     = time.Second
	// reservedAppNames may not be an app: the notification service owns the name.
	reservedAppNames = "notify"
)

var appNamePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Stack is one entry of v2/contract/stacks.json: its template and the commit that template is pinned at.
type Stack struct{ Name, Repository, Revision, Description string }

// Named is one answer a type sets.
type Named struct{ Name, Value string }

// AppType is one type of app: the stacks it builds and every template answer it sets.
type AppType struct {
	Key      string
	Stacks   []string
	Answers  []Named
	Exposure string
}

// StackContract is v2/contract/stacks.json as a new app needs it.
type StackContract struct {
	Copier string
	Stacks []Stack
	Types  []AppType
}

// ReadStackContract reads the stack catalogue, keeping the file's order: a type's first stack is its default.
func ReadStackContract(data []byte) (StackContract, error) {
	document, err := decodeOrdered(data)
	if err != nil {
		return StackContract{}, &Refusal{"app-repo", "the stack catalogue cannot be read: " + err.Error()}
	}
	found := StackContract{Copier: scalarText(document.member("copier"))}
	for _, field := range document.member("stacks").fields {
		found.Stacks = append(found.Stacks, Stack{Name: field.Name,
			Repository:  scalarText(field.Value.member("repository")),
			Revision:    scalarText(field.Value.member("revision")),
			Description: scalarText(field.Value.member("description"))})
	}
	for _, field := range document.member("types").fields {
		appType := AppType{Key: field.Name, Exposure: scalarText(field.Value.member("exposure"))}
		for _, stack := range field.Value.member("stacks").items {
			appType.Stacks = append(appType.Stacks, scalarText(stack))
		}
		for _, answer := range field.Value.member("answers").fields {
			appType.Answers = append(appType.Answers, Named{answer.Name, scalarText(answer.Value)})
		}
		found.Types = append(found.Types, appType)
	}
	return found, nil
}

// Stack reads one stack by name.
func (c StackContract) Stack(name string) (Stack, bool) {
	for _, stack := range c.Stacks {
		if stack.Name == name {
			return stack, stack.Repository != "" && stack.Revision != ""
		}
	}
	return Stack{}, false
}

// Type reads one app type by name.
func (c StackContract) Type(key string) (AppType, bool) {
	for _, appType := range c.Types {
		if appType.Key == key {
			return appType, len(appType.Stacks) > 0
		}
	}
	return AppType{}, false
}

// AppRepoInputs is one new app repository: what to make, where, with which credentials, and the effects it uses.
// Work is a scratch directory the command creates and removes; nothing outside it is written.
type AppRepoInputs struct {
	Name, Type, Stack, Description         string
	Owner, Identity, PlatformRepository    string
	Settings                               []string
	Resume, DryRun                         bool
	Contract                               []byte
	Work                                   string
	Token, ReposToken, HookToken, HookHost string
	Source                                 github.Console
	Renderer                               copier.Renderer
	Repos                                  github.RepositoryWriter
	Images                                 func(image string) (registry.Reader, error)
	Now                                    func() time.Time
	Sleep                                  func(time.Duration)
	Note                                   func(string)
}

// AppRepository is what a new app's repository published: the image the publication starts from, and the repository
// it came from.
type AppRepository struct {
	Image, Digest, Repository, Commit, URL string
	Stack, Template                        string // the stack it was rendered from
	Created                                bool
}

// AppRepo renders and creates one app repository, or (--resume) finds the one an earlier job created, and returns
// the image its first run published. A dry run decides and reports, and writes nothing to GitHub.
func AppRepo(ctx context.Context, inputs AppRepoInputs) (AppRepository, error) {
	name, appType, stack, err := appTarget(inputs)
	if err != nil {
		return AppRepository{}, err
	}
	answers, questions, err := appAnswers(ctx, inputs, appType, stack)
	if err != nil {
		return AppRepository{}, err
	}
	if err := offeredAnswers(appType, questions, stack.Name); err != nil {
		return AppRepository{}, err
	}
	repository := inputs.Owner + "/" + inputs.Name
	created, err := repositoryState(ctx, inputs, repository)
	if err != nil {
		return AppRepository{}, err
	}
	found := AppRepository{Repository: repository, Image: appImage(inputs.Owner, name), URL: "https://github.com/" + repository,
		Stack: stack.Name, Template: stack.Repository, Created: created}
	if inputs.DryRun {
		inputs.Note("Plan (app-repo " + repository + "):")
		inputs.Note("  - render " + stack.Repository + " (" + stack.Name + ") with " + answersLine(answers))
		inputs.Note("  - create the public repository " + repository + " with the rendered tree as its first commit")
		inputs.Note("  - add the image webhook, wait for its first image, then open the staging pull request")
		return found, nil
	}
	if found.Commit, err = publishRepository(ctx, inputs, stack, answers, created); err != nil {
		return AppRepository{}, err
	}
	tag, digest, err := waitForImage(ctx, inputs, found.Image, found.Commit)
	if err != nil {
		return AppRepository{}, err
	}
	found.Image, found.Digest = found.Image+":"+tag, digest
	return found, nil
}

// appTarget checks the name and reads the type and stack the contract states, so a refused name creates nothing.
func appTarget(inputs AppRepoInputs) (string, AppType, Stack, error) {
	contract, err := ReadStackContract(inputs.Contract)
	if err != nil {
		return "", AppType{}, Stack{}, err
	}
	name := strings.TrimSpace(inputs.Name)
	if !appNamePattern.MatchString(name) {
		return "", AppType{}, Stack{}, &Refusal{"app-repo", "name must be a DNS label: lowercase letters, digits and hyphens, at most 40 characters"}
	}
	if name == reservedAppNames {
		return "", AppType{}, Stack{}, &Refusal{"app-repo", name + " is reserved for the platform notification service"}
	}
	appType, known := contract.Type(inputs.Type)
	if !known {
		return "", AppType{}, Stack{}, &Refusal{"app-repo", "type must be one of " + contract.typeNames()}
	}
	stackName := strings.TrimSpace(inputs.Stack)
	if stackName == "" {
		stackName = appType.Stacks[0]
	}
	stack, known := contract.Stack(stackName)
	if !known || !slices.Contains(appType.Stacks, stackName) {
		return "", AppType{}, Stack{}, &Refusal{"app-repo", "stack must be one of " + strings.Join(appType.Stacks, ", ") + " for " + inputs.Type}
	}
	return name, appType, stack, nil
}

// typeNames is the contract's type names, for a refusal.
func (c StackContract) typeNames() string {
	keys := make([]string, 0, len(c.Types))
	for _, appType := range c.Types {
		keys = append(keys, appType.Key)
	}
	return strings.Join(keys, ", ")
}

// appAnswers is the render's answers and the questions the template asks: the type sets every answer, the template
// says which it still offers.
func appAnswers(ctx context.Context, inputs AppRepoInputs, appType AppType, stack Stack) (map[string]string, []copier.Question, error) {
	if !hasTeam(inputs.Settings) {
		return nil, nil, &Refusal{"app-repo", "a new app needs its team: --set team=<team>"}
	}
	answers := map[string]string{"app_name": strings.TrimSpace(inputs.Name)}
	if description := strings.TrimSpace(inputs.Description); description != "" {
		answers["description"] = description
	}
	for _, answer := range appType.Answers {
		answers[answer.Name] = answer.Value
	}
	document, found, err := inputs.Source.PublicFile(ctx, stack.Repository, stack.Revision, "copier.yml")
	if err != nil {
		return nil, nil, &Refusal{"app-repo", "could not read the " + stack.Name + " template's questions (" +
			stack.Repository + "/copier.yml): " + err.Error()}
	}
	if !found {
		return nil, nil, &Refusal{"app-repo", "the " + stack.Name + " template has no copier.yml at " + stack.Revision}
	}
	questions, err := copier.Questions(document)
	if err != nil {
		return nil, nil, &Refusal{"app-repo", "could not read the " + stack.Name + " template's questions: " + err.Error()}
	}
	return answers, questions, nil
}

// hasTeam is whether the change's settings name the app's team.
func hasTeam(settings []string) bool {
	return slices.ContainsFunc(settings, func(setting string) bool {
		return strings.HasPrefix(setting, "team=") && len(strings.TrimPrefix(setting, "team=")) > 0
	})
}

// offeredAnswers refuses a type answer the template no longer offers, before a repository exists for it.
func offeredAnswers(appType AppType, questions []copier.Question, stack string) error {
	for _, answer := range appType.Answers {
		for _, question := range questions {
			if question.Name == answer.Name && !slices.Contains(question.Choices, answer.Value) {
				return &Refusal{"app-repo", "the " + stack + " template no longer offers " + answer.Name + "=" +
					answer.Value + " (it offers " + strings.Join(question.Choices, ", ") + "): the " + appType.Key + " type needs updating"}
			}
		}
	}
	return nil
}

// repositoryState refuses a taken name, or (resuming) a repository an earlier job never created.
func repositoryState(ctx context.Context, inputs AppRepoInputs, repository string) (bool, error) {
	exists, err := inputs.Repos.Exists(ctx, repository, inputs.ReposToken)
	if err != nil {
		return false, err
	}
	if exists && !inputs.Resume {
		return false, &Refusal{"app-repo", "Repository " + repository + " already exists on GitHub. Setup stopped before creating a new " +
			"repository. Choose another name and retry"}
	}
	if !exists && inputs.Resume {
		return false, &Refusal{"app-repo", repository + " does not exist on GitHub: the earlier job stopped before creating it. " +
			"Start again from New app (nothing was created)"}
	}
	return exists, nil
}

// publishRepository renders, creates and pushes the first commit, and adds the image webhook.
func publishRepository(ctx context.Context, inputs AppRepoInputs, stack Stack, answers map[string]string,
	existing bool) (string, error) {
	if existing {
		commit, err := waitForHead(ctx, inputs, inputs.Owner+"/"+inputs.Name)
		if err != nil {
			return "", err
		}
		inputs.Note(inputs.Owner + "/" + inputs.Name + " exists; its newest commit is " + firstSeven(commit) +
			". Waiting for its first image")
		return commit, nil
	}
	dest := filepath.Join(inputs.Work, inputs.Name)
	inputs.Note("Rendering " + stack.Repository + " (" + stack.Name + ") for " + inputs.Name + " with " + answersLine(answers))
	if err := inputs.Renderer.Render(ctx, copier.Request{Template: "https://github.com/" + stack.Repository + ".git",
		Revision: stack.Revision, Answers: answers, Dest: dest}); err != nil {
		return "", err
	}
	tree, err := files.Snapshot(dest)
	if err != nil {
		return "", err
	}
	for _, file := range tree {
		inputs.Note("A " + file.Path)
	}
	url, err := inputs.Repos.Create(ctx, inputs.ReposToken, inputs.Owner, inputs.Name, appDescription(inputs.Description, stack))
	if err != nil {
		return "", err
	}
	inputs.Note("Created " + url)
	repository := inputs.Owner + "/" + inputs.Name
	head, err := waitForHead(ctx, inputs, repository)
	if err != nil {
		return "", err
	}
	commit, err := inputs.Repos.FirstCommit(ctx, repository, inputs.ReposToken, head, appCommitMessage(inputs, stack), newFiles(tree))
	if err != nil {
		return "", err
	}
	inputs.Note("Pushed " + firstSeven(commit) + "; waiting for its first image")
	addImageHook(ctx, inputs, repository)
	return commit, nil
}

// appDescription is a repository's description: the form's, or one from the stack.
func appDescription(description string, stack Stack) string {
	if found := strings.TrimSpace(description); found != "" {
		return found
	}
	return "A swhurl app (" + stack.Name + ")"
}

// appCommitMessage is the first commit's message: what was rendered and who asked for it.
func appCommitMessage(inputs AppRepoInputs, stack Stack) string {
	return "Start " + inputs.Name + " from " + stack.Repository + "\n\nRequested-by: " + inputs.Identity + "\n"
}

// newFiles is a rendered tree as the first commit's files.
func newFiles(tree []files.TreeFile) []github.NewFile {
	found := make([]github.NewFile, 0, len(tree))
	for _, file := range tree {
		found = append(found, github.NewFile{Path: file.Path, Executable: file.Executable, Body: file.Body})
	}
	return found
}

// answersLine is the render's answers as one line, so a job's log says what was chosen.
func answersLine(answers map[string]string) string {
	names := make([]string, 0, len(answers))
	for name := range answers {
		names = append(names, name)
	}
	sort.Strings(names)
	chosen := make([]string, 0, len(names))
	for _, name := range names {
		chosen = append(chosen, name+"="+answers[name])
	}
	return strings.Join(chosen, " ")
}

// waitForHead waits for the initial commit GitHub makes when it creates a repository.
func waitForHead(ctx context.Context, inputs AppRepoInputs, repository string) (string, error) {
	for attempt := 0; attempt < headAttempts; attempt++ {
		head, err := inputs.Repos.Head(ctx, repository, inputs.ReposToken)
		if err == nil {
			return head, nil
		}
		if !errors.Is(err, github.ErrNoMainBranch) {
			return "", err
		}
		if attempt < headAttempts-1 {
			inputs.Sleep(headPoll)
		}
	}
	return "", &Refusal{"app-repo", repository + " has no main branch yet; its first commit did not appear"}
}

// addImageHook adds the webhook that tells Flux when an image is published. A failure is a warning: the hourly scan
// still finds the image, and make app-hooks adds the webhook later.
func addImageHook(ctx context.Context, inputs AppRepoInputs, repository string) {
	if inputs.HookToken == "" || inputs.HookHost == "" {
		inputs.Note("Warning: no image webhook (IMAGE_WEBHOOK_TOKEN is not set); new images are found within the hour. " +
			"Add it with make app-hooks")
		return
	}
	hook := github.RepositoryHook{URL: kernel.ImageHookURL(inputs.HookToken, inputs.HookHost), Secret: inputs.HookToken,
		Events: kernel.ImageHookEvents(), Active: true}
	if err := inputs.Repos.CreateHook(ctx, repository, inputs.ReposToken, hook); err != nil {
		inputs.Note("Warning: no image webhook (" + err.Error() + "); new images are found within the hour. Add it with make app-hooks")
		return
	}
	inputs.Note("Added the image webhook: Flux scans as soon as an image is published")
}

// appImage is the image repository an app's workflow publishes to.
func appImage(owner, name string) string {
	return appImageHost + "/" + strings.ToLower(owner) + "/" + name
}

// waitForImage waits for the app's own first image the repository publishes and names the tag and digest it found.
// The template's seed (`0-<commit>`) is a copied build whose label states the template's commit, not this app's,
// so it is no release the platform can create from.
func waitForImage(ctx context.Context, inputs AppRepoInputs, image, commit string) (string, string, error) {
	reader, err := inputs.Images(image)
	if err != nil {
		return "", "", err
	}
	deadline, short := inputs.Now().Add(firstImageTimeout), firstSeven(commit)
	for {
		if tag, found := imageTag(ctx, reader, short); found {
			if described, err := reader.Describe(ctx, tag); err == nil && described.Digest != "" {
				inputs.Note("Published " + image + ":" + tag + "@" + described.Digest)
				return tag, described.Digest, nil
			}
		}
		if inputs.Now().After(deadline) {
			return "", "", &Refusal{"app-repo", fmt.Sprintf("no image after %d minutes: see https://github.com/%s/actions; "+
				"fix the app and push, then start the app again from New app", int(firstImageTimeout.Minutes()),
				inputs.Owner+"/"+inputs.Name)}
		}
		inputs.Sleep(imagePoll)
	}
}

// imageTag is the tag the repository publishes for this commit: the highest run's. Run 0 is the template's seed,
// a copied image, so no release states it.
func imageTag(ctx context.Context, reader registry.Reader, commit string) (string, bool) {
	tags, err := reader.Tags(ctx)
	if err != nil {
		return "", false
	}
	best, bestRun, found := "", int64(0), false
	for _, tag := range tags {
		run, sha, ok := strings.Cut(tag, "-")
		if !ok || sha != commit {
			continue
		}
		number, err := strconv.ParseInt(run, 10, 64)
		if err != nil || number < 1 {
			continue
		}
		if !found || number > bestRun {
			best, bestRun, found = tag, number, true
		}
	}
	return best, found
}

// firstSeven is the seven characters a branch and a tag name a commit by.
func firstSeven(revision string) string {
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}
