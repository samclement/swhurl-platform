package archtest

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite api.txt, and tighten (never loosen) budgets.json")

// The module root is the parent of this package's directory.
const root = ".."

func module(t *testing.T) *Module {
	t.Helper()
	m, err := loadModule(root)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// report fails the test with one line per violation, then each broken rule's fix and principle once.
func report(t *testing.T, found []Violation) {
	t.Helper()
	var rules []string
	for _, violation := range found {
		t.Error(violation)
		if !slices.Contains(rules, violation.Rule) {
			rules = append(rules, violation.Rule)
		}
	}
	for _, rule := range rules {
		t.Log(advice(rule))
	}
}

func readRepo(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "..", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestStdlibOnly(t *testing.T)      { report(t, checkStdlibOnly(module(t))) }
func TestLayers(t *testing.T)          { report(t, checkLayers(module(t))) }
func TestPure(t *testing.T)            { report(t, checkPure(module(t))) }
func TestNoSubprocess(t *testing.T)    { report(t, checkNoSubprocess(module(t))) }
func TestPlaces(t *testing.T)          { report(t, checkPlaces(module(t), Layers, Pure)) }
func TestAdapterComplete(t *testing.T) { report(t, checkAdapters(module(t))) }

func TestPublicAPI(t *testing.T) {
	got := strings.Join(publicAPI(module(t)), "\n") + "\n"
	if *update {
		if err := os.WriteFile("api.txt", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("api.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got == string(want) {
		return
	}
	old := map[string]bool{}
	for _, line := range strings.Split(string(want), "\n") {
		old[line] = true
	}
	now := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		now[line] = true
		if !old[line] {
			report(t, []Violation{{"public-api", "archtest/api.txt", "added or changed: " + line}})
		}
	}
	for _, line := range strings.Split(string(want), "\n") {
		if !now[line] {
			report(t, []Violation{{"public-api", "archtest/api.txt", "removed or changed: " + line}})
		}
	}
}

func TestBudgets(t *testing.T) {
	budgets, err := readBudgets("budgets.json")
	if err != nil {
		t.Fatal(err)
	}
	found := measures(module(t))
	if *update {
		data, err := json.MarshalIndent(tightened(found, budgets), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("budgets.json", append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		budgets, _ = readBudgets("budgets.json")
	}
	report(t, checkBudgets(found, budgets))
}

func TestRulesDocumented(t *testing.T) {
	report(t, checkDocumented(readRepo(t, plan), readRepo(t, "AGENTS.md")))
}
