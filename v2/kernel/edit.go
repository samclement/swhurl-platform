package kernel

import "slices"

// The named changes an instance's record takes. Each function replaces exactly one setting and applies exactly
// that setting's rules, so a change cannot quietly carry a second fact: the record's own validation runs on the
// result, and the one rule that needs the release is applied here. A change the record or the release does not
// permit is refused with the setting's name, never with the value it rejected.
//
// Team is the one app-level change: the record that holds it is shared by every environment, so it is not an
// instance setting and is not reachable from an instance's record at all.

// WithReplicas sets how many pods the instance runs.
func WithReplicas(spec InstanceSpec, replicas int32) (InstanceSpec, error) {
	spec.Configuration.Replicas = replicas
	return changedInstance(spec)
}

// WithMemory sets the memory one pod is given.
func WithMemory(spec InstanceSpec, memoryMiB int32) (InstanceSpec, error) {
	spec.Configuration.MemoryMiB = memoryMiB
	return changedInstance(spec)
}

// WithExposure sets how the instance is reached. A worker serves nothing, so it has no route to sign in to: the
// release decides that, not the record.
func WithExposure(spec InstanceSpec, release ReleaseBundle, exposure string) (InstanceSpec, error) {
	if exposure != "private" && release.Runtime.Kind != "web" {
		return InstanceSpec{}, refuse(Unsupported, "exposure", "a worker serves nothing, so it has no route")
	}
	spec.Grants.Exposure = exposure
	return changedInstance(spec)
}

// WithCalls replaces the apps the instance calls: what it declares, not who consents. Consent is the callee's,
// and the resolution decides the pair.
func WithCalls(spec InstanceSpec, calls []string) (InstanceSpec, error) {
	if err := notItself(spec.Application, calls, "calls"); err != nil {
		return InstanceSpec{}, err
	}
	spec.Calls = owned(calls)
	return changedInstance(spec)
}

// WithAllows replaces the apps and teams whose calls the instance accepts. A team grant names the rule, never a
// member list, so a caller that joins or leaves the team needs no record rewritten.
func WithAllows(spec InstanceSpec, allows []string) (InstanceSpec, error) {
	if err := notItself(spec.Application, allows, "allows"); err != nil {
		return InstanceSpec{}, err
	}
	spec.Allows = owned(allows)
	return changedInstance(spec)
}

// WithSubscriptions replaces the events the instance receives. An instance signs in to the broker with its own
// key, so one that has no key cannot subscribe: there would be nothing to sign in with.
func WithSubscriptions(spec InstanceSpec, subscriptions []string) (InstanceSpec, error) {
	if len(subscriptions) > 0 && spec.Events.Key == "" {
		return InstanceSpec{}, refuse(Invalid, "subscriptions", "an instance without a broker key cannot subscribe")
	}
	spec.Events.Subscriptions = owned(subscriptions)
	return changedInstance(spec)
}

// WithTeam sets the team that labels every namespace the app has. The app record is shared by the app's
// environments, so this is the one change that reaches all of them.
func WithTeam(app AppSpec, team string) (AppSpec, error) {
	app.Team = team
	app.AppID = ""
	if err := app.validate(); err != nil {
		return AppSpec{}, err
	}
	return app, nil
}

// changedInstance returns the edited record, or the record's own refusal.
func changedInstance(spec InstanceSpec) (InstanceSpec, error) {
	if err := spec.validate(); err != nil {
		return InstanceSpec{}, err
	}
	return spec, nil
}

// notItself refuses a declaration that names the instance's own app: nobody calls or admits itself.
func notItself(application string, names []string, field string) error {
	for _, name := range names {
		if name == application {
			return refuse(Invalid, field, "an app cannot name itself")
		}
	}
	return nil
}

// owned copies a list into the record, so the record's bytes cannot change with the caller's slice.
func owned(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	return slices.Clone(names)
}
