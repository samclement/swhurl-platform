package orchestration_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/copier"
	"github.com/samclement/swhurl-platform/v2/adapters/copier/copiertest"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/registry/registrytest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

const (
	appRevision = "c28c7a0d1b08353f5238a150ef6950efe287077b"
	appHead     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	appCommit   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	appDigest   = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	appShort    = "bbbbbbb"
)

// appContract is the stack catalogue in miniature: one stack and one type, as v2/contract/stacks.json states them.
const appContract = `{"kind":"platform-stacks","version":1,"copier":"copier@9.18.2",
 "stacks":{"typescript":{"repository":"samclement/swhurl-app-template-typescript","revision":"` + appRevision + `","description":"Node and TypeScript."}},
 "types":{"web-app":{"title":"Web app","text":"A site people use.","stacks":["typescript"],
   "answers":{"kind":"web","database":"sqlite","api":"openapi","frontend":"vite"},"exposure":"authenticated-web"},
  "worker":{"title":"Worker","text":"No web address.","stacks":["typescript"],
   "answers":{"kind":"worker","database":"none","api":"none"},"exposure":"private"}}}`

const appQuestions = `app_name:
  type: str
description:
  type: str
kind:
  type: str
  choices: {web: web, worker: worker}
  default: web
database:
  type: str
  choices: [none, sqlite]
  default: none
api:
  type: str
  choices: [none, openapi]
  default: none
frontend:
  type: str
  choices: [none, vite]
  default: none
`

// appFixture is one new app repository's use case with every effect a fake: no network, no Copier, no clock.
type appFixture struct {
	inputs   orchestration.AppRepoInputs
	renderer *copiertest.Fake
	repos    *githubtest.RepositoryFake
	source   *githubtest.ConsoleFake
	images   *registrytest.Fake
	now      time.Time
	notes    []string
}

func newAppFixture(t *testing.T) *appFixture {
	t.Helper()
	f := &appFixture{now: time.Unix(0, 0), renderer: copiertest.New(),
		repos: githubtest.NewRepository(githubtest.RepositoryFixture{Repository: "samclement/notes",
			Missing: "samclement/taken", CreatedURL: "https://github.com/samclement/notes",
			Head: appHead, Commit: appCommit}),
		images: registrytest.New(publishedScenario("0-"+appShort, appDigest))}
	f.repos.Existing = nil
	f.source = githubtest.NewConsole(githubtest.Scenario())
	f.source.Commit = appRevision
	f.source.Public = map[string]string{"samclement/swhurl-app-template-typescript@" + appRevision +
		"/copier.yml": appQuestions}
	f.inputs = orchestration.AppRepoInputs{
		Name: "notes", Type: "web-app", Description: "Notes", Owner: "samclement",
		Identity: "sam@swhurl.com", PlatformRepository: "samclement/swhurl-platform",
		Settings: []string{"team=platform", "exposure=authenticated-web"}, Contract: []byte(appContract),
		Work: t.TempDir(), ReposToken: "repos-token", HookToken: "hook-token", HookHost: "flux-webhook.example.test",
		Source: f.source, Renderer: f.renderer, Repos: f.repos,
		Images: func(string) (registry.Reader, error) { return f.images, nil },
		Now:    func() time.Time { return f.now },
		Sleep:  func(d time.Duration) { f.now = f.now.Add(d) },
		Note:   func(line string) { f.notes = append(f.notes, line) },
	}
	return f
}

// publishedScenario is a registry holding one tag that resolves to one digest.
func publishedScenario(tag, digest string) registrytest.Scenario {
	return registrytest.Scenario{Tags: []string{tag},
		Images:  map[string]registry.Image{tag: {Digest: digest, MediaType: "application/vnd.oci.image.manifest.v1+json"}},
		Missing: "no-such-tag"}
}

func (f *appFixture) run(t *testing.T) orchestration.AppRepository {
	t.Helper()
	found, err := orchestration.AppRepo(context.Background(), f.inputs)
	if err != nil {
		t.Fatalf("app repo: %v", err)
	}
	return found
}

