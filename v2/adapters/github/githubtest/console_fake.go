package githubtest

import (
	"context"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
)

// ConsoleFake answers the console's GitHub reads from what a test stated. It keeps every read's path, so a test can
// assert that a page asked for nothing else.
type ConsoleFake struct {
	Commit   string
	Files    map[string]string
	History  map[string]map[string]string // files at commits other than Commit, by commit and then path
	AppFiles map[string]string            // another repository's file at its default branch, by "owner/repo/path"
	Public   map[string]string            // a public repository's file at a commit, by "owner/repo@commit/path"
	Archive  []byte
	// Changed and Contains are the comparison's answer: the paths after the base, and whether head contains it.
	Changed  []string
	Contains bool
	Pulls    []github.Pull
	ByNumber map[int]github.Pull
	Err      error
	Reads    []string

	mu sync.Mutex // a page reads at once, as it does through the real API
}

var _ github.Console = (*ConsoleFake)(nil)

func (f *ConsoleFake) read(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reads = append(f.Reads, path)
	return f.Err
}

func (f *ConsoleFake) Revision(_ context.Context, repository, token string) (string, error) {
	if err := validate(repository, token); err != nil {
		return "", err
	}
	if err := f.read("revision"); err != nil {
		return "", err
	}
	return f.Commit, nil
}

func (f *ConsoleFake) Contents(_ context.Context, repository, token, revision, path string) ([]byte, bool, error) {
	if err := validate(repository, token); err != nil {
		return nil, false, err
	}
	if err := f.read(path); err != nil {
		return nil, false, err
	}
	if revision != f.Commit {
		return nil, false, nil
	}
	body, found := f.Files[path]
	return []byte(body), found, nil
}

func (f *ConsoleFake) AppFile(_ context.Context, repository, token, path string) ([]byte, bool, error) {
	if err := f.read("appfile " + repository + "/" + path); err != nil {
		return nil, false, err
	}
	body, found := f.AppFiles[repository+"/"+path]
	return []byte(body), found, nil
}

func (f *ConsoleFake) PublicFile(_ context.Context, repository, revision, path string) ([]byte, bool, error) {
	if err := f.read("public " + repository + "@" + revision + "/" + path); err != nil {
		return nil, false, err
	}
	body, found := f.Public[repository+"@"+revision+"/"+path]
	return []byte(body), found, nil
}

func (f *ConsoleFake) Tarball(_ context.Context, repository, token, revision string) ([]byte, error) {
	if err := validate(repository, token); err != nil {
		return nil, err
	}
	if err := f.read("tarball"); err != nil {
		return nil, err
	}
	if revision != f.Commit {
		return nil, &github.Error{Reason: "invalid tarball reference"}
	}
	return slices.Clone(f.Archive), nil
}

// CommitFiles answers the requested paths that Files (at Commit) or History (at an older commit) holds. A commit it
// knows neither way is GitHub's 404, an error: absence is only ever a path missing from a commit it has.
func (f *ConsoleFake) CommitFiles(_ context.Context, repository, token, revision string, paths []string) (map[string][]byte, error) {
	if err := validate(repository, token); err != nil {
		return nil, err
	}
	if !filesReference(revision, paths) {
		return nil, &github.Error{Reason: "invalid files reference"}
	}
	if err := f.read("files " + revision); err != nil {
		return nil, err
	}
	files, known := f.History[revision]
	if revision == f.Commit {
		files, known = f.Files, true
	}
	if !known {
		return nil, &github.Error{Reason: "HTTP 404"}
	}
	found := map[string][]byte{}
	for _, file := range paths {
		if body, held := files[file]; held {
			found[file] = []byte(body)
		}
	}
	return found, nil
}

func (f *ConsoleFake) Compare(_ context.Context, repository, token, from, to string) ([]string, bool, error) {
	if err := validate(repository, token); err != nil {
		return nil, false, err
	}
	if err := f.read("compare"); err != nil {
		return nil, false, err
	}
	if from != f.Commit || to != f.Commit {
		return nil, false, nil
	}
	return slices.Clone(f.Changed), f.Contains, nil
}

func (f *ConsoleFake) OpenPulls(_ context.Context, repository, token string) ([]github.Pull, error) {
	if err := validate(repository, token); err != nil {
		return nil, err
	}
	if err := f.read("pulls"); err != nil {
		return nil, err
	}
	return slices.Clone(f.Pulls), nil
}

