package workstreams

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var (
	// link is a Markdown link's target: `[text](target)`, a target without spaces.
	link = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	// scheme marks a target that leaves the repository (https:, mailto:), which is not resolved here.
	scheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)
)

// lines splits a document into its lines, without their line endings.
func lines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	split := strings.Split(text, "\n")
	for i, line := range split {
		split[i] = strings.TrimSuffix(line, "\r")
	}
	return split
}

// cells is one table row's cells, trimmed.
func cells(line string) []string {
	split := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i, cell := range split {
		split[i] = strings.TrimSpace(cell)
	}
	return split
}

func isRow(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "|") }

// tableRows is the rows of the first table headed exactly header, without its separator; false when there is none.
func tableRows(text []string, header []string) ([][]string, bool) {
	start := slices.IndexFunc(text, func(line string) bool { return isRow(line) && slices.Equal(cells(line), header) })
	if start < 0 {
		return nil, false
	}
	rows := [][]string{}
	for _, line := range text[min(start+2, len(text)):] {
		if !isRow(line) {
			break
		}
		rows = append(rows, cells(line))
	}
	return rows, true
}

// section is the lines under one `## heading`, up to the next `## `; false when the heading is absent.
func section(text []string, heading string) ([]string, bool) {
	start := slices.IndexFunc(text, func(line string) bool { return strings.TrimSpace(line) == "## "+heading })
	if start < 0 {
		return nil, false
	}
	body := text[start+1:]
	if end := slices.IndexFunc(body, func(line string) bool { return strings.HasPrefix(line, "## ") }); end >= 0 {
		body = body[:end]
	}
	return body, true
}

// anchors is the GitHub-style anchor of every line that starts with '#': lower case, word characters, hyphens and
// spaces kept, spaces made hyphens.
func anchors(text string) map[string]bool {
	found := map[string]bool{}
	for _, line := range lines(text) {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
		kept := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-' || r == ' ' {
				return r
			}
			return -1
		}, heading)
		found[strings.ReplaceAll(kept, " ", "-")] = true
	}
	return found
}

// links is every link target in text, in order.
func links(text string) []string {
	var found []string
	for _, match := range link.FindAllStringSubmatch(text, -1) {
		found = append(found, match[1])
	}
	return found
}

// resolve joins a relative target to the folder it was written in, dropping "." and applying ".."; false when it
// leaves the repository or is absolute.
func resolve(folder, target string) (string, bool) {
	if strings.HasPrefix(target, "/") {
		return "", false
	}
	var kept []string
	for _, part := range strings.Split(folder+"/"+target, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(kept) == 0 {
				return "", false
			}
			kept = kept[:len(kept)-1]
		default:
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "/"), true
}

// under says whether file is path or lies inside it.
func under(file, path string) bool {
	return file == path || strings.HasPrefix(file, strings.TrimRight(path, "/")+"/")
}

func underAny(file string, paths []string) bool {
	return slices.ContainsFunc(paths, func(path string) bool { return under(file, path) })
}

// firstOf is at most n of items, for a message that names examples rather than everything.
func firstOf(items []string, n int) string { return strings.Join(items[:min(n, len(items))], ", ") }
