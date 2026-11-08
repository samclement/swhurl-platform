package checkrepo

import (
	"fmt"
	"slices"
	"strings"
)

// unit is one Flux Kustomization Git defines: its name, the file defining it, the folder it applies (as written) and
// its spec, and whether it substitutes or decrypts.
type unit struct {
	name, file, path      string
	spec                  map[string]any
	substitutes, decrypts bool
}

// discovered is every unit, in the order Flux reads their files, and what kept a file or a unit from being read.
type discovered struct {
	units    []unit
	problems []string
}

// unitFiles is where units are defined: the root units, then each YAML or JSON file directly in the cluster folder.
func unitFiles(present []string) []string {
	files := []string{rootUnits}
	for _, file := range present {
		if isUnitFile(file) {
			files = append(files, file)
		}
	}
	slices.Sort(files[1:])
	return files
}

// discover reads every unit the unit files define; a document of another kind is not a unit.
func discover(r Repository) discovered {
	var found discovered
	for _, file := range unitFiles(r.Present) {
		decoded, held := r.Manifests[file]
		switch {
		case !held:
			found.problems = append(found.problems, file+": missing")
			continue
		case decoded.Err != "":
			found.problems = append(found.problems, file+": invalid YAML: "+decoded.Err)
			continue
		}
		for _, document := range decoded.Documents {
			found.add(file, document)
		}
	}
	return found
}

// add records document as a unit when it is a Flux Kustomization.
func (d *discovered) add(file string, document Document) {
	if document.Kind != "Kustomization" || document.Value["apiVersion"] != "kustomize.toolkit.fluxcd.io/v1" {
		return
	}
	if document.Name == "" {
		d.problems = append(d.problems, file+": a Flux Kustomization without metadata.name")
		return
	}
	spec, _ := document.Value["spec"].(map[string]any)
	path, _ := spec["path"].(string)
	_, substitutes := spec["postBuild"]
	_, decrypts := spec["decryption"]
	d.units = append(d.units, unit{name: document.Name, file: file, path: path, spec: spec,
		substitutes: substitutes, decrypts: decrypts})
}

// shared is the units that are not an app instance's (app-<app>-<env>).
func shared(units []unit) map[string]unit {
	found := map[string]unit{}
	for _, u := range units {
		if !strings.HasPrefix(u.name, "app-") {
			found[u.name] = u
		}
	}
	return found
}

// activePaths is every folder Flux renders: the bootstrap folders, then each unit's local spec.path, with the values
// each substitutes. A unit whose path is not local, leaves the repository or repeats another's is left out with its
// problem, as is a folder without a kustomization or a substitution source this check has no mapping for.
func activePaths(r Repository, found discovered, settings map[string]string) ([]Path, []string) {
	order, specs := slices.Clone(bootstrap), map[string]map[string]any{}
	var problems []string
	for _, u := range found.units {
		path, problem := renderPath(u)
		if problem == "" && len(specs[path]) > 0 {
			problem = fmt.Sprintf("%s: duplicate Flux render path %s", u.file, u.path)
		}
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		if !slices.Contains(order, path) {
			order = append(order, path)
		}
		specs[path] = u.spec
	}
	var paths []Path
	for _, path := range order {
		values, problem := pathValues(r.Present, path, specs[path], settings)
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		paths = append(paths, Path{Path: path, Values: values})
	}
	return paths, problems
}

// renderPath is the folder a unit renders, from the repository root, or why it has none.
func renderPath(u unit) (string, string) {
	if !strings.HasPrefix(u.path, "./") {
		return "", u.file + ": Flux Kustomization needs a local spec.path"
	}
	path, inside := resolve("", u.path)
	if !inside {
		return "", u.file + ": spec.path escapes the repository"
	}
	return path, ""
}

// pathValues is what a folder substitutes, after checking Kustomize can render it.
func pathValues(present []string, path string, spec map[string]any, settings map[string]string) (map[string]string, string) {
	if !hasKustomization(present, path) {
		return nil, "missing active Kustomize path: " + path
	}
	values, problem := substitutions(spec, settings)
	if problem != "" {
		return nil, path + ": " + problem
	}
	return values, ""
}

func hasKustomization(present []string, path string) bool {
	prefix := ""
	if path != "." {
		prefix = path + "/"
	}
	return slices.Contains(present, prefix+"kustomization.yaml") || slices.Contains(present, prefix+"kustomization.yml")
}

// resolve joins a relative target to the folder it was written in, dropping "." and applying ".."; false when it
// leaves the repository or is absolute. The repository root itself is ".".
func resolve(folder, target string) (string, bool) {
	if strings.HasPrefix(target, "/") {
		return "", false
	}
	var kept []string
	for _, part := range strings.Split(folder+"/"+target, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(kept) == 0 {
				return "", false
			}
			kept = kept[:len(kept)-1]
		default:
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return ".", true
	}
	return strings.Join(kept, "/"), true
}

func pathsOkay(count int) string {
	return fmt.Sprintf("%d active render paths, each a local Kustomize folder with a known substitution source", count)
}
