package console

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/apps"
	deliveryrules "github.com/samclement/swhurl-platform/v2/delivery/rules"
	"github.com/samclement/swhurl-platform/v2/health"
	healthrules "github.com/samclement/swhurl-platform/v2/health/rules"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/pages"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// Server is what the console's pages read, as the command composes it from the server it runs: the pinned tree's
// source, the cluster, GitHub's comparisons and the clock, and the readings the command keeps for itself (each app's
// stack, the jobs, the flag store's cards). Every read carries the request's context; nothing here writes.
type Server struct {
	Bundle    *Bundle
	Reader    kube.Reader
	Collector kube.Collector
	// Data is the console's own records (dashboards); nil reads none.
	Data kube.Data
	// Compare is GitHub's comparison of two commits: the paths it changes and whether the second contains the first.
	// An error leaves the revision unproved rather than unaffected.
	Compare func(from, to string) ([]string, bool, error)
	Now     func() time.Time
	// Repository is the platform repository a page links into, as owner/name.
	Repository string
	// GitHub is whether this console has a token, and so can open the changes a page offers.
	GitHub bool
	// Stacks is each named app's stack (its language), empty where it is not known.
	Stacks func(ctx context.Context, apps []string) map[string]string
	// Jobs is the console's jobs, newest first.
	Jobs func(ctx context.Context) []pages.Job
	// Flags is the flag store's card for an instance, which its app page shows.
	Flags FlagCards
}

// checkSource is who the platform checks a page shows say ran them.
const checkSource = "console"

// PinnedTree is the selected files of the commit this request reads: one resolution, one download, cached by SHA.
func (s Server) PinnedTree(ctx context.Context) (map[string][]byte, error) {
	revision, err := s.Bundle.Revision(ctx)
	if err != nil {
		return nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	return s.Bundle.Tree(ctx, revision)
}

// Rows is the cluster and the pinned tree one page shows, resolved once and read once: the tree, and one row per
// instance its records hold or the cluster is still removing.
func (s Server) Rows(ctx context.Context) (map[string][]byte, []pages.AppRow, error) {
	tree, collection, _, err := s.status(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := rowsOf(collection)
	if err != nil {
		return nil, nil, err
	}
	return tree, rows, nil
}

// status is the pinned commit's tree and the cluster compared with its records, with the Flux units it read.
func (s Server) status(ctx context.Context) (map[string][]byte, observe.Collection, [][]byte, error) {
	revision, err := s.Bundle.Revision(ctx)
	if err != nil {
		return nil, observe.Collection{}, nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	tree, err := s.Bundle.Tree(ctx, revision)
	if err != nil {
		return nil, observe.Collection{}, nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	inputs, err := records(tree)
	if err != nil {
		return nil, observe.Collection{}, nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	units, err := s.units(ctx)
	if err != nil {
		return nil, observe.Collection{}, nil, err
	}
	changes, err := consoleChanges(units, revision, s.Compare)
	if err != nil {
		return nil, observe.Collection{}, nil, err
	}
	status := apps.StatusInputs{Inputs: inputs.Shared, Revision: revision, RecordsRoot: kernel.RecordsRoot,
		SharedPaths: []string{profilePath, pages.ConsoleCallsFile, pages.ConsoleEgressFile}, Changes: changes,
		Reader: s.Reader, Source: checkSource, Now: s.Now, MaxAge: 26 * time.Hour}
	collection, err := apps.StatusAll(ctx, status, inputs.Instances)
	if err != nil {
		return nil, observe.Collection{}, nil, fmt.Errorf("Go status unavailable: %w", err)
	}
	return tree, collection, units, nil
}

// consoleRecords is the record tree a console request reads: one instance's three records per folder, and the
// documents every compilation takes.
type consoleRecords struct {
	Shared    provisioningrules.Inputs
	Instances []provisioningrules.Inputs
}

// records splits a filtered tree into the instances it holds, in app and environment order, as files.Records reads
// any records tree; each takes the documents every compilation takes.
func records(tree map[string][]byte) (consoleRecords, error) {
	found := consoleRecords{Shared: provisioningrules.Inputs{Profile: tree[profilePath],
		Calls: tree[pages.ConsoleCallsFile], Egress: tree[pages.ConsoleEgressFile]}}
	instances, err := files.Records(tree, kernel.RecordsRoot)
	if err != nil {
		return consoleRecords{}, err
	}
	for _, one := range instances {
		inputs := found.Shared
		inputs.App, inputs.Instance, inputs.Release = one.App, one.Instance, one.Release
		found.Instances = append(found.Instances, inputs)
	}
	return found, nil
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

// units reads the Flux units the platform pages list.
func (s Server) units(ctx context.Context) ([][]byte, error) {
	list, err := s.Collector.ListNamespace(ctx, healthrules.PlatformUnits)
	if err != nil {
		return nil, fmt.Errorf("Flux units unavailable: %w", err)
	}
	if list.Status != observe.Present {
		return nil, fmt.Errorf("Flux units unavailable: %s", list.Reason)
	}
	return list.Items, nil
}

// Units is the Flux units the platform pages list, read and parsed.
func (s Server) Units(ctx context.Context) ([]pages.Unit, error) {
	items, err := s.units(ctx)
	if err != nil {
		return nil, err
	}
	listed, err := pages.Units(items)
	if err != nil {
		return nil, fmt.Errorf("Flux units unavailable: %w", err)
	}
	return listed, nil
}

// Checks runs the platform checks a console page shows: the cluster scope, read through the console's reader, at
// the console's time.
func (s Server) Checks(ctx context.Context) (pages.Checks, error) {
	report, err := health.Platform(ctx, health.PlatformInputs{Reader: s.Reader, Source: checkSource, Now: s.Now})
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

// Dashboards is each app's HyperDX dashboard, empty when the record cannot be read and none without Data.
func (s Server) Dashboards(ctx context.Context) map[string]string {
	if s.Data == nil {
		return nil
	}
	return dashboardsOf(ctx, s.Data)
}
