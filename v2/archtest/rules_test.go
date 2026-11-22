// Package archtest holds v2's design rules as code: each rule has an ID, the principle it enforces
// and how to fix a violation, and is checked by a test in this package. docs/iteration-2-plan.md
// (section "Guardrails") lists the same rules; TestRulesDocumented keeps the two in step.
//
// The package is test files only, so it is not part of v2, cannot be imported and is not itself
// subject to the rules it checks. It uses the standard library only and parses source; it never
// builds or runs the code it checks.
package archtest

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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
	{"approved-modules", "v2 imports only the Go standard library, its own packages and the approved modules in Modules, each from the adapters it was approved for; go.mod requires exactly those modules at their approved versions",
		"remove the import, or ask the operator to approve the module and add it to Modules (archtest/rules_test.go) with the adapters that may import it",
		"third-party-modules"},
	{"layers", "every v2 package has a declared layer (0 pure model, 1 adapters, 2 use cases, 3 commands) and imports only v2 packages in lower layers, a pure package PureDependencies lists for it, or a workflow package WorkflowDependencies lists for it",
		"declare a new package in Layers (archtest/rules_test.go), or move code so dependencies point down; reuse owner pure verdicts through PureDependencies before requesting a named directional WorkflowDependencies edge; workflow edges must join use-case packages without self edges or cycles",
		"4-development-layers-and-adapters"},
	{"pure", "pure packages import only allowlisted standard packages, print nothing and keep no mutable package state",
		"pass the value in as a parameter (time, identifiers, snapshots) or move the effect into an adapter",
		"the-pure-model"},
	{"no-subprocess", "v2 code starts processes only in adapters/sops, adapters/helm, adapters/git and adapters/copier, names no kubectl or flux command, and names a process signal (syscall) only in cmd/swhurl-v2",
		"read or change Kubernetes through the API server; only SOPS encryption, explicit Helm pull/local template, read-only git and one pinned Copier render are approved; SIGTERM is the console's job drain, not a process",
		"kubernetes-the-api-server-not-kubectl"},
	{"public-api", "the exported API of every library package matches archtest/api.txt",
		"if the change is intended, run `go test ./archtest -run TestPublicAPI -update` and say why in the change",
		"guardrails"},
	{"budgets", "no function exceeds the complexity and length limits in archtest/budgets.json unless listed there, and listed ones never grow",
		"split the function; only if it truly cannot be split, add it to budgets.json with a reason (reviewed)",
		"guardrails"},
	{"places", "a package's place matches its layer: layer 0 is listed in Pure, layer 1 is under adapters/, layer 2 is listed in Orchestration, layer 3 is a main package under cmd/",
		"move the package, or correct its entry in Layers, Pure or Orchestration (archtest/rules_test.go)",
		"extension-recipes"},
	{"adapter-complete", "each adapter adapters/<name> exports an interface, has raw fixtures in testdata/, and a fake in adapters/<name>/<name>test that satisfies each interface and runs one Contract suite with the real adapter; only tests import a fake",
		"add the missing piece the violation names, following the new-adapter recipe",
		"fixtures-and-fakes"},
	{"orchestration-wires", "use-case packages (Orchestration) parse no arguments, print nothing, choose no exit code and hold no mutable package state: they assemble, sequence and report, and the command above them does the rest",
		"move the argument parsing, printing or exit status into the command, and the decision into a pure package",
		"4-development-layers-and-adapters"},
	{"rules-documented", "the plan's Guardrails table lists exactly these rules and every principle link resolves",
		"update the table in docs/iteration-2-plan.md (or this file) so both say the same",
		"guardrails"},
}

