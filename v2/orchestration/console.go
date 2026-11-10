package orchestration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The paths a console page may read from the platform repository: the two documents every instance's compilation
// takes, the records themselves, and the compiled manifests and offers a page shows. Nothing else is downloaded.
const (
	consoleProfile = "v2/testdata/profile.json"
	consoleCalls   = "v2-records/calls.json"
	consoleEgress  = "platform/egress/allocation.json"
	consoleRecords = "v2-records"
	consoleApps    = "v2-apps/instances"
	consoleStacks  = "v2/contract/stacks.json"
)

// selected is whether a page may read one repository path.
func selected(file string) bool {
	if file == consoleProfile || file == consoleCalls || file == consoleEgress || file == consoleStacks {
		return true
	}
	if strings.HasPrefix(file, consoleRecords+"/") {
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

// FilterTarball is the console's one archive read: the selected files of one commit, by repository path. The archive
// may carry GitHub's leading folder (the tarball) or none (a local checkout's `git archive`). A path that would leave
// the tree is refused, and a link is never followed: only regular files are read.
func FilterTarball(content []byte, prefixed bool) (map[string][]byte, error) {
	reader, closeArchive := archiveReader(content)
	defer closeArchive()
	archive := tar.NewReader(reader)
	found := map[string][]byte{}
	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read repository archive: %w", err)
		}
		file, body, keep, err := readEntry(archive, entry, prefixed)
		if err != nil {
			return nil, err
		}
		if keep {
			found[file] = body
		}
	}
	return found, nil
}

// readEntry is one archive entry as the filter keeps it: its repository path and bytes, or why the archive is refused.
func readEntry(archive *tar.Reader, entry *tar.Header, prefixed bool) (string, []byte, bool, error) {
	if leaving(entry.Name) {
		return "", nil, false, fmt.Errorf("cannot read repository archive: %s leaves the repository", entry.Name)
	}
	file, ok := archivePath(entry.Name, prefixed)
	if !ok || entry.Typeflag != tar.TypeReg || !selected(file) {
		return "", nil, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(archive, maxArchiveFile+1))
	if err != nil {
		return "", nil, false, fmt.Errorf("cannot read repository archive: %w", err)
	}
	if len(body) > maxArchiveFile {
		return "", nil, false, fmt.Errorf("cannot read repository archive: %s is too large", file)
	}
	return file, body, true, nil
}

// maxArchiveFile bounds one file the console keeps: every document it reads is far smaller.
const maxArchiveFile = 1 << 20

// archiveReader reads a repository archive as tar, gzipped or not: GitHub serves a tarball, a checkout's `git archive`
// need not be compressed.
func archiveReader(content []byte) (io.Reader, func()) {
	gzipped, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return bytes.NewReader(content), func() {}
	}
	return gzipped, func() { gzipped.Close() }
}

// maxTreeFile bounds one file of a whole tree a change is applied to. It is larger than a page's bound because the
// tree holds the repository's documents too, and the whole archive is already bounded by the caller's download.
const maxTreeFile = 8 << 20

// ReadTree reads a whole repository archive: every regular file, with the mode the archive states. The archive may
// carry GitHub's leading folder (the tarball) or none (a checkout's `git archive`). A path that would leave the tree is
// refused and a link is never followed, as the console's filtered read is.
func ReadTree(content []byte, prefixed bool) ([]files.TreeFile, error) {
	reader, closeArchive := archiveReader(content)
	defer closeArchive()
	archive := tar.NewReader(reader)
	found := []files.TreeFile{}
	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read repository archive: %w", err)
		}
		if leaving(entry.Name) {
			return nil, fmt.Errorf("cannot read repository archive: %s leaves the repository", entry.Name)
		}
		file, ok := archivePath(entry.Name, prefixed)
		if !ok || entry.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(archive, maxTreeFile+1))
		if err != nil {
			return nil, fmt.Errorf("cannot read repository archive: %w", err)
		}
		if len(body) > maxTreeFile {
			return nil, fmt.Errorf("cannot read repository archive: %s is too large", file)
		}
		found = append(found, files.TreeFile{Path: file, Executable: entry.Mode&0o111 != 0, Body: body})
	}
	slices.SortFunc(found, func(a, b files.TreeFile) int { return strings.Compare(a.Path, b.Path) })
	return found, nil
}

