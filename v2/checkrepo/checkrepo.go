// Package checkrepo decides what `make check-repo` checks in this checkout: its documentation links, the two tables
// the docs keep in step with Git, the platform names v2's compiled tree gives apps, the SOPS structure of every
// Git-managed Secret, the platform settings, and which folders Flux renders with what each substitutes. Each check is a
// function of the file text and decoded manifests it is given; rendering and schema validation are not decided here
// (v2/check-repo.sh runs them over the paths this package lists), so nothing here starts a process.
package checkrepo

import (
	"slices"
	"strings"
)

// The files and folders the checks read, from the repository root.
const (
	cluster      = "clusters/home"
	rootUnits    = cluster + "/flux-system/kustomizations.yaml"
	settingsFile = cluster + "/flux-system/sources/configmap-platform-settings.yaml"
	unitsDoc     = "docs/architecture.md"
	appsDoc      = "docs/apps.md"
	compiled     = "v2-apps/instances"
	Catalogue    = "v2/contract/names.json"
)

// The SQLite backup's names: the Secret an instance's unit substitutes its upload-only key from, the file its job and
// Secret are compiled into, and the label on the job, which is the platform's rather than the app's.
const (
	backupWriter = "backup-writer"
	backupFile   = "backup.yaml"
	backupLabel  = "platform.swhurl.com/backup"
)

// The checks, in the order they run and report.
const (
	docsLinks         = "docs-links"
	fluxUnitsTable    = "flux-units-table"
	appInterfaceTable = "app-interface-table"
	emittedNames      = "emitted-names"
	sopsSecrets       = "sops-secrets"
	platformSettings  = "platform-settings"
	renderPaths       = "render-paths"
)

// bootstrap is what Flux renders before any unit: the root units' folder and the sources it reads.
var bootstrap = []string{cluster + "/flux-system", cluster + "/flux-system/sources"}

// manifestRoots are the folders whose YAML is checked for Secrets.
var manifestRoots = []string{"clusters", "infra", "platform", "apps"}

// Document is one decoded YAML document. Value is nil for a document that is not a mapping (a list, a scalar or an
// empty document); Kind and Name are its kind and metadata.name when those are strings.
type Document struct {
	Kind, Name string
	Value      map[string]any
}

// Decoded is one manifest file: its documents, or why it is not YAML.
type Decoded struct {
	Documents []Document
	Err       string
}

// Repository is what the checks read. Present is every file the working tree holds that Git does not ignore; Text
// holds the files Needs names as text, and Manifests the ones it names as manifests, decoded.
type Repository struct {
	Present   []string
	Text      map[string]string
	Manifests map[string]Decoded
}

// Names is the names catalogue's one question: which platform names in a file it does not list.
type Names interface {
	Unlisted(text []byte) []string
}

// Result is one check: what holds when it found nothing, or every problem it found.
type Result struct {
	Check, Okay string
	Problems    []string
}

// Path is one folder Flux renders, from the repository root, and the values it substitutes: nil when its unit
// substitutes nothing, so a ${...} in it reaches the cluster as written.
type Path struct {
	Path   string
	Values map[string]string
}

// Report is every check's result, in order, and the active render paths in the order Flux defines them.
type Report struct {
	Results []Result
	Paths   []Path
}

// Needs is the present files Check reads: as text, every Markdown file outside .claude/, every file of the compiled
// tree and the names catalogue; as manifests, the YAML under the four manifest roots and the JSON unit files beside
// the cluster's YAML ones.
func Needs(present []string) (text, manifests []string) {
	for _, file := range present {
		switch {
		case isMarkdown(file), under(file, compiled), file == Catalogue:
			text = append(text, file)
		case isManifest(file), isUnitFile(file):
			manifests = append(manifests, file)
		}
	}
	return text, manifests
}

// PathNeeds is the manifests Paths reads: the unit files and the platform settings.
func PathNeeds(present []string) []string {
	var needed []string
	for _, file := range present {
		if isUnitFile(file) || file == rootUnits || file == settingsFile {
			needed = append(needed, file)
		}
	}
	return needed
}

// Check runs every check, each whether or not an earlier one found a problem.
func Check(r Repository, names Names) Report {
	found := discover(r)
	settings, settingsResult := checkSettings(r)
	paths, pathProblems := activePaths(r, found, settings)
	render := Result{Check: renderPaths, Problems: append(found.problems, pathProblems...)}
	if len(render.Problems) == 0 {
		render.Okay = pathsOkay(len(paths))
	}
	results := []Result{checkLinks(r), checkUnitTable(r, found.units), checkInterfaceTable(r), checkEmitted(r, names),
		checkSecrets(r, found.units), settingsResult, render}
	for i := range results {
		results[i].Problems = compact(results[i].Problems)
	}
	return Report{Results: results, Paths: paths}
}

// Paths is the active render paths alone, for one rendered folder's substitution, and the problems that left a path
// out or stopped one from substituting.
func Paths(r Repository) ([]Path, []string) {
	found := discover(r)
	settings, _ := checkSettings(r)
	paths, problems := activePaths(r, found, settings)
	return paths, append(found.problems, problems...)
}

func isMarkdown(file string) bool {
	return strings.HasSuffix(file, ".md") && !strings.HasPrefix(file, ".claude/")
}

func isManifest(file string) bool {
	return hasYAMLSuffix(file) && slices.ContainsFunc(manifestRoots, func(root string) bool { return under(file, root) })
}

// isUnitFile is a file directly in the cluster folder that may define units: YAML, or the JSON v2 compiles units into.
func isUnitFile(file string) bool {
	folder, name, _ := cutLast(file, "/")
	return folder == cluster && (hasYAMLSuffix(name) || strings.HasSuffix(name, ".json"))
}

func hasYAMLSuffix(file string) bool {
	return strings.HasSuffix(file, ".yaml") || strings.HasSuffix(file, ".yml")
}

// under says whether file is path or lies inside it.
func under(file, path string) bool {
	return file == path || strings.HasPrefix(file, strings.TrimSuffix(path, "/")+"/")
}

// cutLast splits s around its last sep; folder is empty when there is none.
func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+len(sep):], true
}

// baseName is a path's last element.
func baseName(file string) string {
	_, name, _ := cutLast(file, "/")
	return name
}

// compact sorts nothing: it drops a problem repeated in a row, keeping the order the check found them in.
func compact(problems []string) []string {
	return slices.Compact(problems)
}