// Layers: a package may import v2 packages only from lower layers, except the named edges in
// PureDependencies and WorkflowDependencies. 0 is the pure model, 1 adapters
// (one per external dependency, speaking domain terms), 2 the use cases, 3 the command that wires them. A
// capability's pure rules are layer 0 (recovery/rules) and its use cases layer 2 (recovery).
var Layers = map[string]int{
	"certissuer":                         0,
	"checkrepo":                          0,
	"datadestroy":                        0,
	"secretcheck":                        0,
	"teamscenario":                       0,
	"telemetry":                          0,
	"lifecycletest":                      0,
	"apptemplatetest":                    0,
	"dashboards":                         0,
	"kernel":                             0,
	"observe":                            0,
	"pages":                              0,
	"policy":                             0,
	"producer":                           0,
	"workstreams":                        0,
	"adapters/appsource":                 1,
	"adapters/appsource/appsourcetest":   1,
	"adapters/copier":                    1,
	"adapters/copier/copiertest":         1,
	"adapters/clickstack":                1,
	"adapters/clickstack/clickstacktest": 1,
	"adapters/github":                    1,
	"adapters/github/githubtest":         1,
	"adapters/git":                       1,
	"adapters/git/gittest":               1,
	"adapters/helm":                      1,
	"adapters/helm/helmtest":             1,
	"adapters/hoststate":                 1,
	"adapters/hoststate/hoststatetest":   1,
	"adapters/files":                     1,
	"adapters/files/filestest":           1,
	"adapters/manifests":                 1,
	"adapters/manifests/manifeststest":   1,
	"adapters/nats":                      1,
	"adapters/nats/natstest":             1,
	"adapters/kube":                      1,
	"adapters/kube/kubetest":             1,
	"adapters/notifysvc":                 1,
	"adapters/notifysvc/notifysvctest":   1,
	"adapters/ntfy":                      1,
	"adapters/ntfy/ntfytest":             1,
	"adapters/otlp":                      1,
	"adapters/otlp/otlptest":             1,
	"adapters/registry":                  1,
	"adapters/registry/registrytest":     1,
	"adapters/route":                     1,
	"adapters/route/routetest":           1,
	"adapters/s3":                        1,
	"adapters/s3/s3test":                 1,
	"adapters/sops":                      1,
	"adapters/sops/sopstest":             1,
	"orchestration":                      2,
	"recovery":                           2,
	"recovery/rules":                     0,
	"cmd/swhurl-v2":                      3,

	// The notifications capability: its use cases and its pure rules.
	"notifications": 2, "notifications/rules/incident": 0, "notifications/rules/heartbeat": 0,
	"notifications/rules/notify": 0,

	// The health capability: its collectors and pure platform report and checks.
	"health": 2, "health/rules": 0,

	// The delivery capability: its use cases and its pure rules.
	"delivery": 2, "delivery/rules": 0,

	// The provisioning capability: writing an instance's folder and records, and its pure compilation of them.
	"provisioning": 2, "provisioning/rules": 0,

	// The apps capability: its status use cases, comparing the cluster with an instance's records.
	"apps": 2,

	// The connectivity capability: its pure resolvers of which calls and event subscriptions connect.
	"connectivity/rules/calls": 0, "connectivity/rules/events": 0,
}

// PureDependencies are the pure-to-pure imports allowed, by importer: each is a pure type or decision whose owner
// is the imported package, reported by the importer rather than re-derived. A new entry is a reviewed change, and
// a package everyone imports is the shared model this list exists to prevent. observe reports recovery's backup
// verdict and archive types and delivery's currentness and promotion readiness; health reports observed facts
// and recovery's archive verdict, using kernel's shared app vocabulary; delivery's promotion rule refines
// kernel's release selection; incident raises faults from delivery's deployment health verdict; provisioning
// assembles an instance's records into kernel's compiler and declaration (docs/iteration-2-plan.md#capability-map).
var PureDependencies = map[string][]string{
	"observe":            {"recovery/rules", "delivery/rules"},
	"health/rules":       {"observe", "recovery/rules", "kernel"},
	"delivery/rules":     {"kernel"},
	"provisioning/rules": {"kernel"},

	"notifications/rules/incident": {"delivery/rules"},
}

