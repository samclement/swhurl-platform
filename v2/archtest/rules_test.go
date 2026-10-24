// Package archtest holds v2's design rules as code: each rule has an ID, the principle it enforces
// and how to fix a violation, and is checked by a test in this package. docs/iteration-2-plan.md
// (section "Guardrails") lists the same rules; TestRulesDocumented keeps the two in step.
//
// The package is test files only, so it is not part of v2, cannot be imported and is not itself
// subject to the rules it checks. It uses the standard library only and parses source; it never
// builds or runs the code it checks.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const plan = "docs/iteration-2-plan.md"

// Rule is one checked principle. Principle is a section of the v2 plan that explains why.
type Rule struct {
	ID, Statement, Fix, Principle string
}

var Rules = []Rule{
	{"stdlib-only", "v2 imports only the Go standard library and its own packages, and go.mod requires nothing",
		"remove the import; a third-party dependency needs the operator's approval first",
		"kubernetes-the-api-server-not-kubectl"},
	{"layers", "every v2 package has a declared layer and imports only v2 packages in lower layers",
		"declare a new package in Layers (archtest/rules_test.go), or move code so dependencies point down",
		"4-development-layers-and-adapters"},
	{"pure", "pure packages import only allowlisted standard packages, print nothing and keep no mutable package state",
		"pass the value in as a parameter (time, identifiers, snapshots) or move the effect into an adapter",
		"the-pure-model"},
	{"no-subprocess", "v2 code starts no processes and names no kubectl or flux command",
		"read or change Kubernetes through the API server, in an adapter",
		"kubernetes-the-api-server-not-kubectl"},
	{"public-api", "the exported API of every library package matches archtest/api.txt",
		"if the change is intended, run `go test ./archtest -run TestPublicAPI -update` and say why in the change",
		"guardrails"},
	{"budgets", "no function exceeds the complexity and length limits in archtest/budgets.json unless listed there, and listed ones never grow",
		"split the function; only if it truly cannot be split, add it to budgets.json with a reason (reviewed)",
		"guardrails"},
	{"rules-documented", "the plan's Guardrails table lists exactly these rules and every principle link resolves",
		"update the table in docs/iteration-2-plan.md (or this file) so both say the same",
		"guardrails"},
}

// Layers: a package may import v2 packages only from lower layers. 0 is the pure model, 1 adapters
// (one per external dependency, speaking domain terms), 2 commands that wire adapters to the model.
var Layers = map[string]int{
	"kernel":        0,
	"cmd/swhurl-v2": 2,
}

// Pure packages: decisions only. Their inputs are arguments; they have no effects.
var Pure = []string{"kernel"}

// PureImports are the standard packages a pure package may use: none reads the clock, randomness,
// environment, files, network or processes. Adding one is a reviewed change to this list.
var PureImports = []string{
	"bytes", "crypto/sha256", "encoding/hex", "encoding/json", "errors", "fmt", "maps", "math", "regexp",
	"slices", "sort", "strconv", "strings", "unicode", "unicode/utf8",
}

// Printing is an effect even with an allowed package.
var pureBannedCalls = []string{"fmt.Print", "fmt.Printf", "fmt.Println"}

var subprocessImports = []string{"os/exec", "syscall"}
var subprocessCalls = []string{"os.StartProcess"}
var commandNames = []string{"kubectl", "flux"}

// ---- Loading ----

// File is one parsed Go file; Path is relative to the module root, with '/' separators.
type File struct {
	Path string
	Test bool
	AST  *ast.File
}

// Package is the Go files of one directory; Dir is relative to the module root ("." for the root).
type Package struct {
	Dir   string
	Files []File
}

func (p Package) Source() []File {
	return slices.DeleteFunc(slices.Clone(p.Files), func(f File) bool { return f.Test })
}

// Module is the parsed v2 module: its path, go.mod and packages by directory.
type Module struct {
	Root, Path, GoMod string
	Fset              *token.FileSet
	Packages          map[string]*Package
}

