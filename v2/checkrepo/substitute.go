package checkrepo

import (
	"fmt"
	"strings"
)

// backupVariables is what an app unit with a SQLite database substitutes from the backup writer's Secret. Its values
// are not in Git, so a rendered folder gets a placeholder for each.
var backupVariables = []string{"BACKUP_AWS_ACCESS_KEY_ID", "BACKUP_AWS_SECRET_ACCESS_KEY"}

// substitutedPlaceholder stands for a value Flux reads from a Secret.
const substitutedPlaceholder = "<substituted>"

// substitutions is the values a unit's postBuild.substituteFrom gives its folder: none without one, the platform
// settings, or the backup writer's placeholders. Any other source is a problem: this check maps each source it knows.
func substitutions(spec map[string]any, settings map[string]string) (map[string]string, string) {
	postBuild, _ := spec["postBuild"].(map[string]any)
	sources, _ := postBuild["substituteFrom"].([]any)
	switch {
	case len(sources) == 0:
		return nil, ""
	case isOnlySource(sources, "ConfigMap", "platform-settings"):
		if settings == nil {
			return nil, "substitutes the platform settings, which are unreadable"
		}
		return settings, ""
	case isOnlySource(sources, "Secret", backupWriter):
		values := map[string]string{}
		for _, name := range backupVariables {
			values[name] = substitutedPlaceholder
		}
		return values, ""
	}
	return nil, "validation needs an explicit mapping for new Flux substitution sources"
}

// isOnlySource is whether sources is exactly one required source of that kind and name.
func isOnlySource(sources []any, kind, name string) bool {
	if len(sources) != 1 {
		return false
	}
	source, _ := sources[0].(map[string]any)
	optional, stated := source["optional"].(bool)
	return len(source) == 3 && source["kind"] == kind && source["name"] == name && stated && !optional
}

// Finding is a rendered folder Flux would not apply as Git holds it: a problem with the checkout, not a failure to
// read it.
type Finding struct{ Problem string }

func (f *Finding) Error() string { return f.Problem }

// Substitute is what Flux applies from a folder's rendered manifests: with no values, the text as Kustomize rendered
// it; otherwise every ${NAME} replaced by its value. A reference escaped as $${NAME} is left as written, and a NAME
// with no value is an error naming it, because Flux would apply the reference unresolved.
func Substitute(path Path, rendered string) (string, error) {
	if path.Values == nil {
		return rendered, nil
	}
	var out strings.Builder
	for rest := rendered; ; {
		start, name, end := nextReference(rest)
		if start < 0 {
			out.WriteString(rest)
			return out.String(), nil
		}
		value, known := path.Values[name]
		if !known {
			return "", &Finding{fmt.Sprintf("%s: unresolved Flux substitution ${%s} (this unit substitutes; escape a "+
				"literal reference as $${...})", path.Path, name)}
		}
		out.WriteString(rest[:start])
		out.WriteString(value)
		rest = rest[end:]
	}
}

// nextReference finds the first ${NAME} in text not preceded by '$': where it starts, NAME, and where it ends; -1
// when there is none. NAME is everything up to the first '}', and is never empty.
func nextReference(text string) (int, string, int) {
	for from := 0; ; {
		at := strings.Index(text[from:], "${")
		if at < 0 {
			return -1, "", -1
		}
		start := from + at
		closing := strings.IndexByte(text[start+2:], '}')
		if closing < 0 {
			return -1, "", -1
		}
		if closing > 0 && (start == 0 || text[start-1] != '$') {
			return start, text[start+2 : start+2+closing], start + 3 + closing
		}
		from = start + 1
	}
}

// Complete is whether each rendered document names its apiVersion, kind and metadata, as Flux needs to apply it.
func Complete(path Path, documents []Document) error {
	for _, document := range documents {
		_, version := document.Value["apiVersion"]
		_, kind := document.Value["kind"]
		_, metadata := document.Value["metadata"]
		if document.Value == nil || !version || !kind || !metadata {
			return &Finding{path.Path + ": rendered document lacks apiVersion, kind or metadata"}
		}
	}
	return nil
}