// Orchestration is the published list of packages that run use cases (layer 2): orchestration, and each
// capability's own use-case package. They sequence pure decisions and the effects a use case performs, are held
// to OrchestrationImports and OrchestrationCalls, and import another use-case package only through
// a named directional edge in WorkflowDependencies.
var Orchestration = []string{"orchestration", "recovery", "delivery", "notifications", "health", "apps", "provisioning"}

// WorkflowDependencies are the reviewed workflow-to-workflow imports allowed, by importer. Reuse the
// owner's pure verdict first; add an edge only when a use case needs the owner's workflow. Endpoints
// must be declared use-case packages; self edges and cycles are forbidden. The console's composition stays in
// orchestration: ConsoleChecks invokes health's collector and ConsoleStatus apps' status of every instance;
// reconsider these edges in the console slice. Recovery's restore takes apps' status as a port the command wires,
// so it needs no edge. Provisioning owns writing an instance's folder and records: orchestration's create and edit
// and delivery's deploy and promote write through it.
var WorkflowDependencies = map[string][]string{
	"orchestration": {"health", "apps", "provisioning"},
	"delivery":      {"provisioning"},
}

// OrchestrationImports are the packages a use case may not import: arguments, printing and process exit are
// the command's business, and a mutable package variable would make a use case's result depend on history.
var OrchestrationImports = []string{"flag", "os", "log", "testing"}
var OrchestrationCalls = []string{"fmt.Print", "fmt.Printf", "fmt.Println", "fmt.Fprint", "fmt.Fprintf", "fmt.Fprintln", "panic"}

// Pure packages: decisions only. Their inputs are arguments; they have no effects.
var Pure = []string{"health/rules", "notifications/rules/incident", "notifications/rules/heartbeat", "notifications/rules/notify", "apptemplatetest", "certissuer", "checkrepo", "dashboards", "datadestroy", "kernel", "lifecycletest", "observe", "pages", "policy", "producer", "recovery/rules", "secretcheck", "teamscenario", "telemetry", "workstreams", "delivery/rules", "provisioning/rules", "connectivity/rules/calls", "connectivity/rules/events"}

// encoding/base64 carries offered bytes; math/big compares source flag integers without rounding.
// PureImports are the standard packages a pure package may use: none reads the clock, randomness,
// environment, files, network or processes. `crypto/ed25519` derives a key pair from a seed the caller supplies
// (its own key generation, which draws randomness, is never called), and `encoding/base32` and
// `encoding/binary` state the nkeys text form. `crypto/subtle` compares the notify webhook's secret with the one the
// caller read, in constant time. `time` is here for the timestamps a caller supplies — parsing,
// formatting and comparing them — not for the clock, which pureBannedCalls still refuses. `net/url` builds and
// escapes the links a page shows: no I/O, clock or environment. Adding one is a reviewed change to this list.
var PureImports = []string{
	"bytes", "crypto/ed25519", "crypto/sha256", "crypto/subtle", "encoding/base32", "encoding/base64", "encoding/binary", "encoding/hex",
	"encoding/json", "errors", "fmt", "maps", "math", "math/big", "net/url", "regexp", "slices", "sort", "strconv", "strings", "time",
	"unicode", "unicode/utf8",
}

// Printing is an effect even with an allowed package, and so is asking for the current time: a pure package
// formats and compares the times it was given, never the clock.
var pureBannedCalls = []string{"fmt.Print", "fmt.Printf", "fmt.Println", "time.Now", "time.Since", "time.Until",
	"time.After", "time.Tick", "time.Sleep", "time.NewTimer", "time.NewTicker", "time.AfterFunc"}

// Where two layers live: adapters under adapters/, commands under cmd/. Use cases are the packages Orchestration lists.
const adapterRoot, commandRoot = "adapters/", "cmd/"

// ApprovedModule is one approved third-party module at its one approved version. Importers are the directories whose
// source may import its packages, and Tests the directories whose tests may; a module with neither comes only as
// another's requirement. Pure packages import none: rule pure allows them standard packages alone.
type ApprovedModule struct {
	Path, Version    string
	Importers, Tests []string
}

