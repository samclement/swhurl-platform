package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// PromoteInputs is one promotion's documents: the two instances v2 owns — staging, whose release moves, and
// production, whose own facts stay — the app record, the platform profile, the two optional documents, and the
// revision of production the caller reviewed.
type PromoteInputs struct {
	StagingInstance, StagingRelease []byte
	Instance, Release, App, Profile []byte
	Calls, Egress                   []byte
	Expected                        kernel.InstanceRevision
}

// Promotion is what one promotion produced: both releases, the selected instance, the compilation and the files.
// Compilation is zero for a promotion that changed nothing.
type Promotion struct {
	Staging     kernel.ReleaseBundle
	Before      kernel.ReleaseBundle
	Release     kernel.ReleaseBundle
	Instance    kernel.InstanceSpec
	Compilation kernel.Compilation
	Changed     bool
	Written     []files.Outcome
}

// promotionRequest is one promotion's decoded inputs: the two instances, the release staging selects, production's
// current release, the app record, the profile and the two documents reduced to production's own facts.
type promotionRequest struct {
	staging, spec      kernel.InstanceSpec
	candidate, current kernel.ReleaseBundle
	app                kernel.AppSpec
	profile            kernel.PlatformProfile
	connections        kernel.Connections
	egress             *kernel.Egress
}

func newPromotionRequest(inputs PromoteInputs) (promotionRequest, error) {
	request := promotionRequest{}
	var err error
	if request.staging, err = kernel.DecodeInstance(inputs.StagingInstance); err != nil {
		return promotionRequest{}, err
	}
	if request.candidate, err = kernel.DecodeRelease(inputs.StagingRelease); err != nil {
		return promotionRequest{}, err
	}
	if request.spec, err = kernel.DecodeInstance(inputs.Instance); err != nil {
		return promotionRequest{}, err
	}
	if request.current, err = kernel.DecodeRelease(inputs.Release); err != nil {
		return promotionRequest{}, err
	}
	if request.app, err = kernel.DecodeApp(inputs.App); err != nil {
		return promotionRequest{}, err
	}
	if request.profile, err = kernel.DecodeProfile(inputs.Profile); err != nil {
		return promotionRequest{}, err
	}
	request.connections, request.egress, err = documents(Inputs{Calls: inputs.Calls, Egress: inputs.Egress},
		request.spec.Application, request.spec.Environment)
	return request, err
}

// Promote replaces production's release with the one staging selects, and nothing else: production's
// configuration, grants, calls, storage and broker identity are its own and stay, and its files are compiled from
// those. The release already selected is a no-op that writes nothing and compiles the pair it kept, so a promotion
// never leaves production unexplained.
func Promote(inputs PromoteInputs, destination string, writer files.Writer) (Promotion, error) {
	request, err := newPromotionRequest(inputs)
	if err != nil {
		return Promotion{}, err
	}
	result := Promotion{Staging: request.candidate, Before: request.current, Release: request.current}
	selected, err := kernel.SelectPromotion(request.staging, request.spec, request.current, request.candidate,
		inputs.Expected)
	if err != nil {
		return result, err
	}
	result.Instance = selected
	if selected.Release == request.spec.Release {
		_, err := kernel.CompileWith(selected, request.current, request.app, request.profile,
			request.connections, request.egress)
		return result, err
	}
	compiled, err := kernel.CompileWith(selected, request.candidate, request.app, request.profile,
		request.connections, request.egress)
	if err != nil {
		return result, err
	}
	documents, err := instanceDocuments(selected, request.candidate, compiled)
	if err != nil {
		return result, err
	}
	result.Compilation, result.Release, result.Changed = compiled, request.candidate, true
	result.Written, err = writer.Write(destination, documents)
	return result, err
}
