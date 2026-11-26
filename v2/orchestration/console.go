package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/apps"
	deliveryrules "github.com/samclement/swhurl-platform/v2/delivery/rules"
	"github.com/samclement/swhurl-platform/v2/health"
	healthrules "github.com/samclement/swhurl-platform/v2/health/rules"
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
	_, found := b.held(revision)
	return found
}

// held is one commit's tree when it is in hand, without reading it.
func (b *ConsoleBundle) held(revision string) (map[string][]byte, bool) {
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

// ConsoleRecords is the record tree a console request reads: one instance's three records per folder, and the
// documents every compilation takes.
type ConsoleRecords struct {
	Shared    provisioningrules.Inputs
	Instances []provisioningrules.Inputs
}

// Records splits a filtered tree into the instances it holds, in app and environment order, as files.Records reads
// any records tree; each takes the documents every compilation takes.
func Records(tree map[string][]byte) (ConsoleRecords, error) {
	found := ConsoleRecords{Shared: provisioningrules.Inputs{Profile: tree[consoleProfile], Calls: tree[consoleCalls],
		Egress: tree[consoleEgress]}}
	instances, err := files.Records(tree, consoleRecords)
	if err != nil {
		return ConsoleRecords{}, err
	}
	for _, one := range instances {
		inputs := found.Shared
		inputs.App, inputs.Instance, inputs.Release = one.App, one.Instance, one.Release
		found.Instances = append(found.Instances, inputs)
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
	status := apps.StatusInputs{Inputs: inputs.Shared, Revision: revision, RecordsRoot: consoleRecords,
		SharedPaths: []string{consoleProfile, consoleCalls, consoleEgress}, Changes: changes, Reader: reader,
		Source: source, Now: now, MaxAge: 26 * time.Hour}
	result, err := apps.StatusAll(ctx, status, inputs.Instances)
	if err != nil {
		return observe.Collection{}, fmt.Errorf("Go status unavailable: %w", err)
	}
	return result, nil
}

// consoleChanges is one comparison per distinct applied commit, finished or not: a comparison that could not be
// completed is evidence the revision is not proved unaffected.
func consoleChanges(units [][]byte, revision string, compare func(from, to string) ([]string, bool, error)) (*deliveryrules.Changes, error) {
	seen := map[string]bool{}
	changes := &deliveryrules.Changes{Kind: "v2-changes", Version: 1, Comparisons: []deliveryrules.Comparison{}}
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
	slices.SortFunc(changes.Comparisons, func(a, b deliveryrules.Comparison) int { return strings.Compare(a.From, b.From) })
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
func comparisonOf(from, to string, compare func(from, to string) ([]string, bool, error)) deliveryrules.Comparison {
	found := deliveryrules.Comparison{From: from, To: to, Status: "unavailable", Paths: []string{}}
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
	list, err := collector.ListNamespace(ctx, healthrules.PlatformUnits)
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
	report, err := health.Platform(ctx, health.PlatformInputs{Reader: reader, Source: source, Now: now})
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
