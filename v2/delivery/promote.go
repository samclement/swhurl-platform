package delivery

import (
	"fmt"
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/delivery/rules"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/provisioning"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// PromoteInputs is one promotion's documents: the two instances v2 owns — staging, whose release moves, and
// production, whose own facts stay — the app record, the platform profile, the two optional documents, the documents
// staging offers (which production inherits with the release), and the revision of production the caller reviewed.
type PromoteInputs struct {
	StagingInstance, StagingRelease []byte
	Instance, Release, App, Profile []byte
	Calls, Egress                   []byte
	Offers                          []kernel.File
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
	request.connections, request.egress, err = provisioningrules.Documents(inputs.Calls, inputs.Egress, request.spec.Application, request.spec.Environment)
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
	selected, err := rules.SelectPromotion(request.staging, request.spec, request.current, request.candidate,
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
	documents, err := provisioning.InstanceDocuments(selected, request.candidate, compiled, inputs.Offers)
	if err != nil {
		return result, err
	}
	result.Compilation, result.Release, result.Changed = compiled, request.candidate, true
	result.Written, err = provisioning.WriteCompiled(destination, compiled, documents, writer)
	return result, err
}

// PromoteIn promotes one app's instance in the checkout to the release its staging selects, if the instance's record
// is still the expected revision: the two instances' records, the staging documents production inherits, and the
// documents the checkout names.
func PromoteIn(checkout files.Checkout, application, environment string, expected kernel.InstanceRevision,
	writer files.Writer) (Promotion, error) {
	inputs, err := promotionIn(checkout, application, environment)
	if err != nil {
		return Promotion{}, err
	}
	inputs.Expected = expected
	return Promote(inputs, checkout.Root, writer)
}

// promotionIn reads one promotion's documents from the checkout, each instance checked to be the one its folder names.
func promotionIn(checkout files.Checkout, application, environment string) (PromoteInputs, error) {
	inputs := PromoteInputs{}
	var err error
	if inputs.Profile, err = files.Read(checkout.Profile, "profile"); err != nil {
		return PromoteInputs{}, err
	}
	if inputs.Calls, err = files.Optional(checkout.Calls, "calls"); err != nil {
		return PromoteInputs{}, err
	}
	if inputs.Egress, err = files.Optional(checkout.Egress, "egress"); err != nil {
		return PromoteInputs{}, err
	}
	records, err := checkout.Instances()
	if err != nil {
		return PromoteInputs{}, err
	}
	staging, err := promoted(records, application, deployEnvironment)
	if err != nil {
		return PromoteInputs{}, err
	}
	target, err := promoted(records, application, environment)
	if err != nil {
		return PromoteInputs{}, err
	}
	inputs.StagingInstance, inputs.StagingRelease = staging.Instance, staging.Release
	inputs.Instance, inputs.Release, inputs.App = target.Instance, target.Release, target.App
	inputs.Offers, err = files.ReadOffers(filepath.Join(checkout.Records, application, deployEnvironment,
		files.OffersFolder))
	return inputs, err
}

// promoted is one of a promotion's two instances, checked to hold what its folder names.
func promoted(records []files.InstanceRecords, application, environment string) (files.InstanceRecords, error) {
	record, err := files.Find(records, application, environment)
	if err != nil {
		return files.InstanceRecords{}, err
	}
	_, _, err = provisioningrules.FolderHolds(application, environment, record.Instance, record.Release)
	return record, err
}

// Summary is the promotion's one line: what production selects now, and what changed.
func (p Promotion) Summary() string {
	commit := p.Staging.Source.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if !p.Changed {
		return fmt.Sprintf("%s/%s: production already selects %s at %s", p.Instance.Application,
			p.Instance.Environment, p.Staging.Artifact.Digest, commit)
	}
	return fmt.Sprintf("%s/%s: %s -> %s at %s", p.Instance.Application, p.Instance.Environment,
		p.Before.Artifact.Digest, p.Staging.Artifact.Digest, commit)
}
