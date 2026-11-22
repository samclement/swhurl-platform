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
	m, err := loadModule(writeFiles(t, files), nil)
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

func TestCheckApprovedModules(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":      "package kernel\n\nimport (\n\t\"strings\"\n\t\"example.com/v2/other\"\n)\n",
		"kernel/a_test.go": "package kernel\n\nimport \"github.com/x/assert\"\n",
	})
	m.GoMod += "require github.com/x/assert v1.0.0\n"
	expect(t, checkApprovedModules(m),
		"approved-modules go.mod: requires an unapproved module or version",
		"approved-modules kernel/a_test.go:3: imports github.com/x/assert")
}

func TestCheckLayers(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":           "package kernel\n\nimport \"example.com/v2/cmd/swhurl-v2\"\n",
		"cmd/swhurl-v2/main.go": "package main\n\nimport \"example.com/v2/kernel\"\n",
		"newpkg/b.go":           "package newpkg\n",
		"only/b_test.go":        "package only\n",
		// observe may import recovery/rules (a PureDependencies edge); recovery/rules may not import observe back.
		"observe/o.go":        "package observe\n\nimport \"example.com/v2/recovery/rules\"\n",
		"recovery/rules/r.go": "package rules\n\nimport \"example.com/v2/observe\"\n",
		// The same holds for delivery's rules: observe reports them, and they never import observe.
		"observe/d.go":        "package observe\n\nimport \"example.com/v2/delivery/rules\"\n",
		"delivery/rules/r.go": "package rules\n\nimport \"example.com/v2/observe\"\n",
		// A capability's use cases are layer 2 like orchestration; unlisted peer imports are forbidden.
		"notifications/n.go": "package notifications\n\nimport \"example.com/v2/orchestration\"\n",
		"orchestration/o.go": "package orchestration\n\nimport \"example.com/v2/notifications\"\n",
		"kernel/effect.go":   "package kernel\n\nimport \"example.com/v2/adapters/s3\"\n",
		"recovery/up.go":     "package recovery\n\nimport \"example.com/v2/cmd/swhurl-v2\"\n",
	})
	expect(t, checkLayers(m, nil),
		"layers delivery/rules/r.go:3: delivery/rules (layer 0) imports observe (layer 0)",
		"layers kernel/a.go:3: kernel (layer 0) imports cmd/swhurl-v2 (layer 3)",
		"layers kernel/effect.go:3: kernel (layer 0) imports adapters/s3 (layer 1)",
		"layers newpkg/b.go: package newpkg has no declared layer",
		"layers notifications/n.go:3: notifications (layer 2) imports orchestration (layer 2)",
		"layers orchestration/o.go:3: orchestration (layer 2) imports notifications (layer 2)",
		"layers recovery/up.go:3: recovery (layer 2) imports cmd/swhurl-v2 (layer 3)",
		"layers recovery/rules/r.go:3: recovery/rules (layer 0) imports observe (layer 0)")
}

func TestWorkflowDependencies(t *testing.T) {
	m := fixture(t, map[string]string{
		"orchestration/o.go":  "package orchestration\n\nimport \"example.com/v2/recovery\"\n",
		"recovery/r.go":       "package recovery\n\nimport \"example.com/v2/orchestration\"\n",
		"delivery/d.go":       "package delivery\n\nimport \"example.com/v2/recovery\"\n",
		"kernel/k.go":         "package kernel\n\nimport \"example.com/v2/recovery\"\n",
		"kernel/effect.go":    "package kernel\n\nimport \"example.com/v2/adapters/s3\"\n",
		"recovery/command.go": "package recovery\n\nimport \"example.com/v2/cmd/swhurl-v2\"\n",
		"recovery/rules/r.go": "package rules\n\nimport \"example.com/v2/observe\"\n",
		// Ordinary downward imports and the existing pure exception stay allowed.
		"observe/o.go":               "package observe\n\nimport \"example.com/v2/recovery/rules\"\n",
		"recovery/down.go":           "package recovery\n\nimport (\n\"example.com/v2/recovery/rules\"\n\"example.com/v2/adapters/s3\"\n)\n",
		"adapters/s3/s3test/fake.go": "package s3test\n\nimport \"example.com/v2/adapters/s3\"\n",
	})
	// This local registry allows only orchestration -> recovery; it changes no global policy.
	workflows := map[string][]string{"orchestration": {"recovery"}}
	expect(t, checkLayers(m, workflows),
		"layers delivery/d.go:3: delivery (layer 2) imports recovery (layer 2)",
		"layers kernel/effect.go:3: kernel (layer 0) imports adapters/s3 (layer 1)",
		"layers kernel/k.go:3: kernel (layer 0) imports recovery (layer 2)",
		"layers recovery/command.go:3: recovery (layer 2) imports cmd/swhurl-v2 (layer 3)",
		"layers recovery/r.go:3: recovery (layer 2) imports orchestration (layer 2)",
		"layers recovery/rules/r.go:3: recovery/rules (layer 0) imports observe (layer 0)")
	// Without the local exception the same import fails again.
	if !slices.Contains(lines(checkLayers(m, nil)), "layers orchestration/o.go:3: orchestration (layer 2) imports recovery (layer 2)") {
		t.Fatal("checking a listed edge changed the default policy")
	}
}

