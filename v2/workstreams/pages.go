package workstreams

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	// blockFields are what a status page states near its top; the registry states the first two as well.
	blockFields = []string{"Status", "Gate", "Uncommitted"}
	// A progress page is a map, not a log: this is its shape, and its size is the forcing function.
	sections         = []string{"Where it stands", "Remaining work", "Deferred features", "Durable decisions", "How this page is kept"}
	capability       = []string{"Capability", "State", "Evidence"}
	capabilityStates = []string{"done", "partial"}
	deferred         = []string{"Feature", "v2 today", "Reopen when"}
	sliceRecord      = regexp.MustCompile(`(?m)^##\s+Slice record\b`)
)

const (
	// blockLines is how near the top a status page states its block.
	blockLines = 25
	// MaxLines is the most lines a progress page may have.
	MaxLines = 250
)

// block is the `Key: value` lines a status page states near its top, the first occurrence of each winning.
func block(text string) map[string]string {
	stated := map[string]string{}
	all := lines(text)
	for _, line := range all[:min(blockLines, len(all))] {
		key, value, found := strings.Cut(line, ":")
		if _, seen := stated[key]; found && slices.Contains(blockFields, key) && !seen {
			stated[key] = strings.TrimSpace(value)
		}
	}
	return stated
}

// blockProblems is where a status page's own state and gate disagree with its row.
func (c Checkout) blockProblems(r row) []string {
	where, stated := r.file(), block(c.Markdown[r.file()])
	var missing []string
	for _, key := range blockFields {
		if _, found := stated[key]; !found {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return []string{fmt.Sprintf("%s: a status page states %s in its first %d lines; missing %s", where,
			strings.Join(blockFields, ", "), blockLines, strings.Join(missing, ", "))}
	}
	var found []string
	if stated["Status"] != r.state {
		found = append(found, fmt.Sprintf("%s: Status is %q but the registry says %q", where, stated["Status"], r.state))
	}
	if stated["Gate"] != r.gate {
		found = append(found, fmt.Sprintf("%s: Gate is %q but the registry says %q", where, stated["Gate"], r.gate))
	}
	return found
}

// progressProblems checks a progress page's shape, not just its block: it is overwritten, not appended to.
func (c Checkout) progressProblems(r row) []string {
	where, text := r.file(), c.Markdown[r.file()]
	all := lines(text)
	var found []string
	if len(all) > MaxLines {
		found = append(found, fmt.Sprintf("%s: %d lines; a progress page stays at or under %d. Move narrative to Git "+
			"or current-state and delete what the goal no longer needs", where, len(all), MaxLines))
	}
	if sliceRecord.MatchString(text) {
		found = append(found, where+`: has a "## Slice record" heading; a progress page is overwritten, so the record `+
			"belongs in Git and one dated current-state entry")
	}
	var missing []string
	for _, name := range sections {
		if _, present := section(all, name); !present {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		found = append(found, fmt.Sprintf("%s: missing the section(s) %s; a progress page states Where it stands, "+
			"Remaining work, Deferred features, Durable decisions and How this page is kept", where, strings.Join(missing, ", ")))
	}
	if body, present := section(all, "Where it stands"); present {
		found = append(found, c.capabilityProblems(where, body)...)
	}
	if body, present := section(all, "Deferred features"); present {
		found = append(found, deferredProblems(where, body)...)
	}
	return found
}

// capabilityProblems checks the Where it stands table: a known state and evidence that resolves, per row.
func (c Checkout) capabilityProblems(where string, body []string) []string {
	rows, present := tableRows(body, capability)
	switch {
	case !present:
		return []string{fmt.Sprintf("%s: Where it stands has no \"| %s |\" table", where, strings.Join(capability, " | "))}
	case len(rows) == 0:
		return []string{where + ": Where it stands has no capability row"}
	}
	var found []string
	for _, cell := range rows {
		label := cell[0]
		if len(cell) != len(capability) {
			found = append(found, fmt.Sprintf("%s: capability %s: expected %d cells, found %d", where, label, len(capability), len(cell)))
			continue
		}
		if !slices.Contains(capabilityStates, strings.Trim(cell[1], "* ")) {
			found = append(found, fmt.Sprintf("%s: capability %s: State is %q; expected done or partial", where, label, cell[1]))
		}
		evidence := links(cell[2])
		if len(evidence) == 0 {
			found = append(found, fmt.Sprintf("%s: capability %s: Evidence has no link", where, label))
		}
		for _, target := range evidence {
			if problem := c.linkProblem(where, target); problem != "" {
				found = append(found, fmt.Sprintf("%s: capability %s: Evidence %s (%s)", where, label, target, problem))
			}
		}
	}
	return found
}

func deferredProblems(where string, body []string) []string {
	rows, present := tableRows(body, deferred)
	if !present {
		return []string{fmt.Sprintf("%s: Deferred features has no \"| %s |\" table", where, strings.Join(deferred, " | "))}
	}
	var found []string
	for _, cell := range rows {
		switch {
		case len(cell) != len(deferred):
			found = append(found, fmt.Sprintf("%s: a deferred row has %d cells, not %d", where, len(cell), len(deferred)))
		case cell[1] == "" || cell[2] == "":
			found = append(found, fmt.Sprintf("%s: deferred %s: state what v2 does today and what reopens it", where, cell[0]))
		}
	}
	return found
}

// linkProblem is why a link written in page does not resolve, or "" when it does. A target with a scheme leaves the
// repository and is not checked.
func (c Checkout) linkProblem(page, target string) string {
	if scheme.MatchString(target) {
		return ""
	}
	resolved, anchor, inside := linkTarget(page, target)
	if !inside || !c.exists(resolved) {
		return "missing file"
	}
	if anchor != "" && strings.HasSuffix(resolved, ".md") && !anchors(c.Markdown[resolved])[anchor] {
		return "missing heading"
	}
	return ""
}

// linkTarget is the repository path and anchor a link written in page names; a link with no path names page itself.
// false when the path leaves the repository.
func linkTarget(page, target string) (string, string, bool) {
	file, anchor, _ := strings.Cut(target, "#")
	if file == "" {
		return page, anchor, true
	}
	resolved, inside := resolve(page[:max(strings.LastIndex(page, "/"), 0)], file)
	return resolved, anchor, inside
}

// evidencePages is every file a status page's capability evidence names a heading of, which linkProblem reads.
func (c Checkout) evidencePages(page string) []string {
	body, _ := section(lines(c.Markdown[page]), "Where it stands")
	rows, _ := tableRows(body, capability)
	var found []string
	for _, cell := range rows {
		if len(cell) != len(capability) {
			continue
		}
		for _, target := range links(cell[2]) {
			if resolved, anchor, inside := linkTarget(page, target); inside && anchor != "" && !scheme.MatchString(target) {
				found = append(found, resolved)
			}
		}
	}
	return found
}
