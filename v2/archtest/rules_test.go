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
	"path"
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
	{"layers", "every v2 package has a declared layer (0 pure model, 1 adapters, 2 use cases, 3 commands) and imports only v2 packages in lower layers",
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
	{"places", "a package's place matches its layer: layer 0 is listed in Pure, layer 1 is under adapters/, layer 2 is orchestration, layer 3 is a main package under cmd/",
		"move the package, or correct its entry in Layers or Pure (archtest/rules_test.go)",
		"extension-recipes"},
	{"adapter-complete", "each adapter adapters/<name> exports an interface, has raw fixtures in testdata/, and a fake in adapters/<name>/<name>test that satisfies each interface and runs one Contract suite with the real adapter; only tests import a fake",
		"add the missing piece the violation names, following the new-adapter recipe",
		"fixtures-and-fakes"},
	{"orchestration-wires", "orchestration packages parse no arguments, print nothing, choose no exit code and hold no mutable package state: they assemble, sequence and report, and the command above them does the rest",
		"move the argument parsing, printing or exit status into the command, and the decision into a pure package",
		"4-development-layers-and-adapters"},
	{"rules-documented", "the plan's Guardrails table lists exactly these rules and every principle link resolves",
		"update the table in docs/iteration-2-plan.md (or this file) so both say the same",
		"guardrails"},
}

// Layers: a package may import v2 packages only from lower layers. 0 is the pure model, 1 adapters
// (one per external dependency, speaking domain terms), 2 commands that wire adapters to the model.
var Layers = map[string]int{
	"calls":                    0,
	"kernel":                   0,
	"legacy":                   0,
	"observe":                  0,
	"shadow":                   0,
	"adapters/files":           1,
	"adapters/files/filestest": 1,
	"adapters/kube":            1,
	"adapters/kube/kubetest":   1,
	"adapters/s3":              1,
	"adapters/s3/s3test":       1,
	"orchestration":            2,
	"cmd/swhurl-v2":            3,
}

// Orchestration is the published list of packages that run use cases: they sequence pure decisions and the
// one effect a use case performs, and they are held to OrchestrationNot.
var Orchestration = []string{"orchestration"}

// OrchestrationImports are the packages a use case may not import: arguments, printing and process exit are
// the command's business, and a mutable package variable would make a use case's result depend on history.
var OrchestrationImports = []string{"flag", "os", "log", "testing"}
var OrchestrationCalls = []string{"fmt.Print", "fmt.Printf", "fmt.Println", "fmt.Fprint", "fmt.Fprintf", "fmt.Fprintln", "panic"}

// Pure packages: decisions only. Their inputs are arguments; they have no effects.
var Pure = []string{"calls", "kernel", "legacy", "observe", "shadow"}

// PureImports are the standard packages a pure package may use: none reads the clock, randomness,
// environment, files, network or processes. `time` is here for the timestamps a caller supplies — parsing,
// formatting and comparing them — not for the clock, which pureBannedCalls still refuses. Adding one is a
// reviewed change to this list.
var PureImports = []string{
	"bytes", "crypto/sha256", "encoding/hex", "encoding/json", "errors", "fmt", "maps", "math", "regexp",
	"slices", "sort", "strconv", "strings", "time", "unicode", "unicode/utf8",
}

// Printing is an effect even with an allowed package, and so is asking for the current time: a pure package
// formats and compares the times it was given, never the clock.
var pureBannedCalls = []string{"fmt.Print", "fmt.Printf", "fmt.Println", "time.Now", "time.Since", "time.Until",
	"time.After", "time.Tick", "time.Sleep", "time.NewTimer", "time.NewTicker", "time.AfterFunc"}

