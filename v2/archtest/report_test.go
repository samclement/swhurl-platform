package archtest

// The change report: what a change to v2 does, as facts for a reviewer or an agent. It compares two
// trees, each a v2 directory read the same way from its own source and baselines, and never builds
// or runs either. It is a report, not a gate: make check-v2 decides pass or fail. Run it with
// `make v2-report`; without -base TestChangeReport skips, so go test ./... is unchanged.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	reportBase      = flag.String("base", "", "change report: the base tree's v2 directory; empty skips the report")
	reportAfter     = flag.String("after", root, "change report: the changed tree's v2 directory")
	reportBaseName  = flag.String("base-name", "base", "change report: how the report names the base")
	reportAfterName = flag.String("after-name", "working tree", "change report: how the report names the changed tree")
	reportOut       = flag.String("out", "", "change report: the directory for report.json and report.txt")
)

func TestChangeReport(t *testing.T) {
	if *reportBase == "" {
		t.Skip("the change report runs through make v2-report")
	}
	if *reportOut == "" {
		t.Fatal("-out is required with -base")
	}
	before, err := readTree(*reportBase)
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	after, err := readTree(*reportAfter)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	report := compareTrees(Side{*reportBaseName, before.Present}, before, Side{*reportAfterName, after.Present}, after)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*reportOut, "report.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*reportOut, "report.txt"), []byte(summary(report)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- One tree: the facts the report compares ----

// Tree is what one v2 directory holds. Present is false for a revision without v2 (an empty
// directory); every other field is then empty.
type Tree struct {
	Present    bool
	Files      map[string]string // path (relative, '/' separators) to SHA-256
	Packages   map[string]PackageFacts
	API        []string
	Measures   map[string]Size
	Budgets    Budgets // zero when the tree has no archtest/budgets.json
	Exceptions Exceptions
}

// PackageFacts: Source is whether the package has non-test files; Imports are sorted by path.
type PackageFacts struct {
	Source  bool
	Imports []Import
}

// Import is one imported path. Class is "standard", "local" (this module) or "external"; Test
// marks an import only test files use.
type Import struct {
	Path  string `json:"path"`
	Class string `json:"class"`
	Test  bool   `json:"test,omitempty"`
}

type Size struct {
	Complexity int `json:"complexity"`
	Lines      int `json:"lines"`
}

// Exceptions are the rule settings in the tree's own archtest source. Present is false, and the
// rest empty, when the tree has no archtest package.
type Exceptions struct {
	Present                  bool
	Layers                   map[string]int
	Pure, PureImports, Rules []string
}

// readTree reads a v2 directory. A missing directory, files without go.mod, Go that does not parse
// and rule settings that are not plain literals are errors, never an empty report.
func readTree(dir string) (Tree, error) {
	tree := Tree{Files: map[string]string{}, Packages: map[string]PackageFacts{}, Measures: map[string]Size{},
		Exceptions: Exceptions{Layers: map[string]int{}}}
	if err := hashFiles(dir, tree.Files); err != nil {
		return tree, err
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); errors.Is(err, fs.ErrNotExist) {
		if len(tree.Files) == 0 {
			return tree, nil
		}
		return tree, fmt.Errorf("%s has files but no go.mod", dir)
	}
	tree.Present = true
	m, err := loadModule(dir)
	if err != nil {
		return tree, err
	}
	for _, dir := range m.Dirs() {
		tree.Packages[dir] = packageFacts(m, m.Packages[dir])
	}
	tree.API = publicAPI(m)
	for _, measure := range measures(m) {
		tree.Measures[measure.Function] = Size{measure.Complexity, measure.Lines}
	}
	if budgets := filepath.Join(dir, "archtest", "budgets.json"); tree.Files["archtest/budgets.json"] != "" {
		if tree.Budgets, err = readBudgets(budgets); err != nil {
			return tree, err
		}
	}
	tree.Exceptions, err = readExceptions(m)
	return tree, err
}

func hashFiles(dir string, into map[string]string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		sum := sha256.Sum256(data)
		into[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
}

func packageFacts(m *Module, pkg *Package) PackageFacts {
	facts := PackageFacts{Source: len(pkg.Source()) > 0}
	inSource, inTests := map[string]bool{}, map[string]bool{}
	for _, file := range pkg.Files {
		for _, spec := range file.AST.Imports {
			if file.Test {
				inTests[importPath(spec)] = true
			} else {
				inSource[importPath(spec)] = true
			}
		}
	}
	for _, path := range slices.Sorted(maps.Keys(union(inSource, inTests))) {
		facts.Imports = append(facts.Imports, Import{path, importClass(m, path), !inSource[path]})
	}
	return facts
}

func union(a, b map[string]bool) map[string]bool {
	out := maps.Clone(a)
	maps.Copy(out, b)
	return out
}

func importClass(m *Module, path string) string {
	switch {
	case m.local(path) != "":
		return "local"
	case isStd(path):
		return "standard"
	}
	return "external"
}

// readExceptions reads Layers, Pure, PureImports and the rule IDs as literals from the archtest
// package's source, so a base revision is read exactly as the working tree is.
func readExceptions(m *Module) (Exceptions, error) {
	out := Exceptions{Layers: map[string]int{}}
	pkg := m.Packages["archtest"]
	if pkg == nil {
		return out, nil
	}
	out.Present = true
	values := map[string]ast.Expr{}
	for _, file := range pkg.Files {
		for _, decl := range file.AST.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					spec := spec.(*ast.ValueSpec)
					for i, name := range spec.Names {
						if i < len(spec.Values) {
							values[name.Name] = spec.Values[i]
						}
					}
				}
			}
		}
	}
	var err error
	if out.Layers, err = layerLiteral(values["Layers"]); err != nil {
		return out, err
	}
	if out.Pure, err = stringsLiteral(values["Pure"], "Pure"); err != nil {
		return out, err
	}
	if out.PureImports, err = stringsLiteral(values["PureImports"], "PureImports"); err != nil {
		return out, err
	}
	out.Rules, err = ruleIDsLiteral(values["Rules"])
	return out, err
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func elements(expr ast.Expr, name string) ([]ast.Expr, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("archtest: %s is not a literal the report can read", name)
	}
	return lit.Elts, nil
}

