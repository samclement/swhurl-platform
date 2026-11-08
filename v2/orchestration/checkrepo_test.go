package orchestration

import (
	"errors"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/git/gittest"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/checkrepo"
)

const (
	kustomization = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"
	settings      = "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: platform-settings}\ndata: {CERT_ISSUER: x, BASE_DOMAIN: example.test}\n"
	catalogue     = `{"kind": "platform-names", "version": 1, "names": [
		{"name": "SWHURL_APP", "surface": "env", "meaning": "App name", "availability": "always"},
		{"name": "SWHURL_SERVICE_<APP>_URL", "surface": "env family", "meaning": "A callee", "availability": "per call"}]}`
	appsTable = "# Apps\n<!-- app-interface -->\n| Name | Surface | Meaning | Availability |\n| --- | --- | --- | --- |\n" +
		"| `SWHURL_APP` | env | App name | always |\n| `SWHURL_SERVICE_<APP>_URL` | env family | A callee | per call |\n" +
		"<!-- /app-interface -->\n"
	unitsTable = "# Architecture\n\n## Flux units\n\n| Unit | Applies | Depends on | Inputs |\n| --- | --- | --- | --- |\n" +
		"| `platform-svc` | the service | — | settings, SOPS |\n"
	svcUnit = "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata: {name: platform-svc}\n" +
		"spec:\n  path: ./platform/svc\n  decryption: {provider: sops}\n" +
		"  postBuild:\n    substituteFrom: [{kind: ConfigMap, name: platform-settings, optional: false}]\n"
	encrypted = "apiVersion: v1\nkind: Secret\nmetadata: {name: s}\nstringData: {token: 'ENC[AES256_GCM,data:x]'}\n" +
		"sops:\n  age: [{recipient: age1x}]\n  mac: 'ENC[AES256_GCM,data:m]'\n  encrypted_regex: ^(data|stringData)$\n"
)

// tree is a minimal checkout that passes every check: the bootstrap folders, one shared unit that substitutes the
// settings and decrypts its one Secret, the two tables the docs keep in step, and a compiled file.
func tree() map[string]string {
	return map[string]string{
		"README.md":              "# Readme\n\n[ok](README.md) [a folder](platform) [a heading](docs/apps.md#apps) [a site](https://example.com)\n",
		"docs/apps.md":           appsTable,
		"docs/architecture.md":   unitsTable,
		"v2/contract/names.json": catalogue,
		"clusters/home/flux-system/kustomization.yaml":                       kustomization,
		"clusters/home/flux-system/kustomizations.yaml":                      "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: placeholder}\n",
		"clusters/home/flux-system/sources/kustomization.yaml":               kustomization,
		"clusters/home/flux-system/sources/configmap-platform-settings.yaml": settings,
		"clusters/home/platform.yaml":                                        svcUnit,
		"platform/svc/kustomization.yaml":                                    kustomization,
		"platform/svc/secret.sops.yaml":                                      encrypted,
		"v2-apps/instances/shop/staging/helmrelease.json":                    `{"env": {"SWHURL_APP": "shop", "SWHURL_SERVICE_ORDERS_URL": "x"}}`,
		"v2-apps/instances/shop/staging/backup.json":                         `{"labels": {"platform.swhurl.com/backup": "true"}}`,
	}
}

func checkTree(t *testing.T, files map[string]string) checkrepo.Report {
	t.Helper()
	present := slices.Sorted(maps.Keys(files))
	report, err := CheckRepo("/repo", gittest.New("/repo", git.Checkout{Present: present}), manifests.YAML{}, readFrom(files))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func readFrom(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		text, held := files[path]
		if !held {
			return nil, fs.ErrNotExist
		}
		return []byte(text), nil
	}
}

// problems is every problem the report holds, check by check.
func problems(report checkrepo.Report) map[string][]string {
	found := map[string][]string{}
	for _, result := range report.Results {
		if len(result.Problems) > 0 {
			found[result.Check] = result.Problems
		}
	}
	return found
}

func expectProblem(t *testing.T, report checkrepo.Report, check, problem string) {
	t.Helper()
	got := problems(report)
	if len(got) != 1 || !slices.ContainsFunc(got[check], func(p string) bool { return strings.Contains(p, problem) }) {
		t.Fatalf("problems = %q, want only %s: %q", got, check, problem)
	}
}