func (f *appFixture) refused(t *testing.T, contains string) {
	t.Helper()
	_, err := orchestration.AppRepo(context.Background(), f.inputs)
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("refusal = %v, want %q", err, contains)
	}
}

func TestAppRepoRendersCreatesPushesAndWaitsForTheSeedImage(t *testing.T) {
	f := newAppFixture(t)
	found := f.run(t)
	if found.Image != "ghcr.io/samclement/notes:0-"+appShort || found.Digest != appDigest {
		t.Fatalf("image = %q", found.Image)
	}
	if len(f.repos.Created) != 1 || f.repos.Created[0].Name != "notes" ||
		f.repos.Created[0].Description != "Notes" {
		t.Fatalf("created = %#v", f.repos.Created)
	}
	render := f.renderer.Requests[0]
	if render.Template != "https://github.com/samclement/swhurl-app-template-typescript.git" ||
		render.Revision != appRevision || render.Answers["kind"] != "web" ||
		render.Answers["frontend"] != "vite" || render.Answers["app_name"] != "notes" {
		t.Fatalf("render = %#v", render)
	}
	if len(f.repos.Firsts) != 1 || len(f.repos.Firsts[0].Files) != 1 ||
		f.repos.Firsts[0].Files[0].Path != "rendered.txt" || f.repos.Firsts[0].Parent != appHead {
		t.Fatalf("first commit = %#v", f.repos.Firsts)
	}
	if hook := f.repos.NewHooks[0]; hook.Secret != "hook-token" || !strings.HasPrefix(hook.URL, "https://flux-webhook.example.test/hook/") ||
		strings.Join(hook.Events, ",") != "package,registry_package" {
		t.Fatalf("hook = %#v", hook)
	}
	for _, wanted := range []string{"Created https://github.com/samclement/notes", "Added the image webhook", "Seeded "} {
		if !containsLine(f.notes, wanted) {
			t.Errorf("no line %q in %q", wanted, f.notes)
		}
	}
}

func containsLine(lines []string, wanted string) bool {
	for _, line := range lines {
		if strings.Contains(line, wanted) {
			return true
		}
	}
	return false
}

func TestAppRepoPrefersTheRunOverNothingButTheSeedOverTheRun(t *testing.T) {
	f := newAppFixture(t)
	f.images = registrytest.New(publishedScenario("1-"+appShort, appDigest))
	f.inputs.Images = func(string) (registry.Reader, error) { return f.images, nil }
	if found := f.run(t); found.Image != "ghcr.io/samclement/notes:1-"+appShort || found.Digest != appDigest {
		t.Fatalf("image = %q", found.Image)
	}
}

func TestAppRepoRefusesATakenNameBeforeAnythingIsRendered(t *testing.T) {
	f := newAppFixture(t)
	f.repos.Existing = []string{"samclement/notes"}
	f.refused(t, "already exists on GitHub")
	if len(f.renderer.Requests) != 0 || len(f.repos.Created) != 0 {
		t.Fatalf("a taken name rendered %#v and created %#v", f.renderer.Requests, f.repos.Created)
	}
}

func TestAppRepoResumeFindsTheRepositoryAndWritesNothingToIt(t *testing.T) {
	f := newAppFixture(t)
	f.repos.Existing = []string{"samclement/notes"}
	f.images = registrytest.New(publishedScenario("0-aaaaaaa", appDigest))
	f.inputs.Images = func(string) (registry.Reader, error) { return f.images, nil }
	f.inputs.Resume = true
	if found := f.run(t); found.Image == "" {
		t.Fatal("no image")
	}
	if len(f.renderer.Requests) != 0 || len(f.repos.Created) != 0 || len(f.repos.Firsts) != 0 || len(f.repos.NewHooks) != 0 {
		t.Fatalf("a resume wrote: %#v %#v", f.repos.Created, f.repos.Firsts)
	}
	if !containsLine(f.notes, "exists; its newest commit is "+"aaaaaaa") {
		t.Fatalf("notes = %q", f.notes)
	}
}

func TestAppRepoResumeRefusesARepositoryAnEarlierJobNeverCreated(t *testing.T) {
	f := newAppFixture(t)
	f.inputs.Resume = true
	f.refused(t, "does not exist on GitHub")
}

