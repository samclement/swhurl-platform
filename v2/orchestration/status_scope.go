package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// statusScopedTarget uses the same record and compiler vocabulary as writing and pruning. Shared
// inputs conservatively count as relevant; no assumption that committed rendering is up to date is needed.
func statusScopedTarget(inputs StatusInputs) (observe.Target, error) {
	compiled, err := Compile(inputs.Inputs)
	if err != nil {
		return observe.Target{}, err
	}
	target, err := targetFromCompilation(compiled, inputs.Revision)
	if err != nil || inputs.RecordsRoot == "" {
		return target, err
	}
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		return observe.Target{}, err
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return observe.Target{}, err
	}
	target.Scope = &observe.ChangeScope{
		Files: append([]string{kernel.AppRecordPath(inputs.RecordsRoot, spec.Application)}, compiled.Registrations...),
		Folders: []string{kernel.RecordFolder(inputs.RecordsRoot, spec.Application, spec.Environment),
			kernel.InstanceFolder(spec.Application, spec.Environment, profile)},
	}
	target.Scope.Files = append(target.Scope.Files, inputs.SharedPaths...)
	return target, nil
}
