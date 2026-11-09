package githubtest

import (
	"context"
	"slices"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
)

// RepositoryFixture is what a repository writer contract answers, stated once for the real adapter's scripted
// server and the fake alike.
type RepositoryFixture struct {
	Repository, Missing      string
	CreatedURL, Head, Commit string
	Blob, Tree               string
	Hooks                    []github.Hook
}

// RepositoryScenario is the repository contract's fixed answer.
func RepositoryScenario() RepositoryFixture {
	return RepositoryFixture{
		Repository: "samclement/notes",
		Missing:    "samclement/hello",
		CreatedURL: "https://github.com/samclement/notes",
		Head:       "4444444444444444444444444444444444444444",
		Commit:     "5555555555555555555555555555555555555555",
		Blob:       "6666666666666666666666666666666666666666",
		Tree:       "7777777777777777777777777777777777777777",
		Hooks: []github.Hook{{ID: 7, Active: true, Events: []string{"package", "registry_package"},
			URL: "https://flux-webhook.example.test/hook/abc"}},
	}
}

// CreatedRepository is one repository a writer was asked to create.
type CreatedRepository struct{ Owner, Name, Description string }

// FirstWrite is one first commit a writer was asked for.
type FirstWrite struct {
	Repository, Parent, Message string
	Files                       []github.NewFile
}

// UpdatedHook is one webhook change a writer was asked for.
type UpdatedHook struct {
	ID   int
	Hook github.RepositoryHook
}

// RepositoryFake answers the repository write port from what a test stated, and keeps every write so a test can
// assert what was asked for and in which order.
type RepositoryFake struct {
	Scene       RepositoryFixture
	Existing    []string // the repositories Exists answers true for
	HeadMissing int      // Head answers ErrNoMainBranch this many times before the fixture's head
	FailAt      string   // the call that fails: "exists", "create", "head", "firstcommit", "hooks", "createhook", "updatehook"
	Created     []CreatedRepository
	Firsts      []FirstWrite
	NewHooks    []github.RepositoryHook
	Updated     []UpdatedHook
	HeadCalls   int
}

var _ github.RepositoryWriter = (*RepositoryFake)(nil)

func (f *RepositoryFake) fail(call string) error {
	if f.FailAt == call {
		return &github.Error{Reason: "HTTP 422: refused by the fixture"}
	}
	return nil
}

func (f *RepositoryFake) Exists(_ context.Context, repository, token string) (bool, error) {
	if token == "" || repository == "" {
		return false, &github.Error{Reason: "invalid repository"}
	}
	if err := f.fail("exists"); err != nil {
		return false, err
	}
	return slices.Contains(f.Existing, repository), nil
}

func (f *RepositoryFake) Create(_ context.Context, token, owner, name, description string) (string, error) {
	if token == "" || owner == "" || name == "" {
		return "", &github.Error{Reason: "invalid repository"}
	}
	if err := f.fail("create"); err != nil {
		return "", err
	}
	f.Created = append(f.Created, CreatedRepository{Owner: owner, Name: name, Description: description})
	return f.Scene.CreatedURL, nil
}

func (f *RepositoryFake) Head(_ context.Context, repository, token string) (string, error) {
	if token == "" || repository == "" {
		return "", &github.Error{Reason: "invalid repository"}
	}
	if err := f.fail("head"); err != nil {
		return "", err
	}
	f.HeadCalls++
	if f.HeadCalls <= f.HeadMissing {
		return "", github.ErrNoMainBranch
	}
	return f.Scene.Head, nil
}

func (f *RepositoryFake) FirstCommit(_ context.Context, repository, token, parent, message string,
	files []github.NewFile) (string, error) {
	if token == "" || repository == "" || parent == "" || message == "" || len(files) == 0 {
		return "", &github.Error{Reason: "invalid commit"}
	}
	if err := f.fail("firstcommit"); err != nil {
		return "", err
	}
	f.Firsts = append(f.Firsts, FirstWrite{Repository: repository, Parent: parent, Message: message,
		Files: slices.Clone(files)})
	return f.Scene.Commit, nil
}

func (f *RepositoryFake) Hooks(_ context.Context, repository, token string) ([]github.Hook, error) {
	if token == "" || repository == "" {
		return nil, &github.Error{Reason: "invalid repository"}
	}
	if err := f.fail("hooks"); err != nil {
		return nil, err
	}
	return slices.Clone(f.Scene.Hooks), nil
}

func (f *RepositoryFake) CreateHook(_ context.Context, repository, token string, hook github.RepositoryHook) error {
	if token == "" || repository == "" {
		return &github.Error{Reason: "invalid repository"}
	}
	if err := f.fail("createhook"); err != nil {
		return err
	}
	f.NewHooks = append(f.NewHooks, hook)
	return nil
}