func layerLiteral(expr ast.Expr) (map[string]int, error) {
	elts, err := elements(expr, "Layers")
	out := map[string]int{}
	for _, elt := range elts {
		pair, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil, fmt.Errorf("archtest: Layers has an entry that is not \"dir\": layer")
		}
		dir, okDir := stringLiteral(pair.Key)
		lit, okLayer := pair.Value.(*ast.BasicLit)
		if !okDir || !okLayer || lit.Kind != token.INT {
			return nil, fmt.Errorf("archtest: Layers has an entry that is not \"dir\": layer")
		}
		out[dir], _ = strconv.Atoi(lit.Value)
	}
	return out, err
}

func stringsLiteral(expr ast.Expr, name string) ([]string, error) {
	elts, err := elements(expr, name)
	out := []string{}
	for _, elt := range elts {
		value, ok := stringLiteral(elt)
		if !ok {
			return nil, fmt.Errorf("archtest: %s has an entry that is not a string", name)
		}
		out = append(out, value)
	}
	slices.Sort(out)
	return out, err
}

// ruleIDsLiteral is the first field of each Rule literal (positional or ID: "...").
func ruleIDsLiteral(expr ast.Expr) ([]string, error) {
	elts, err := elements(expr, "Rules")
	out := []string{}
	for _, elt := range elts {
		rule, ok := elt.(*ast.CompositeLit)
		if !ok || len(rule.Elts) == 0 {
			return nil, fmt.Errorf("archtest: Rules has an entry that is not a Rule literal")
		}
		first := rule.Elts[0]
		if pair, ok := first.(*ast.KeyValueExpr); ok {
			first = pair.Value
		}
		id, ok := stringLiteral(first)
		if !ok {
			return nil, fmt.Errorf("archtest: Rules has an entry whose ID is not a string")
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, err
}

// ---- Two trees: the report ----

// ChangeReport is the JSON document `make v2-report FORMAT=json` prints. Lists are sorted and never
// null, so identical trees give identical bytes. Review lists the facts a change must explain.
type ChangeReport struct {
	Kind       string           `json:"kind"`
	Version    int              `json:"version"`
	Base       Side             `json:"base"`
	After      Side             `json:"after"`
	Review     []string         `json:"review"`
	Files      FileChanges      `json:"files"`
	Packages   PackageChanges   `json:"packages"`
	Imports    []ImportChange   `json:"imports"`
	API        APIChanges       `json:"api"`
	Budgets    BudgetChanges    `json:"budgets"`
	Exceptions ExceptionChanges `json:"exceptions"`
}

type Side struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
}

type FileChanges struct {
	Added   []FileChange `json:"added"`
	Changed []FileChange `json:"changed"`
	Removed []FileChange `json:"removed"`
}

// FileChange: Kind is module, baseline, rules, test, testdata, source or other.
type FileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type PackageChanges struct {
	Added   []PackageInfo `json:"added"`
	Removed []PackageInfo `json:"removed"`
}

// PackageInfo: Layer is null when the package has no declared layer.
type PackageInfo struct {
	Dir       string `json:"dir"`
	Layer     *int   `json:"layer"`
	Pure      bool   `json:"pure"`
	TestsOnly bool   `json:"testsOnly"`
}

type ImportChange struct {
	Package string   `json:"package"`
	Added   []Import `json:"added"`
	Removed []Import `json:"removed"`
}

type APIChanges struct {
	Added           []string `json:"added"`
	Removed         []string `json:"removed"`
	BaselineChanged bool     `json:"baselineChanged"`
}

type BudgetChanges struct {
	Limits    *LimitsChange    `json:"limits,omitempty"`
	Functions []FunctionChange `json:"functions"`
	Entries   []EntryChange    `json:"entries"`
}

type LimitsChange struct {
	Before Size `json:"before"`
	After  Size `json:"after"`
}

// FunctionChange: Before or After is null for a new or removed function. Listed and Over describe
// the changed tree: whether budgets.json lists it, and whether it exceeds the limits.
type FunctionChange struct {
	Function string `json:"function"`
	Before   *Size  `json:"before"`
	After    *Size  `json:"after"`
	Listed   bool   `json:"listed"`
	Over     bool   `json:"over"`
}

type EntryChange struct {
	Function string   `json:"function"`
	Before   *Allowed `json:"before"`
	After    *Allowed `json:"after"`
}

// ExceptionChanges: New is true when the base has no design rules and the changed tree has them.
type ExceptionChanges struct {
	New         bool          `json:"new"`
	Layers      []LayerChange `json:"layers"`
	Pure        ListChange    `json:"pure"`
	PureImports ListChange    `json:"pureImports"`
	Rules       ListChange    `json:"rules"`
}

type LayerChange struct {
	Dir    string `json:"dir"`
	Before *int   `json:"before"`
	After  *int   `json:"after"`
}

type ListChange struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

func compareTrees(baseSide Side, before Tree, afterSide Side, after Tree) ChangeReport {
	r := ChangeReport{Kind: "v2-change-report", Version: 1, Base: baseSide, After: afterSide, Review: []string{}}
	r.Files = compareFiles(before, after)
	r.Packages = comparePackages(before, after)
	r.Imports = compareImports(before, after)
	r.API = compareAPI(before, after, r.Files)
	r.Budgets = compareBudgets(before, after)
	r.Exceptions = compareExceptions(before.Exceptions, after.Exceptions)
	r.Review = review(r)
	return r
}

func compareFiles(before, after Tree) FileChanges {
	out := FileChanges{[]FileChange{}, []FileChange{}, []FileChange{}}
	for _, path := range sortedKeys(before.Files, after.Files) {
		old, had := before.Files[path]
		now, has := after.Files[path]
		change := FileChange{path, fileKind(path)}
		switch {
		case !had:
			out.Added = append(out.Added, change)
		case !has:
			out.Removed = append(out.Removed, change)
		case old != now:
			out.Changed = append(out.Changed, change)
		}
	}
	return out
}

func fileKind(path string) string {
	switch {
	case path == "go.mod" || path == "go.sum":
		return "module"
	case path == "archtest/api.txt" || path == "archtest/budgets.json":
		return "baseline"
	case strings.HasPrefix(path, "archtest/"):
		return "rules"
	case strings.HasPrefix(path, "testdata/") || strings.Contains(path, "/testdata/"):
		return "testdata"
	case strings.HasSuffix(path, "_test.go"):
		return "test"
	case strings.HasSuffix(path, ".go"):
		return "source"
	}
	return "other"
}

func comparePackages(before, after Tree) PackageChanges {
	out := PackageChanges{[]PackageInfo{}, []PackageInfo{}}
	for _, dir := range sortedKeys(before.Packages, after.Packages) {
		_, had := before.Packages[dir]
		_, has := after.Packages[dir]
		switch {
		case !had:
			out.Added = append(out.Added, packageInfo(after, dir))
		case !has:
			out.Removed = append(out.Removed, packageInfo(before, dir))
		}
	}
	return out
}

func packageInfo(tree Tree, dir string) PackageInfo {
	info := PackageInfo{Dir: dir, Pure: slices.Contains(tree.Exceptions.Pure, dir), TestsOnly: !tree.Packages[dir].Source}
	if layer, ok := tree.Exceptions.Layers[dir]; ok {
		info.Layer = &layer
	}
	return info
}

func compareImports(before, after Tree) []ImportChange {
	out := []ImportChange{}
	for _, dir := range sortedKeys(before.Packages, after.Packages) {
		old, now := before.Packages[dir].Imports, after.Packages[dir].Imports
		change := ImportChange{dir, missingFrom(old, now), missingFrom(now, old)}
		if len(change.Added)+len(change.Removed) > 0 {
			out = append(out, change)
		}
	}
	return out
}

// missingFrom is the entries of b that a does not have, in b's order.
func missingFrom[T comparable](a, b []T) []T {
	out := []T{}
	for _, item := range b {
		if !slices.Contains(a, item) {
			out = append(out, item)
		}
	}
	return out
}

func compareAPI(before, after Tree, files FileChanges) APIChanges {
	baseline := slices.ContainsFunc(slices.Concat(files.Added, files.Changed, files.Removed),
		func(f FileChange) bool { return f.Path == "archtest/api.txt" })
	return APIChanges{missingFrom(before.API, after.API), missingFrom(after.API, before.API), baseline}
}

func compareBudgets(before, after Tree) BudgetChanges {
	out := BudgetChanges{Functions: []FunctionChange{}, Entries: []EntryChange{}}
	oldLimits := Size{before.Budgets.MaxComplexity, before.Budgets.MaxLines}
	newLimits := Size{after.Budgets.MaxComplexity, after.Budgets.MaxLines}
	if oldLimits != newLimits {
		out.Limits = &LimitsChange{oldLimits, newLimits}
	}
	listed := entries(after.Budgets)
	for _, name := range sortedKeys(before.Measures, after.Measures) {
		old, had := before.Measures[name]
		now, has := after.Measures[name]
		if had && has && old == now {
			continue
		}
		change := FunctionChange{Function: name, Before: optional(old, had), After: optional(now, has)}
		_, change.Listed = listed[name]
		change.Over = has && (now.Complexity > newLimits.Complexity || now.Lines > newLimits.Lines)
		out.Functions = append(out.Functions, change)
	}
	previous := entries(before.Budgets)
	for _, name := range sortedKeys(previous, listed) {
		old, had := previous[name]
		now, has := listed[name]
		if !had || !has || old != now {
			out.Entries = append(out.Entries, EntryChange{name, optional(old, had), optional(now, has)})
		}
	}
	return out
}

func entries(budgets Budgets) map[string]Allowed {
	out := map[string]Allowed{}
	for _, entry := range budgets.Allow {
		out[entry.Function] = entry
	}
	return out
}

func optional[T any](value T, ok bool) *T {
	if !ok {
		return nil
	}
	return &value
}

func compareExceptions(before, after Exceptions) ExceptionChanges {
	out := ExceptionChanges{New: !before.Present && after.Present, Layers: []LayerChange{}}
	for _, dir := range sortedKeys(before.Layers, after.Layers) {
		old, had := before.Layers[dir]
		now, has := after.Layers[dir]
		if had != has || old != now {
			out.Layers = append(out.Layers, LayerChange{dir, optional(old, had), optional(now, has)})
		}
	}
	out.Pure = ListChange{missingFrom(before.Pure, after.Pure), missingFrom(after.Pure, before.Pure)}
	out.PureImports = ListChange{missingFrom(before.PureImports, after.PureImports), missingFrom(after.PureImports, before.PureImports)}
	out.Rules = ListChange{missingFrom(before.Rules, after.Rules), missingFrom(after.Rules, before.Rules)}
	return out
}

func sortedKeys[V any](a, b map[string]V) []string {
	keys := slices.Collect(maps.Keys(a))
	for key := range b {
		if _, ok := a[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// ---- What a change must explain ----

// review lists the facts a reviewer should see justified: a new v2, a changed go.mod, a new package
// and its layer, an external import, any change to a rule's settings, budgets or exported API.
func review(r ChangeReport) []string {
	out := []string{}
	if !r.Base.Present && r.After.Present {
		out = append(out, "v2 is new in this change")
	}
	for _, file := range slices.Concat(r.Files.Added, r.Files.Changed, r.Files.Removed) {
		if file.Kind == "module" && r.Base.Present {
			out = append(out, file.Path+" changed")
		}
	}
	for _, pkg := range r.Packages.Added {
		if !pkg.TestsOnly {
			out = append(out, "new package "+pkg.Dir+" ("+layerText(pkg.Layer)+")")
		}
	}
	for _, change := range r.Imports {
		for _, imp := range change.Added {
			if imp.Class == "external" {
				out = append(out, change.Package+" imports external module "+imp.Path)
			}
		}
	}
	if r.Exceptions.New {
		out = append(out, "the design rules are new in this change")
	} else {
		out = append(out, exceptionReview(r.Exceptions)...)
	}
	out = append(out, budgetReview(r.Budgets, r.Exceptions.New)...)
	if len(r.API.Added)+len(r.API.Removed) > 0 {
		baseline := "api.txt unchanged"
		if r.API.BaselineChanged {
			baseline = "api.txt changed"
		}
		out = append(out, fmt.Sprintf("exported API: +%d -%d (%s)", len(r.API.Added), len(r.API.Removed), baseline))
	}
	return out
}

func layerText(layer *int) string {
	if layer == nil {
		return "no declared layer"
	}
	return fmt.Sprintf("layer %d", *layer)
}

func exceptionReview(e ExceptionChanges) []string {
	var out []string
	for _, change := range e.Layers {
		switch {
		case change.Before == nil:
			out = append(out, fmt.Sprintf("Layers + %s %d", change.Dir, *change.After))
		case change.After == nil:
			out = append(out, fmt.Sprintf("Layers - %s %d", change.Dir, *change.Before))
		default:
			out = append(out, fmt.Sprintf("Layers %s %d → %d", change.Dir, *change.Before, *change.After))
		}
	}
	for _, list := range []struct {
		name   string
		change ListChange
	}{{"Pure", e.Pure}, {"PureImports", e.PureImports}, {"Rules", e.Rules}} {
		for _, item := range list.change.Added {
			out = append(out, list.name+" + "+item)
		}
		for _, item := range list.change.Removed {
			out = append(out, list.name+" - "+item)
		}
	}
	return out
}

// budgetReview: changed limits (unless the rules are new), and budgets.json entries added or raised
// (lowering is the ratchet).
func budgetReview(b BudgetChanges, rulesNew bool) []string {
	var out []string
	if b.Limits != nil && !rulesNew {
		out = append(out, fmt.Sprintf("budget limits %s → %s", sizeText(b.Limits.Before), sizeText(b.Limits.After)))
	}
	for _, entry := range b.Entries {
		switch {
		case entry.Before == nil:
			out = append(out, fmt.Sprintf("budgets.json lists %s at %d/%d: %s",
				entry.Function, entry.After.Complexity, entry.After.Lines, entry.After.Reason))
		case entry.After != nil && (entry.After.Complexity > entry.Before.Complexity || entry.After.Lines > entry.Before.Lines):
			out = append(out, fmt.Sprintf("budgets.json raises %s: %d/%d → %d/%d", entry.Function,
				entry.Before.Complexity, entry.Before.Lines, entry.After.Complexity, entry.After.Lines))
		}
	}
	return out
}

// ---- The text summary ----

const shownItems = 5

// summary is a few lines for a person: the review list, then one line per kind of change.
func summary(r ChangeReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "v2 change: %s → %s\n", sideText(r.Base), sideText(r.After))
	if len(r.Files.Added)+len(r.Files.Changed)+len(r.Files.Removed) == 0 {
		b.WriteString("no changes under v2/\n")
		return b.String()
	}
	if len(r.Review) > 0 {
		b.WriteString("review:\n")
		for _, line := range r.Review {
			b.WriteString("  ! " + line + "\n")
		}
	}
	section(&b, "files", []string{counted(len(r.Files.Added), "added"), counted(len(r.Files.Changed), "changed"),
		counted(len(r.Files.Removed), "removed")})
	section(&b, "packages", packageItems(r.Packages))
	section(&b, "imports", importItems(r.Imports))
	if len(r.API.Added)+len(r.API.Removed) > 0 {
		section(&b, "api", []string{counted(len(r.API.Added), "added"), counted(len(r.API.Removed), "removed")})
	}
	section(&b, "budgets (complexity/lines)", functionItems(r.Budgets.Functions))
	return b.String()
}

func sideText(s Side) string {
	if !s.Present {
		return s.Name + " (no v2)"
	}
	return s.Name
}

func counted(n int, what string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d %s", n, what)
}

// section writes "name: a, b, c" for the non-empty items, at most shownItems of them.
func section(b *strings.Builder, name string, items []string) {
	items = slices.DeleteFunc(items, func(item string) bool { return item == "" })
	if len(items) == 0 {
		return
	}
	if len(items) > shownItems {
		items = append(items[:shownItems], fmt.Sprintf("and %d more", len(items)-shownItems))
	}
	fmt.Fprintf(b, "%s: %s\n", name, strings.Join(items, ", "))
}

func packageItems(p PackageChanges) []string {
	var out []string
	for _, pkg := range p.Added {
		if pkg.TestsOnly {
			out = append(out, "+ "+pkg.Dir+" (tests only)")
		} else {
			out = append(out, "+ "+pkg.Dir+" ("+layerText(pkg.Layer)+")")
		}
	}
	for _, pkg := range p.Removed {
		out = append(out, "- "+pkg.Dir)
	}
	return out
}

func importItems(changes []ImportChange) []string {
	var out []string
	for _, change := range changes {
		for _, imp := range change.Added {
			out = append(out, change.Package+" + "+imp.Path)
		}
		for _, imp := range change.Removed {
			out = append(out, change.Package+" - "+imp.Path)
		}
	}
	return out
}

func functionItems(changes []FunctionChange) []string {
	var out []string
	for _, change := range changes {
		switch {
		case change.Before == nil:
			out = append(out, "+ "+change.Function+" "+sizeText(*change.After))
		case change.After == nil:
			out = append(out, "- "+change.Function)
		default:
			out = append(out, change.Function+" "+sizeText(*change.Before)+" → "+sizeText(*change.After))
		}
	}
	return out
}

func sizeText(s Size) string { return fmt.Sprintf("%d/%d", s.Complexity, s.Lines) }