// Modules are the approved third-party modules (docs/iteration-2-plan.md#third-party-modules). Each was approved
// with its slice; a new entry, a new importer or a new version is the operator's approval first.
var Modules = []ApprovedModule{
	// Chart values, app sources, the checkout's Kubernetes manifests and a stack template's copier.yml.
	{"go.yaml.in/yaml/v3", "v3.0.6", []string{"adapters/helm", "adapters/appsource", "adapters/manifests", "adapters/copier"}, nil},
	// The flag store: the official client keeps the console's connection alive and reconnects it, and its tests run
	// against an embedded server under the console's rendered permissions.
	{"github.com/nats-io/nats.go", "v1.55.0", []string{"adapters/nats"}, []string{"adapters/nats"}},
	{"github.com/nats-io/nkeys", "v0.4.16", []string{"adapters/nats"}, []string{"adapters/nats"}},
	{"github.com/nats-io/nats-server/v2", "v2.15.2", nil, []string{"adapters/nats"}},
	{"github.com/nats-io/nuid", "v1.0.1", nil, nil},
	// S3 request signing: AWS's own SigV4 signer, the package alone (no service client).
	{"github.com/aws/aws-sdk-go-v2", "v1.48.0", []string{"adapters/s3"}, nil},
	{"github.com/aws/smithy-go", "v1.28.9", nil, nil},
	// The exec WebSocket: handshake, framing, pings and closes; the v5 channels stay adapters/kube's.
	{"github.com/coder/websocket", "v1.8.15", []string{"adapters/kube"}, nil},
	{"github.com/nats-io/jwt/v2", "v2.8.2", nil, nil},
	{"github.com/klauspost/compress", "v1.20.3", nil, nil},
	{"github.com/minio/highwayhash", "v1.0.4", nil, nil},
	{"github.com/google/go-tpm", "v0.9.8", nil, nil},
	{"github.com/antithesishq/antithesis-sdk-go", "v0.8.0-default-no-op", nil, nil},
	{"golang.org/x/crypto", "v0.58.0", nil, nil},
	{"golang.org/x/sys", "v0.49.0", nil, nil},
	{"golang.org/x/time", "v0.16.0", nil, nil},
}

var subprocessImports = []string{"os/exec"}
var subprocessCalls = []string{"os.StartProcess"}
var commandNames = []string{"kubectl", "flux"}

// signalImports is syscall, which starts nothing: it names SIGTERM, the signal a pod's replacement sends. Only the
// console's own command imports it, and only to drain the jobs it runs before the pod's grace period ends.
var signalImports = []string{"syscall"}
var signalAllowed = []string{"cmd/swhurl-v2"}

// subprocessAllowed is restricted to the four approved adapter directories, never their fakes or subpackages.
// SOPS encrypts, and decrypts locally with a key file the operator's command names; Helm explicitly acquires a pinned chart or renders a local chart; Git reads one checkout; Copier
// renders one pinned stack template. No exception permits subprocess cluster access or arbitrary commands.
var subprocessAllowed = []string{"adapters/sops", "adapters/helm", "adapters/git", "adapters/copier"}

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

// loadModule parses a module's Go files. files is nil to walk the directory, or the paths (relative,
// '/' separators) to read instead, so a caller can read a tree as Git carries it rather than as the
// disk holds it. Both count the same files: "go.mod" decides the module, and a .go file under
// testdata, a dot directory or an underscore one belongs to no package.
func loadModule(root string, files []string) (*Module, error) {
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
	if files != nil {
		return m, m.addFiles(files)
	}
	err = filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(root, file)
		if relErr != nil {
			return relErr
		}
		return m.addFile(filepath.ToSlash(rel))
	})
	return m, err
}

// addFiles parses the Go files among rel, in the order given; addFile is one of them.
func (m *Module) addFiles(rel []string) error {
	for _, path := range rel {
		if err := m.addFile(path); err != nil {
			return err
		}
	}
	return nil
}

