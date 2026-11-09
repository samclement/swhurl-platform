package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/copier/copiertest"
	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

const appHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// appRepoTag is the seed a repository's first run copies: the tag names the commit the create's revision label does.
var appRepoTag = "0-" + createCommit[:7]

// appRepoSources is a new app's command with every effect a fake: the platform repository a change downloads, the
// console's publisher, the repository write port, Copier, and the registry its first image is read from.
func appRepoSources(t *testing.T) (*githubtest.ConsoleFake, *githubtest.PublisherFake, *githubtest.RepositoryFake, *registrytest.Fake) {
	t.Helper()
	source, publisher := changeSources(t)
	contractDocument, err := os.ReadFile("../../contract/stacks.json")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := orchestration.ReadStackContract(contractDocument)
	if err != nil {
		t.Fatal(err)
	}
	stack, found := contract.Stack("typescript")
	if !found {
		t.Fatal("the catalogue has no typescript stack")
	}
	questions, err := os.ReadFile("../../adapters/copier/testdata/copier.yml")
	if err != nil {
		t.Fatal(err)
	}
	source.Public = map[string]string{stack.Repository + "@" + stack.Revision + "/copier.yml": string(questions)}
	repos := githubtest.NewRepository(githubtest.RepositoryFixture{Repository: "samclement/notes",
		Missing: "samclement/taken", CreatedURL: "https://github.com/samclement/notes", Head: appHead, Commit: createCommit})
	repos.Existing = nil
	images := registrytest.New(registrytest.Scenario{Tags: []string{appRepoTag},
		Images: map[string]registry.Image{appRepoTag: {Digest: createDigest,
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Labels: map[string]string{"platform.swhurl.com/declaration": createLabel,
				"org.opencontainers.image.revision": createCommit}}},
		Missing: "no-such-tag"})
	return source, publisher, repos, images
}

// appRepoRun runs the command as the console does, with the tokens and repository in the environment.
func appRepoRun(t *testing.T, repos *githubtest.RepositoryFake, images *registrytest.Fake, env map[string]string,
	args ...string) (int, string, string, *githubtest.PublisherFake) {
	t.Helper()
	source, publisher, _, _ := appRepoSources(t)
	var stdout, diagnostic bytes.Buffer
	c := cli{readFile: os.ReadFile, writer: files.Disk{}, consoleSource: source, publisher: publisher,
		repositoryWriter: repos, renderer: copiertest.New(),
		registries: func(string) (registry.Reader, error) { return images, nil },
		seed:       func() ([32]byte, error) { return [32]byte{3}, nil },
		encrypter:  sopstest.New(),
		sleep:      func(time.Duration) {},
		stdin:      strings.NewReader(""),
		getenv: func(name string) string {
			if value, found := env[name]; found {
				return value
			}
			return ""
		}}
	status := runWith(args, &stdout, &diagnostic, c)
	return status, stdout.String(), diagnostic.String(), publisher
}

func appRepoArgs(args ...string) []string {
	return append([]string{"app-repo", "--app", "notes", "--type", "web-app", "--stack", "typescript",
		"--description", "Notes", "--set", "team=demo", "--set", "exposure=authenticated-web",
		"--requested-by", "operator@example.test", "--repository", "owner/repo", "--owner", "samclement",
		"--hook-host", "flux-webhook.example.test", "--contract", "../../contract/stacks.json"}, args...)
}

func appRepoTokens() map[string]string {
	return map[string]string{consoleTokenEnv: "fixture-token", appReposTokenEnv: "repos-token",
		appWebhookTokenEnv: "hook-token"}
}

// A new app's command renders, creates and waits, then opens the ordinary staging pull request whose create line
// names the image the repository published — never a digest, which create reads from the registry itself.
func TestAppRepoCLIOpensTheStagingPullRequest(t *testing.T) {
	_, _, repos, images := appRepoSources(t)
	status, stdout, diagnostic, publisher := appRepoRun(t, repos, images, appRepoTokens(), appRepoArgs()...)
	if status != 0 {
		t.Fatalf("app-repo exited %d: %s%s", status, diagnostic, stdout)
	}
	if len(repos.Created) != 1 || repos.Created[0].Name != "notes" {
		t.Fatalf("created = %#v", repos.Created)
	}
	if len(repos.NewHooks) != 1 || repos.NewHooks[0].Secret != "hook-token" {
		t.Fatalf("hooks = %#v", repos.NewHooks)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if lines[len(lines)-1] != "Opened "+githubtest.PublisherScenario().PullURL {
		t.Errorf("the command ended %q, want the pull request's URL", lines[len(lines)-1])
	}
	for _, wanted := range []string{"Created https://github.com/samclement/notes", "Added the image webhook", "Seeded "} {
		if !strings.Contains(stdout, wanted) {
			t.Errorf("no line %q in:\n%s", wanted, stdout)
		}
	}
	if len(publisher.Pulls) != 1 {
		t.Fatalf("pull requests = %#v", publisher.Pulls)
	}
	pull := publisher.Pulls[0]
	if pull.Title != "[console] v2-records: add notes/staging" {
		t.Errorf("title = %q", pull.Title)
	}
	if !strings.Contains(pull.Body, "--image ghcr.io/samclement/notes:"+appRepoTag+" ") {
		t.Errorf("the create line does not name the published image:\n%s", pull.Body)
	}
	if !strings.HasPrefix(pull.Body, "The repository https://github.com/samclement/notes was created from [the typescript template]") {
		t.Errorf("the body does not say what made the repository:\n%s", pull.Body)
	}
}

func TestAppRepoCLIRefusesWithoutTheRepositoryToken(t *testing.T) {
	_, _, repos, images := appRepoSources(t)
	env := appRepoTokens()
	delete(env, appReposTokenEnv)
	status, _, diagnostic, _ := appRepoRun(t, repos, images, env, appRepoArgs()...)
	if status == 0 || !strings.Contains(diagnostic, appReposTokenEnv) {
		t.Fatalf("exit %d, diagnostic %q", status, diagnostic)
	}
	if len(repos.Created) != 0 {
		t.Fatal("a command without a token created a repository")
	}
}

func TestAppRepoCLIDryRunWritesNothing(t *testing.T) {
	_, _, repos, images := appRepoSources(t)
	status, stdout, diagnostic, _ := appRepoRun(t, repos, images, appRepoTokens(), appRepoArgs("--dry-run")...)
	if status != 0 {
		t.Fatalf("app-repo exited %d: %s%s", status, diagnostic, stdout)
	}
	if !strings.Contains(stdout, "Dry run: would create https://github.com/samclement/notes") {
		t.Errorf("stdout = %q", stdout)
	}
	if len(repos.Created) != 0 || len(repos.Firsts) != 0 {
		t.Fatal("a dry run wrote to GitHub")
	}
}
