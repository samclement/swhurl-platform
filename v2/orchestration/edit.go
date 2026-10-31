package orchestration

import (
	"bytes"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// InstanceInputs is one instance's documents and the file its record is written to. A use case that changes a
// record writes it back where the operator keeps it: which records root a tree uses is the caller's choice, not a
// convention of this layer.
type InstanceInputs struct {
	Inputs
	InstancePath string
}

// Editable is one instance's inputs and the change to apply: the name a report states, and the kernel rule bound
// to the caller's value. A command parses the value and names the change; the kernel decides what it means and
// refuses one the record or the release does not permit.
type Editable struct {
	InstanceInputs
	Change string
	Apply  func(kernel.InstanceSpec, kernel.ReleaseBundle) (kernel.InstanceSpec, error)
}

// Edition is one applied change: the record before and after, the compilation the edited record produces, and the
// files the change wrote. ResolutionStale says the change altered a declaration the resolution settles — what the
// instance calls, admits or subscribes to — so the committed resolution predates the record and the compiled files
// still show the relationships it names. Regenerating it is the caller's next step, not this use case's: the
// resolution is decided from every instance in a pinned revision, which is more than one record pair.
type Edition struct {
	Application, Environment, Change string
	Before, After                    kernel.InstanceSpec
	Compilation                      kernel.Compilation
	ResolutionStale                  bool
	Written                          []files.Outcome
}

// Edit applies one named change to an instance's record, compiles the edited record, and writes the record and the
// files it renders in one effect. A change that leaves the record as it was is refused rather than written, so an
// operator who repeats a command is told nothing changed instead of being shown a rewrite.
func Edit(editable Editable, destination string, writer files.Writer) (Edition, error) {
	before, release, err := decodePair(editable.Instance, editable.Release)
	if err != nil {
		return Edition{}, err
	}
	after, err := editable.Apply(before, release)
	if err != nil {
		return Edition{}, err
	}
	record, changed, err := changedRecord(before, after)
	if err != nil {
		return Edition{}, err
	}
	if !changed {
		return Edition{}, &Refusal{Subject: editable.Change, Reason: "already_set"}
	}
	inputs := editable.Inputs
	inputs.Instance = record
	compiled, files, err := compileComplete(inputs)
	if err != nil {
		return Edition{Compilation: compiled}, err
	}
	result := Edition{Application: after.Application, Environment: after.Environment, Change: editable.Change,
		Before: before, After: after, Compilation: compiled, ResolutionStale: relationshipsChanged(before, after)}
	written := append([]kernel.File{{Path: editable.InstancePath, Document: record}}, files...)
	result.Written, err = writeCompiled(destination, compiled, written, writer)
	return result, err
}

// TeamInputs is an app's record, the team to set, and every instance the change re-renders. A team is the app's
// fact, so one change reaches each environment the app has: the app record is shared, and the namespace label it
// renders is not.
type TeamInputs struct {
	App       []byte
	AppPath   string
	Team      string
	Instances []InstanceInputs
}

// TeamChange is an applied team change: the app record before and after, the instances it re-rendered, and the
// files it wrote.
type TeamChange struct {
	Application string
	Before      kernel.AppSpec
	After       kernel.AppSpec
	Instances   []kernel.InstanceSpec
	Written     []files.Outcome
}

// SetTeam applies one app-level change — its team — and writes the app record with every environment's files. The
// instances' own records are unchanged: the team is not an instance fact, so nothing rewrites them.
func SetTeam(inputs TeamInputs, destination string, writer files.Writer) (TeamChange, error) {
	before, err := kernel.DecodeApp(inputs.App)
	if err != nil {
		return TeamChange{}, err
	}
	after, err := kernel.WithTeam(before, inputs.Team)
	if err != nil {
		return TeamChange{}, err
	}
	if before.Team == after.Team {
		return TeamChange{}, &Refusal{Subject: "team", Reason: "already_set"}
	}
	if len(inputs.Instances) == 0 {
		return TeamChange{}, &Refusal{Subject: "team", Reason: "the_app_has_no_instance"}
	}
	app, err := kernel.EncodeApp(after)
	if err != nil {
		return TeamChange{}, err
	}
	pending := []kernel.File{{Path: inputs.AppPath, Document: app}}
	result := TeamChange{Application: after.Application, Before: before, After: after}
	for _, one := range inputs.Instances {
		spec, _, err := decodePair(one.Instance, one.Release)
		if err != nil {
			return result, err
		}
		one.App = app
		compiled, rendered, err := compileComplete(one.Inputs)
		if err != nil {
			return result, err
		}
		result.Instances = append(result.Instances, spec)
		outcomes, err := writeCompiled(destination, compiled, append(pending, rendered...), writer)
		result.Written = append(result.Written, outcomes...)
		if err != nil {
			return result, err
		}
		// The app record is shared, so it is written once; each instance's own files are written with it.
		pending = nil
	}
	return result, nil
}

// decodePair reads the two records a change needs: the instance and the release whose kind decides some of them.
func decodePair(instance, release []byte) (kernel.InstanceSpec, kernel.ReleaseBundle, error) {
	spec, err := kernel.DecodeInstance(instance)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, err
	}
	decoded, err := kernel.DecodeRelease(release)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, err
	}
	if decoded.Application != spec.Application {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, &Refusal{Subject: "records",
			Reason: "the_instance_and_its_release_name_different_apps"}
	}
	return spec, decoded, nil
}

// changedRecord is the edited record's bytes, and whether the change altered the record at all.
func changedRecord(before, after kernel.InstanceSpec) ([]byte, bool, error) {
	was, err := kernel.EncodeInstance(before)
	if err != nil {
		return nil, false, err
	}
	now, err := kernel.EncodeInstance(after)
	if err != nil {
		return nil, false, err
	}
	return now, !bytes.Equal(was, now), nil
}

// relationshipsChanged is whether the change altered a declaration the resolution settles.
func relationshipsChanged(before, after kernel.InstanceSpec) bool {
	return !equal(before.Calls, after.Calls) || !equal(before.Allows, after.Allows) ||
		!equal(before.Events.Subscriptions, after.Events.Subscriptions)
}

// equal is two declarations with the same names in the same order.
func equal(one, other []string) bool {
	if len(one) != len(other) {
		return false
	}
	for index := range one {
		if one[index] != other[index] {
			return false
		}
	}
	return true
}