// addFile parses one file (relative, '/' separators) into its package, unless it is not Go or not
// part of the module: the walk and the given paths must count the same files, or a report of one
// tree would describe packages the other never sees.
func (m *Module) addFile(rel string) error {
	if !strings.HasSuffix(rel, ".go") || !inModule(rel) {
		return nil
	}
	source := filepath.Join(m.Root, filepath.FromSlash(rel))
	if _, err := os.Stat(source); errors.Is(err, fs.ErrNotExist) {
		return nil // a listed path that is gone is a removal, not a file to parse
	}
	parsed, err := parser.ParseFile(m.Fset, source, nil, parser.ParseComments)
	if err != nil {
		return err
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if m.Packages[dir] == nil {
		m.Packages[dir] = &Package{Dir: dir}
	}
	m.Packages[dir].Files = append(m.Packages[dir].Files, File{rel, strings.HasSuffix(rel, "_test.go"), parsed})
	return nil
}

// inModule is whether a path is one the module holds: its directories are none of testdata, a dot
// directory or an underscore one.
func inModule(rel string) bool {
	for _, dir := range strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/") {
		if dir == "." || dir == "" {
			continue
		}
		if dir == "testdata" || strings.HasPrefix(dir, ".") || strings.HasPrefix(dir, "_") {
			return false
		}
	}
	return true
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

func checkApprovedModules(m *Module) []Violation {
	var found []Violation
	found = append(found, checkModuleRequirements(m.GoMod)...)
	for _, dir := range m.Dirs() {
		for _, file := range m.Packages[dir].Files {
			for _, spec := range file.AST.Imports {
				if path := importPath(spec); !isStd(path) && m.local(path) == "" && !importApproved(path, dir, file.Test) {
					found = append(found, Violation{"approved-modules", m.at(file, spec.Pos()), "imports " + path})
				}
			}
		}
	}
	return found
}

// importApproved is whether a directory's source, or its tests, may import a package of an approved module.
func importApproved(path, dir string, test bool) bool {
	for _, module := range Modules {
		if path == module.Path || strings.HasPrefix(path, module.Path+"/") {
			return slices.Contains(module.Importers, dir) || test && slices.Contains(module.Tests, dir)
		}
	}
	return false
}

// checkModuleRequirements keeps go.mod to the approved modules at their approved versions, each once, with no
// replace/exclude/tool escape hatch.
func checkModuleRequirements(text string) []Violation {
	var found []Violation
	inRequire, seen := false, map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "module" || fields[0] == "go" {
			continue
		}
		if line = strings.Join(fields, " "); line == "require (" {
			inRequire = true
			continue
		}
		if line == ")" && inRequire {
			inRequire = false
			continue
		}
		if fields[0] == "require" {
			fields = fields[1:]
		} else if !inRequire {
			found = append(found, Violation{"approved-modules", "go.mod", "unapproved module directive"})
			continue
		}
		if len(fields) != 2 || !requirementApproved(fields[0], fields[1]) || seen[fields[0]] {
			found = append(found, Violation{"approved-modules", "go.mod", "requires an unapproved module or version"})
		}
		if len(fields) > 0 {
			seen[fields[0]] = true
		}
	}
	return found
}

// requirementApproved is whether go.mod may require a module at a version.
func requirementApproved(path, version string) bool {
	return slices.ContainsFunc(Modules, func(module ApprovedModule) bool {
		return module.Path == path && module.Version == version
	})
}

func checkLayers(m *Module, workflows map[string][]string) []Violation {
	found := checkWorkflowDependencies(workflows)
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
				if other, ok := Layers[target]; !ok || (other >= layer && !pureDependency(dir, target) && !workflowDependency(workflows, dir, target)) {
					found = append(found, Violation{"layers", m.at(file, spec.Pos()),
						fmt.Sprintf("%s (layer %d) imports %s (layer %s)", dir, layer, target, layerName(target))})
				}
			}
		}
	}
	return found
}

// workflowPackage requires both the layer and the use-case registry, not a name or path convention.
func workflowPackage(dir string) bool {
	return Layers[dir] == 2 && slices.Contains(Orchestration, dir)
}