func TestAppRepoRefusesAnAnswerTheTemplateNoLongerOffers(t *testing.T) {
	f := newAppFixture(t)
	f.source.Public["samclement/swhurl-app-template-typescript@"+appRevision+"/copier.yml"] =
		strings.Replace(appQuestions, "choices: [none, vite]", "choices: [none]", 1)
	f.refused(t, "no longer offers frontend=vite")
	if len(f.renderer.Requests) != 0 || len(f.repos.Created) != 0 {
		t.Fatal("a refused answer rendered or created anything")
	}
}

func TestAppRepoRefusesASettingWithoutATeam(t *testing.T) {
	f := newAppFixture(t)
	f.inputs.Settings = []string{"exposure=private"}
	f.refused(t, "needs its team")
}

func TestAppRepoRefusesANameThatIsNotADNSLabel(t *testing.T) {
	for _, name := range []string{"", "Notes", "-notes", "notes-", strings.Repeat("a", 41)} {
		f := newAppFixture(t)
		f.inputs.Name = name
		f.refused(t, "name")
	}
	f := newAppFixture(t)
	f.inputs.Name = "notify"
	f.refused(t, "reserved for the platform notification service")
}

func TestAppRepoRefusesATypeOrStackTheCatalogueDoesNotState(t *testing.T) {
	f := newAppFixture(t)
	f.inputs.Type = "job"
	f.refused(t, "type must be one of")
	f = newAppFixture(t)
	f.inputs.Stack = "kotlin"
	f.refused(t, "stack must be one of")
}

func TestAppRepoDryRunWritesNothingToGitHub(t *testing.T) {
	f := newAppFixture(t)
	f.inputs.DryRun = true
	if found := f.run(t); found.Repository != "samclement/notes" {
		t.Fatalf("found = %#v", found)
	}
	if len(f.renderer.Requests) != 0 || len(f.repos.Created) != 0 || len(f.repos.Firsts) != 0 || len(f.repos.NewHooks) != 0 {
		t.Fatal("a dry run wrote to GitHub")
	}
	if !containsLine(f.notes, "Plan (app-repo samclement/notes)") {
		t.Fatalf("notes = %q", f.notes)
	}
}

func TestAppRepoWaitsForTheInitialCommitGitHubMakes(t *testing.T) {
	f := newAppFixture(t)
	f.repos.HeadMissing = 2
	f.run(t)
	if f.repos.HeadCalls != 3 {
		t.Fatalf("head reads = %d", f.repos.HeadCalls)
	}
}

func TestAppRepoRefusesWhenNoImageAppears(t *testing.T) {
	f := newAppFixture(t)
	f.images = registrytest.New(registrytest.Scenario{Missing: "none"})
	f.inputs.Images = func(string) (registry.Reader, error) { return f.images, nil }
	f.refused(t, "no image after 10 minutes")
	f.refused(t, "github.com/samclement/notes/actions")
}

func TestAppRepoAClosedWebhookIsAWarningNotAFailure(t *testing.T) {
	f := newAppFixture(t)
	f.repos.FailAt = "createhook"
	if found := f.run(t); found.Image == "" {
		t.Fatal("no image")
	}
	if !containsLine(f.notes, "Warning: no image webhook") {
		t.Fatalf("notes = %q", f.notes)
	}
}

func TestReadStackContractReadsTheRealCatalogue(t *testing.T) {
	document, err := os.ReadFile("../contract/stacks.json")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := orchestration.ReadStackContract(document)
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	if contract.Copier != "copier@9.18.2" {
		t.Fatalf("copier = %q", contract.Copier)
	}
	stack, found := contract.Stack("typescript")
	if !found || len(stack.Revision) != 40 || stack.Repository == "" {
		t.Fatalf("typescript = %#v", stack)
	}
	worker, found := contract.Type("worker")
	if !found || len(worker.Stacks) != 2 || worker.Exposure != "private" {
		t.Fatalf("worker = %#v", worker)
	}
}

var _ = copier.Question{}