// Exercise the approved production registry, not just a hypothetical local exception. The console's
// composition may call health; neither the reverse edge nor other peers gain permission.
func TestHealthWorkflowDependency(t *testing.T) {
	m := fixture(t, map[string]string{
		"orchestration/console.go": "package orchestration\n\nimport \"example.com/v2/health\"\n",
		"orchestration/other.go":   "package orchestration\n\nimport \"example.com/v2/recovery\"\n",
		"health/reverse.go":        "package health\n\nimport \"example.com/v2/orchestration\"\n",
		"health/peer.go":           "package health\n\nimport \"example.com/v2/recovery\"\n",
		"health/down.go":           "package health\n\nimport (\n\"example.com/v2/health/rules\"\n\"example.com/v2/adapters/kube\"\n)\n",
		"health/rules/up.go":       "package rules\n\nimport \"example.com/v2/health\"\n",
		"cmd/swhurl-v2/verify.go":  "package main\n\nimport \"example.com/v2/health\"\n",
	})
	expect(t, checkLayers(m, WorkflowDependencies),
		"layers health/peer.go:3: health (layer 2) imports recovery (layer 2)",
		"layers health/reverse.go:3: health (layer 2) imports orchestration (layer 2)",
		"layers health/rules/up.go:3: health/rules (layer 0) imports health (layer 2)",
		"layers orchestration/other.go:3: orchestration (layer 2) imports recovery (layer 2)")
	if !slices.Contains(lines(checkLayers(m, nil)), "layers orchestration/console.go:3: orchestration (layer 2) imports health (layer 2)") {
		t.Fatal("the console edge must require explicit permission")
	}
}

// The console's composition may call apps' status; the reverse edge and apps' peers gain no permission, and
// apps reaches provisioning's compilation as a pure package, not through orchestration. Apps' create writes its
// records through provisioning; edit and SetTeam re-render through it, and provisioning does not reach back.
// Recovery's restore is given apps' status by the command, so recovery importing apps is refused.
func TestAppsWorkflowDependency(t *testing.T) {
	m := fixture(t, map[string]string{
		"orchestration/console.go":    "package orchestration\n\nimport \"example.com/v2/apps\"\n",
		"apps/reverse.go":             "package apps\n\nimport \"example.com/v2/orchestration\"\n",
		"apps/peer.go":                "package apps\n\nimport \"example.com/v2/delivery\"\n",
		"recovery/observe.go":         "package recovery\n\nimport \"example.com/v2/apps\"\n",
		"cmd/swhurl-v2/restore.go":    "package main\n\nimport (\n\"example.com/v2/apps\"\n\"example.com/v2/recovery\"\n)\n",
		"apps/down.go":                "package apps\n\nimport (\n\"example.com/v2/provisioning/rules\"\n\"example.com/v2/adapters/kube\"\n)\n",
		"provisioning/rules/up.go":    "package rules\n\nimport \"example.com/v2/apps\"\n",
		"apps/create.go":              "package apps\n\nimport \"example.com/v2/provisioning\"\n",
		"apps/edit.go":                "package apps\n\nimport \"example.com/v2/provisioning\"\n",
		"provisioning/back.go":        "package provisioning\n\nimport \"example.com/v2/apps\"\n",
		"cmd/swhurl-v2/status_all.go": "package main\n\nimport \"example.com/v2/apps\"\n",
	})
	expect(t, checkLayers(m, WorkflowDependencies),
		"layers apps/peer.go:3: apps (layer 2) imports delivery (layer 2)",
		"layers apps/reverse.go:3: apps (layer 2) imports orchestration (layer 2)",
		"layers provisioning/back.go:3: provisioning (layer 2) imports apps (layer 2)",
		"layers provisioning/rules/up.go:3: provisioning/rules (layer 0) imports apps (layer 2)",
		"layers recovery/observe.go:3: recovery (layer 2) imports apps (layer 2)")
	if !slices.Contains(lines(checkLayers(m, nil)), "layers orchestration/console.go:3: orchestration (layer 2) imports apps (layer 2)") {
		t.Fatal("the console edge must require explicit permission")
	}
	if !slices.Contains(lines(checkLayers(m, nil)), "layers apps/edit.go:3: apps (layer 2) imports provisioning (layer 2)") {
		t.Fatal("editing must require the explicit apps-to-provisioning edge")
	}
}

