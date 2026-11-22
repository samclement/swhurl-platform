package rules

import "github.com/samclement/swhurl-platform/v2/kernel"

// SelectPromotion is promotion's own rule on top of selection: the candidate is the release staging selects, the
// target is production, and the user that owns the files on a volume cannot change. Selection then replaces the
// target's release reference and nothing else — production's configuration, grants, calls, storage and broker
// identity are its own, and a promotion never copies staging's.
//
// The order is fixed: the candidate and the two instances, then the user, then selection's own checks (validation,
// staleness, the no-op). A promotion's direction is not decided here: a release states no build order, so the
// caller states both releases and a person reads a rollback from them.
func SelectPromotion(staging, target kernel.InstanceSpec, current, candidate kernel.ReleaseBundle,
	expected kernel.InstanceRevision) (kernel.InstanceSpec, error) {
	id, err := candidate.ID()
	if err != nil {
		return kernel.InstanceSpec{}, err
	}
	if staging.Environment != "staging" {
		return kernel.InstanceSpec{}, refuse(Invalid, "environment", "a promotion takes staging's release")
	}
	if target.Environment != "prod" {
		return kernel.InstanceSpec{}, refuse(Invalid, "environment", "a promotion moves into production")
	}
	if staging.Application != target.Application || candidate.Application != target.Application {
		return kernel.InstanceSpec{}, refuse(WrongApplication, "application", "a promotion stays in one application")
	}
	if staging.Release != id {
		return kernel.InstanceSpec{}, refuse(WrongRelease, "release", "the candidate is not the release staging selects")
	}
	if (target.Storage.ClaimName != "" || target.Storage.SizeMiB > 0) && candidate.Runtime.UID != current.Runtime.UID {
		return kernel.InstanceSpec{}, refuse(Unsupported, "runtime.uid", "the user cannot change on an instance with a volume")
	}
	return kernel.SelectRelease(target, candidate, expected)
}
