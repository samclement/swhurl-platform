package workstreams

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	// cited is a commit a page names: an abbreviated or full hash in backticks.
	cited = regexp.MustCompile("`([0-9a-f]{7,40})`")
	// separators split the paths an Uncommitted line names.
	separators = regexp.MustCompile(`[,;]`)
)

// trackedProblems: every path a row owns must hold a tracked file, not merely exist on this disk. A folder of
// untracked files exists in one checkout and nowhere else, so a claim it satisfies would pass here and fail on a
// fresh clone, which is exactly what a reader has.
func (c Checkout) trackedProblems(rows []row) []string {
	var found []string
	for _, r := range rows {
		for _, path := range r.paths {
			if !slices.ContainsFunc(c.Tracked, func(file string) bool { return under(file, path) }) {
				found = append(found, fmt.Sprintf("%s: %s: Paths names %s, which holds no tracked file", Registry, r.name, path))
			}
		}
	}
	return found
}

// uncommittedProblems: `Uncommitted: none` must match a clean workstream, and named paths must cover its dirty ones.
// A row without paths owns the whole repository.
func (c Checkout) uncommittedProblems(r row) []string {
	stated := block(c.Markdown[r.file()])["Uncommitted"]
	dirty := slices.DeleteFunc(slices.Clone(c.Uncommitted), func(file string) bool {
		return len(r.paths) > 0 && !underAny(file, r.paths)
	})
	where := r.file() + ": Uncommitted"
	switch {
	case slices.Contains(empty, stated) || stated == "none":
		if len(dirty) > 0 {
			return []string{fmt.Sprintf("%s says none, but %s has uncommitted work: %s", where, r.name, firstOf(dirty, 5))}
		}
		return nil
	case len(dirty) == 0:
		return []string{fmt.Sprintf("%s says %q, but nothing under %s is uncommitted", where, stated, strings.Join(r.scope(), ", "))}
	}
	var named []string
	for _, part := range separators.Split(stated, -1) {
		named = append(named, strings.Trim(strings.TrimSpace(part), "`"))
	}
	if stray := slices.DeleteFunc(dirty, func(file string) bool { return underAny(file, named) }); len(stray) > 0 {
		return []string{fmt.Sprintf("%s omits %s", where, firstOf(stray, 5))}
	}
	return nil
}

// scope is what a row's paths cover, as a message names it.
func (r row) scope() []string {
	if len(r.paths) == 0 {
		return []string{"."}
	}
	return r.paths
}

// cited: every commit a status page cites must be reachable from a ref, not merely in the object store. A rewritten
// commit still resolves locally through the reflog while no reader of the repository can find it. In a shallow
// checkout the history is not there to check, which is a warning rather than a pass.
func (c Checkout) cited(pages []row) Result {
	if c.Shallow {
		return Result{Warning: "the checkout is shallow, so cited commits were not checked against history"}
	}
	var found, seen []string
	for _, r := range pages {
		for _, match := range cited.FindAllStringSubmatch(c.Markdown[r.file()], -1) {
			hash := match[1]
			if slices.Contains(seen, hash) {
				continue
			}
			seen = append(seen, hash)
			if !slices.ContainsFunc(c.Commits, func(commit string) bool { return strings.HasPrefix(commit, hash) }) {
				found = append(found, fmt.Sprintf("%s: cites %s, which no ref reaches (rewritten by a rebase, or never committed)", r.file(), hash))
			}
		}
	}
	return result(found, "every cited commit is reachable from a ref")
}

// changedProblems: a change under a workstream's paths must also change that workstream's page. A change is anything
// uncommitted, an untracked file included.
func (c Checkout) changedProblems(r row) []string {
	if len(r.paths) == 0 {
		return nil
	}
	var touched []string
	for _, file := range c.Uncommitted {
		if underAny(file, r.paths) {
			touched = append(touched, file)
		}
	}
	if len(touched) == 0 || slices.Contains(c.Uncommitted, r.file()) {
		return nil
	}
	return []string{fmt.Sprintf("%s changed without %s: record the slice on its page in the same change", firstOf(touched, 3), r.file())}
}

// orphanProblems: a tracked `docs/*-status.md` page no row names is a second, unchecked record of progress.
func (c Checkout) orphanProblems(rows []row) []string {
	var found []string
	for _, file := range c.Tracked {
		if !strings.HasPrefix(file, "docs/") || !strings.HasSuffix(file, statusSuffix) {
			continue
		}
		if !slices.ContainsFunc(rows, func(r row) bool { return r.file() == file }) {
			found = append(found, fmt.Sprintf("%s: a status page no row of %s names", file, Registry))
		}
	}
	return found
}
