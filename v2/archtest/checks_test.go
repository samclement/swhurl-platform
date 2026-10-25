package archtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Tests of the checks themselves: each runs on a small module written to a temporary directory,
// so a rule is shown to catch what it claims and to leave legitimate code alone.

func fixture(t *testing.T, files map[string]string) *Module {
	t.Helper()
	files["go.mod"] = "module example.com/v2\n\ngo 1.26.0\n"
	m, err := loadModule(writeFiles(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// writeFiles writes files (path: content) to a new temporary directory and returns it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func lines(found []Violation) []string {
	out := []string{}
	for _, violation := range found {
		out = append(out, violation.String())
	}
	return out
}

func expect(t *testing.T, found []Violation, want ...string) {
	t.Helper()
	got := lines(found)
	if !slices.Equal(got, want) {
		t.Errorf("got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestCheckStdlibOnly(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":      "package kernel\n\nimport (\n\t\"strings\"\n\t\"example.com/v2/other\"\n)\n",
		"kernel/a_test.go": "package kernel\n\nimport \"github.com/x/assert\"\n",
	})
	m.GoMod += "require github.com/x/assert v1.0.0\n"
	expect(t, checkStdlibOnly(m),
		"stdlib-only go.mod: requires a module",
		"stdlib-only kernel/a_test.go:3: imports github.com/x/assert")
}

func TestCheckLayers(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":           "package kernel\n\nimport \"example.com/v2/cmd/swhurl-v2\"\n",
		"cmd/swhurl-v2/main.go": "package main\n\nimport \"example.com/v2/kernel\"\n",
		"newpkg/b.go":           "package newpkg\n",
		"only/b_test.go":        "package only\n",
	})
	expect(t, checkLayers(m),
		"layers kernel/a.go:3: kernel (layer 0) imports cmd/swhurl-v2 (layer 2)",
		"layers newpkg/b.go: package newpkg has no declared layer")
}