// leaving reports whether an entry's name would escape the repository, which no archive GitHub serves may contain.
func leaving(name string) bool {
	return strings.HasPrefix(name, "/") || strings.Contains(name, "..")
}

// archivePath is an entry's repository path, with GitHub's leading folder removed, or false for a path that leaves
// the tree or names nothing.
func archivePath(name string, prefixed bool) (string, bool) {
	clean := path.Clean("/" + name)
	if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || clean == "/" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if prefixed {
		if len(parts) < 2 {
			return "", false
		}
		parts = parts[1:]
	}
	return strings.Join(parts, "/"), len(parts) > 0
}

// ConsoleSource is where a console reads its pinned tree: GitHub at main's commit, or the operator's own checkout
// under --dev. Revision is resolved once per request and never falls back to a moving branch.
type ConsoleSource interface {
	Revision(ctx context.Context) (string, error)
	Tree(ctx context.Context, revision string) (map[string][]byte, error)
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
	b.mu.Lock()
	defer b.mu.Unlock()
	_, found := b.trees[revision]
	return found
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

// ConsoleRecords is the record tree a console request reads: one instance's three records per folder, and the
// documents every compilation takes.
type ConsoleRecords struct {
	Shared    Inputs
	Instances []Inputs
}

// Records splits a filtered tree into the instances it holds, in app and environment order. A folder without an
// instance record is not an instance; a record that cannot be read is refused rather than shown.
func Records(files map[string][]byte) (ConsoleRecords, error) {
	found := ConsoleRecords{Shared: Inputs{Profile: files[consoleProfile], Calls: files[consoleCalls],
		Egress: files[consoleEgress]}}
	instances := map[string]Inputs{}
	for file := range files {
		rest, ok := strings.CutPrefix(file, consoleRecords+"/")
		if !ok || !strings.HasSuffix(rest, "/instance.json") {
			continue
		}
		folder := strings.TrimSuffix(rest, "/instance.json")
		app, env, split := strings.Cut(folder, "/")
		if !split || strings.Contains(env, "/") {
			continue
		}
		key := app + "/" + env
		one := instances[key]
		one.Instance, one.Release = files[consoleRecords+"/"+folder+"/instance.json"], files[consoleRecords+"/"+folder+"/release.json"]
		one.App = files[consoleRecords+"/"+app+"/app.json"]
		one.Profile, one.Calls, one.Egress = found.Shared.Profile, found.Shared.Calls, found.Shared.Egress
		instances[key] = one
	}
	for _, key := range slices.Sorted(maps.Keys(instances)) {
		found.Instances = append(found.Instances, instances[key])
	}
	return found, nil
}

// ConsoleStatus compares the cluster with the records at one pinned commit. The comparisons are the caller's
// evidence: a unit whose applied commit is behind is only updating when a complete comparison shows a relevant path.
func ConsoleStatus(ctx context.Context, inputs ConsoleRecords, revision string, reader kube.Reader, units [][]byte,
	compare func(from, to string) ([]string, bool, error), source string, now func() time.Time) (observe.Collection, error) {
	changes, err := consoleChanges(units, revision, compare)
	if err != nil {
		return observe.Collection{}, err
	}
	status := StatusInputs{Inputs: inputs.Shared, Revision: revision, RecordsRoot: consoleRecords,
		SharedPaths: []string{consoleProfile, consoleCalls, consoleEgress}, Changes: changes, Reader: reader,
		Source: source, Now: now, MaxAge: 26 * time.Hour}
	result, err := StatusAll(ctx, status, inputs.Instances)
	if err != nil {
		return observe.Collection{}, fmt.Errorf("Go status unavailable: %w", err)
	}
	return result, nil
}

// consoleChanges is one comparison per distinct applied commit, finished or not: a comparison that could not be
// completed is evidence the revision is not proved unaffected.
func consoleChanges(units [][]byte, revision string, compare func(from, to string) ([]string, bool, error)) (*observe.Changes, error) {
	seen := map[string]bool{}
	changes := &observe.Changes{Kind: "v2-changes", Version: 1, Comparisons: []observe.Comparison{}}
	for _, unit := range units {
		applied, err := appliedCommitOf(unit)
		if err != nil {
			return nil, err
		}
		if applied == "" || applied == revision || seen[applied] {
			continue
		}
		seen[applied] = true
		changes.Comparisons = append(changes.Comparisons, comparisonOf(applied, revision, compare))
	}
	slices.SortFunc(changes.Comparisons, func(a, b observe.Comparison) int { return strings.Compare(a.From, b.From) })
	return changes, nil
}

// appliedCommitOf is the commit one unit's applied Flux revision names.
func appliedCommitOf(unit []byte) (string, error) {
	var one struct {
		Status struct {
			Applied string `json:"lastAppliedRevision"`
		} `json:"status"`
	}
	if err := json.Unmarshal(unit, &one); err != nil {
		return "", fmt.Errorf("invalid Flux unit: %w", err)
	}
	return commitOf(one.Status.Applied), nil
}

// comparisonOf is one ended comparison: complete with its paths, incomplete when GitHub truncated the list, or
// unavailable when it could not be read or does not contain the commit.
func comparisonOf(from, to string, compare func(from, to string) ([]string, bool, error)) observe.Comparison {
	found := observe.Comparison{From: from, To: to, Status: "unavailable", Paths: []string{}}
	if compare == nil {
		return found
	}
	paths, contained, err := compare(from, to)
	switch {
	case err != nil:
	case contained && paths != nil:
		found.Status, found.Paths = "complete", paths
	case contained:
		found.Status = "incomplete"
	}
	return found
}

// commitOf is the commit a Flux revision names (`main@sha1:<commit>` or the commit alone).
func commitOf(revision string) string {
	_, after, found := strings.Cut(revision, "@sha1:")
	if found {
		revision = after
	}
	if len(revision) != 40 || strings.Trim(revision, "0123456789abcdef") != "" {
		return ""
	}
	return revision
}

// ConsoleUnits reads the Flux units the platform pages list.
func ConsoleUnits(ctx context.Context, collector kube.Collector) ([][]byte, error) {
	list, err := collector.ListNamespace(ctx, observe.PlatformUnits)
	if err != nil {
		return nil, fmt.Errorf("Flux units unavailable: %w", err)
	}
	if list.Status != observe.Present {
		return nil, fmt.Errorf("Flux units unavailable: %s", list.Reason)
	}
	return list.Items, nil
}

// ConsoleChecks runs the platform checks a console page shows: the cluster scope, read through the reader the console
// was given, with the time the caller supplies.
func ConsoleChecks(ctx context.Context, reader kube.Reader, source string, now func() time.Time) (pages.Checks, error) {
	report, err := Platform(ctx, PlatformInputs{Reader: reader, Source: source, Now: now})
	if err != nil {
		return pages.Checks{}, fmt.Errorf("platform checks unavailable: %w", err)
	}
	checks := make([]pages.Check, 0, len(report.Checks))
	for _, check := range report.Checks {
		entries := make([]pages.Entry, 0, len(check.Entries))
		for _, entry := range check.Entries {
			entries = append(entries, pages.Entry{Level: string(entry.Level), Message: entry.Message})
		}
		checks = append(checks, pages.Check{Section: check.Section, Entries: entries})
	}
	return pages.ChecksOf(checks), nil
}

// ConsoleSourceOf is the adapter-backed console source: GitHub's tarball at main's commit.
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