func TestTheMinimalTreePassesEveryCheckInOrder(t *testing.T) {
	report := checkTree(t, tree())
	var checks []string
	for _, result := range report.Results {
		checks = append(checks, result.Check)
		if len(result.Problems) > 0 || result.Okay == "" {
			t.Errorf("%s: %q, %q", result.Check, result.Okay, result.Problems)
		}
	}
	want := []string{"docs-links", "flux-units-table", "app-interface-table", "emitted-names", "sops-secrets",
		"platform-settings", "render-paths"}
	if !slices.Equal(checks, want) {
		t.Errorf("checks %q, want %q", checks, want)
	}
	var paths []string
	for _, path := range report.Paths {
		paths = append(paths, path.Path)
	}
	if !slices.Equal(paths, []string{"clusters/home/flux-system", "clusters/home/flux-system/sources", "platform/svc"}) {
		t.Errorf("paths %q", paths)
	}
	if report.Paths[0].Values != nil || report.Paths[2].Values["BASE_DOMAIN"] != "example.test" {
		t.Errorf("values: %v, %v", report.Paths[0].Values, report.Paths[2].Values)
	}
	if report.Results[0].Okay != "3 relative documentation links resolve" {
		t.Errorf("links: %q", report.Results[0].Okay)
	}
}

// One run reports every problem: a broken link and a plain Secret are both found, and the render paths are still
// listed.
func TestEveryFailureIsReported(t *testing.T) {
	files := tree()
	files["README.md"] = "[ok](README.md) [gone](missing.md)\n"
	files["infra/secret.yaml"] = "apiVersion: v1\nkind: Secret\nmetadata: {name: s}\nstringData: {a: plain}\n"
	report := checkTree(t, files)
	got := problems(report)
	if len(got) != 2 || !strings.Contains(got["docs-links"][0], "README.md: missing.md (missing file)") ||
		!strings.Contains(got["sops-secrets"][0], "infra/secret.yaml: Git-managed Secret must be a .sops.yaml or .sops.yml file") {
		t.Fatalf("problems = %q", got)
	}
	if len(report.Paths) != 3 {
		t.Errorf("render paths after earlier failures: %v", report.Paths)
	}
}

func TestBrokenLinks(t *testing.T) {
	for target, problem := range map[string]string{
		"docs/apps.md#nowhere": "docs/apps.md#nowhere (missing heading)",
		"ignored.txt":          "ignored.txt (missing file)",
		"../outside.md":        "../outside.md (missing file)",
	} {
		files := tree()
		files["README.md"] = "[x](" + target + ")\n"
		expectProblem(t, checkTree(t, files), "docs-links", "broken documentation link: README.md: "+problem)
	}
}

func TestTheUnitsTableAndEachKindOfDrift(t *testing.T) {
	for _, drift := range []struct{ table, problem string }{
		{strings.Replace(unitsTable, "platform-svc", "platform-svx", 1), "platform-svc is not in the table"},
		{strings.Replace(unitsTable, "platform-svc", "platform-svx", 1), "platform-svx is in the table but not in Git"},
		{strings.Replace(unitsTable, "settings, SOPS", "settings", 1), "platform-svc: Inputs omits SOPS"},
		{strings.Replace(unitsTable, "settings, SOPS", "SOPS", 1), "platform-svc: Inputs omits settings"},
	} {
		files := tree()
		files["docs/architecture.md"] = drift.table
		expectProblem(t, checkTree(t, files), "flux-units-table", "docs/architecture.md Flux units table differs from Git: "+drift.problem)
	}
	files := tree()
	files["clusters/home/platform.yaml"] = strings.Replace(svcUnit, "  decryption: {provider: sops}\n", "", 1)
	delete(files, "platform/svc/secret.sops.yaml")
	expectProblem(t, checkTree(t, files), "flux-units-table", "platform-svc: Inputs names SOPS")
	delete(files, "docs/architecture.md")
	expectProblem(t, checkTree(t, files), "flux-units-table", "docs/architecture.md is missing")
}

// An app instance's unit is not a shared unit, so the table does not list it.
func TestAppUnitsAreNotInTheTable(t *testing.T) {
	files := tree()
	files["clusters/home/app-shop-staging.json"] = `{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": {"name": "app-shop-staging"}, "spec": {"path": "./v2-apps/instances/shop/staging"}}`
	files["v2-apps/instances/shop/staging/kustomization.yaml"] = kustomization
	report := checkTree(t, files)
	if got := problems(report); len(got) != 0 || len(report.Paths) != 4 {
		t.Fatalf("problems %q, paths %v", got, report.Paths)
	}
}

