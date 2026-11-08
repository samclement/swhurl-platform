package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/git/gittest"
)

// checkRepoFiles is a minimal checkout every check passes: the bootstrap folders and settings, one unit that
// substitutes the settings, the two tables the docs keep in step and an empty catalogue.
var checkRepoFiles = map[string]string{
	"README.md":              "# Readme\n",
	"docs/architecture.md":   "| `platform-svc` | x | — | settings |\n",
	"docs/apps.md":           "<!-- app-interface -->\n<!-- /app-interface -->\n",
	"v2/contract/names.json": `{"kind": "platform-names", "version": 1, "names": []}`,
	"clusters/home/flux-system/kustomization.yaml":         "resources: []\n",
	"clusters/home/flux-system/kustomizations.yaml":        "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: placeholder}\n",
	"clusters/home/flux-system/sources/kustomization.yaml": "resources: []\n",
	"clusters/home/flux-system/sources/configmap-platform-settings.yaml": "apiVersion: v1\nkind: ConfigMap\n" +
		"metadata: {name: platform-settings}\ndata: {BASE_DOMAIN: example.test, CERT_ISSUER: x}\n",
	"clusters/home/platform.yaml": "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata: {name: platform-svc}\n" +
		"spec: {path: ./platform/svc, postBuild: {substituteFrom: [{kind: ConfigMap, name: platform-settings, optional: false}]}}\n",
	"platform/svc/kustomization.yaml": "resources: []\n",
}

// checkRepoTree writes files into a temporary checkout, and a fake Git that lists them.
func checkRepoTree(t *testing.T, files map[string]string) (string, *gittest.Fake) {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, gittest.New(root, git.Checkout{Present: slices.Sorted(maps.Keys(files))})
}

func runCheckRepo(t *testing.T, reader git.Reader, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := runWith(append([]string{"check-repo"}, args...), &out, &diagnostic, cli{git: reader, stdin: strings.NewReader(stdin)})
	return status, out.String(), diagnostic.String()
}

func TestCheckRepoPassesAndListsTheRenderPaths(t *testing.T) {
	root, reader := checkRepoTree(t, checkRepoFiles)
	status, out, diagnostic := runCheckRepo(t, reader, "", "--root", root)
	if status != 0 || diagnostic != "" || strings.Count(out, "[OK] ") != 7 ||
		!strings.Contains(out, "\n3 active render path(s):\n  clusters/home/flux-system\n  clusters/home/flux-system/sources\n"+
			"  platform/svc (substitutes 2 value(s))\n") || !strings.HasSuffix(out, "\n0 error(s).\n") {
		t.Fatalf("exit %d\n%s%s", status, out, diagnostic)
	}
}

// --json is one document: each check by name, and each path with its values or null.
func TestCheckRepoJSON(t *testing.T) {
	root, reader := checkRepoTree(t, checkRepoFiles)
	status, out, _ := runCheckRepo(t, reader, "", "--root", root, "--json")
	var document struct {
		Kind    string
		Version int
		Passed  bool
		Checks  []struct{ Check, Okay string }
		Paths   []struct {
			Path   string
			Values map[string]string
		}
	}
	if err := json.Unmarshal([]byte(out), &document); err != nil || status != 0 {
		t.Fatalf("exit %d, %v\n%s", status, err, out)
	}
	if document.Kind != "v2-check-repo" || document.Version != 1 || !document.Passed || len(document.Checks) != 7 ||
		document.Checks[0].Check != "docs-links" || len(document.Paths) != 3 || document.Paths[0].Values != nil ||
		document.Paths[2].Values["BASE_DOMAIN"] != "example.test" {
		t.Fatalf("document = %+v", document)
	}
}

// A finding is a [BAD] line, counted, and exit 1; the other checks still run.
func TestCheckRepoFailsOnAFinding(t *testing.T) {
	files := maps.Clone(checkRepoFiles)
	files["README.md"] = "[gone](missing.md)\n"
	root, reader := checkRepoTree(t, files)
	status, out, _ := runCheckRepo(t, reader, "", "--root", root)
	if status != 1 || !strings.Contains(out, "[BAD] broken documentation link: README.md: missing.md (missing file)\n") ||
		strings.Count(out, "[OK] ") != 6 || !strings.HasSuffix(out, "\n1 error(s).\n") {
		t.Fatalf("exit %d\n%s", status, out)
	}
}

func TestCheckRepoRendered(t *testing.T) {
	root, reader := checkRepoTree(t, checkRepoFiles)
	rendered := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\ndata: {host: \"a.${BASE_DOMAIN}\"}\n"
	status, out, diagnostic := runCheckRepo(t, reader, rendered, "--root", root, "--rendered", "platform/svc")
	if status != 0 || diagnostic != "" || out != strings.Replace(rendered, "${BASE_DOMAIN}", "example.test", 1) {
		t.Fatalf("exit %d\n%q%s", status, out, diagnostic)
	}
	status, out, diagnostic = runCheckRepo(t, reader, strings.Replace(rendered, "BASE_DOMAIN", "UNKNOWN", 1),
		"--root", root, "--rendered", "platform/svc")
	if status != 1 || out != "" || !strings.HasPrefix(diagnostic, "[BAD] platform/svc: unresolved Flux substitution ${UNKNOWN}") {
		t.Fatalf("unresolved: exit %d\n%q%s", status, out, diagnostic)
	}
	status, _, diagnostic = runCheckRepo(t, reader, rendered, "--root", root, "--rendered", "platform/none")
	if status != 2 || !strings.Contains(diagnostic, "platform/none is not an active render path") {
		t.Fatalf("a path Flux does not render: exit %d %s", status, diagnostic)
	}
}

func TestCheckRepoRefusals(t *testing.T) {
	root, reader := checkRepoTree(t, checkRepoFiles)
	if status, _, diagnostic := runCheckRepo(t, reader, "", "--root", root, "--rendered", "platform/svc", "--json"); status != 2 ||
		!strings.Contains(diagnostic, "usage") {
		t.Errorf("--rendered with --json: exit %d", status)
	}
	if status, _, _ := runCheckRepo(t, reader, "", "--root", filepath.Join(root, "absent")); status != 2 {
		t.Errorf("an absent root: exit %d", status)
	}
	reader.Err = &git.Error{Reason: git.NotARepository}
	if status, _, diagnostic := runCheckRepo(t, reader, "", "--root", root); status != 2 || !strings.Contains(diagnostic, "not_a_repository") {
		t.Errorf("a root Git refuses: exit %d %s", status, diagnostic)
	}
}