// The command is the declaration change's only caller: connectivity's use case reads kernel's record alone, and no
// workflow — orchestration's console included — reaches it, nor it them, nor its own rules it.
func TestConnectivityWorkflowHasNoEdges(t *testing.T) {
	m := fixture(t, map[string]string{
		"connectivity/connectivity.go":  "package connectivity\n\nimport \"example.com/v2/kernel\"\n",
		"connectivity/peer.go":          "package connectivity\n\nimport \"example.com/v2/provisioning\"\n",
		"connectivity/rules/up.go":      "package rules\n\nimport \"example.com/v2/connectivity\"\n",
		"orchestration/console.go":      "package orchestration\n\nimport \"example.com/v2/connectivity\"\n",
		"cmd/swhurl-v2/change_steps.go": "package main\n\nimport \"example.com/v2/connectivity\"\n",
	})
	expect(t, checkLayers(m, WorkflowDependencies),
		"layers connectivity/peer.go:3: connectivity (layer 2) imports provisioning (layer 2)",
		"layers connectivity/rules/up.go:3: connectivity/rules (layer 0) imports connectivity (layer 2)",
		"layers orchestration/console.go:3: orchestration (layer 2) imports connectivity (layer 2)")
}

func TestWorkflowDependencyRegistry(t *testing.T) {
	for _, endpoint := range []string{"kernel", "adapters/s3", "cmd/swhurl-v2", "unknown"} {
		t.Run(endpoint, func(t *testing.T) {
			m := fixture(t, map[string]string{})
			expect(t, checkLayers(m, map[string][]string{endpoint: {"recovery"}}),
				"layers archtest/rules_test.go: WorkflowDependencies importer "+endpoint+" is not a declared workflow package")
			expect(t, checkLayers(m, map[string][]string{"recovery": {endpoint}}),
				"layers archtest/rules_test.go: WorkflowDependencies target "+endpoint+" is not a declared workflow package")
		})
	}
	for _, tc := range []struct {
		name      string
		workflows map[string][]string
		want      string
	}{
		{"empty", nil, ""},
		{"directional", map[string][]string{"orchestration": {"recovery"}}, ""},
		{"diamond", map[string][]string{"orchestration": {"delivery", "notifications"}, "delivery": {"recovery"}, "notifications": {"recovery"}}, ""},
		{"self", map[string][]string{"recovery": {"recovery"}}, "WorkflowDependencies self edge: recovery -> recovery"},
		{"health-cycle", map[string][]string{"orchestration": {"health"}, "health": {"orchestration"}}, "WorkflowDependencies cycle: health -> orchestration -> health"},
		{"apps-cycle", map[string][]string{"orchestration": {"apps"}, "apps": {"orchestration"}}, "WorkflowDependencies cycle: apps -> orchestration -> apps"},
		{"two-cycle", map[string][]string{"orchestration": {"recovery"}, "recovery": {"orchestration"}}, "WorkflowDependencies cycle: orchestration -> recovery -> orchestration"},
		{"three-cycle", map[string][]string{"delivery": {"orchestration"}, "orchestration": {"recovery"}, "recovery": {"delivery"}}, "WorkflowDependencies cycle: delivery -> orchestration -> recovery -> delivery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture(t, map[string]string{})
			var want []string
			if tc.want != "" {
				want = []string{"layers archtest/rules_test.go: " + tc.want}
			}
			expect(t, checkLayers(m, tc.workflows), want...)
		})
	}
}