func TestCheckPure(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go": `package kernel

import (
	"fmt"
	"os"
	"time"
)

var count int
var table = map[string]int{}
var pattern = 1 // read-only after initialisation: allowed

func f(count int) int {
	count++ // a parameter shadows the package variable
	table["x"] = pattern
	fmt.Println(os.Getenv("HOME"), time.Now())
	return fmt.Sprint(count) == "" && pattern > 0
}
`,
		"kernel/b.go":        "package kernel\n\nfunc g() { count = 2 }\n",
		"observe/explain.go": "package observe\n",
		"shadow/http.go":     "package shadow\n",
	})
	expect(t, checkPure(m),
		"pure kernel/a.go:5: imports os, which is not in PureImports",
		"pure kernel/a.go:6: imports time, which is not in PureImports",
		"pure kernel/a.go:15: assigns package variable table",
		"pure kernel/a.go:16: calls fmt.Println",
		"pure kernel/b.go:3: assigns package variable count")
}

func TestCheckNoSubprocess(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":      "package kernel\n\nimport \"os/exec\"\n\nvar c = exec.Command(\"kubectl get pods\")\nvar d = \"fluxion\"\n",
		"kernel/b.go":      "package kernel\n\nimport \"os\"\n\nfunc f() { os.StartProcess(\"flux\", nil, nil) }\n",
		"kernel/b_test.go": "package kernel\n\nimport \"os/exec\"\n\nvar e = exec.Command(\"go\")\n",
	})
	expect(t, checkNoSubprocess(m),
		"no-subprocess kernel/a.go:3: imports os/exec",
		"no-subprocess kernel/a.go:5: names the kubectl command",
		"no-subprocess kernel/b.go:5: calls os.StartProcess",
		"no-subprocess kernel/b.go:5: names the flux command")
}

func TestCheckPlaces(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":         "package kernel\n",
		"adapters/git/git.go": "package git\n",
		"cmd/tool/main.go":    "package main\n",
		"undeclared/u.go":     "package undeclared\n",
		"only/o_test.go":      "package only\n",
		"model/a.go":          "package model\n",
		"listed/l.go":         "package listed\n",
		"store/s.go":          "package store\n",
		"adapters/bad/b.go":   "package bad\n",
		"cmd/lib/lib.go":      "package lib\n",
		"tools/x/main.go":     "package main\n",
		"cmd/pure/p.go":       "package pure\n",
		"odd/o.go":            "package odd\n",
	})
	layers := map[string]int{"kernel": 0, "adapters/git": 1, "cmd/tool": 2, "only": 2, "model": 0, "listed": 1, "store": 1,
		"adapters/bad": 2, "cmd/lib": 2, "tools/x": 2, "cmd/pure": 0, "odd": 3}
	expect(t, checkPlaces(m, layers, []string{"kernel", "listed", "cmd/pure"}),
		"places adapters/bad/b.go: adapters/bad is under adapters/ but is layer 2",
		"places cmd/lib/lib.go: cmd/lib is layer 2 (commands) but is not a main package under cmd/",
		"places cmd/pure/p.go: cmd/pure is under cmd/ but is layer 0",
		"places listed/l.go: listed is listed in Pure but is layer 1",
		"places model/a.go: model is layer 0, the pure model, but is not listed in Pure",
		"places odd/o.go: odd has layer 3; the layers are 0 (pure model), 1 (adapters) and 2 (commands)",
		"places store/s.go: store is layer 1 (adapters) but is not under adapters/",
		"places tools/x/main.go: tools/x is layer 2 (commands) but is not a main package under cmd/")
}

// completeAdapter is an adapter that follows the new-adapter recipe, used by a command and its test.
func completeAdapter() map[string]string {
	return map[string]string{
		"adapters/git/git.go": "package git\n\ntype Reader interface{ Read(path string) ([]byte, error) }\n\n" +
			"type Repo struct{}\n\nfunc (Repo) Read(string) ([]byte, error) { return nil, nil }\n",
		"adapters/git/testdata/tree.json": "{}\n",
		"adapters/git/git_test.go": "package git_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/v2/adapters/git\"\n" +
			"\tfake \"example.com/v2/adapters/git/gittest\"\n)\n\nfunc TestContract(t *testing.T) { fake.Contract(t, git.Repo{}) }\n",
		"adapters/git/gittest/fake.go": "package gittest\n\nimport (\n\t\"testing\"\n\n\t\"example.com/v2/adapters/git\"\n)\n\n" +
			"type Fake struct{}\n\nvar _ git.Reader = (*Fake)(nil)\n\nfunc (*Fake) Read(string) ([]byte, error) { return nil, nil }\n\n" +
			"func Contract(t *testing.T, r git.Reader) {}\n",
		"adapters/git/gittest/fake_test.go": "package gittest\n\nimport \"testing\"\n\nfunc TestFake(t *testing.T) { Contract(t, &Fake{}) }\n",
		"cmd/tool/main.go":                  "package main\n\nimport \"example.com/v2/adapters/git\"\n\nvar _ = git.Repo{}\n",
		"cmd/tool/main_test.go":             "package main\n\nimport \"example.com/v2/adapters/git/gittest\"\n\nvar _ = gittest.Fake{}\n",
	}
}

func TestCheckAdapters(t *testing.T) {
	expect(t, checkAdapters(fixture(t, completeAdapter()))) // tests may import a fake; an aliased import counts
	const at = "adapter-complete adapters/git/git.go: adapters/git "
	for _, tt := range []struct {
		name    string
		changes map[string]string
		want    []string
	}{
		{"no interface", map[string]string{"adapters/git/git.go": "package git\n\ntype Reader struct{}\n"},
			[]string{at + "exports no interface: the port its users depend on, in domain terms"}},
		{"no raw fixtures", map[string]string{"adapters/git/testdata/tree.json": ""},
			[]string{at + "has no raw fixtures in adapters/git/testdata"}},
		{"no fake", map[string]string{"adapters/git/gittest/fake.go": "", "adapters/git/gittest/fake_test.go": ""},
			[]string{at + "has no fake package adapters/git/gittest"}},
		{"no assertion", map[string]string{"adapters/git/gittest/fake.go": strings.Replace(completeAdapter()["adapters/git/gittest/fake.go"],
			"var _ git.Reader = (*Fake)(nil)", "var _ = (*Fake)(nil)", 1)},
			[]string{at + "has no assertion in adapters/git/gittest that a fake satisfies Reader (var _ git.Reader = ...)"}},
		{"assertion against another package", map[string]string{"adapters/git/gittest/fake.go": strings.NewReplacer(
			"var _ git.Reader", "var _ io.Reader", "import (", "import (\n\t\"io\"").Replace(completeAdapter()["adapters/git/gittest/fake.go"])},
			[]string{at + "has no assertion in adapters/git/gittest that a fake satisfies Reader (var _ git.Reader = ...)"}},
		{"Contract without *testing.T", map[string]string{"adapters/git/gittest/fake.go": strings.Replace(completeAdapter()["adapters/git/gittest/fake.go"],
			"func Contract(t *testing.T, r git.Reader)", "func Contract(r git.Reader)", 1)},
			[]string{at + "has no Contract(t *testing.T, ...) suite in adapters/git/gittest"}},
		{"real adapter not run through Contract", map[string]string{"adapters/git/git_test.go": ""},
			[]string{at + "has no test running gittest.Contract against the real adapter"}},
		{"real adapter run through another Contract", map[string]string{"adapters/git/git_test.go": strings.Replace(
			completeAdapter()["adapters/git/git_test.go"], `fake "example.com/v2/adapters/git/gittest"`, `fake "example.com/v2/adapters/other/othertest"`, 1)},
			[]string{at + "has no test running gittest.Contract against the real adapter"}},
		{"fake not run through Contract", map[string]string{"adapters/git/gittest/fake_test.go": ""},
			[]string{at + "has no test in adapters/git/gittest running Contract against the fake"}},
		{"a command imports the fake", map[string]string{"cmd/tool/main.go": "package main\n\nimport \"example.com/v2/adapters/git/gittest\"\n"},
			[]string{"adapter-complete cmd/tool/main.go:3: imports the fake adapters/git/gittest; only tests use fakes"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expect(t, checkAdapters(fixture(t, with(completeAdapter(), tt.changes))), tt.want...)
		})
	}
}

func TestPublicAPIRendering(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":           "package kernel\n\n// Doc is dropped.\ntype T struct {\n\tA string `json:\"a\"`\n\tb int\n}\n\nfunc (t *T) Get(x int) string { return \"\" }\nfunc (t *T) hidden() {}\nfunc New() T { return T{} }\n\nconst C, d = 1, 2\n",
		"cmd/swhurl-v2/main.go": "package main\n\nfunc Exported() {}\n",
	})
	got := publicAPI(m)
	want := []string{"kernel: const C = 1", "kernel: func (*T) Get(x int) string", "kernel: func New() T",
		"kernel: type T struct {A string `json:\"a\"`}"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCheckBudgets(t *testing.T) {
	limits := Budgets{MaxComplexity: 3, MaxLines: 100}
	found := []Measure{
		{"kernel.small", "kernel/a.go:1", 2, 5},
		{"kernel.big", "kernel/a.go:9", 5, 5},
		{"kernel.listed", "kernel/a.go:20", 4, 5},
		{"kernel.grew", "kernel/a.go:30", 6, 5},
		{"kernel.shrank", "kernel/a.go:40", 4, 5},
		{"kernel.fits", "kernel/a.go:50", 3, 5},
	}
	limits.Allow = []Allowed{
		{"kernel.listed", 4, 5, "reason"}, {"kernel.grew", 5, 5, "reason"}, {"kernel.shrank", 5, 5, "reason"},
		{"kernel.fits", 4, 5, "reason"}, {"kernel.gone", 4, 5, "reason"}, {"kernel.small", 9, 9, ""},
	}
	expect(t, checkBudgets(found, limits),
		"budgets archtest/budgets.json: kernel.small is listed without a reason",
		"budgets archtest/budgets.json: kernel.small now fits the limits: remove its entry",
		"budgets kernel/a.go:9: kernel.big has complexity 5 and 5 lines; the limits are 3 and 100",
		"budgets kernel/a.go:30: kernel.grew grew to complexity 6 and 5 lines; budgets.json allows 5 and 5",
		"budgets archtest/budgets.json: kernel.shrank shrank to complexity 4 and 5 lines: lower its entry to match (or run with -update)",
		"budgets archtest/budgets.json: kernel.fits now fits the limits: remove its entry",
		"budgets archtest/budgets.json: kernel.gone no longer exists: remove its entry")
	// -update tightens: it lowers and removes entries, and never adds the unlisted kernel.big.
	tight := tightened(found, limits)
	var names []string
	for _, entry := range tight.Allow {
		names = append(names, entry.Function)
	}
	if !slices.Equal(names, []string{"kernel.listed", "kernel.grew", "kernel.shrank"}) || tight.Allow[2].Complexity != 4 || tight.Allow[1].Complexity != 5 {
		t.Errorf("tightened: %+v", tight.Allow)
	}
}

func TestMeasures(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go": "package kernel\n\nfunc f(a, b bool, xs []int) int {\n\tif a && b || a {\n\t\treturn 1\n\t}\n\tfor range xs {\n\t}\n\tswitch {\n\tcase a:\n\tdefault:\n\t}\n\treturn 0\n}\n",
	})
	got := measures(m)
	if len(got) != 1 || got[0] != (Measure{"kernel.f", "kernel/a.go:3", 6, 12}) {
		t.Errorf("got %+v", got)
	}
}

