package checkrepo

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// link is a Markdown link's target; any whitespace, Unicode separators included, ends it.
var link = regexp.MustCompile(`\[[^\]]*\]\(([^)\s\x0b\x1c-\x1f\x85\p{Z}]+)\)`)

// scheme marks a target that is a URL rather than a path in this checkout.
var scheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)

// unitRow is a shared unit's row in the architecture page's Flux units table: its name and its Inputs cell.
var unitRow = regexp.MustCompile("(?m)^\\| `((?:cluster|infra|platform)-[a-z0-9-]+)` \\|.*\\|([^|]*)\\|$")

// interfaceRow is a row of the app interface table: a name, its surface, meaning and availability.
var interfaceRow = regexp.MustCompile("(?m)^\\| `([^`]+)` \\| ([^|]+) \\| ([^|]+) \\| ([^|]+) \\|$")

// The app interface table lies between these two markers in docs/apps.md.
const interfaceStart, interfaceEnd = "<!-- app-interface -->", "<!-- /app-interface -->"

// checkLinks is every relative Markdown link naming a file or folder the checkout holds and, for a Markdown file, a
// heading it has. A link to a file Git ignores is broken: no other checkout holds it.
func checkLinks(r Repository) Result {
	result := Result{Check: docsLinks}
	checked := 0
	for _, file := range sortedText(r, isMarkdown) {
		for _, match := range link.FindAllStringSubmatch(r.Text[file], -1) {
			target := match[1]
			if scheme.MatchString(target) {
				continue
			}
			checked++
			if problem := linkProblem(r, file, target); problem != "" {
				result.Problems = append(result.Problems, fmt.Sprintf("broken documentation link: %s: %s (%s)", file, target, problem))
			}
		}
	}
	if len(result.Problems) == 0 {
		result.Okay = fmt.Sprintf("%d relative documentation links resolve", checked)
	}
	return result
}

// linkProblem is why one target written in file does not resolve, or "".
func linkProblem(r Repository, file, target string) string {
	pathPart, anchor, _ := strings.Cut(target, "#")
	resolved := file
	if pathPart != "" {
		folder, _, _ := cutLast(file, "/")
		var inside bool
		if resolved, inside = resolve(folder, pathPart); !inside {
			return "missing file"
		}
	}
	if !holds(r.Present, resolved) {
		return "missing file"
	}
	if anchor != "" && strings.HasSuffix(resolved, ".md") && !anchors(r.Text[resolved])[anchor] {
		return "missing heading"
	}
	return ""
}

// holds is whether the checkout holds path as a file or as a folder of files.
func holds(present []string, path string) bool {
	if path == "." {
		return true
	}
	return slices.ContainsFunc(present, func(file string) bool { return under(file, path) })
}

// anchors is the GitHub-style anchor of every line that starts with '#': lower case, letters, numbers, '_', '-' and
// spaces kept, spaces made hyphens.
func anchors(text string) map[string]bool {
	found := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
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

// checkUnitTable is the architecture page's Flux units table naming exactly the shared units Git defines, each with
// "settings" in its Inputs when it substitutes and "SOPS" when it decrypts.
func checkUnitTable(r Repository, units []unit) Result {
	result := Result{Check: fluxUnitsTable}
	text, held := r.Text[unitsDoc]
	if !held {
		result.Problems = []string{unitsDoc + " is missing, so its Flux units table cannot be compared with Git"}
		return result
	}
	rows := map[string]string{}
	for _, match := range unitRow.FindAllStringSubmatch(text, -1) {
		rows[match[1]] = match[2]
	}
	defined := shared(units)
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(defined)) {
		inputs, listed := rows[name]
		if !listed {
			problems = append(problems, name+" is not in the table")
			continue
		}
		problems = append(problems, inputProblems(defined[name], inputs)...)
	}
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		if _, inGit := defined[name]; !inGit {
			problems = append(problems, name+" is in the table but not in Git")
		}
	}
	slices.Sort(problems)
	for _, problem := range problems {
		result.Problems = append(result.Problems, unitsDoc+" Flux units table differs from Git: "+problem)
	}
	if len(result.Problems) == 0 {
		result.Okay = fmt.Sprintf("%s lists the %d shared Flux units and their inputs", unitsDoc, len(defined))
	}
	return result
}

