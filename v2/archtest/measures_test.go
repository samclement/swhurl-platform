package archtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"os"
	"regexp"
	"slices"
	"strings"
)

// ---- public-api: the exported surface of every library package, one declaration per line ----

// publicAPI lists the exported declarations of every package that is not a command, sorted, as
// "dir: declaration". Struct types show exported fields with their tags; methods show their
// receiver type, not its name; values show their type and value.
func publicAPI(m *Module) []string {
	var lines []string
	for _, dir := range m.Dirs() {
		for _, file := range m.Packages[dir].Source() {
			if file.AST.Name.Name == "main" {
				continue
			}
			for _, decl := range file.AST.Decls {
				for _, text := range exported(m.Fset, decl) {
					lines = append(lines, dir+": "+text)
				}
			}
		}
	}
	slices.Sort(lines)
	return lines
}

func exported(fset *token.FileSet, decl ast.Decl) []string {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		if !decl.Name.IsExported() || decl.Recv != nil && !ast.IsExported(receiverType(decl.Recv)) {
			return nil
		}
		copied := *decl
		copied.Doc, copied.Body = nil, nil
		if decl.Recv != nil {
			field := *decl.Recv.List[0]
			field.Names = nil
			copied.Recv = &ast.FieldList{List: []*ast.Field{&field}}
		}
		return []string{render(fset, &copied)}
	case *ast.GenDecl:
		var out []string
		for _, spec := range decl.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				if !spec.Name.IsExported() {
					continue
				}
				copied := *spec
				copied.Doc, copied.Comment = nil, nil
				if st, ok := spec.Type.(*ast.StructType); ok {
					fields := &ast.FieldList{}
					for _, field := range st.Fields.List {
						if len(field.Names) == 0 || field.Names[0].IsExported() {
							kept := *field
							kept.Doc, kept.Comment = nil, nil
							fields.List = append(fields.List, &kept)
						}
					}
					copied.Type = &ast.StructType{Fields: fields}
				}
				out = append(out, "type "+render(fset, &copied))
			case *ast.ValueSpec:
				for i, name := range spec.Names {
					if !name.IsExported() {
						continue
					}
					text := decl.Tok.String() + " " + name.Name
					if spec.Type != nil {
						text += " " + render(fset, spec.Type)
					}
					if i < len(spec.Values) {
						text += " = " + render(fset, spec.Values[i])
					}
					out = append(out, text)
				}
			}
		}
		return out
	}
	return nil
}

func receiverType(recv *ast.FieldList) string {
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

var spaces = regexp.MustCompile(`\s+`)

// render prints a node on one line with single spaces.
func render(fset *token.FileSet, node any) string {
	var out bytes.Buffer
	if err := printer.Fprint(&out, fset, node); err != nil {
		return fmt.Sprintf("<unprintable: %v>", err)
	}
	text := spaces.ReplaceAllString(out.String(), " ")
	return strings.NewReplacer("{ ", "{", " }", "}").Replace(text)
}

// ---- budgets: complexity and length per function, ratcheted ----

// Budgets is archtest/budgets.json: the limits, and the functions allowed above them. An allowed
// function may shrink but never grow; one that fits the limits again must be removed from the list.
type Budgets struct {
	MaxComplexity int       `json:"maxComplexity"`
	MaxLines      int       `json:"maxLines"`
	Allow         []Allowed `json:"allow"`
}

type Allowed struct {
	Function   string `json:"function"`
	Complexity int    `json:"complexity"`
	Lines      int    `json:"lines"`
	Reason     string `json:"reason"`
}

// Measure is one function's size: Function is "dir.Name" or "dir.Type.Method".
type Measure struct {
	Function   string
	Where      string
	Complexity int
	Lines      int
}

// measures covers every non-test function. Complexity is 1 plus each if, for, range, non-default
// case, select case, && and || (function literals count towards the function they are in).
func measures(m *Module) []Measure {
	var out []Measure
	for _, dir := range m.Dirs() {
		for _, file := range m.Packages[dir].Source() {
			for _, decl := range file.AST.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				name := dir + "." + fn.Name.Name
				if fn.Recv != nil {
					name = dir + "." + receiverType(fn.Recv) + "." + fn.Name.Name
				}
				complexity := 1
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					switch node := node.(type) {
					case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
						complexity++
					case *ast.CaseClause:
						if node.List != nil {
							complexity++
						}
					case *ast.CommClause:
						if node.Comm != nil {
							complexity++
						}
					case *ast.BinaryExpr:
						if node.Op == token.LAND || node.Op == token.LOR {
							complexity++
						}
					}
					return true
				})
				lines := m.Fset.Position(fn.End()).Line - m.Fset.Position(fn.Pos()).Line + 1
				out = append(out, Measure{name, m.at(file, fn.Pos()), complexity, lines})
			}
		}
	}
	return out
}

func readBudgets(path string) (Budgets, error) {
	var budgets Budgets
	data, err := os.ReadFile(path)
	if err != nil {
		return budgets, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&budgets); err != nil {
		return budgets, fmt.Errorf("%s: %w", path, err)
	}
	if budgets.MaxComplexity < 1 || budgets.MaxLines < 1 {
		return budgets, fmt.Errorf("%s: limits must be positive", path)
	}
	return budgets, nil
}

