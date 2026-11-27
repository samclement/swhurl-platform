package orchestration

import (
	"context"
	"strings"
	"sync"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// The paths a console page may read from the platform repository: the two documents every instance's compilation
// takes, the records themselves, and the compiled manifests and offers a page shows. Nothing else is downloaded.
const (
	consoleProfile = "v2/testdata/profile.json"
	consoleCalls   = kernel.RecordsRoot + "/calls.json"
	consoleEgress  = "platform/egress/allocation.json"
	consoleApps    = "v2-apps/instances"
	consoleStacks  = "v2/contract/stacks.json"
)

// selected is whether a page may read one repository path.
func selected(file string) bool {
	if file == consoleProfile || file == consoleCalls || file == consoleEgress || file == consoleStacks {
		return true
	}
	if strings.HasPrefix(file, kernel.RecordsRoot+"/") {
		return strings.HasSuffix(file, ".json")
	}
	parts := strings.Split(file, "/")
	return len(parts) >= 5 && parts[0] == "v2-apps" && parts[1] == "instances" && instanceFile(parts)
}

// instanceFile is whether a compiled instance path is one a page shows: its HelmRelease, or one of its offers
// documents.
func instanceFile(parts []string) bool {
	if len(parts) < 5 {
		return false
	}
	if len(parts) == 5 {
		return parts[4] == "helmrelease.json"
	}
	return parts[4] == "offers"
}

// FilterTarball is the console's one archive read: the selected files of one commit, by repository path, read as
// files.ReadArchive reads any repository archive.
func FilterTarball(content []byte, prefixed bool) (map[string][]byte, error) {
	tree, err := files.ReadArchive(content, prefixed, selected)
	if err != nil {
		return nil, err
	}
	found := make(map[string][]byte, len(tree))
	for _, file := range tree {
		found[file.Path] = file.Body
	}
	return found, nil
}

// ConsoleSource is where a console reads its pinned tree: GitHub at main's commit, or the operator's own checkout
// under --dev. Revision is resolved once per request and never falls back to a moving branch.
type ConsoleSource interface {
	Revision(ctx context.Context) (string, error)
	Tree(ctx context.Context, revision string) (map[string][]byte, error)
	// Files is the regular files among paths at one commit, without the rest of its tree. A path is missing from the
	// answer only when the commit's whole tree was known not to hold it; anything less is an error.
	Files(ctx context.Context, revision string, paths []string) (map[string][]byte, error)
}

// ConsoleBundle serves one source's trees, keeping at most 60 commits' bytes in the order they were read.
type ConsoleBundle struct {
	Source ConsoleSource
	// Max is how many commits are kept; zero is sixty.
	Max int

	mu    sync.Mutex
	trees map[string]map[string][]byte
	order []string
}

// Revision resolves the source's pinned commit.
func (b *ConsoleBundle) Revision(ctx context.Context) (string, error) {
	return b.Source.Revision(ctx)
}

// Cached is whether one commit's tree is already in hand: a page's bounded reads are spent on the ones it must fetch.
func (b *ConsoleBundle) Cached(revision string) bool {
	_, found := b.Held(revision)
	return found
}

// Held is one commit's tree when it is in hand, without reading it.
func (b *ConsoleBundle) Held(revision string) (map[string][]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	found, ok := b.trees[revision]
	return found, ok
}

// Tree is one commit's selected files, from the cache or read once. A failed read is never kept.
func (b *ConsoleBundle) Tree(ctx context.Context, revision string) (map[string][]byte, error) {
	b.mu.Lock()
	if found, ok := b.trees[revision]; ok {
		b.mu.Unlock()
		return found, nil
	}
	b.mu.Unlock()
	files, err := b.Source.Tree(ctx, revision)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.trees == nil {
		b.trees = map[string]map[string][]byte{}
	}
	limit := b.Max
	if limit <= 0 {
		limit = 60
	}
	if _, raced := b.trees[revision]; !raced {
		for len(b.order) >= limit {
			delete(b.trees, b.order[0])
			b.order = b.order[1:]
		}
		b.trees[revision] = files
		b.order = append(b.order, revision)
	}
	return b.trees[revision], nil
}

// ConsoleSourceOf is the adapter-backed console source: GitHub's tarball at main's commit, and one commit's files
// by blob.
type ConsoleSourceOf struct {
	Console    github.Console
	Repository string
	Token      string
}

// Revision resolves main's commit.
func (s ConsoleSourceOf) Revision(ctx context.Context) (string, error) {
	return s.Console.Revision(ctx, s.Repository, s.Token)
}

// Tree downloads and filters one commit's tarball.
func (s ConsoleSourceOf) Tree(ctx context.Context, revision string) (map[string][]byte, error) {
	tarball, err := s.Console.Tarball(ctx, s.Repository, s.Token, revision)
	if err != nil {
		return nil, err
	}
	files, err := FilterTarball(tarball, true)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// Files reads the paths at one commit from its complete tree listing, and nothing else: no tarball, no other commit.
func (s ConsoleSourceOf) Files(ctx context.Context, revision string, paths []string) (map[string][]byte, error) {
	return s.Console.CommitFiles(ctx, s.Repository, s.Token, revision, paths)
}