// Where each layer lives: adapters under adapters/, use cases under orchestration/, commands under cmd/.
const adapterRoot, orchestrationRoot, commandRoot = "adapters/", "orchestration/", "cmd/"

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
				if isFake(dir) && target == path.Dir(dir) {
					continue // a fake asserts it satisfies the adapter it fakes; the recipe requires it
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

// checkOrchestration holds the use-case layer to its contract: it may not parse arguments, print, exit or keep
// mutable package state, because those belong to the command above it, and a history-dependent use case is
// not a use case.
func checkOrchestration(m *Module, orchestration []string) []Violation {
	var found []Violation
	for _, dir := range m.Dirs() {
		if !slices.Contains(orchestration, dir) {
			continue
		}
		state := map[string]bool{}
		for _, file := range m.Packages[dir].Source() {
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
		for _, file := range m.Packages[dir].Source() {
			top := map[*ast.Object]bool{}
			for _, object := range file.AST.Scope.Objects {
				top[object] = true
			}
			for _, spec := range file.AST.Imports {
				if path := importPath(spec); slices.Contains(OrchestrationImports, path) {
					found = append(found, Violation{"orchestration-wires", m.at(file, spec.Pos()), "imports " + path})
				}
			}
			ast.Inspect(file.AST, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					if call := selector(node); slices.Contains(OrchestrationCalls, call) {
						found = append(found, Violation{"orchestration-wires", m.at(file, node.Pos()), "calls " + call})
					}
				case *ast.AssignStmt:
					if node.Tok != token.DEFINE {
						for _, target := range node.Lhs {
							if name := stateName(target, state, top); name != "" {
								found = append(found, Violation{"orchestration-wires", m.at(file, node.Pos()), "assigns package variable " + name})
							}
						}
					}
				}
				return true
			})
		}
	}
	return found
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

func checkPlaces(m *Module, layers map[string]int, pure []string) []Violation {
	var found []Violation
	for _, dir := range m.Dirs() {
		source := m.Packages[dir].Source()
		layer, declared := layers[dir]
		if len(source) == 0 || !declared {
			continue // tests only, or undeclared: the layers rule reports that
		}
		if problem := placeProblem(dir, layer, slices.Contains(pure, dir), source[0].AST.Name.Name == "main"); problem != "" {
			found = append(found, Violation{"places", source[0].Path, problem})
		}
	}
	return found
}

// placeProblem is what is wrong with a package's place for its layer, or "".
func placeProblem(dir string, layer int, pure, main bool) string {
	adapter := strings.HasPrefix(dir, adapterRoot)
	// The use-case layer is one package, so its own directory is the root.
	orchestration := dir == strings.TrimSuffix(orchestrationRoot, "/") || strings.HasPrefix(dir, orchestrationRoot)
	command := strings.HasPrefix(dir, commandRoot)
	switch {
	case layer < 0 || layer > 3:
		return fmt.Sprintf("%s has layer %d; the layers are 0 (pure model), 1 (adapters), 2 (use cases) and 3 (commands)", dir, layer)
	case layer == 0 && !pure:
		return dir + " is layer 0, the pure model, but is not listed in Pure"
	case layer != 0 && pure:
		return fmt.Sprintf("%s is listed in Pure but is layer %d", dir, layer)
	case layer == 1 && !adapter:
		return dir + " is layer 1 (adapters) but is not under " + adapterRoot
	case layer != 1 && adapter:
		return fmt.Sprintf("%s is under %s but is layer %d", dir, adapterRoot, layer)
	case layer == 2 && !orchestration:
		return dir + " is layer 2 (use cases) but is not " + orchestrationRoot
	case layer != 2 && orchestration:
		return fmt.Sprintf("%s is %s but is layer %d", dir, orchestrationRoot, layer)
	case layer == 3 && !(command && main):
		return dir + " is layer 3 (commands) but is not a main package under " + commandRoot
	case layer != 3 && command:
		return fmt.Sprintf("%s is under %s but is layer %d", dir, commandRoot, layer)
	}
	return ""
}

// ---- adapter-complete: the new-adapter recipe ----

// fakeOf is the fake package of an adapter: adapters/git has adapters/git/gittest.
func fakeOf(adapter string) string { return adapter + "/" + path.Base(adapter) + "test" }

// isFake is whether dir is some adapter's fake package.
func isFake(dir string) bool {
	parts := strings.Split(dir, "/")
	return len(parts) == 3 && parts[0]+"/" == adapterRoot && parts[2] == parts[1]+"test"
}

func checkAdapters(m *Module) []Violation {
	var found []Violation
	for _, dir := range m.Dirs() {
		source := m.Packages[dir].Source()
		if strings.HasPrefix(dir, adapterRoot) && strings.Count(dir, "/") == 1 && len(source) > 0 {
			found = append(found, checkAdapter(m, dir, source[0].Path)...)
		}
		for _, file := range source {
			if isFake(dir) {
				break // a fake may build on another fake
			}
			for _, spec := range file.AST.Imports {
				if target := m.local(importPath(spec)); isFake(target) {
					found = append(found, Violation{"adapter-complete", m.at(file, spec.Pos()), "imports the fake " + target + "; only tests use fakes"})
				}
			}
		}
	}
	return found
}

// checkAdapter checks one adapter against the recipe; where is a file of it, for the report.
func checkAdapter(m *Module, dir, where string) []Violation {
	var found []Violation
	missing := func(what string) { found = append(found, Violation{"adapter-complete", where, dir + " " + what}) }
	ports := interfaces(m.Packages[dir])
	if len(ports) == 0 {
		missing("exports no interface: the port its users depend on, in domain terms")
	}
	if entries, err := os.ReadDir(filepath.Join(m.Root, dir, "testdata")); err != nil || len(entries) == 0 {
		missing("has no raw fixtures in " + dir + "/testdata")
	}
	fake := m.Packages[fakeOf(dir)]
	if fake == nil || len(fake.Source()) == 0 {
		missing("has no fake package " + fakeOf(dir))
		return found
	}
	for _, port := range ports {
		if !asserts(m, fake.Source(), m.Path+"/"+dir, port) {
			missing(fmt.Sprintf("has no assertion in %s that a fake satisfies %s (var _ %s.%s = ...)", fakeOf(dir), port, path.Base(dir), port))
		}
	}
	if !declaresContract(fake.Source()) {
		missing("has no Contract(t *testing.T, ...) suite in " + fakeOf(dir))
	}
	if !callsContract(m, tests(m.Packages[dir]), m.Path+"/"+fakeOf(dir)) {
		missing("has no test running " + path.Base(fakeOf(dir)) + ".Contract against the real adapter")
	}
	if !callsContract(m, tests(fake), m.Path+"/"+fakeOf(dir)) {
		missing("has no test in " + fakeOf(dir) + " running Contract against the fake")
	}
	return found
}

func tests(pkg *Package) []File {
	return slices.DeleteFunc(slices.Clone(pkg.Files), func(f File) bool { return !f.Test })
}

// interfaces are the exported interface types a package declares, sorted.
func interfaces(pkg *Package) []string {
	var out []string
	for _, file := range pkg.Source() {
		for _, decl := range file.AST.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.TYPE {
				for _, spec := range gen.Specs {
					spec := spec.(*ast.TypeSpec)
					if _, ok := spec.Type.(*ast.InterfaceType); ok && spec.Name.IsExported() {
						out = append(out, spec.Name.Name)
					}
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// imported is the name each import of a file is known by.
func imported(file File) map[string]string {
	names := map[string]string{}
	for _, spec := range file.AST.Imports {
		name := path.Base(importPath(spec))
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = importPath(spec)
	}
	return names
}

// asserts is whether a file has `var _ pkg.Port = ...` with pkg the adapter at importPath: the
// compiler then proves the fake satisfies the interface.
func asserts(m *Module, files []File, adapter, port string) bool {
	for _, file := range files {
		names := imported(file)
		for _, decl := range file.AST.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				spec := spec.(*ast.ValueSpec)
				sel, ok := spec.Type.(*ast.SelectorExpr)
				if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "_" || len(spec.Values) != 1 || sel.Sel.Name != port {
					continue
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && names[pkg.Name] == adapter {
					return true
				}
			}
		}
	}
	return false
}

func declaresContract(files []File) bool {
	for _, file := range files {
		for _, decl := range file.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "Contract" && len(fn.Type.Params.List) > 0 &&
				render(token.NewFileSet(), fn.Type.Params.List[0].Type) == "*testing.T" {
				return true
			}
		}
	}
	return false
}

// callsContract is whether a test calls the fake package's Contract, as fake.Contract from another
// package or as Contract from inside it.
func callsContract(m *Module, files []File, fake string) bool {
	for _, file := range files {
		names := imported(file)
		inside := m.Path+"/"+path.Dir(file.Path) == fake && file.AST.Name.Name == path.Base(fake)
		calls := false
		ast.Inspect(file.AST, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return !calls
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				pkg, ok := fun.X.(*ast.Ident)
				calls = calls || ok && fun.Sel.Name == "Contract" && names[pkg.Name] == fake
			case *ast.Ident:
				calls = calls || inside && fun.Name == "Contract"
			}
			return !calls
		})
		if calls {
			return true
		}
	}
	return false
}