func TestTheInterfaceTableChecksMissingExtraDuplicateAndDescription(t *testing.T) {
	row := "| `SWHURL_APP` | env | App name | always |\n"
	for changed, problem := range map[string]string{
		strings.Replace(appsTable, row, "", 1):                                           "SWHURL_APP: missing",
		strings.Replace(appsTable, row, strings.Replace(row, "APP`", "EXTRA`", 1), 1):    "SWHURL_EXTRA: extra",
		strings.Replace(appsTable, row, row+row, 1):                                      "SWHURL_APP: duplicate",
		strings.Replace(appsTable, row, strings.Replace(row, "App name", "Other", 1), 1): "SWHURL_APP: description differs",
	} {
		files := tree()
		files["docs/apps.md"] = changed
		files["README.md"] = "# Readme\n"
		expectProblem(t, checkTree(t, files), "app-interface-table", "docs/apps.md app interface table differs from registry: "+problem)
	}
}

// v2's compiled tree gives apps only listed names: a family member and the backup label pass, a new name does not.
func TestTheCompiledTreeGivesAppsOnlyListedNames(t *testing.T) {
	files := tree()
	files["v2-apps/instances/shop/staging/helmrelease.json"] = `{"env": {"SWHURL_NEW": "x"}}`
	expectProblem(t, checkTree(t, files), "emitted-names", "SWHURL_NEW: v2-apps/instances/shop/staging/helmrelease.json")
}

func TestSecrets(t *testing.T) {
	for _, c := range []struct{ name, text, problem string }{
		{"no age", strings.Replace(encrypted, "  age: [{recipient: age1x}]\n", "", 1), "missing SOPS age metadata"},
		{"no mac", strings.Replace(encrypted, "  mac: 'ENC[AES256_GCM,data:m]'\n", "", 1), "missing SOPS age metadata"},
		{"regex", strings.Replace(encrypted, "^(data|stringData)$", "^data$", 1), "unexpected SOPS encrypted_regex"},
		{"plain value", strings.Replace(encrypted, "'ENC[AES256_GCM,data:x]'", "plain", 1), "Secret contains an unencrypted value"},
		{"empty data", strings.Replace(encrypted, "{token: 'ENC[AES256_GCM,data:x]'}", "{}", 1), "Secret needs nonempty encrypted data or stringData"},
		{"no Secret", "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: c}\n", "expected an encrypted Secret"},
		{"not YAML", "a: [1\n", "invalid YAML"},
		{"not Kubernetes", "- a\n", "expected Kubernetes YAML documents"},
	} {
		files := tree()
		files["platform/svc/secret.sops.yaml"] = c.text
		expectProblem(t, checkTree(t, files), "sops-secrets", "platform/svc/secret.sops.yaml: "+c.problem)
	}
}

// The backup writer's Secret in an app's backup file holds only Flux substitutions, so it is not ciphertext.
func TestTheBackupWritersSubstitutedSecretIsExempt(t *testing.T) {
	files := tree()
	backup := "apiVersion: v1\nkind: Secret\nmetadata: {name: w}\nstringData:\n  a: ${BACKUP_AWS_ACCESS_KEY_ID}\n  b: ${BACKUP_AWS_SECRET_ACCESS_KEY}\n"
	files["apps/shop/staging/backup.yaml"] = backup
	if got := problems(checkTree(t, files)); len(got) != 0 {
		t.Fatalf("problems = %q", got)
	}
	files["apps/shop/staging/backup.yaml"] = backup + "  c: plain\n"
	expectProblem(t, checkTree(t, files), "sops-secrets", "apps/shop/staging/backup.yaml: Git-managed Secret must be a .sops.yaml")
}

func TestADecryptingUnitNeedsASecretFile(t *testing.T) {
	files := tree()
	delete(files, "platform/svc/secret.sops.yaml")
	expectProblem(t, checkTree(t, files), "sops-secrets", "platform-svc decrypts SOPS but its path has no *.sops.yaml Secret")
}

func TestTheSettings(t *testing.T) {
	for text, problem := range map[string]string{
		strings.Replace(settings, "BASE_DOMAIN: example.test", "BASE_DOMAIN: ''", 1): "BASE_DOMAIN missing from platform-settings",
		strings.Replace(settings, "CERT_ISSUER: x, ", "", 1):                         "CERT_ISSUER missing from platform-settings",
		strings.Replace(settings, "CERT_ISSUER: x", "CERT_ISSUER: on", 1):            "settings data must contain strings",
		strings.Replace(settings, "platform-settings", "other", 1):                   "expected platform-settings ConfigMap",
	} {
		files := tree()
		files["clusters/home/flux-system/sources/configmap-platform-settings.yaml"] = text
		report := checkTree(t, files)
		got := problems(report)
		if !slices.ContainsFunc(got["platform-settings"], func(p string) bool { return strings.Contains(p, problem) }) {
			t.Errorf("problems = %q, want %q", got, problem)
		}
	}
}