func workflowDependency(workflows map[string][]string, dir, target string) bool {
	return dir != target && workflowPackage(dir) && workflowPackage(target) && slices.Contains(workflows[dir], target)
}

// checkWorkflowDependencies validates the registry even when an edge is not yet imported.
func checkWorkflowDependencies(workflows map[string][]string) []Violation {
	var found []Violation
	var importers []string
	for dir := range workflows {
		importers = append(importers, dir)
	}
	slices.Sort(importers)
	valid := map[string][]string{}
	for _, dir := range importers {
		if !workflowPackage(dir) {
			found = append(found, Violation{"layers", "archtest/rules_test.go", "WorkflowDependencies importer " + dir + " is not a declared workflow package"})
		}
		for _, target := range workflows[dir] {
			switch {
			case !workflowPackage(target):
				found = append(found, Violation{"layers", "archtest/rules_test.go", "WorkflowDependencies target " + target + " is not a declared workflow package"})
			case dir == target:
				found = append(found, Violation{"layers", "archtest/rules_test.go", "WorkflowDependencies self edge: " + dir + " -> " + target})
			case workflowPackage(dir):
				valid[dir] = append(valid[dir], target)
			}
		}
	}
	return append(found, workflowCycles(valid, importers)...)
}

func workflowCycles(workflows map[string][]string, importers []string) []Violation {
	var found []Violation
	visited := map[string]bool{}
	var stack []string
	var visit func(string)
	visit = func(dir string) {
		if at := slices.Index(stack, dir); at >= 0 {
			cycle := append(slices.Clone(stack[at:]), dir)
			found = append(found, Violation{"layers", "archtest/rules_test.go", "WorkflowDependencies cycle: " + strings.Join(cycle, " -> ")})
			return
		}
		if visited[dir] {
			return
		}
		visited[dir] = true
		stack = append(stack, dir)
		for _, target := range workflows[dir] {
			visit(target)
		}
		stack = stack[:len(stack)-1]
	}
	for _, dir := range importers {
		visit(dir)
	}
	return found
}

// pureDependency is whether one pure package may import another: only an edge PureDependencies lists.
func pureDependency(dir, target string) bool {
	return Layers[dir] == 0 && Layers[target] == 0 && slices.Contains(PureDependencies[dir], target)
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
			allowed := slices.Contains(subprocessAllowed, dir)
			signals := slices.Contains(signalAllowed, dir)
			for _, spec := range file.AST.Imports {
				path := importPath(spec)
				if !allowed && slices.Contains(subprocessImports, path) {
					found = append(found, Violation{"no-subprocess", m.at(file, spec.Pos()), "imports " + path})
				}
				if !allowed && !signals && slices.Contains(signalImports, path) {
					found = append(found, Violation{"no-subprocess", m.at(file, spec.Pos()), "imports " + path})
				}
			}
			ast.Inspect(file.AST, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					if call := selector(node); !allowed && slices.Contains(subprocessCalls, call) {
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

func checkPlaces(m *Module, layers map[string]int, pure, useCases []string) []Violation {
	var found []Violation
	for _, dir := range m.Dirs() {
		source := m.Packages[dir].Source()
		layer, declared := layers[dir]
		if len(source) == 0 || !declared {
			continue // tests only, or undeclared: the layers rule reports that
		}
		if problem := placeProblem(dir, layer, slices.Contains(pure, dir), slices.Contains(useCases, dir),
			source[0].AST.Name.Name == "main"); problem != "" {
			found = append(found, Violation{"places", source[0].Path, problem})
		}
	}
	return found
}

// placeProblem is what is wrong with a package's place for its layer, or "".
func placeProblem(dir string, layer int, pure, useCase, main bool) string {
	adapter := strings.HasPrefix(dir, adapterRoot)
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
	case layer == 2 && !useCase:
		return dir + " is layer 2 (use cases) but is not listed in Orchestration"
	case layer != 2 && useCase:
		return fmt.Sprintf("%s is listed in Orchestration but is layer %d", dir, layer)
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
