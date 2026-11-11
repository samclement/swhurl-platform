package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/github/githubtest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const (
	hooksGitHubToken = "fixture-gh-token"
	hooksImageToken  = "fixture-image-webhook-token"
	hooksHost        = "flux-webhook.example.test"
)

// countingRepos is the repository fake listing each repository's own webhooks, with every listing counted, so a
// refusal can show it made none.
type countingRepos struct {
	*githubtest.RepositoryFake
	listed map[string][]github.Hook
	calls  int
}

func (r *countingRepos) Hooks(ctx context.Context, repository, token string) ([]github.Hook, error) {
	r.calls++
	if _, err := r.RepositoryFake.Hooks(ctx, repository, token); err != nil {
		return nil, err
	}
	return r.listed[repository], nil
}

// hooksRecords is a records tree: each app's environments, with a release record publishing image from repository
// (or the raw bytes of a broken one).
func hooksRecords(t *testing.T, releases map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, release := range releases {
		folder := filepath.Join(root, path)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, kernel.ReleaseRecordName), []byte(release), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func hooksRelease(application, image, repository string) string {
	return `{"schemaVersion": 1, "application": "` + application + `",
  "source": {"repository": "` + repository + `", "commit": "` + strings.Repeat("b", 40) + `"},
  "artifact": {"image": "` + image + `", "digest": "sha256:` + strings.Repeat("b", 64) + `",
    "mediaType": "application/vnd.oci.image.manifest.v1+json", "platform": "linux/amd64"},
  "runtime": {"kind": "web", "port": 8080, "healthPath": "/healthz", "uid": 65532}}`
}

// hooksRun runs the command with the tokens in the environment only.
func hooksRun(repos github.RepositoryWriter, env map[string]string, args ...string) (int, string, string) {
	var stdout, diagnostic bytes.Buffer
	c := cli{readFile: os.ReadFile, repositoryWriter: repos, getenv: func(name string) string { return env[name] }}
	status := runWith(append([]string{"app-hooks"}, args...), &stdout, &diagnostic, c)
	return status, stdout.String(), diagnostic.String()
}

func hooksEnv() map[string]string {
	return map[string]string{gitHubTokenEnv: hooksGitHubToken, appWebhookTokenEnv: hooksImageToken}
}

// hooksScene is samclement/notes with its correct webhook and samclement/orders with a webhook to another host.
func hooksScene() *countingRepos {
	return &countingRepos{RepositoryFake: githubtest.NewRepository(githubtest.RepositoryScenario()),
		listed: map[string][]github.Hook{
			"samclement/notes": {{ID: 7, Active: true, Events: []string{"package", "registry_package"},
				URL: kernel.ImageHookURL(hooksImageToken, hooksHost)}},
			"samclement/orders": {{ID: 8, Active: true, Events: []string{"push"}, URL: "https://elsewhere.example.test/"}},
		}}
}

func assertNoToken(t *testing.T, outputs ...string) {
	t.Helper()
	digest := strings.TrimPrefix(kernel.ImageHookURL(hooksImageToken, hooksHost), "https://"+hooksHost+"/hook/")
	for _, output := range outputs {
		for _, secret := range []string{hooksGitHubToken, hooksImageToken, digest} {
			if strings.Contains(output, secret) {
				t.Fatalf("output carries a credential: %q", output)
			}
		}
	}
}

// Only staging's own platform images are kept; a broken record is an error line and the rest still run, so the exit
// status is 1 after every app's line.
func TestAppHooksCLIKeepsEachAppAndReportsTheBrokenOne(t *testing.T) {
	records := hooksRecords(t, map[string]string{
		"notes/staging":  hooksRelease("notes", "ghcr.io/samclement/notes", "samclement/notes"),
		"notes/prod":     hooksRelease("notes", "ghcr.io/samclement/notes", "samclement/notes"),
		"orders/staging": hooksRelease("orders", "ghcr.io/samclement/orders", "samclement/orders"),
		"pinned/staging": hooksRelease("pinned", "ghcr.io/other/pinned", "samclement/pinned"),
		"prodonly/prod":  hooksRelease("prodonly", "ghcr.io/samclement/prodonly", "samclement/prodonly"),
		"broken/staging": `{"schemaVersion": 1`,
	})
	repos := hooksScene()
	status, stdout, diagnostic := hooksRun(repos, hooksEnv(), "--records", records, "--owner", "samclement",
		"--hook-host", hooksHost)
	if status != 1 {
		t.Fatalf("exit %d: %s%s", status, stdout, diagnostic)
	}
	if stdout != "[OK] notes: image webhook on samclement/notes is in place\n"+
		"[OK] orders: image webhook on samclement/orders created\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.HasPrefix(diagnostic, "[ERROR] broken: the staging release record cannot be read") ||
		strings.Count(diagnostic, "\n") != 1 {
		t.Fatalf("diagnostic = %q", diagnostic)
	}
	if len(repos.NewHooks) != 1 || len(repos.Updated) != 0 || repos.NewHooks[0].Secret != hooksImageToken {
		t.Fatalf("writes = %#v %#v", repos.NewHooks, repos.Updated)
	}
	assertNoToken(t, stdout, diagnostic)
}

// A dry run lists and says what would change; it writes nothing and succeeds.
func TestAppHooksCLIDryRunWritesNothing(t *testing.T) {
	records := hooksRecords(t, map[string]string{
		"orders/staging": hooksRelease("orders", "ghcr.io/samclement/orders", "samclement/orders")})
	repos := hooksScene()
	repos.listed["samclement/orders"] = append(repos.listed["samclement/orders"], github.Hook{ID: 9, Active: false,
		Events: []string{"package", "registry_package"}, URL: kernel.ImageHookURL(hooksImageToken, hooksHost)})
	status, stdout, diagnostic := hooksRun(repos, hooksEnv(), "--records", records, "--owner", "samclement",
		"--hook-host", hooksHost, "--dry-run")
	if status != 0 || stdout != "[OK] orders: image webhook on samclement/orders would be updated\n" {
		t.Fatalf("exit %d: %q %q", status, stdout, diagnostic)
	}
	if len(repos.NewHooks) != 0 || len(repos.Updated) != 0 || repos.calls != 1 {
		t.Fatalf("a dry run wrote or did not list: %#v %#v %d", repos.NewHooks, repos.Updated, repos.calls)
	}
}

// A GitHub refusal is that app's error and exit 1, without a credential in what is printed.
func TestAppHooksCLIAGitHubRefusalIsAnAppsError(t *testing.T) {
	records := hooksRecords(t, map[string]string{
		"orders/staging": hooksRelease("orders", "ghcr.io/samclement/orders", "samclement/orders")})
	repos := hooksScene()
	repos.FailAt = "createhook"
	status, stdout, diagnostic := hooksRun(repos, hooksEnv(), "--records", records, "--owner", "samclement",
		"--hook-host", hooksHost)
	if status != 1 || stdout != "" || diagnostic != "[ERROR] orders: cannot keep the image webhook on samclement/orders: "+
		"GitHub: HTTP 422: refused by the fixture\n" {
		t.Fatalf("exit %d: %q %q", status, stdout, diagnostic)
	}
	assertNoToken(t, stdout, diagnostic)
}

// Without either token, or with a flag missing, nothing is listed; a records tree that cannot be read stops too.
func TestAppHooksCLIRefusesBeforeAnyCall(t *testing.T) {
	records := hooksRecords(t, map[string]string{
		"orders/staging": hooksRelease("orders", "ghcr.io/samclement/orders", "samclement/orders")})
	args := []string{"--records", records, "--owner", "samclement", "--hook-host", hooksHost}
	cases := []struct {
		name   string
		env    map[string]string
		args   []string
		status int
		says   string
	}{
		{"no GitHub token", map[string]string{appWebhookTokenEnv: hooksImageToken}, args, 2, gitHubTokenEnv + " is not set"},
		{"no webhook token", map[string]string{gitHubTokenEnv: hooksGitHubToken}, args, 2, appWebhookTokenEnv + " is not set"},
		{"placeholder token", map[string]string{gitHubTokenEnv: hooksGitHubToken, appWebhookTokenEnv: "REPLACE_ME"}, args, 2, "is not set"},
		{"no host", hooksEnv(), args[:4], 2, "usage"},
		{"no owner", hooksEnv(), append(args[:2:2], args[4:]...), 2, "usage"},
		{"a token as an argument", hooksEnv(), append(args, hooksImageToken), 2, "usage"},
		{"no records tree", hooksEnv(), []string{"--records", filepath.Join(records, "missing"), "--owner", "samclement",
			"--hook-host", hooksHost}, 1, "cannot read"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repos := hooksScene()
			status, stdout, diagnostic := hooksRun(repos, tt.env, tt.args...)
			if status != tt.status || !strings.Contains(diagnostic, tt.says) || repos.calls != 0 {
				t.Fatalf("exit %d, calls %d: %q %q", status, repos.calls, stdout, diagnostic)
			}
			assertNoToken(t, stdout, diagnostic)
		})
	}
}

// The platform's own records name the demo apps' repositories, as Python's app-hooks found them.
func TestAppHooksCLIReadsThePlatformRecords(t *testing.T) {
	repos := hooksScene()
	status, stdout, diagnostic := hooksRun(repos, hooksEnv(), "--records", "../../../v2-records", "--owner", "samclement",
		"--hook-host", "flux-webhook.homelab.swhurl.com", "--dry-run")
	if status != 0 || !strings.Contains(stdout, "[OK] demo-shop: image webhook on samclement/demo-shop would be created\n") {
		t.Fatalf("exit %d: %s%s", status, stdout, diagnostic)
	}
	if len(repos.NewHooks) != 0 {
		t.Fatal("a dry run wrote")
	}
}
