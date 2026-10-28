package kernel

import "slices"

// SelectRelease returns a new value with only its release reference changed.
// This is not production authorisation, publication, provisioning or promotion.
// Validation precedes application and revision checks; stale precedes no-op.
// A refusal returns the zero value, never a partially changed instance.
func SelectRelease(current InstanceSpec, candidate ReleaseBundle, expected InstanceRevision) (InstanceSpec, error) {
	revision, err := current.Revision()
	if err != nil {
		return InstanceSpec{}, err
	}
	id, err := candidate.ID()
	if err != nil {
		return InstanceSpec{}, err
	}
	if !revisionPattern.MatchString(string(expected)) {
		return InstanceSpec{}, refuse(Invalid, "expected", "expected an instance revision")
	}
	if current.Application != candidate.Application {
		return InstanceSpec{}, refuse(WrongApplication, "application", "candidate belongs to a different application")
	}
	if revision != expected {
		return InstanceSpec{}, refuse(StaleInstance, "expected", "instance changed; review its current specification")
	}
	current.Release = id
	// A record with declarations carries reference-bearing members: the result owns its lists, so a caller
	// that edits what it was given cannot reach back into the record it came from.
	current.Calls, current.Allows = slices.Clone(current.Calls), slices.Clone(current.Allows)
	return current, nil
}
