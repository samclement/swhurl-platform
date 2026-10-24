package archtest

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// Tests of the change report on small trees written to temporary directories: what it reports,
// what it leaves out, and what it refuses to read.

const reportRules = `package archtest

var Rules = []Rule{{"layers", "s", "f", "p"}, {ID: "pure", Statement: "s"}}
var Layers = map[string]int{%s}
var Pure = []string{"kernel"}
var PureImports = []string{%s}
`

// baseFiles is a small v2: one pure package, rule settings, baselines.
func baseFiles() map[string]string {
	return map[string]string{
		"go.mod":                 "module example.com/v2\n\ngo 1.26.0\n",
		"kernel/a.go":            "package kernel\n\nimport \"strings\"\n\nfunc A(s string) string { return strings.ToUpper(s) }\n",
		"archtest/rules_test.go": fmt.Sprintf(reportRules, `"kernel": 0`, `"strings"`),
		"archtest/budgets.json":  `{"maxComplexity": 10, "maxLines": 60, "allow": []}`,
		"archtest/api.txt":       "kernel: func A(s string) string\n",
	}
}

func with(files map[string]string, changes map[string]string) map[string]string {
	out := maps.Clone(files)
	for path, content := range changes {
		if content == "" {
			delete(out, path)
		} else {
			out[path] = content
		}
	}
	return out
}