func loadModule(root string) (*Module, error) {
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	path := ""
	for _, line := range strings.Split(string(gomod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			path = strings.TrimSpace(rest)
		}
	}
	if path == "" {
		return nil, fmt.Errorf("go.mod has no module line")
	}
	m := &Module{Root: root, Path: path, GoMod: string(gomod), Fset: token.NewFileSet(), Packages: map[string]*Package{}}
	err = filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if file != root && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		parsed, err := parser.ParseFile(m.Fset, file, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, file)
		dir := filepath.ToSlash(filepath.Dir(rel))
		if m.Packages[dir] == nil {
			m.Packages[dir] = &Package{Dir: dir}
		}
		m.Packages[dir].Files = append(m.Packages[dir].Files, File{filepath.ToSlash(rel), strings.HasSuffix(name, "_test.go"), parsed})
		return nil
	})
	return m, err
}

// Dirs is every package directory, sorted.
func (m *Module) Dirs() []string {
	dirs := make([]string, 0, len(m.Packages))
	for dir := range m.Packages {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	return dirs
}

// Violation is one broken rule at one place; its String is what the agent reads.
type Violation struct {
	Rule  string
	Where string // file:line, or a file
	What  string
}

func (v Violation) String() string { return fmt.Sprintf("%s %s: %s", v.Rule, v.Where, v.What) }

// advice is what to do about a rule's violations, and where its principle is explained.
func advice(id string) string {
	for _, rule := range Rules {
		if rule.ID == id {
			return fmt.Sprintf("%s: %s. Fix: %s. Why: %s#%s", rule.ID, rule.Statement, rule.Fix, plan, rule.Principle)
		}
	}
	return id
}

func (m *Module) at(file File, pos token.Pos) string {
	return fmt.Sprintf("%s:%d", file.Path, m.Fset.Position(pos).Line)
}

func importPath(spec *ast.ImportSpec) string {
	path, _ := strconv.Unquote(spec.Path.Value)
	return path
}

// isStd follows the Go convention: a standard package's first path element has no dot.
func isStd(path string) bool { return !strings.Contains(strings.Split(path, "/")[0], ".") }

// local is the module-relative directory of an import of this module, or "" for another import.
func (m *Module) local(path string) string {
	if path == m.Path {
		return "."
	}
	if rest, ok := strings.CutPrefix(path, m.Path+"/"); ok {
		return rest
	}
	return ""
}

// selector is "pkg.Name" for a call such as fmt.Println(...), or "".
func selector(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name + "." + sel.Sel.Name
}

// ---- Rules ----

func checkStdlibOnly(m *Module) []Violation {
	var found []Violation
	for _, line := range strings.Split(m.GoMod, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "require") {
			found = append(found, Violation{"stdlib-only", "go.mod", "requires a module"})
		}
	}
	for _, dir := range m.Dirs() {
		for _, file := range m.Packages[dir].Files {
			for _, spec := range file.AST.Imports {
				if path := importPath(spec); !isStd(path) && m.local(path) == "" {
					found = append(found, Violation{"stdlib-only", m.at(file, spec.Pos()), "imports " + path})
				}
			}
		}
	}
	return found
}

func checkLayers(m *Module) []Violation {
	var found []Violation
	for _, dir := range m.Dirs() {
		source := m.Packages[dir].Source()
		if len(source) == 0 {
			continue // tests only, such as the coexistence test and archtest itself
		}
		layer, declared := Layers[dir]
		if !declared {
			found = append(found, Violation{"layers", source[0].Path, fmt.Sprintf("package %s has no declared layer", dir)})
			continue
		}
		for _, file := range source {
			for _, spec := range file.AST.Imports {
				target := m.local(importPath(spec))
				if target == "" {
					continue
				}
				if other, ok := Layers[target]; !ok || other >= layer {
					found = append(found, Violation{"layers", m.at(file, spec.Pos()),
						fmt.Sprintf("%s (layer %d) imports %s (layer %s)", dir, layer, target, layerName(target))})
				}
			}
		}
	}
	return found
}

