package githubtest

import (
	"context"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/github"
)

// PublisherFixture is what a publisher contract's writer answers, stated once for the real adapter's scripted
// server and the fake alike.
type PublisherFixture struct {
	Blob, Tree, Commit string
	Base               string
	Branch             string
	PullNumber         int
	PullURL            string
}

// PublisherScenario is the publisher contract's fixed answer.
func PublisherScenario() PublisherFixture {
	return PublisherFixture{
		Blob:       "1111111111111111111111111111111111111111",
		Tree:       "2222222222222222222222222222222222222222",
		Commit:     "3333333333333333333333333333333333333333",
		Base:       "4444444444444444444444444444444444444444",
		Branch:     "console/v2-scale-web-staging-0123456",
		PullNumber: 12,
		PullURL:    "https://github.com/owner/repo/pull/12",
	}
}

// TreeWrite is one tree a publisher was asked for.
type TreeWrite struct {
	Base    string
	Entries []github.TreeEntry
}

// BranchWrite is one reference a publisher was asked to create.
type BranchWrite struct {
	Name, Commit string
}

// PublisherFake answers the console's GitHub writes from what a test stated, and keeps every write so a test can
// assert what was asked for and in which order.
type PublisherFake struct {
	Scene   PublisherFixture
	FailAt  string // the call that fails: "basetree", "blob", "tree", "commit", "branch" or "pull"
	Blobs   [][]byte
	Trees   []TreeWrite
	Commits []github.NewCommit
	Refs    []BranchWrite
	Pulls   []github.NewPull
}

var _ github.Publisher = (*PublisherFake)(nil)

func (f *PublisherFake) fail(call string) error {
	if f.FailAt == call {
		return &github.Error{Reason: "POST /git/" + call + ": HTTP 422: refused by the fixture"}
	}
	return nil
}

func (f *PublisherFake) BaseTree(_ context.Context, repository, token, commit string) (string, error) {
	if err := validate(repository, token); err != nil {
		return "", err
	}
	if err := f.fail("basetree"); err != nil {
		return "", err
	}
	if len(commit) != 40 {
		return "", &github.Error{Reason: "GET /git/commits: HTTP 404"}
	}
	return f.Scene.Base, nil
}

func (f *PublisherFake) Blob(_ context.Context, repository, token string, content []byte) (string, error) {
	if err := validate(repository, token); err != nil {
		return "", err
	}
	if err := f.fail("blob"); err != nil {
		return "", err
	}
	f.Blobs = append(f.Blobs, append([]byte(nil), content...))
	return f.Scene.Blob, nil
}

func (f *PublisherFake) Tree(_ context.Context, repository, token, base string, entries []github.TreeEntry) (string, error) {
	if err := validate(repository, token); err != nil {
		return "", err
	}
	if err := f.fail("tree"); err != nil {
		return "", err
	}
	f.Trees = append(f.Trees, TreeWrite{Base: base, Entries: append([]github.TreeEntry(nil), entries...)})
	return f.Scene.Tree, nil
}

func (f *PublisherFake) Commit(_ context.Context, repository, token string, commit github.NewCommit) (string, error) {
	if err := validate(repository, token); err != nil {
		return "", err
	}
	if err := f.fail("commit"); err != nil {
		return "", err
	}
	f.Commits = append(f.Commits, commit)
	return f.Scene.Commit, nil
}

func (f *PublisherFake) Branch(_ context.Context, repository, token, name, commit string) error {
	if err := validate(repository, token); err != nil {
		return err
	}
	if err := f.fail("branch"); err != nil {
		return err
	}
	f.Refs = append(f.Refs, BranchWrite{Name: name, Commit: commit})
	return nil
}

func (f *PublisherFake) OpenPull(_ context.Context, repository, token string, pull github.NewPull) (github.Pull, error) {
	if err := validate(repository, token); err != nil {
		return github.Pull{}, err
	}
	if err := f.fail("pull"); err != nil {
		return github.Pull{}, err
	}
	f.Pulls = append(f.Pulls, pull)
	return github.Pull{Number: f.Scene.PullNumber, Title: pull.Title, URL: f.Scene.PullURL,
		Branch: pull.Head, State: "open"}, nil
}

// NewPublisher returns a fake that answers one fixture.
func NewPublisher(scene PublisherFixture) *PublisherFake {
	return &PublisherFake{Scene: scene}
}

// PublisherContract is the behaviour every github.Publisher must have: a blob, a tree over a base, a commit, a new
// console branch, and the pull request asking for it — and the refusals below. open returns the writer and the
// fixture it answers.
func PublisherContract(t *testing.T, open func(*testing.T) (github.Publisher, PublisherFixture)) {
	t.Helper()
	publisher, want := open(t)
	ctx := context.Background()
	base := contractBaseTree(t, ctx, publisher, want)
	blob := contractBlob(t, ctx, publisher, want)
	tree := contractTree(t, ctx, publisher, want, base, blob)
	commit := contractCommit(t, ctx, publisher, want, tree)
	contractBranchAndPull(t, ctx, publisher, want, commit)
	publisherRefusals(t, ctx, publisher, want, commit)
}

