// Package workstreams keeps the workstream record honest: one table, docs/workstreams.md, and one progress page per
// workstream, compared with the checkout Git sees (docs/workstreams.md#what-make-check-docs-enforces).
//
// Progress drifts because status is prose: a page says "pending review" after the review, or a sidequest quietly
// becomes the main line, and only re-reading finds it. So the state lives once, in the table, and Check compares it
// with the repository: the table's shape, one workstream in progress, pages and paths that resolve, each status page's
// block against its row and against `git status`, cited commits a ref reaches, a change under a workstream's paths
// that also changes its page, and each progress page's shape and size.
//
// Nothing here judges whether a contract is good. It catches the record going stale, which is what makes a long
// migration hard to resume. Every input is an argument: the Git facts and the Markdown text are read by the caller.
package workstreams

import (
	"fmt"
	"slices"
	"strings"
)

// Registry is the one table's file, and where every page it names is resolved from.
const Registry = "docs/workstreams.md"

// Checkout is what the check reads: the repository as Git sees it, and the text of its Markdown files.
type Checkout struct {
	// Tracked is every file in the index; Present every file the working tree holds that Git does not ignore.
	Tracked, Present []string
	// Uncommitted is every path with a change not yet committed, untracked files included.
	Uncommitted []string
	// Shallow says Commits is a truncated history, so a citation cannot be checked against it.
	Shallow bool
	// Commits is the full hash of every commit a ref reaches.
	Commits []string
	// Markdown is the text of the files Needs names, by repository path.
	Markdown map[string]string
}

// Result is one check: what holds when it finds nothing, every problem it found, or why it could not run.
type Result struct {
	Okay     string
	Problems []string
	Warning  string
}

// Needs is the present Markdown files Check reads that c does not hold yet: the registry, then each row's page, then
// each Markdown file a status page's evidence names a heading of. A caller reads them and asks again until nothing is
// needed; each answer depends on text an earlier one supplied. An absent file is never needed: it is a finding.
func Needs(c Checkout) []string {
	registry, found := c.Markdown[Registry]
	if !found {
		if c.isFile(Registry) {
			return []string{Registry}
		}
		return nil
	}
	rows, _ := parseRegistry(registry)
	var needed []string
	for _, r := range rows {
		needed = append(needed, r.file())
	}
	for _, r := range c.statusPages(rows) {
		needed = append(needed, c.evidencePages(r.file())...)
	}
	needed = slices.DeleteFunc(needed, func(path string) bool {
		_, held := c.Markdown[path]
		return held || !strings.HasSuffix(path, ".md") || !c.isFile(path)
	})
	slices.Sort(needed)
	return slices.Compact(needed)
}

// Check runs every check in a fixed order and returns each one's result. A missing registry is the only result.
func Check(c Checkout) []Result {
	registry, found := c.Markdown[Registry]
	if !found {
		return []Result{{Problems: []string{Registry + " is missing"}}}
	}
	rows, problems := parseRegistry(registry)
	pages := c.statusPages(rows)
	results := []Result{
		result(problems, fmt.Sprintf("%s declares %d workstreams", Registry, len(rows))),
		result(each(rows, c.rowProblems), "every workstream names a page that resolves, a next gate and what it waits for"),
		inProgress(rows),
		result(each(pages, c.blockProblems), fmt.Sprintf("%d status page(s) agree with the registry", len(pages))),
		result(each(pages, c.progressProblems), fmt.Sprintf("%d progress page(s) keep the progress shape", len(pages))),
		result(c.trackedProblems(rows), "every owned path holds a tracked file"),
		result(each(pages, c.uncommittedProblems), "uncommitted work matches each status page"),
		c.cited(pages),
		result(each(rows, c.changedProblems), "no workstream code changed without its page"),
		result(c.orphanProblems(rows), "every status page is named by the registry"),
	}
	return results
}

func result(problems []string, okay string) Result { return Result{Okay: okay, Problems: problems} }

func each(rows []row, check func(row) []string) []string {
	var found []string
	for _, r := range rows {
		found = append(found, check(r)...)
	}
	return found
}

func inProgress(rows []row) Result {
	var working []string
	for _, r := range rows {
		if r.state == "in progress" {
			working = append(working, r.name)
		}
	}
	if len(working) > 1 {
		return result([]string{fmt.Sprintf("%d workstreams are in progress (%s); one at a time", len(working),
			strings.Join(working, ", "))}, "")
	}
	return result(nil, fmt.Sprintf("%d workstream(s) in progress", len(working)))
}

// exists says whether path is a present file or a folder holding one; "" is the repository itself.
func (c Checkout) exists(path string) bool {
	return path == "" || slices.ContainsFunc(c.Present, func(file string) bool { return under(file, path) })
}

func (c Checkout) isFile(path string) bool { return slices.Contains(c.Present, path) }

// statusPages is the rows whose page is a present `*-status.md` file.
func (c Checkout) statusPages(rows []row) []row {
	return slices.DeleteFunc(slices.Clone(rows), func(r row) bool {
		file := r.file()
		return !strings.HasSuffix(file[strings.LastIndex(file, "/")+1:], statusSuffix) || !c.isFile(file)
	})
}
