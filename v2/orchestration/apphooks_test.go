package orchestration_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

const (
	hookToken = "fixture-image-webhook-token"
	hookHost  = "flux-webhook.example.test"
	ghToken   = "fixture-gh-token"
)

// hookRepos is the repository write port with each repository's own webhooks: it lists what a test stated per
// repository, refuses a listing or a write for the repository a test names, and keeps each write with its repository.
type hookRepos struct {
	*githubtest.RepositoryFake
	listed   map[string][]github.Hook
	refuse   map[string]string // repository: "hooks", "createhook" or "updatehook"
	writes   []string
	tokensOK bool
}

func newHookRepos(listed map[string][]github.Hook) *hookRepos {
	return &hookRepos{RepositoryFake: githubtest.NewRepository(githubtest.RepositoryScenario()), listed: listed,
		refuse: map[string]string{}, tokensOK: true}
}

func (r *hookRepos) Hooks(_ context.Context, repository, token string) ([]github.Hook, error) {
	r.tokensOK = r.tokensOK && token == ghToken
	if r.refuse[repository] == "hooks" {
		return nil, &github.Error{Reason: "GET /repos/" + repository + "/hooks: HTTP 404: Not Found"}
	}
	return r.listed[repository], nil
}

func (r *hookRepos) CreateHook(ctx context.Context, repository, token string, hook github.RepositoryHook) error {
	r.tokensOK = r.tokensOK && token == ghToken
	if r.refuse[repository] == "createhook" {
		return &github.Error{Reason: "HTTP 422: refused by the fixture"}
	}
	r.writes = append(r.writes, "create "+repository)
	return r.RepositoryFake.CreateHook(ctx, repository, token, hook)
}

func (r *hookRepos) UpdateHook(ctx context.Context, repository, token string, id int, hook github.RepositoryHook) error {
	r.tokensOK = r.tokensOK && token == ghToken
	if r.refuse[repository] == "updatehook" {
		return &github.Error{Reason: "HTTP 422: refused by the fixture"}
	}
	r.writes = append(r.writes, "update "+repository)
	return r.RepositoryFake.UpdateHook(ctx, repository, token, id, hook)
}

// hookRecord is a staging release record publishing image from repository.
func hookRecord(application, image, repository string) []byte {
	return []byte(`{"schemaVersion": 1, "application": "` + application + `",
  "source": {"repository": "` + repository + `", "commit": "` + strings.Repeat("b", 40) + `"},
  "artifact": {"image": "` + image + `", "digest": "sha256:` + strings.Repeat("b", 64) + `",
    "mediaType": "application/vnd.oci.image.manifest.v1+json", "platform": "linux/amd64"},
  "runtime": {"kind": "web", "port": 8080, "healthPath": "/healthz", "uid": 65532}}`)
}

func hookInputs(repos github.RepositoryWriter, staging map[string][]byte) orchestration.AppHooksInputs {
	return orchestration.AppHooksInputs{Staging: staging, Owner: "samclement", HookHost: hookHost,
		GitHubToken: ghToken, HookToken: hookToken, Repos: repos}
}

// Each repository gets the change its webhooks need: created, repointed or left alone, in name order, and every
// write states the URL, the image events, the token as its secret and active.
func TestAppHooksCreatesRepointsAndLeavesAlone(t *testing.T) {
	want := kernel.ImageHookURL(hookToken, hookHost)
	repos := newHookRepos(map[string][]github.Hook{
		"samclement/created": {{ID: 1, Active: true, Events: []string{"push"}, URL: "https://elsewhere.example.test/x"}},
		"samclement/rotated": {{ID: 7, Active: true, Events: []string{"package", "registry_package"},
			URL: kernel.ImageHookURL("old", hookHost)}},
		"samclement/correct": {{ID: 9, Active: true, Events: []string{"registry_package", "package"}, URL: want}},
	})
	staging := map[string][]byte{
		"rotated": hookRecord("rotated", "ghcr.io/samclement/rotated", "samclement/rotated"),
		"created": hookRecord("created", "ghcr.io/samclement/created", "samclement/created"),
		"correct": hookRecord("correct", "ghcr.io/samclement/correct", "samclement/correct"),
	}
	outcomes, err := orchestration.AppHooks(context.Background(), hookInputs(repos, staging))
	if err != nil {
		t.Fatal(err)
	}
	wanted := []orchestration.AppHookOutcome{
		{Application: "correct", Repository: "samclement/correct", State: kernel.ImageHookInPlace},
		{Application: "created", Repository: "samclement/created", State: kernel.ImageHookMissing},
		{Application: "rotated", Repository: "samclement/rotated", State: kernel.ImageHookStale},
	}
	if !reflect.DeepEqual(outcomes, wanted) {
		t.Fatalf("outcomes = %#v", outcomes)
	}
	if !reflect.DeepEqual(repos.writes, []string{"create samclement/created", "update samclement/rotated"}) {
		t.Fatalf("writes = %v", repos.writes)
	}
	written := []github.RepositoryHook{repos.NewHooks[0], repos.Updated[0].Hook}
	for _, hook := range written {
		if hook.URL != want || hook.Secret != hookToken || !hook.Active ||
			!reflect.DeepEqual(hook.Events, []string{"package", "registry_package"}) {
			t.Fatalf("hook = %#v", hook)
		}
	}
	if repos.Updated[0].ID != 7 || !repos.tokensOK {
		t.Fatalf("updated %d; GitHub token used for every call: %v", repos.Updated[0].ID, repos.tokensOK)
	}
}

