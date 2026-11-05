package workstreams

import (
	"fmt"
	"slices"
	"strings"
)

var (
	columns = []string{"Workstream", "Kind", "State", "Current slice", "Next gate", "Needs", "Page", "Paths"}
	kinds   = []string{"main", "sidequest"}
	states  = []string{"proposed", "approved", "in progress", "blocked", "parked", "done"}
	// empty is what a cell says when it says nothing.
	empty = []string{"", "—", "-"}
)

const statusSuffix = "-status.md"

// row is one workstream as the registry states it.
type row struct {
	name, kind, state, gate, needs string
	page                           string // docs/-relative path, plus an optional #anchor
	paths                          []string
}

// file is the row's page as a repository path; "" for a page outside the repository, which no file is.
func (r row) file() string {
	page, _, _ := strings.Cut(r.page, "#")
	file, _ := resolve("docs", page)
	return file
}

// parseRegistry parses the one table headed by columns, returning its rows and any structural problem.
func parseRegistry(text string) ([]row, []string) {
	all := lines(text)
	var starts []int
	for i, line := range all {
		if strings.HasPrefix(strings.TrimSpace(line), "| Workstream |") {
			starts = append(starts, i)
		}
	}
	if len(starts) != 1 {
		return nil, []string{fmt.Sprintf(`%s: expected exactly one table headed "| Workstream |", found %d`, Registry, len(starts))}
	}
	if header := cells(all[starts[0]]); !slices.Equal(header, columns) {
		return nil, []string{fmt.Sprintf("%s: columns are %s; found %s", Registry, strings.Join(columns, ", "), strings.Join(header, ", "))}
	}
	var rows []row
	var problems []string
	for _, line := range all[min(starts[0]+2, len(all)):] {
		if !isRow(line) {
			break
		}
		parsed, problem := parseRow(line)
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		rows = append(rows, parsed)
	}
	if len(rows) == 0 && len(problems) == 0 {
		problems = append(problems, Registry+": the table has no rows")
	}
	return rows, problems
}

func parseRow(line string) (row, string) {
	cell := cells(line)
	if len(cell) != len(columns) {
		return row{}, fmt.Sprintf("%s: a row has %d cells, not %d: %s", Registry, len(cell), len(columns), clip(strings.TrimSpace(line), 80))
	}
	targets := links(cell[6])
	if len(targets) == 0 {
		return row{}, fmt.Sprintf("%s: %s: Page must be a Markdown link, found %q", Registry, cell[0], cell[6])
	}
	var paths []string
	for _, path := range strings.Split(cell[7], ",") {
		if path = strings.Trim(strings.TrimSpace(path), "`"); !slices.Contains(empty, path) {
			paths = append(paths, path)
		}
	}
	return row{name: cell[0], kind: cell[1], state: cell[2], gate: cell[4], needs: cell[5], page: targets[0], paths: paths}, ""
}

func clip(text string, n int) string {
	if len(text) > n {
		return text[:n]
	}
	return text
}

// rowProblems is what a row states that the vocabulary or the checkout refuses.
func (c Checkout) rowProblems(r row) []string {
	var found []string
	where := Registry + ": " + r.name
	if !slices.Contains(kinds, r.kind) {
		found = append(found, fmt.Sprintf("%s: Kind is %q; expected one of %s", where, r.kind, strings.Join(kinds, ", ")))
	}
	if !slices.Contains(states, r.state) {
		found = append(found, fmt.Sprintf("%s: State is %q; expected one of %s", where, r.state, strings.Join(states, ", ")))
	}
	for _, cell := range [][2]string{{"Next gate", r.gate}, {"Needs", r.needs}} {
		if slices.Contains(empty, cell[1]) {
			found = append(found, fmt.Sprintf("%s: %s is empty; state what finishes it or what it waits for", where, cell[0]))
		}
	}
	page, anchor, _ := strings.Cut(r.page, "#")
	switch file := r.file(); {
	case !c.isFile(file):
		found = append(found, fmt.Sprintf("%s: Page %s does not exist", where, page))
	case anchor != "" && !anchors(c.Markdown[file])[anchor]:
		found = append(found, fmt.Sprintf("%s: Page %s has no heading %s", where, page, anchor))
	}
	for _, path := range r.paths {
		if !c.exists(path) {
			found = append(found, fmt.Sprintf("%s: Paths names %s, which does not exist", where, path))
		}
	}
	return found
}