func (f *ConsoleFake) Pull(_ context.Context, repository, token string, number int) (github.Pull, error) {
	if err := validate(repository, token); err != nil {
		return github.Pull{}, err
	}
	if err := f.read("pull"); err != nil {
		return github.Pull{}, err
	}
	found, ok := f.ByNumber[number]
	if !ok {
		return github.Pull{}, &github.Error{Reason: "HTTP 404"}
	}
	return found, nil
}

// ConsoleFixture is what a console contract's reader answers, stated once for the real adapter's scripted server
// and the fake alike.
type ConsoleFixture struct {
	Revision     string
	File         string
	Body         string
	AppFile      string // another repository and its swhurl.yaml: "owner/app/swhurl.yaml"
	AppBody      string
	Changed      []string
	Pulls        []github.Pull
	Merged       github.Pull
	Template     string // a public template's file at the pinned commit: "owner/repo/path"
	TemplateBody string
}

// Scenario is the console contract's fixed answer.
func Scenario() ConsoleFixture {
	return ConsoleFixture{
		Revision: "0123456789abcdef0123456789abcdef01234567",
		File:     "v2-records/web/staging/instance.json",
		Body:     "{\"kind\":\"instance\"}\n",
		AppFile:  "samclement/weather-api/swhurl.yaml",
		AppBody:  "version: 1\nkind: worker\nstack: typescript\n",
		Changed:  []string{"v2-records/web/staging/instance.json", "platform/egress/allocation.json", "old.json"},
		Pulls: []github.Pull{{Number: 7, Title: "[console] edit web", URL: "https://github.com/owner/repo/pull/7",
			Branch: "console/edit-web-0123456789ab", Opened: "2026-11-01", Body: "Merge plan\n\n1. Merge it.",
			Head: "0123456789abcdef0123456789abcdef01234567", Checks: "passed",
			ChecksURL: "https://github.com/owner/repo/actions/runs/1", State: "open"}},
		Template:     "samclement/swhurl-app-template-typescript/copier.yml",
		TemplateBody: "app_name:\n  type: str\n",
		Merged: github.Pull{Number: 8, Title: "merged", URL: "https://github.com/owner/repo/pull/8",
			Branch: "console/promote-web-0123456789ab", Opened: "2026-10-31", State: "merged",
			MergedAt: "2026-11-01T10:00:00Z", MergeCommit: "fedcba9876543210fedcba9876543210fedcba98"},
	}
}

// NewConsole returns a fake that answers one fixture.
func NewConsole(fixture ConsoleFixture) *ConsoleFake {
	byNumber := map[int]github.Pull{}
	for _, pull := range append(slices.Clone(fixture.Pulls), fixture.Merged) {
		byNumber[pull.Number] = pull
	}
	public := map[string]string{}
	if parts := strings.SplitN(fixture.Template, "/", 3); fixture.Template != "" && len(parts) == 3 {
		public[parts[0]+"/"+parts[1]+"@"+fixture.Revision+"/"+parts[2]] = fixture.TemplateBody
	}
	return &ConsoleFake{Commit: fixture.Revision, Files: map[string]string{fixture.File: fixture.Body},
		Public: public, AppFiles: map[string]string{fixture.AppFile: fixture.AppBody},
		Changed: fixture.Changed, Contains: true, Pulls: fixture.Pulls, ByNumber: byNumber}
}

// ConsoleContract is the behaviour every github.Console must have: a pinned main commit, one file at it and an
// absent one, its tarball, a comparison that contains its base with the changed paths, one open console pull request
// with its checks, and one merged pull request read by number. open returns the reader and the fixture it answers.
func ConsoleContract(t *testing.T, open func(*testing.T) (github.Console, ConsoleFixture)) {
	t.Helper()
	console, want := open(t)
	ctx := context.Background()
	revision, err := console.Revision(ctx, "owner/repo", "token")
	if err != nil || revision != want.Revision {
		t.Fatalf("revision = %q, %v", revision, err)
	}
	contractContents(t, ctx, console, want)
	contractAppFile(t, ctx, console, want)
	contractPublicFile(t, ctx, console, want)
	contractCompare(t, ctx, console, want, revision)
	contractPulls(t, ctx, console, want)
}

func contractContents(t *testing.T, ctx context.Context, console github.Console, want ConsoleFixture) {
	t.Helper()
	body, found, err := console.Contents(ctx, "owner/repo", "token", want.Revision, want.File)
	if err != nil || !found || string(body) != want.Body {
		t.Fatalf("contents = %q, found %v, %v", body, found, err)
	}
	if _, found, err := console.Contents(ctx, "owner/repo", "token", want.Revision, "no/such/file"); err != nil || found {
		t.Fatalf("an absent file: found %v, %v", found, err)
	}
	if tarball, err := console.Tarball(ctx, "owner/repo", "token", want.Revision); err != nil || len(tarball) == 0 {
		t.Fatalf("tarball: %d bytes, %v", len(tarball), err)
	}
	contractCommitFiles(t, ctx, console, want)
}