// A dry run lists and decides each repository, and writes nothing.
func TestAppHooksDryRunWritesNothing(t *testing.T) {
	repos := newHookRepos(map[string][]github.Hook{"samclement/rotated": {{ID: 7, Active: false,
		Events: []string{"package", "registry_package"}, URL: kernel.ImageHookURL(hookToken, hookHost)}}})
	inputs := hookInputs(repos, map[string][]byte{
		"rotated": hookRecord("rotated", "ghcr.io/samclement/rotated", "samclement/rotated"),
		"created": hookRecord("created", "ghcr.io/samclement/created", "samclement/created"),
	})
	inputs.DryRun = true
	outcomes, err := orchestration.AppHooks(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].State != kernel.ImageHookMissing || outcomes[1].State != kernel.ImageHookStale ||
		outcomes[0].Problem != "" || outcomes[1].Problem != "" {
		t.Fatalf("outcomes = %#v", outcomes)
	}
	if len(repos.writes) != 0 || len(repos.NewHooks) != 0 || len(repos.Updated) != 0 {
		t.Fatalf("a dry run wrote: %v", repos.writes)
	}
}

// A malformed record, a listing GitHub refuses and a refused write are each that app's problem; the other apps still
// run, and an app publishing another image is not a candidate at all.
func TestAppHooksOneAppsFailureIsItsOwn(t *testing.T) {
	repos := newHookRepos(nil)
	repos.refuse["samclement/unlisted"] = "hooks"
	repos.refuse["samclement/refused"] = "createhook"
	outcomes, err := orchestration.AppHooks(context.Background(), hookInputs(repos, map[string][]byte{
		"broken":   []byte(`{"schemaVersion": 1`),
		"unlisted": hookRecord("unlisted", "ghcr.io/samclement/unlisted", "samclement/unlisted"),
		"refused":  hookRecord("refused", "ghcr.io/samclement/refused", "samclement/refused"),
		"pinned":   hookRecord("pinned", "ghcr.io/other/pinned", "samclement/pinned"),
		"zeta":     hookRecord("zeta", "ghcr.io/samclement/zeta", "samclement/zeta"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	problems := map[string]string{}
	for _, outcome := range outcomes {
		problems[outcome.Application] = outcome.Problem
	}
	if len(outcomes) != 4 || problems["zeta"] != "" || outcomes[3].State != kernel.ImageHookMissing ||
		!strings.HasPrefix(problems["broken"], "the staging release record cannot be read") ||
		problems["unlisted"] != "cannot list the webhooks of samclement/unlisted: GitHub: GET /repos/samclement/unlisted/hooks: HTTP 404: Not Found" ||
		problems["refused"] != "cannot keep the image webhook on samclement/refused: GitHub: HTTP 422: refused by the fixture" {
		t.Fatalf("outcomes = %#v", outcomes)
	}
	if !reflect.DeepEqual(repos.writes, []string{"create samclement/zeta"}) {
		t.Fatalf("writes = %v", repos.writes)
	}
	for _, outcome := range outcomes {
		if strings.Contains(outcome.Problem, hookToken) || strings.Contains(outcome.Problem, ghToken) {
			t.Fatalf("a problem carries a token: %q", outcome.Problem)
		}
	}
}

// Without either token, the host or the owner nothing is listed.
func TestAppHooksRefusesWithoutItsInputs(t *testing.T) {
	staging := map[string][]byte{"notes": hookRecord("notes", "ghcr.io/samclement/notes", "samclement/notes")}
	for name, change := range map[string]func(*orchestration.AppHooksInputs){
		"github token": func(i *orchestration.AppHooksInputs) { i.GitHubToken = "" },
		"hook token":   func(i *orchestration.AppHooksInputs) { i.HookToken = "" },
		"host":         func(i *orchestration.AppHooksInputs) { i.HookHost = "" },
		"owner":        func(i *orchestration.AppHooksInputs) { i.Owner = "" },
	} {
		t.Run(name, func(t *testing.T) {
			repos := newHookRepos(nil)
			repos.refuse["samclement/notes"] = "hooks"
			inputs := hookInputs(repos, staging)
			change(&inputs)
			if outcomes, err := orchestration.AppHooks(context.Background(), inputs); err == nil || outcomes != nil {
				t.Fatalf("outcomes = %#v, err = %v", outcomes, err)
			}
		})
	}
}
