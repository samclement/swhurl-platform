package kernel

import (
	"encoding/json"
	"strings"
)

// InstanceFolder is the folder the compiler writes one instance's files into: the platform profile's delivery
// root and the instance's identity. It is the one place the convention is written, so the renderer, a pruning
// writer and a removal cannot disagree about where an instance's files are.
func InstanceFolder(application, environment string, profile PlatformProfile) string {
	return profile.Delivery.OutputRoot + "/instances/" + application + "/" + environment
}

// UnitPath is the file Flux applies one instance from: the profile's unit root and the unit's name.
func UnitPath(application, environment string, profile PlatformProfile) string {
	return profile.Delivery.UnitsRoot + "/" + unitFileName(application, environment) + ".json"
}

// unitFileName is the name a unit is registered by in the unit root's kustomization.
func unitFileName(application, environment string) string {
	return "app-" + NamespaceOf(application, environment)
}

// Rendered is whether a file name is one the renderer writes into an instance's folder: the resources a release,
// an exposure, a database, an event or a call can add, and the folder's kustomization. A name it does not write
// is one the folder holds as a declared input — its seed, the documents it publishes — and a writer that prunes
// a folder must keep it.
func Rendered(name string) bool {
	if strings.HasPrefix(name, "consumer-") && strings.HasSuffix(name, ".json") {
		return true
	}
	switch name {
	case "namespace.json", "helmrelease.json", "resourcequota.json", "networkpolicy.json",
		"image-repository.json", "image-policy.json", "ingress.json", "callers.json", "stream.json",
		"backup-secret.json", "backup-cronjob.json", "backup-networkpolicy.json", "kustomization.yaml":
		return true
	}
	return false
}

// WithoutResource is a kustomization v2 rendered, without the one resource it lists by name: v2's own documents are JSON,
// so dropping a file from a folder is dropping it from the list. Every other field and resource is kept, and the answer is
// the canonical bytes the renderer writes, so the removal and a re-render cannot disagree.
func WithoutResource(document []byte, name string) ([]byte, bool, error) {
	var parsed manifest
	if err := json.Unmarshal(document, &parsed); err != nil {
		return nil, false, refuse(Invalid, "registration", "expected a JSON kustomization")
	}
	listed, ok := parsed["resources"].([]any)
	if !ok {
		return nil, false, refuse(Invalid, "registration", "the kustomization lists no resources")
	}
	remaining := make([]any, 0, len(listed))
	removed := false
	for _, entry := range listed {
		if entry != name {
			remaining = append(remaining, entry)
			continue
		}
		if removed {
			return nil, false, refuse(Invalid, "registration", "the resource is listed twice")
		}
		removed = true
	}
	if !removed {
		return document, false, nil
	}
	parsed["resources"] = remaining
	return encodeManifest(parsed), true, nil
}

// WithoutEntry is a kustomization without the one line that lists entry: the platform's own registrations are
// lists a removal edits, and each line is exactly one listed file. It reports whether the entry was there, and
// refuses a document that lists it twice, because the two mentions are not the same registration to v2. Every
// other byte of the document is kept as it was: the file may hold entries and comments v2 did not write.
func WithoutEntry(document []byte, entry string) ([]byte, bool, error) {
	listed := "- " + entry
	lines := strings.SplitAfter(string(document), "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		if strings.TrimSpace(line) != listed {
			kept = append(kept, line)
			continue
		}
		if removed {
			return nil, false, refuse(Invalid, "registration", "the entry is listed twice")
		}
		removed = true
	}
	if !removed {
		return document, false, nil
	}
	return []byte(strings.Join(kept, "")), true, nil
}