// An invalid registry cannot authorise an otherwise prohibited dependency, even before
// considering its configuration violation. In particular it cannot make pure code effectful.
func TestWorkflowDependenciesDoNotOverrideLayers(t *testing.T) {
	for _, edge := range [][2]string{
		{"kernel", "recovery"}, {"kernel", "adapters/s3"},
		{"adapters/s3", "recovery"}, {"adapters/s3", "adapters/kube"},
		{"recovery", "cmd/swhurl-v2"}, {"recovery/rules", "observe"},
	} {
		t.Run(edge[0]+"->"+edge[1], func(t *testing.T) {
			m := fixture(t, map[string]string{edge[0] + "/a.go": "package p\n\nimport \"example.com/v2/" + edge[1] + "\"\n"})
			without := lines(checkLayers(m, nil))
			with := lines(checkLayers(m, map[string][]string{edge[0]: {edge[1]}}))
			if len(without) != 1 || !slices.Contains(with, without[0]) || len(with) <= len(without) {
				t.Fatalf("invalid entry must fail validation and preserve the import violation; without: %v; with: %v", without, with)
			}
		})
	}
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
	at, _ := time.Parse(time.RFC3339, "2026-10-26T00:00:00Z")
	return fmt.Sprint(count) == "" && pattern > 0 && at.IsZero()
}
`,
		"kernel/b.go":                          "package kernel\n\nfunc g() { count = 2 }\n",
		"producer/a.go":                        "package producer\n",
		"connectivity/rules/calls/calls.go":    "package calls\n",
		"connectivity/rules/documents.go":      "package rules\n",
		"certissuer/a.go":                      "package certissuer\n",
		"checkrepo/a.go":                       "package checkrepo\n",
		"datadestroy/a.go":                     "package datadestroy\n",
		"dashboards/a.go":                      "package dashboards\n",
		"connectivity/rules/events/events.go":  "package events\n",
		"teamscenario/a.go":                    "package teamscenario\n",
		"lifecycletest/a.go":                   "package lifecycletest\n",
		"apptemplatetest/a.go":                 "package apptemplatetest\n",
		"notifications/rules/notify/notify.go": "package notify\n",
		"observe/explain.go":                   "package observe\n",
		"pages/a.go":                           "package pages\n",
		"policy/policy.go":                     "package policy\n",
		"secretcheck/a.go":                     "package secretcheck\n",
		"telemetry/a.go":                       "package telemetry\n",
		"workstreams/a.go":                     "package workstreams\n",
		// A capability's rules are held to the same rule: the clock is an argument there too.
		"health/rules/a.go":                  "package rules\n\nimport \"time\"\n\nfunc f() time.Time { return time.Now() }\n",
		"recovery/rules/a.go":                "package rules\n\nimport \"time\"\n\nfunc f() time.Time { return time.Now() }\n",
		"delivery/rules/a.go":                "package rules\n\nimport \"time\"\n\nfunc f() time.Time { return time.Now() }\n",
		"provisioning/rules/a.go":            "package rules\n\nimport \"time\"\n\nfunc f() time.Time { return time.Now() }\n",
		"notifications/rules/incident/a.go":  "package incident\n\nimport \"time\"\n\nfunc f() time.Time { return time.Now() }\n",
		"notifications/rules/heartbeat/a.go": "package heartbeat\n\nimport \"time\"\n\nfunc f() time.Time { return time.Now() }\n",
	})
	expect(t, checkPure(m),
		"pure health/rules/a.go:5: calls time.Now",
		"pure notifications/rules/incident/a.go:5: calls time.Now",
		"pure notifications/rules/heartbeat/a.go:5: calls time.Now",
		"pure kernel/a.go:5: imports os, which is not in PureImports",
		"pure kernel/a.go:15: assigns package variable table",
		"pure kernel/a.go:16: calls fmt.Println",
		// The clock is refused by call, so a supplied timestamp is still a pure computation.
		"pure kernel/a.go:16: calls time.Now",
		"pure kernel/b.go:3: assigns package variable count",
		"pure recovery/rules/a.go:5: calls time.Now",
		"pure delivery/rules/a.go:5: calls time.Now",
		"pure provisioning/rules/a.go:5: calls time.Now")
}

func TestCheckNoSubprocess(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":                   "package kernel\n\nimport \"os/exec\"\n\nvar c = exec.Command(\"kubectl get pods\")\nvar d = \"fluxion\"\n",
		"kernel/b.go":                   "package kernel\n\nimport \"os\"\n\nfunc f() { os.StartProcess(\"flux\", nil, nil) }\n",
		"kernel/b_test.go":              "package kernel\n\nimport \"os/exec\"\n\nvar e = exec.Command(\"go\")\n",
		"adapters/registry/registry.go": "package registry\n\nimport \"os/exec\"\n\nvar c = exec.Command(\"curl\")\n",
		// An allowed adapter may start a process, and may still not name a Kubernetes command.
		"adapters/sops/sops.go": "package sops\n\nimport \"os/exec\"\n\nvar c = exec.Command(\"sops\")\nvar d = \"kubectl get pods\"\n",
		"adapters/git/git.go":   "package git\n\nimport \"os/exec\"\n\nvar c = exec.Command(\"git\", \"status\")\n",
		// An allowed adapter's fake is not the adapter: it starts nothing.
		"adapters/git/gittest/fake.go": "package gittest\n\nimport \"os/exec\"\n\nvar c = exec.Command(\"git\")\n",
		// The command may name SIGTERM, which starts nothing; nothing else may import syscall.
		"cmd/swhurl-v2/main.go": "package main\n\nimport \"syscall\"\n\nvar s = syscall.SIGTERM\n",
		"orchestration/jobs.go": "package orchestration\n\nimport \"syscall\"\n\nvar s = syscall.SIGTERM\n",
	})
	expect(t, checkNoSubprocess(m),
		"no-subprocess adapters/git/gittest/fake.go:3: imports os/exec",
		"no-subprocess adapters/registry/registry.go:3: imports os/exec",
		"no-subprocess adapters/sops/sops.go:6: names the kubectl command",
		"no-subprocess kernel/a.go:3: imports os/exec",
		"no-subprocess kernel/a.go:5: names the kubectl command",
		"no-subprocess kernel/b.go:5: calls os.StartProcess",
		"no-subprocess kernel/b.go:5: names the flux command",
		"no-subprocess orchestration/jobs.go:3: imports syscall")
}

func TestCheckPlaces(t *testing.T) {
	m := fixture(t, map[string]string{
		"kernel/a.go":         "package kernel\n",
		"adapters/git/git.go": "package git\n",
		"cmd/tool/main.go":    "package main\n",
		"orchestration/o.go":  "package orchestration\n",
		"recovery/r.go":       "package recovery\n",
		"usecase/u.go":        "package usecase\n",
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
	layers := map[string]int{"kernel": 0, "adapters/git": 1, "cmd/tool": 3, "orchestration": 2, "only": 3, "model": 0,
		"listed": 1, "store": 1, "adapters/bad": 2, "cmd/lib": 2, "tools/x": 3, "cmd/pure": 0, "odd": 4,
		"recovery": 2, "usecase": 1}
	expect(t, checkPlaces(m, layers, []string{"kernel", "listed", "cmd/pure"}, []string{"orchestration", "recovery", "usecase"}),
		"places adapters/bad/b.go: adapters/bad is under adapters/ but is layer 2",
		"places cmd/lib/lib.go: cmd/lib is layer 2 (use cases) but is not listed in Orchestration",
		"places cmd/pure/p.go: cmd/pure is under cmd/ but is layer 0",
		"places listed/l.go: listed is listed in Pure but is layer 1",
		"places model/a.go: model is layer 0, the pure model, but is not listed in Pure",
		"places odd/o.go: odd has layer 4; the layers are 0 (pure model), 1 (adapters), 2 (use cases) and 3 (commands)",
		"places store/s.go: store is layer 1 (adapters) but is not under adapters/",
		"places tools/x/main.go: tools/x is layer 3 (commands) but is not a main package under cmd/",
		"places usecase/u.go: usecase is layer 1 (adapters) but is not under adapters/")
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

func TestYAMLApprovalIsNarrow(t *testing.T) {
	for _, dir := range []string{"kernel", "adapters/kube", "adapters/helm/helmtest", "adapters/appsource/appsourcetest", "adapters/appsource/other",
		"adapters/manifests/manifeststest", "checkrepo", "cmd/swhurl-v2"} {
		m := fixture(t, map[string]string{dir + "/a.go": "package probe\nimport \"go.yaml.in/yaml/v3\"\n"})
		if len(checkApprovedModules(m)) != 1 {
			t.Errorf("YAML escaped into %s", dir)
		}
	}
	m := fixture(t, map[string]string{"adapters/helm/a.go": "package helm\nimport \"go.yaml.in/yaml/v3\"\n"})
	m.GoMod += "require go.yaml.in/yaml/v3 v3.0.6\n"
	expect(t, checkApprovedModules(m))
	for _, dir := range []string{"adapters/appsource", "adapters/manifests"} {
		approved := fixture(t, map[string]string{dir + "/a.go": "package probe\nimport \"go.yaml.in/yaml/v3\"\n"})
		approved.GoMod += "require go.yaml.in/yaml/v3 v3.0.6\n"
		expect(t, checkApprovedModules(approved))
	}
	for _, requirement := range []string{
		"require go.yaml.in/yaml/v3 v3.0.5", "require example.com/other v1.0.0",
		"require (\n go.yaml.in/yaml/v3 v3.0.6\n example.com/other v1.0.0\n)",
		"replace go.yaml.in/yaml/v3 => example.com/fork v3.0.6", "exclude go.yaml.in/yaml/v3 v3.0.6",
		"require go.yaml.in/yaml/v3 v3.0.6\nrequire go.yaml.in/yaml/v3 v3.0.6",
		"require github.com/nats-io/nats.go v1.54.0",
	} {
		if len(checkModuleRequirements(requirement)) == 0 {
			t.Errorf("accepted %s", requirement)
		}
	}
	expect(t, checkModuleRequirements("require (\n go.yaml.in/yaml/v3 v3.0.6\n github.com/nats-io/nuid v1.0.1 // indirect\n)\n"))
}

// The NATS client is the flag store's alone, and the embedded server only its tests': a requirement that comes with
// a module is never an import of its own.
func TestNATSApprovalIsNarrow(t *testing.T) {
	cases := []struct {
		path, imports string
		violations    int
	}{
		{"adapters/nats/a.go", "github.com/nats-io/nats.go/jetstream", 0},
		{"adapters/nats/a_test.go", "github.com/nats-io/nats-server/v2/server", 0},
		{"adapters/nats/a.go", "github.com/nats-io/nats-server/v2/server", 1},
		{"adapters/nats/natstest/a.go", "github.com/nats-io/nats.go", 1},
		{"orchestration/a.go", "github.com/nats-io/nats.go", 1},
		{"cmd/swhurl-v2/a_test.go", "github.com/nats-io/nats-server/v2/server", 1},
		{"adapters/nats/a.go", "golang.org/x/crypto/ed25519", 1},
		{"adapters/nats/a.go", "github.com/nats-io/nats.gofake", 1},
	}
	for _, c := range cases {
		m := fixture(t, map[string]string{c.path: "package probe\nimport \"" + c.imports + "\"\n"})
		if found := checkApprovedModules(m); len(found) != c.violations {
			t.Errorf("%s importing %s: %v", c.path, c.imports, found)
		}
	}
}

func TestHelmSubprocessApprovalDoesNotLeak(t *testing.T) {
	for _, dir := range []string{"adapters/helm", "adapters/helm/helmtest", "adapters/helm/other", "orchestration"} {
		m := fixture(t, map[string]string{dir + "/a.go": "package probe\nimport \"os/exec\"\n"})
		want := 1
		if dir == "adapters/helm" {
			want = 0
		}
		if got := len(checkNoSubprocess(m)); got != want {
			t.Errorf("%s: got %d want %d", dir, got, want)
		}
	}
}