func checkBudgets(found []Measure, budgets Budgets) []Violation {
	var out []Violation
	allowed := map[string]Allowed{}
	for _, entry := range budgets.Allow {
		if strings.TrimSpace(entry.Reason) == "" {
			out = append(out, Violation{"budgets", "archtest/budgets.json", entry.Function + " is listed without a reason"})
		}
		allowed[entry.Function] = entry
	}
	seen := map[string]bool{}
	for _, measure := range found {
		seen[measure.Function] = true
		over := measure.Complexity > budgets.MaxComplexity || measure.Lines > budgets.MaxLines
		entry, listed := allowed[measure.Function]
		switch {
		case over && !listed:
			out = append(out, Violation{"budgets", measure.Where, fmt.Sprintf("%s has complexity %d and %d lines; the limits are %d and %d",
				measure.Function, measure.Complexity, measure.Lines, budgets.MaxComplexity, budgets.MaxLines)})
		case listed && (measure.Complexity > entry.Complexity || measure.Lines > entry.Lines):
			out = append(out, Violation{"budgets", measure.Where, fmt.Sprintf("%s grew to complexity %d and %d lines; budgets.json allows %d and %d",
				measure.Function, measure.Complexity, measure.Lines, entry.Complexity, entry.Lines)})
		case listed && !over:
			out = append(out, Violation{"budgets", "archtest/budgets.json", measure.Function + " now fits the limits: remove its entry"})
		case listed && (measure.Complexity < entry.Complexity || measure.Lines < entry.Lines):
			out = append(out, Violation{"budgets", "archtest/budgets.json", fmt.Sprintf("%s shrank to complexity %d and %d lines: lower its entry to match (or run with -update)",
				measure.Function, measure.Complexity, measure.Lines)})
		}
	}
	for _, entry := range budgets.Allow {
		if !seen[entry.Function] {
			out = append(out, Violation{"budgets", "archtest/budgets.json", entry.Function + " no longer exists: remove its entry"})
		}
	}
	return out
}

// tightened is budgets after -update: entries lowered to their current size, and removed once they
// fit the limits or the function is gone. It never adds an entry or raises one.
func tightened(found []Measure, budgets Budgets) Budgets {
	current := map[string]Measure{}
	for _, measure := range found {
		current[measure.Function] = measure
	}
	out := Budgets{MaxComplexity: budgets.MaxComplexity, MaxLines: budgets.MaxLines, Allow: []Allowed{}}
	for _, entry := range budgets.Allow {
		measure, ok := current[entry.Function]
		if !ok || measure.Complexity <= budgets.MaxComplexity && measure.Lines <= budgets.MaxLines {
			continue
		}
		entry.Complexity, entry.Lines = min(entry.Complexity, measure.Complexity), min(entry.Lines, measure.Lines)
		out.Allow = append(out.Allow, entry)
	}
	return out
}

// ---- rules-documented: the plan's Guardrails table and the rules agree ----

var tableRow = regexp.MustCompile("^\\| `([a-z-]+)` \\|")
var heading = regexp.MustCompile(`^#{1,6} (.+)$`)

// anchors are the GitHub anchors of a Markdown document's headings.
func anchors(markdown string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(markdown, "\n") {
		match := heading.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		var slug strings.Builder
		for _, r := range strings.ToLower(match[1]) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
				slug.WriteRune(r)
			case r == ' ':
				slug.WriteRune('-')
			}
		}
		out[slug.String()] = true
	}
	return out
}

// guardrailIDs are the rule IDs in the first column of the table under the "Guardrails" heading.
func guardrailIDs(markdown string) []string {
	var ids []string
	inside := false
	for _, line := range strings.Split(markdown, "\n") {
		if match := heading.FindStringSubmatch(line); match != nil {
			inside = strings.HasPrefix(match[1], "Guardrails")
			continue
		}
		if match := tableRow.FindStringSubmatch(line); inside && match != nil {
			ids = append(ids, match[1])
		}
	}
	return ids
}

func checkDocumented(markdown, agents string) []Violation {
	var out []Violation
	var want []string
	for _, rule := range Rules {
		want = append(want, rule.ID)
	}
	got := guardrailIDs(markdown)
	for _, id := range want {
		if !slices.Contains(got, id) {
			out = append(out, Violation{"rules-documented", plan, "the Guardrails table does not list " + id})
		}
	}
	for _, id := range got {
		if !slices.Contains(want, id) {
			out = append(out, Violation{"rules-documented", plan, "the Guardrails table lists " + id + ", which no check enforces"})
		}
	}
	headings := anchors(markdown)
	for _, rule := range Rules {
		if !headings[rule.Principle] {
			out = append(out, Violation{"rules-documented", plan, rule.ID + " links to #" + rule.Principle + ", which is not a heading"})
		}
	}
	if !strings.Contains(agents, "make check-v2") {
		out = append(out, Violation{"rules-documented", "AGENTS.md", "does not tell agents to run make check-v2"})
	}
	return out
}