// A commit's files are the requested paths it holds; one it lacks is absent. A commit GitHub does not have, a
// moving name or a path that leaves the tree is an error, never absence.
func contractCommitFiles(t *testing.T, ctx context.Context, console github.Console, want ConsoleFixture) {
	t.Helper()
	files, err := console.CommitFiles(ctx, "owner/repo", "token", want.Revision, []string{want.File, "no/such/file"})
	if err != nil || len(files) != 1 || string(files[want.File]) != want.Body {
		t.Fatalf("commit files = %q, %v", files, err)
	}
	for name, read := range map[string][2]string{"unknown commit": {strings.Repeat("e", 40), want.File},
		"moving name": {"main", want.File}, "leaving path": {want.Revision, "../" + want.File}} {
		if files, err := console.CommitFiles(ctx, "owner/repo", "token", read[0], []string{read[1]}); err == nil || files != nil {
			t.Fatalf("%s: %q, %v", name, files, err)
		}
	}
}

// An app's declaration is read anonymously, at its repository's default branch, and an absent one is not a failure.
func contractAppFile(t *testing.T, ctx context.Context, console github.Console, want ConsoleFixture) {
	t.Helper()
	body, found, err := console.AppFile(ctx, "samclement/weather-api", "", want.AppFile[len("samclement/weather-api/"):])
	if err != nil || !found || string(body) != want.AppBody {
		t.Fatalf("app file = %q, found %v, %v", body, found, err)
	}
	if _, found, err := console.AppFile(ctx, "samclement/weather-api", "", "swhurl.yamlx"); err != nil || found {
		t.Fatalf("an absent app file: found %v, %v", found, err)
	}
}

// A stack template's copier.yml is read at the revision the contract pins, anonymously.
func contractPublicFile(t *testing.T, ctx context.Context, console github.Console, want ConsoleFixture) {
	t.Helper()
	parts := strings.SplitN(want.Template, "/", 3)
	if len(parts) != 3 {
		t.Fatalf("the fixture's template file %q names no repository", want.Template)
	}
	repository, path := parts[0]+"/"+parts[1], parts[2]
	body, known, err := console.PublicFile(ctx, repository, want.Revision, path)
	if err != nil || !known || string(body) != want.TemplateBody {
		t.Fatalf("public file = %q, found %v, %v", body, known, err)
	}
	if _, known, err := console.PublicFile(ctx, repository, want.Revision, "no-such-file"); err != nil || known {
		t.Fatalf("an absent public file: found %v, %v", known, err)
	}
}

func contractCompare(t *testing.T, ctx context.Context, console github.Console, want ConsoleFixture, revision string) {
	t.Helper()
	paths, contained, err := console.Compare(ctx, "owner/repo", "token", revision, revision)
	if err != nil || !contained || !slices.Equal(paths, want.Changed) {
		t.Fatalf("compare: %q, contained %v, %v", paths, contained, err)
	}
}

func contractPulls(t *testing.T, ctx context.Context, console github.Console, want ConsoleFixture) {
	t.Helper()
	pulls, err := console.OpenPulls(ctx, "owner/repo", "token")
	if err != nil || len(pulls) != 1 || pulls[0].Checks != "passed" || pulls[0].Branch != want.Pulls[0].Branch {
		t.Fatalf("open pull requests: %#v, %v", pulls, err)
	}
	merged, err := console.Pull(ctx, "owner/repo", "token", want.Merged.Number)
	if err != nil || merged.State != "merged" {
		t.Fatalf("a merged pull request: %#v, %v", merged, err)
	}
}

// filesReference is a targeted read the real adapter makes: an immutable commit and paths inside the tree.
func filesReference(revision string, paths []string) bool {
	return commitID(revision) && len(paths) > 0 && !slices.ContainsFunc(paths, func(p string) bool {
		return p == "" || p[0] == '/' || path.Clean(p) != p || strings.HasPrefix(p, "..")
	})
}

// commitID is the only revision a targeted read names, as the real adapter checks it.
func commitID(revision string) bool {
	return len(revision) == 40 && strings.Trim(revision, "0123456789abcdef") == ""
}

func validate(repository, token string) error {
	if repository != "owner/repo" {
		return &github.Error{Reason: "invalid repository"}
	}
	if token == "" {
		return &github.Error{Reason: "token not supplied"}
	}
	return nil
}