func layerName(dir string) string {
	if layer, ok := Layers[dir]; ok {
		return strconv.Itoa(layer)
	}
	return "undeclared"
}

func checkPure(m *Module) []Violation {
	var found []Violation
	for _, dir := range Pure {
		pkg := m.Packages[dir]
		if pkg == nil {
			found = append(found, Violation{"pure", dir, "a declared pure package does not exist"})
			continue
		}
		state := map[string]bool{} // package-level variables
		for _, file := range pkg.Source() {
			for _, decl := range file.AST.Decls {
				if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
					for _, spec := range gen.Specs {
						for _, name := range spec.(*ast.ValueSpec).Names {
							state[name.Name] = true
						}
					}
				}
			}
		}
		for _, file := range pkg.Source() {
			top := map[*ast.Object]bool{} // this file's package-level objects
			for _, object := range file.AST.Scope.Objects {
				top[object] = true
			}
			for _, spec := range file.AST.Imports {
				if path := importPath(spec); m.local(path) == "" && !slices.Contains(PureImports, path) {
					found = append(found, Violation{"pure", m.at(file, spec.Pos()), "imports " + path + ", which is not in PureImports"})
				}
			}
			ast.Inspect(file.AST, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					if call := selector(node); slices.Contains(pureBannedCalls, call) {
						found = append(found, Violation{"pure", m.at(file, node.Pos()), "calls " + call})
					}
				case *ast.AssignStmt:
					if node.Tok != token.DEFINE {
						for _, target := range node.Lhs {
							if name := stateName(target, state, top); name != "" {
								found = append(found, Violation{"pure", m.at(file, node.Pos()), "assigns package variable " + name})
							}
						}
					}
				case *ast.IncDecStmt:
					if name := stateName(node.X, state, top); name != "" {
						found = append(found, Violation{"pure", m.at(file, node.Pos()), "changes package variable " + name})
					}
				}
				return true
			})
		}
	}
	return found
}

// stateName is the package variable an assignment target changes (x, x[i], x.f, *x), or "".
// The parser resolves names to this file's objects; a name declared in another file of the
// package stays unresolved. A local of the same name resolves to the local and is not state.
func stateName(target ast.Expr, state map[string]bool, top map[*ast.Object]bool) string {
	for {
		switch expr := target.(type) {
		case *ast.IndexExpr:
			target = expr.X
		case *ast.SelectorExpr:
			target = expr.X
		case *ast.StarExpr:
			target = expr.X
		case *ast.ParenExpr:
			target = expr.X
		case *ast.Ident:
			if state[expr.Name] && (expr.Obj == nil || top[expr.Obj]) {
				return expr.Name
			}
			return ""
		default:
			return ""
		}
	}
}

func checkNoSubprocess(m *Module) []Violation {
	var found []Violation
	for _, dir := range m.Dirs() {
		for _, file := range m.Packages[dir].Source() {
			for _, spec := range file.AST.Imports {
				if path := importPath(spec); slices.Contains(subprocessImports, path) {
					found = append(found, Violation{"no-subprocess", m.at(file, spec.Pos()), "imports " + path})
				}
			}
			ast.Inspect(file.AST, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					if call := selector(node); slices.Contains(subprocessCalls, call) {
						found = append(found, Violation{"no-subprocess", m.at(file, node.Pos()), "calls " + call})
					}
				case *ast.BasicLit:
					if node.Kind == token.STRING {
						value, _ := strconv.Unquote(node.Value)
						if word := strings.Fields(value); len(word) > 0 && slices.Contains(commandNames, word[0]) {
							found = append(found, Violation{"no-subprocess", m.at(file, node.Pos()), "names the " + word[0] + " command"})
						}
					}
				}
				return true
			})
		}
	}
	return found
}