// Unreadable settings leave the units that substitute them out of the render paths, with the reason.
func TestUnreadableSettingsStopOnlyTheUnitsThatSubstituteThem(t *testing.T) {
	files := tree()
	files["clusters/home/flux-system/sources/configmap-platform-settings.yaml"] = "apiVersion: v1\nkind: Secret\nmetadata: {name: x}\n"
	report := checkTree(t, files)
	got := problems(report)
	if len(report.Paths) != 2 || !slices.ContainsFunc(got["render-paths"], func(p string) bool {
		return strings.Contains(p, "platform/svc: substitutes the platform settings, which are unreadable")
	}) {
		t.Fatalf("paths %v, problems %q", report.Paths, got)
	}
}

func TestRenderPathProblems(t *testing.T) {
	for _, c := range []struct{ unit, problem string }{
		{strings.Replace(svcUnit, "./platform/svc", "platform/svc", 1), "clusters/home/platform.yaml: Flux Kustomization needs a local spec.path"},
		{strings.Replace(svcUnit, "./platform/svc", "./../outside", 1), "clusters/home/platform.yaml: spec.path escapes the repository"},
		{strings.Replace(svcUnit, "./platform/svc", "./platform/none", 1), "missing active Kustomize path: platform/none"},
		{svcUnit + "---\n" + strings.Replace(svcUnit, "platform-svc", "platform-twin", 1), "clusters/home/platform.yaml: duplicate Flux render path ./platform/svc"},
		{strings.Replace(svcUnit, "ConfigMap, name: platform-settings", "ConfigMap, name: other", 1), "platform/svc: validation needs an explicit mapping for new Flux substitution sources"},
		{"a: [1\n", "clusters/home/platform.yaml: invalid YAML"},
	} {
		files := tree()
		files["clusters/home/platform.yaml"] = c.unit
		got := problems(checkTree(t, files))
		if !slices.ContainsFunc(got["render-paths"], func(p string) bool { return strings.Contains(p, c.problem) }) {
			t.Errorf("problems = %q, want %q", got, c.problem)
		}
	}
}

func TestRenderPathSubstitutesAsFluxDoes(t *testing.T) {
	files := tree()
	reader := gittest.New("/repo", git.Checkout{Present: slices.Sorted(maps.Keys(files))})
	render := func(path, rendered string) (string, error) {
		return RenderPath("/repo", reader, manifests.YAML{}, readFrom(files), path, []byte(rendered))
	}
	doc := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\ndata: {host: \"a.${BASE_DOMAIN}\", kept: \"$${BASE_DOMAIN}\"}\n"
	if got, err := render("platform/svc", doc); err != nil || !strings.Contains(got, "a.example.test") || !strings.Contains(got, "$${BASE_DOMAIN}") {
		t.Errorf("substituting unit: %q, %v", got, err)
	}
	unescaped := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\ndata: {auth: \"${env:KEY}\"}\n"
	if got, err := render("clusters/home/flux-system", unescaped); err != nil || got != unescaped {
		t.Errorf("a unit without substitution keeps ${...}: %q, %v", got, err)
	}
	var finding *checkrepo.Finding
	if _, err := render("platform/svc", unescaped); !errors.As(err, &finding) ||
		!strings.Contains(finding.Problem, "unresolved Flux substitution ${env:KEY}") || !strings.Contains(finding.Problem, "$${...}") {
		t.Errorf("an unescaped reference in a substituting unit: %v", err)
	}
	if _, err := render("platform/svc", "kind: ConfigMap\n"); !errors.As(err, &finding) || !strings.Contains(err.Error(), "lacks apiVersion") {
		t.Errorf("an incomplete document: %v", err)
	}
	if _, err := render("platform/none", doc); err == nil || errors.As(err, &finding) || !strings.Contains(err.Error(), "not an active render path") {
		t.Errorf("a path Flux does not render: %v", err)
	}
}

// A refused Git read and a catalogue v2 cannot decode stop the check rather than passing it.
func TestCheckRepoStopsOnWhatItCannotRead(t *testing.T) {
	files := tree()
	reader := gittest.New("/repo", git.Checkout{Present: slices.Sorted(maps.Keys(files))})
	reader.Err = &git.Error{Reason: git.NotARepository}
	if _, err := CheckRepo("/repo", reader, manifests.YAML{}, readFrom(files)); err == nil {
		t.Error("a refused Git read passed")
	}
	files["v2/contract/names.json"] = "{}"
	if _, err := CheckRepo("/repo", gittest.New("/repo", git.Checkout{Present: slices.Sorted(maps.Keys(files))}),
		manifests.YAML{}, readFrom(files)); err == nil || !strings.Contains(err.Error(), "v2/contract/names.json") {
		t.Errorf("an undecodable catalogue: %v", err)
	}
}
