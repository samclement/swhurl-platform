package kernel

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
	return current, nil
}