func readFixture(t *testing.T, files map[string]string) Tree {
	t.Helper()
	tree, err := readTree(writeFiles(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func compareFixtures(t *testing.T, before, after map[string]string) ChangeReport {
	t.Helper()
	a, b := readFixture(t, before), readFixture(t, after)
	return compareTrees(Side{"base", a.Present}, a, Side{"working tree", b.Present}, b)
}

func expectLines(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got  %q\n want %q", what, got, want)
	}
}

func TestReportOfIdenticalTreesIsEmpty(t *testing.T) {
	r := compareFixtures(t, baseFiles(), baseFiles())
	expectLines(t, "review", r.Review)
	if got := summary(r); got != "v2 change: base → working tree\nno changes under v2/\n" {
		t.Errorf("summary:\n%s", got)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "null") {
		t.Errorf("an empty list is [], not null: %s", data)
	}
}

func TestReportNewPackageImportsAndAPI(t *testing.T) {
	after := with(baseFiles(), map[string]string{
		"adapters/git/git.go": "package git\n\nimport (\n\t\"os\"\n\t\"example.com/v2/kernel\"\n\t\"github.com/x/y\"\n)\n\n" +
			"func Read(path string) ([]byte, error) { _, _ = kernel.A, y.Z; return os.ReadFile(path) }\n",
		"kernel/a.go":            baseFiles()["kernel/a.go"] + "\nfunc B() {}\n",
		"archtest/rules_test.go": fmt.Sprintf(reportRules, `"kernel": 0, "adapters/git": 1`, `"strings"`),
	})
	r := compareFixtures(t, baseFiles(), after)
	expectLines(t, "review", r.Review,
		"new package adapters/git (layer 1)",
		"adapters/git imports external module github.com/x/y",
		"Layers + adapters/git 1",
		"exported API: +2 -0 (api.txt unchanged)")
	want := "v2 change: base → working tree\n" +
		"review:\n" +
		"  ! new package adapters/git (layer 1)\n" +
		"  ! adapters/git imports external module github.com/x/y\n" +
		"  ! Layers + adapters/git 1\n" +
		"  ! exported API: +2 -0 (api.txt unchanged)\n" +
		"files: 1 added, 2 changed\n" +
		"packages: + adapters/git (layer 1)\n" +
		"imports: adapters/git + example.com/v2/kernel, adapters/git + github.com/x/y, adapters/git + os\n" +
		"api: 2 added\n" +
		"budgets (complexity/lines): + adapters/git.Read 1/1, + kernel.B 1/1\n"
	if got := summary(r); got != want {
		t.Errorf("summary:\n%s\nwant:\n%s", got, want)
	}
	if got := r.Imports[0].Added; !slices.Equal(got, []Import{
		{"example.com/v2/kernel", "local", false}, {"github.com/x/y", "external", false}, {"os", "standard", false}}) {
		t.Errorf("imports: %+v", got)
	}
	if got := fmt.Sprint(r.Files.Changed); got != "[{archtest/rules_test.go rules} {kernel/a.go source}]" {
		t.Errorf("changed files: %s", got)
	}
}

func TestReportUpdatedBaselineAndRemovedCode(t *testing.T) {
	after := with(baseFiles(), map[string]string{
		"kernel/a.go":      "package kernel\n",
		"archtest/api.txt": "\n",
	})
	r := compareFixtures(t, baseFiles(), after)
	expectLines(t, "review", r.Review, "exported API: +0 -1 (api.txt changed)")
	expectLines(t, "api removed", r.API.Removed, "kernel: func A(s string) string")
	expectLines(t, "budgets", functionItems(r.Budgets.Functions), "- kernel.A")
	if got := fmt.Sprint(r.Imports); got != "[{kernel [] [{strings standard false}]}]" {
		t.Errorf("imports: %s", got)
	}
}

func TestReportUndeclaredAndTestOnlyPackages(t *testing.T) {
	after := with(baseFiles(), map[string]string{
		"tool/x.go":        "package tool\n",
		"e2e/e2e_test.go":  "package e2e\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
		"testdata/in.json": "{}\n",
	})
	r := compareFixtures(t, baseFiles(), after)
	expectLines(t, "review", r.Review, "new package tool (no declared layer)")
	if got := fmt.Sprint(r.Packages.Added); !strings.Contains(got, "{e2e <nil> false true}") {
		t.Errorf("a tests-only package is reported but not for review: %s", got)
	}
	if got := r.Imports[0]; got.Package != "e2e" || !got.Added[0].Test {
		t.Errorf("an import only tests use is marked test: %+v", got)
	}
	if got := fmt.Sprint(r.Files.Added); !strings.Contains(got, "{testdata/in.json testdata}") {
		t.Errorf("files: %s", got)
	}
}

func TestReportBudgets(t *testing.T) {
	big := "func Big(a, b, c, d bool) int { if a { return 1 }; if b { return 2 }; if c { return 3 }; if d { return 4 }; return 0 }\n"
	small := "func Small(a, b bool) int { if a { return 1 }; if b { return 2 }; return 0 }\n"
	before := with(baseFiles(), map[string]string{
		"kernel/big.go": "package kernel\n\n" + big + small,
		"archtest/budgets.json": `{"maxComplexity": 3, "maxLines": 60, "allow": [
			{"function": "kernel.Big", "complexity": 5, "lines": 1, "reason": "old"},
			{"function": "kernel.Small", "complexity": 4, "lines": 1, "reason": "old"}]}`,
	})
	after := with(before, map[string]string{
		"kernel/big.go": "package kernel\n\n" + strings.Replace(big, "if a {", "if a && b || c {", 1) + small +
			"func New(a, b, c bool) { if a && b && c {} }\n",
		"archtest/budgets.json": `{"maxComplexity": 4, "maxLines": 60, "allow": [
			{"function": "kernel.Big", "complexity": 7, "lines": 1, "reason": "old"},
			{"function": "kernel.Small", "complexity": 3, "lines": 1, "reason": "old"},
			{"function": "kernel.New", "complexity": 4, "lines": 1, "reason": "why"}]}`,
	})
	r := compareFixtures(t, before, after)
	expectLines(t, "review", r.Review,
		"budget limits 3/60 → 4/60",
		"budgets.json raises kernel.Big: 5/1 → 7/1",
		"budgets.json lists kernel.New at 4/1: why",
		"exported API: +1 -0 (api.txt unchanged)")
	expectLines(t, "functions", functionItems(r.Budgets.Functions), "kernel.Big 5/1 → 7/1", "+ kernel.New 4/1")
	if big := r.Budgets.Functions[0]; !big.Listed || !big.Over {
		t.Errorf("kernel.Big is listed and over the limits: %+v", big)
	}
	if len(r.Budgets.Entries) != 3 {
		t.Errorf("every changed entry is reported, a lowered one too: %+v", r.Budgets.Entries)
	}
}

func TestReportExceptions(t *testing.T) {
	after := with(baseFiles(), map[string]string{
		"archtest/rules_test.go": strings.Replace(fmt.Sprintf(reportRules, `"kernel": 1, "tool": 2`, `"strings", "time"`),
			`{ID: "pure", Statement: "s"}`, `{"stdlib-only", "s", "f", "p"}`, 1),
	})
	r := compareFixtures(t, baseFiles(), after)
	if r.Exceptions.New {
		t.Error("the rules are not new: the base has them")
	}
	expectLines(t, "review", r.Review,
		"Layers kernel 0 → 1", "Layers + tool 2", "PureImports + time", "Rules + stdlib-only", "Rules - pure")
}

func TestReportAgainstARevisionWithoutV2(t *testing.T) {
	r := compareFixtures(t, map[string]string{}, baseFiles())
	expectLines(t, "review", r.Review, "v2 is new in this change", "new package kernel (layer 0)",
		"the design rules are new in this change", "exported API: +1 -0 (api.txt changed)")
	expectLines(t, "every setting is still in the document", r.Exceptions.PureImports.Added, "strings")
	expectLines(t, "packages", packageItems(r.Packages), "+ archtest (tests only)", "+ kernel (layer 0)")
	if got := strings.SplitN(summary(r), "\n", 2)[0]; got != "v2 change: base (no v2) → working tree" {
		t.Errorf("header: %s", got)
	}
	if len(r.Files.Added) != len(baseFiles()) || slices.Contains(r.Review, "go.mod changed") {
		t.Errorf("every file is added; go.mod is not a change of its own: %+v", r)
	}
}

func TestReportSummaryShowsAFewItems(t *testing.T) {
	var source strings.Builder
	source.WriteString("package kernel\n")
	for i := range 7 {
		fmt.Fprintf(&source, "func f%d() {}\n", i)
	}
	r := compareFixtures(t, baseFiles(), with(baseFiles(), map[string]string{"kernel/f.go": source.String()}))
	if got := summary(r); !strings.Contains(got, "+ kernel.f4 1/1, and 2 more\n") {
		t.Errorf("summary:\n%s", got)
	}
	again := compareFixtures(t, baseFiles(), with(baseFiles(), map[string]string{"kernel/f.go": source.String()}))
	first, _ := json.Marshal(r)
	second, _ := json.Marshal(again)
	if string(first) != string(second) {
		t.Error("the same trees give the same document")
	}
}

func TestReportRefusesWhatItCannotRead(t *testing.T) {
	cases := map[string]map[string]string{
		"rule settings that are not literals": with(baseFiles(), map[string]string{
			"archtest/rules_test.go": "package archtest\n\nvar Layers = layers()\nvar Pure, PureImports, Rules = []string{}, []string{}, []Rule{}\n"}),
		"a rule ID that is not a string": with(baseFiles(), map[string]string{
			"archtest/rules_test.go": strings.Replace(fmt.Sprintf(reportRules, "", ""), `{"layers"`, `{id`, 1)}),
		"a missing setting":       with(baseFiles(), map[string]string{"archtest/rules_test.go": "package archtest\n"}),
		"files but no go.mod":     with(baseFiles(), map[string]string{"go.mod": ""}),
		"Go that does not parse":  with(baseFiles(), map[string]string{"kernel/bad.go": "package kernel\n\nfunc {\n"}),
		"an invalid budgets.json": with(baseFiles(), map[string]string{"archtest/budgets.json": `{"maxComplexity": 0}`}),
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readTree(writeFiles(t, files)); err == nil {
				t.Error("read without an error")
			}
		})
	}
	if _, err := readTree(t.TempDir() + "/missing"); err == nil {
		t.Error("a missing directory is an error, not an absent v2")
	}
}
