package kernel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// Canonical identity is a versioned positional JSON array, not a serialization of
// a Go struct. See docs/iteration-2-plan.md for the language-independent encoding.
func releaseCanonical(r ReleaseBundle) []byte {
	return canonical([]any{"swhurl.release.v1", r.SchemaVersion, r.Application,
		r.Source.Repository, r.Source.Commit, r.Artifact.Image, r.Artifact.Digest,
		r.Artifact.MediaType, r.Artifact.Platform, r.Runtime.Kind, r.Runtime.Port,
		r.Runtime.HealthPath, r.Runtime.UID, r.Database, r.Telemetry})
}

func instanceCanonical(s InstanceSpec) []byte {
	return canonical([]any{"swhurl.instance.v1", s.SchemaVersion, s.Application,
		s.Environment, s.Release, s.Configuration.Replicas, s.Configuration.MemoryMiB,
		s.Grants.Exposure, s.Bindings, s.Storage.SizeMiB, s.Storage.ClaimName,
		sortedNames(s.Calls), sortedNames(s.Allows)})
}

// labelPairs is a label set as sorted "key=value" strings: a map's iteration order is not an identity.
func labelPairs(labels map[string]string) []any {
	pairs := make([]any, 0, len(labels))
	for _, key := range sortedKeys(labels) {
		pairs = append(pairs, key+"="+labels[key])
	}
	return pairs
}

func canonical(value []any) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	// Only arrays of validated ASCII strings, int32s and an empty object reach here.
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
}

// sortedNames is a declaration list in its canonical order and never nil, so a record's bytes and its identity
// do not depend on the order the operator wrote, and an empty list encodes as [] rather than null.
func sortedNames(names []string) []string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	if sorted == nil {
		return []string{}
	}
	return sorted
}

func hash(prefix string, content []byte) string {
	sum := sha256.Sum256(content)
	return prefix + hex.EncodeToString(sum[:])
}

func (r ReleaseBundle) ID() (ReleaseID, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	if r.ReleaseID != "" && !releasePattern.MatchString(string(r.ReleaseID)) {
		return "", refuse(Invalid, "releaseId", "expected a release ID")
	}
	id := ReleaseID(hash("rel-sha256:", releaseCanonical(r)))
	if r.ReleaseID != "" && r.ReleaseID != id {
		return "", refuse(IdentityMismatch, "releaseId", "asserted identity does not match content")
	}
	return id, nil
}

func (s InstanceSpec) Revision() (InstanceRevision, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	return InstanceRevision(hash("inst-sha256:", instanceCanonical(s))), nil
}