// inputProblems is where a unit's Inputs cell disagrees with what its definition substitutes and decrypts.
func inputProblems(u unit, inputs string) []string {
	var problems []string
	for _, input := range []struct {
		word string
		has  bool
	}{{"settings", u.substitutes}, {"SOPS", u.decrypts}} {
		if strings.Contains(inputs, input.word) == input.has {
			continue
		}
		verb := "names"
		if input.has {
			verb = "omits"
		}
		problems = append(problems, fmt.Sprintf("%s: Inputs %s %s", u.name, verb, input.word))
	}
	return problems
}

// entry is one name the catalogue lists, with the three cells the table shows for it.
type entry struct{ surface, meaning, availability string }

// catalogueEntries is the names catalogue's entries by name, or why it cannot be read.
func catalogueEntries(r Repository) (map[string]entry, string) {
	var document struct {
		Names []struct{ Name, Surface, Meaning, Availability string }
	}
	if err := json.Unmarshal([]byte(r.Text[Catalogue]), &document); err != nil {
		return nil, Catalogue + ": " + err.Error()
	}
	entries := map[string]entry{}
	for _, e := range document.Names {
		entries[e.Name] = entry{e.Surface, e.Meaning, e.Availability}
	}
	return entries, ""
}

// checkInterfaceTable is the app interface table in docs/apps.md holding exactly the catalogue's names, once each,
// with the catalogue's surface, meaning and availability.
func checkInterfaceTable(r Repository) Result {
	result := Result{Check: appInterfaceTable}
	text, held := r.Text[appsDoc]
	entries, problem := catalogueEntries(r)
	if !held {
		problem = appsDoc + " is missing, so its app interface table cannot be compared with the catalogue"
	}
	if problem != "" {
		result.Problems = []string{problem}
		return result
	}
	_, section, _ := strings.Cut(text, interfaceStart)
	section, _, _ = strings.Cut(section, interfaceEnd)
	rows := interfaceRow.FindAllStringSubmatch(section, -1)
	problems := interfaceProblems(rows, entries)
	for _, problem := range problems {
		result.Problems = append(result.Problems, appsDoc+" app interface table differs from registry: "+problem)
	}
	if len(result.Problems) == 0 {
		result.Okay = fmt.Sprintf("%s lists the %d app interface entries", appsDoc, len(rows))
	}
	return result
}

// interfaceProblems is each name missing from the rows, extra, listed twice or described differently, sorted.
func interfaceProblems(rows [][]string, entries map[string]entry) []string {
	seen := map[string]int{}
	var problems []string
	for _, row := range rows {
		name := row[1]
		seen[name]++
		if want, listed := entries[name]; listed && (entry{row[2], row[3], row[4]}) != want {
			problems = append(problems, name+": description differs")
		}
	}
	for name, count := range seen {
		if _, listed := entries[name]; !listed {
			problems = append(problems, name+": extra")
		}
		if count > 1 {
			problems = append(problems, name+": duplicate")
		}
	}
	for name := range entries {
		if seen[name] == 0 {
			problems = append(problems, name+": missing")
		}
	}
	slices.Sort(problems)
	return slices.Compact(problems)
}

// checkEmitted is every platform name in v2's compiled tree listed in the catalogue. The backup label is the
// platform's own, on the backup job rather than the app's objects, so it is not an app's name.
func checkEmitted(r Repository, names Names) Result {
	result := Result{Check: emittedNames}
	files := sortedText(r, func(file string) bool { return under(file, compiled) })
	var problems []string
	for _, file := range files {
		for _, name := range names.Unlisted([]byte(r.Text[file])) {
			if name != backupLabel {
				problems = append(problems, name+": "+file)
			}
		}
	}
	slices.Sort(problems)
	for _, problem := range problems {
		result.Problems = append(result.Problems, compiled+" gives apps a name the app interface registry does not list: "+problem)
	}
	if len(result.Problems) == 0 {
		result.Okay = fmt.Sprintf("every platform name in %s (%d files) is in the app interface registry", compiled, len(files))
	}
	return result
}

// sortedText is the text files that keep, in order.
func sortedText(r Repository, keep func(string) bool) []string {
	var files []string
	for file := range r.Text {
		if keep(file) {
			files = append(files, file)
		}
	}
	slices.Sort(files)
	return files
}
