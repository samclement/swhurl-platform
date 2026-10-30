package orchestration

import (
	"context"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// DeployInputs is one staging instance's delivery inputs: its records, the app's record (its team), the
// platform profile, the two optional documents, and the instance revision the caller reviewed.
type DeployInputs struct {
	Instance, Release, App, Profile []byte
	Calls, Egress                   []byte
	Expected                        kernel.InstanceRevision
}

// Deploy is what one delivery produced: the newest tag, the release it became, the selected instance, the
// compilation and every file it wrote. Changed is false when the newest image is the one already selected, so
// nothing was written.
type Deploy struct {
	Tag         string
	Release     kernel.ReleaseBundle
	Instance    kernel.InstanceSpec
	Compilation kernel.Compilation
	Written     []files.Outcome
	Changed     bool
}

// deployRequest is one delivery's decoded inputs: what it needs to compare, refuse, select and render.
type deployRequest struct {
	instance    kernel.InstanceSpec
	current     kernel.ReleaseBundle
	app         kernel.AppSpec
	profile     kernel.PlatformProfile
	connections kernel.Connections
	egress      *kernel.Egress
	expected    kernel.InstanceRevision
}

// newDeployRequest decodes the documents a delivery reads and the connections it renders. Every refusal here
// is the kernel's own: a malformed record, a profile outside this phase, a resolution naming another instance.
func newDeployRequest(inputs DeployInputs) (deployRequest, error) {
	request := deployRequest{expected: inputs.Expected}
	var err error
	if request.instance, err = kernel.DecodeInstance(inputs.Instance); err != nil {
		return deployRequest{}, err
	}
	if request.current, err = kernel.DecodeRelease(inputs.Release); err != nil {
		return deployRequest{}, err
	}
	if request.app, err = kernel.DecodeApp(inputs.App); err != nil {
		return deployRequest{}, err
	}
	if request.profile, err = kernel.DecodeProfile(inputs.Profile); err != nil {
		return deployRequest{}, err
	}
	request.connections, request.egress, err = documents(inputs.compilation(),
		request.instance.Application, request.instance.Environment)
	return request, err
}

// DeployImage brings one staging instance to the newest image its repository publishes. It resolves the tag
// with the platform's policy, reads the image's own declaration, refuses what the instance cannot take,
// selects the release against the revision the caller reviewed, and writes the record pair and the compiled
// files in one effect. A failed or refused read writes nothing and leaves the running image selected.
func DeployImage(ctx context.Context, inputs DeployInputs, images registry.Reader, destination string,
	writer files.Writer) (Deploy, error) {
	request, err := newDeployRequest(inputs)
	if err != nil {
		return Deploy{}, err
	}
	tags, err := images.Tags(ctx)
	if err != nil {
		return Deploy{}, err
	}
	tag, found := kernel.NewestTag(tags)
	if !found {
		return Deploy{}, &Refusal{"image", "no_tag_matches"}
	}
	published, err := images.Describe(ctx, tag.Name)
	if err != nil {
		return Deploy{}, err
	}
	if published.Digest == request.current.Artifact.Digest {
		return request.unchanged(tag.Name)
	}
	candidate, err := request.candidate(tag, published)
	if err != nil {
		return Deploy{}, err
	}
	selected, err := kernel.SelectRelease(request.instance, candidate, request.expected)
	if err != nil {
		return Deploy{}, err
	}
	compiled, err := request.compile(selected, candidate)
	if err != nil {
		return Deploy{}, err
	}
	documents, err := deployDocuments(selected, candidate, compiled)
	if err != nil {
		return Deploy{}, err
	}
	outcomes, err := writer.Write(destination, documents)
	return Deploy{Tag: tag.Name, Release: candidate, Instance: selected, Compilation: compiled,
		Written: outcomes, Changed: true}, err
}

// unchanged is the newest image already being selected: the request is checked as any selection would be, and
// nothing is written. Compiling the current pair is the check that the instance still selects it.
func (r deployRequest) unchanged(tag string) (Deploy, error) {
	if _, err := kernel.SelectRelease(r.instance, r.current, r.expected); err != nil {
		return Deploy{}, err
	}
	if _, err := kernel.CompileWith(r.instance, r.current, r.app, r.profile, r.connections, r.egress); err != nil {
		return Deploy{}, err
	}
	return Deploy{Tag: tag, Release: r.current, Instance: r.instance, Changed: false}, nil
}

// candidate is the release the newest image states, after the instance refuses what it cannot take. The release
// states the commit the image says it was built from, checked against the tag that published it.
func (r deployRequest) candidate(tag kernel.ImageTag, published registry.Image) (kernel.ReleaseBundle, error) {
	declaration, err := declarationOf(published)
	if err != nil {
		return kernel.ReleaseBundle{}, err
	}
	commit, err := revisionOf(tag, published)
	if err != nil {
		return kernel.ReleaseBundle{}, err
	}
	if err := kernel.AcceptDeclaration(declaration, r.instance, r.current); err != nil {
		return kernel.ReleaseBundle{}, err
	}
	return declaration.Release(r.instance.Application, r.current.Source.Repository, commit,
		r.current.Artifact.Image, published.Digest, published.MediaType)
}

// revisionOf reads the commit an image states it was built from. A tag alone never supplies it: the platform's tag
// carries an abbreviation, and the release states a full commit.
func revisionOf(tag kernel.ImageTag, published registry.Image) (string, error) {
	revision := published.Labels[kernel.RevisionLabel]
	if revision == "" {
		return "", &Refusal{"source", "revision_absent"}
	}
	return kernel.RevisionOf(tag, revision)
}

// compile renders the selected pair with the platform's documents.
func (r deployRequest) compile(selected kernel.InstanceSpec,
	release kernel.ReleaseBundle) (kernel.Compilation, error) {
	return kernel.CompileWith(selected, release, r.app, r.profile, r.connections, r.egress)
}

// compilation turns the delivery's inputs into the compilation use case's own, so the two decode the same
// documents the same way.
func (d DeployInputs) compilation() Inputs {
	return Inputs{Instance: d.Instance, Release: d.Release, App: d.App, Profile: d.Profile, Calls: d.Calls, Egress: d.Egress}
}

// declarationOf reads the image's declaration label. An image without one is refused rather than guessed:
// the platform cannot apply facts the build did not state.
func declarationOf(published registry.Image) (kernel.Declaration, error) {
	value, found := published.Labels[kernel.DeclarationLabel]
	if !found || value == "" {
		return kernel.Declaration{}, &Refusal{"declaration", "absent"}
	}
	return kernel.DecodeDeclaration([]byte(value))
}

// deployDocuments is what a delivery writes: the release first, then the instance that selects it, then the
// compiled files. A failure leaves at worst a release nothing selects.
func deployDocuments(spec kernel.InstanceSpec, release kernel.ReleaseBundle,
	compiled kernel.Compilation) ([]kernel.File, error) {
	releaseDocument, err := kernel.EncodeRelease(release)
	if err != nil {
		return nil, err
	}
	instanceDocument, err := kernel.EncodeInstance(spec)
	if err != nil {
		return nil, err
	}
	folder := recordsRoot + "/" + spec.Application + "/" + spec.Environment
	return append([]kernel.File{
		{Path: folder + "/release.json", Document: releaseDocument},
		{Path: folder + "/instance.json", Document: instanceDocument},
	}, compiled.Files...), nil
}
