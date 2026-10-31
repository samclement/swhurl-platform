package orchestration

import (
	"path"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Registration is one of the platform's kustomizations that lists a file a removal deletes: the document, and where
// it is written. A removal edits the one entry it owns and keeps every other byte, because the list holds entries
// and comments v2 did not write.
type Registration struct {
	Path     string
	Document []byte
}

// RemovalInputs is one instance to remove: its records, the resolution that settles its relationships, the places its
// own files are, and the two platform registrations its existence holds an entry in.
type RemovalInputs struct {
	Instance, Release []byte
	// Calls is the resolution. A removal refuses while the instance is still part of a connected relationship: a
	// caller would keep a caller token and an address for something that is gone, and a subscriber's Consumer would
	// name a Stream that went with the publisher's folder.
	Calls [][]byte
	// Records, Compiled and Unit are the instance's own places, relative to the destination.
	Records, Compiled, Unit string
	// AppRecord is the app's record, and LastEnvironment says no other environment keeps the app alive, so the
	// registration takes it with the instance.
	AppRecord       string
	LastEnvironment bool
	// AuthFile is the namespace's auth object.
	AuthFile         string
	UnitRegistration Registration
	AuthRegistration Registration
}

// Removal is what a removal did: the files it wrote and deleted, and what it left to the platform's own renderers.
type Removal struct {
	Application, Environment string
	Written                  []files.Outcome
	Removed                  []files.Outcome
	Notes                    []string
}

// Remove deletes one instance: its records, the files compiled from them and its unit, the namespace's auth object, and
// the app record when this was the app's last environment. It writes the two registrations without the entries that
// named them first, so a commit never lists a file it deleted.
//
// The platform's own documents are not re-rendered here. Each is rendered from a pinned revision of the whole tree —
// the egress allocation and the proxy's objects, the broker's user list, the Reloader's watch list and the resolution —
// and this removal is not in that revision yet, so the report names the commands that regenerate them instead of
// writing documents that still name the instance.
func Remove(inputs RemovalInputs, destination string, writer files.Writer) (Removal, error) {
	spec, release, err := decodePair(inputs.Instance, inputs.Release)
	if err != nil {
		return Removal{}, err
	}
	if err := removable(spec, inputs.Calls); err != nil {
		return Removal{}, err
	}
	result := Removal{Application: spec.Application, Environment: spec.Environment}
	documents, err := registrationFiles(inputs, spec)
	if err != nil {
		return result, err
	}
	result.Written, err = writer.Write(destination, documents)
	if err != nil {
		return result, err
	}
	for _, removed := range removalPaths(inputs) {
		if removed == "" {
			continue
		}
		outcomes, err := writer.Remove(destination, removed)
		result.Removed = append(result.Removed, outcomes...)
		if err != nil {
			return result, err
		}
	}
	result.Notes = removalNotes(release)
	return result, nil
}

// removable refuses an instance the resolution still connects: a caller that calls it, or a subscriber that receives
// its events. Both are relationships another instance's files render, so they are removed first.
func removable(spec kernel.InstanceSpec, calls [][]byte) error {
	for _, document := range calls {
		connections, err := kernel.ConnectionsOf(document, spec.Application, spec.Environment)
		if err != nil {
			return err
		}
		if len(connections.Callers) > 0 {
			return &Refusal{Subject: "remove", Reason: "called_by " + strings.Join(connections.Callers, " ")}
		}
		subscribers, err := kernel.SubscribersOf(document, spec.Application, spec.Environment)
		if err != nil {
			return err
		}
		if len(subscribers) > 0 {
			return &Refusal{Subject: "remove", Reason: "subscribed_by " + strings.Join(subscribers, " ")}
		}
	}
	return nil
}

// registrationFiles is the platform's kustomizations without the entries that name the instance: its unit, and its
// namespace's auth object.
func registrationFiles(inputs RemovalInputs, spec kernel.InstanceSpec) ([]kernel.File, error) {
	entries := []struct {
		registration Registration
		entry        string
		drop         func([]byte, string) ([]byte, bool, error)
	}{
		{inputs.UnitRegistration, path.Base(inputs.Unit), kernel.WithoutEntry},
		{inputs.AuthRegistration, kernel.AuthFileName(kernel.NamespaceOf(spec.Application, spec.Environment)),
			kernel.WithoutResource},
	}
	documents := make([]kernel.File, 0, len(entries))
	for _, listed := range entries {
		if listed.registration.Path == "" {
			continue
		}
		document, removed, err := listed.drop(listed.registration.Document, listed.entry)
		if err != nil {
			return nil, err
		}
		if removed {
			documents = append(documents, kernel.File{Path: listed.registration.Path, Document: document})
		}
	}
	return documents, nil
}

// removalPaths is every path a removal deletes, in the order it deletes them: the instance's own files before the
// app record that outlives them.
func removalPaths(inputs RemovalInputs) []string {
	paths := []string{inputs.Compiled, inputs.Records, inputs.Unit, inputs.AuthFile}
	if inputs.LastEnvironment {
		paths = append(paths, inputs.AppRecord)
	}
	return paths
}

// removalNotes is what the removal left to the platform's own renderers, named as the command that regenerates each.
func removalNotes(release kernel.ReleaseBundle) []string {
	notes := []string{
		"the egress allocation and the proxy's objects still name the instance: swhurl-v2 egress, then swhurl-v2 proxy",
		"the resolution still names its relationships: swhurl-v2 resolve",
		"the broker's user list: make v2-users",
		"the Reloader's watch list: make v2-reloader",
		"the platform's auth fan-out: swhurl-v2 auth",
	}
	if release.Database == "sqlite" {
		notes = append(notes, "a retained volume outlives the instance: remove it only when its data is wanted gone")
	}
	return notes
}