func (f *RepositoryFake) UpdateHook(_ context.Context, repository, token string, id int, hook github.RepositoryHook) error {
	if token == "" || repository == "" || id <= 0 {
		return &github.Error{Reason: "invalid hook"}
	}
	if err := f.fail("updatehook"); err != nil {
		return err
	}
	f.Updated = append(f.Updated, UpdatedHook{ID: id, Hook: hook})
	return nil
}

// NewRepository returns a fake that answers one fixture.
func NewRepository(scene RepositoryFixture) *RepositoryFake {
	return &RepositoryFake{Scene: scene, Existing: []string{scene.Repository}}
}

// RepositoryContract is the behaviour every github.RepositoryWriter must have: whether a repository exists, the
// one it creates, the head its first commit states a parent of, the hooks it reads, creates and changes — and the
// refusals below. open returns the writer and the fixture it answers.
func RepositoryContract(t *testing.T, open func(*testing.T) (github.RepositoryWriter, RepositoryFixture)) {
	t.Helper()
	writer, want := open(t)
	ctx := context.Background()
	repositoryExistence(t, ctx, writer, want)
	created, err := writer.Create(ctx, "token", "samclement", "notes", "A swhurl app")
	if err != nil || created != want.CreatedURL {
		t.Fatalf("create = %q, %v", created, err)
	}
	head, err := writer.Head(ctx, want.Repository, "token")
	if err != nil || head != want.Head {
		t.Fatalf("head = %q, %v", head, err)
	}
	firstCommit(t, ctx, writer, want, head)
	hooks(t, ctx, writer, want)
	repositoryRefusals(t, ctx, writer, want)
}

// repositoryExistence is the answer for a repository that is there and one that is not.
func repositoryExistence(t *testing.T, ctx context.Context, writer github.RepositoryWriter, want RepositoryFixture) {
	t.Helper()
	if found, err := writer.Exists(ctx, want.Repository, "token"); err != nil || !found {
		t.Fatalf("an existing repository: found %v, %v", found, err)
	}
	if found, err := writer.Exists(ctx, want.Missing, "token"); err != nil || found {
		t.Fatalf("an absent repository: found %v, %v", found, err)
	}
}

// firstCommit writes two files, one of them executable, over the head.
func firstCommit(t *testing.T, ctx context.Context, writer github.RepositoryWriter, want RepositoryFixture, head string) {
	t.Helper()
	commit, err := writer.FirstCommit(ctx, want.Repository, "token", head, "Start notes\n",
		[]github.NewFile{{Path: "README.md", Body: []byte("notes\n")},
			{Path: "scripts/run.sh", Executable: true, Body: []byte("#!/bin/sh\n")}})
	if err != nil || commit != want.Commit {
		t.Fatalf("first commit = %q, %v", commit, err)
	}
}

// hooks reads the repository's webhook, adds one and changes it.
func hooks(t *testing.T, ctx context.Context, writer github.RepositoryWriter, want RepositoryFixture) {
	t.Helper()
	found, err := writer.Hooks(ctx, want.Repository, "token")
	if err != nil || len(found) != len(want.Hooks) || found[0].URL != want.Hooks[0].URL || found[0].ID != want.Hooks[0].ID {
		t.Fatalf("hooks = %#v, %v", found, err)
	}
	hook := github.RepositoryHook{URL: "https://flux-webhook.example.test/hook/abc", Secret: "secret",
		Events: []string{"package", "registry_package"}, Active: true}
	if err := writer.CreateHook(ctx, want.Repository, "token", hook); err != nil {
		t.Fatalf("create hook: %v", err)
	}
	if err := writer.UpdateHook(ctx, want.Repository, "token", want.Hooks[0].ID, hook); err != nil {
		t.Fatalf("update hook: %v", err)
	}
}

// repositoryRefusals are the calls every repository writer refuses however it is asked.
func repositoryRefusals(t *testing.T, ctx context.Context, writer github.RepositoryWriter, want RepositoryFixture) {
	t.Helper()
	if _, err := writer.Exists(ctx, want.Repository, ""); err == nil {
		t.Error("a repository was read without a token")
	}
	if _, err := writer.Create(ctx, "token", "samclement", "", "x"); err == nil {
		t.Error("a repository with no name was created")
	}
	if _, err := writer.Head(ctx, "not a repository", "token"); err == nil {
		t.Error("a head was read from a name that is not a repository")
	}
	if _, err := writer.FirstCommit(ctx, want.Repository, "token", "not-a-commit", "x",
		[]github.NewFile{{Path: "README.md"}}); err == nil {
		t.Error("a first commit with no parent was made")
	}
	if _, err := writer.FirstCommit(ctx, want.Repository, "token", want.Head, "x", nil); err == nil {
		t.Error("a first commit with no file was made")
	}
	if err := writer.CreateHook(ctx, want.Repository, "token", github.RepositoryHook{Events: []string{"package"}}); err == nil {
		t.Error("a webhook with no URL was created")
	}
	if err := writer.UpdateHook(ctx, want.Repository, "token", 0, github.RepositoryHook{}); err == nil {
		t.Error("a webhook with no id was changed")
	}
}