func TestCheckDocumented(t *testing.T) {
	var table strings.Builder
	table.WriteString("# Plan\n\n## The pure model\n\n### Guardrails\n\n| Rule | What |\n| --- | --- |\n")
	for _, rule := range Rules[1:] {
		table.WriteString("| `" + rule.ID + "` | x |\n")
	}
	table.WriteString("| `retired` | x |\n\n## Next\n\n| `outside` | not in the table |\n")
	found := lines(checkDocumented(table.String(), "run make check-v2"))
	for _, want := range []string{"the Guardrails table does not list " + Rules[0].ID, "lists retired, which no check enforces",
		"links to #kubernetes-the-api-server-not-kubectl, which is not a heading"} {
		if !slices.ContainsFunc(found, func(line string) bool { return strings.Contains(line, want) }) {
			t.Errorf("missing %q in %q", want, found)
		}
	}
	if slices.ContainsFunc(found, func(line string) bool {
		return strings.Contains(line, "outside") || strings.Contains(line, "AGENTS.md")
	}) {
		t.Errorf("unexpected: %q", found)
	}
	if !anchors("## 4. Development layers and adapters\n### Kubernetes: the API server, not `kubectl`\n")["kubernetes-the-api-server-not-kubectl"] {
		t.Error("anchor of a heading with punctuation")
	}
}