// contractBaseTree reads the tree the base commit names.
func contractBaseTree(t *testing.T, ctx context.Context, publisher github.Publisher, want PublisherFixture) string {
	t.Helper()
	base, err := publisher.BaseTree(ctx, "owner/repo", "token", want.Commit)
	if err != nil || base != want.Base {
		t.Fatalf("base tree = %q, %v", base, err)
	}
	if _, err := publisher.BaseTree(ctx, "owner/repo", "token", "not-a-commit"); err == nil {
		t.Error("a base tree was read from a name that is not a commit")
	}
	return base
}

// contractBlob stores one file and returns its identity.
func contractBlob(t *testing.T, ctx context.Context, publisher github.Publisher, want PublisherFixture) string {
	t.Helper()
	blob, err := publisher.Blob(ctx, "owner/repo", "token", []byte("hello\n"))
	if err != nil || blob != want.Blob {
		t.Fatalf("blob = %q, %v", blob, err)
	}
	return blob
}

// contractTree writes one file and deletes another, over the base.
func contractTree(t *testing.T, ctx context.Context, publisher github.Publisher, want PublisherFixture, base, blob string) string {
	t.Helper()
	tree, err := publisher.Tree(ctx, "owner/repo", "token", base, []github.TreeEntry{
		{Path: "v2-records/web/staging/instance.json", Mode: "100644", SHA: blob},
		{Path: "old.json", Mode: "100755"},
	})
	if err != nil || tree != want.Tree {
		t.Fatalf("tree = %q, %v", tree, err)
	}
	return tree
}

// contractCommit makes the one commit, authored by the console, with the one parent.
func contractCommit(t *testing.T, ctx context.Context, publisher github.Publisher, want PublisherFixture, tree string) string {
	t.Helper()
	commit, err := publisher.Commit(ctx, "owner/repo", "token", github.NewCommit{
		Message: "[console] apps: scale web/staging\n\nbody\n", Tree: tree, Parent: want.Commit,
		Author: github.Signature{Name: "swhurl console", Email: "console@users.noreply.github.com"}})
	if err != nil || commit != want.Commit {
		t.Fatalf("commit = %q, %v", commit, err)
	}
	return commit
}

// contractBranchAndPull creates the branch and opens the pull request that asks for it.
func contractBranchAndPull(t *testing.T, ctx context.Context, publisher github.Publisher, want PublisherFixture, commit string) {
	t.Helper()
	if err := publisher.Branch(ctx, "owner/repo", "token", want.Branch, commit); err != nil {
		t.Fatalf("branch: %v", err)
	}
	pull, err := publisher.OpenPull(ctx, "owner/repo", "token", github.NewPull{
		Title: "[console] apps: scale web/staging", Head: want.Branch, Base: "main", Body: "body"})
	if err != nil || pull.URL != want.PullURL || pull.Number != want.PullNumber {
		t.Fatalf("pull = %#v, %v", pull, err)
	}
}

// publisherRefusals are the writes every publisher refuses however it is asked: a ref that is not the console's, a
// tree that states nothing, and no token.
func publisherRefusals(t *testing.T, ctx context.Context, publisher github.Publisher, want PublisherFixture, commit string) {
	t.Helper()
	if err := publisher.Branch(ctx, "owner/repo", "token", "main", commit); err == nil {
		t.Error("a publisher may write no ref but a console branch")
	}
	if err := publisher.Branch(ctx, "owner/repo", "token", "console/", commit); err == nil {
		t.Error("an empty console branch was created")
	}
	if _, err := publisher.Tree(ctx, "owner/repo", "token", want.Commit, nil); err == nil {
		t.Error("a tree with no entry was created")
	}
	if _, err := publisher.Tree(ctx, "owner/repo", "token", "not-a-commit", []github.TreeEntry{
		{Path: "a", Mode: "100644"}}); err == nil {
		t.Error("a tree over a base that is not a commit was created")
	}
	if _, err := publisher.Tree(ctx, "owner/repo", "token", want.Commit, []github.TreeEntry{
		{Path: "a", Mode: "100777"}}); err == nil {
		t.Error("a tree entry with a mode a repository cannot hold was created")
	}
	if _, err := publisher.Blob(ctx, "owner/repo", "", []byte("x")); err == nil {
		t.Error("a blob was stored without a token")
	}
	if _, err := publisher.Commit(ctx, "owner/repo", "token", github.NewCommit{Message: "no tree"}); err == nil {
		t.Error("a commit with no tree was created")
	}
	if _, err := publisher.OpenPull(ctx, "owner/repo", "token", github.NewPull{Title: "no head"}); err == nil {
		t.Error("a pull request with no head was opened")
	}
}
