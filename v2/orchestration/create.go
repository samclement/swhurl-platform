package orchestration

import (
	"context"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// CreateInputs is what a create takes: the app and its team, the image reference the operator named, the offered facts
// the image does not state, the documents it publishes, the platform profile, the records root the pair is written
// under, the instance's own choices, and the seed the caller obtained for its broker identity.
type CreateInputs struct {
	Application, Team    string
	Image                string // REPO:<run>-<commit>, as the app's repository publishes it
	Description, APIPath string
	Offers               []kernel.File
	Profile              []byte
	// RecordsRoot is the tree the records are written under, as the destination addresses it. A create states its own
	// records; the compiled files and the unit are the whole tree's, which the next render decides.
	RecordsRoot           string
	MemoryMiB, StorageMiB int32
	Exposure              string
	Seed                  [32]byte
}

// Created is what a create produced: the release its image publishes, the pair it wrote, the public half of the broker
// identity and every path written, in the order they were written.
type Created struct {
	Tag      string
	Release  kernel.ReleaseBundle
	Instance kernel.InstanceSpec
	App      kernel.AppSpec
	Public   string
	Written  []files.Outcome
}

// Create writes the records a new staging instance is, from what its image publishes: the image resolves through the
// registry adapter a delivery uses, states its own declaration and the commit it was built from, and the pair is the
// kernel's creation. The seed is encrypted and written before the instance record that states its public key.
func Create(ctx context.Context, inputs CreateInputs, images func(image string) (registry.Reader, error),
	destination string, writer files.Writer, encrypter sops.Encrypter) (Created, error) {
	published, err := publishedFor(ctx, inputs, images)
	if err != nil {
		return Created{}, err
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return Created{}, err
	}
	pair := kernel.NewUserKey(inputs.Seed)
	creation := kernel.Creation{Application: inputs.Application, Team: inputs.Team, Release: published.release,
		Declaration: published.declaration, MemoryMiB: inputs.MemoryMiB, StorageMiB: inputs.StorageMiB,
		Exposure: inputs.Exposure, Key: pair.Public}
	spec, app, err := creation.Instance()
	if err != nil {
		return Created{}, err
	}
	secret, err := encryptedSeed(spec, pair, profile, encrypter)
	if err != nil {
		return Created{}, err
	}
	records, err := createdRecords(inputs, spec, app, published.release, secret)
	if err != nil {
		return Created{}, err
	}
	written, err := writer.Write(destination, records)
	return Created{Tag: published.tag.Name, Release: published.release, Instance: spec, App: app, Public: pair.Public,
		Written: written}, err
}

// publishedImage is what the registry and the tag state about the image a create starts from: the release it publishes,
// the declaration that release is, and the tag the platform's policy ranks it with.
type publishedImage struct {
	tag         kernel.ImageTag
	release     kernel.ReleaseBundle
	declaration kernel.Declaration
}

// publishedFor resolves one image reference into the release it publishes. The source repository is the image's own
// path, which is the platform's convention for an app image, so nothing else names it; the documents' provenance is the
// commit the image was built from, because that is the commit they were read at.
func publishedFor(ctx context.Context, inputs CreateInputs,
	images func(image string) (registry.Reader, error)) (publishedImage, error) {
	image, tag, err := kernel.SplitImage(inputs.Image)
	if err != nil {
		return publishedImage{}, err
	}
	reader, err := images(image)
	if err != nil {
		return publishedImage{}, err
	}
	published, err := reader.Describe(ctx, tag.Name)
	if err != nil {
		return publishedImage{}, err
	}
	declaration, err := declarationOf(published)
	if err != nil {
		return publishedImage{}, err
	}
	commit, err := revisionOf(tag, published)
	if err != nil {
		return publishedImage{}, err
	}
	repository := kernel.ImageRepository(image)
	if repository == "" {
		return publishedImage{}, &Refusal{"artifact.image", "the_image_names_no_app_repository"}
	}
	release, err := declaration.Release(inputs.Application, repository, commit, image, published.Digest,
		published.MediaType)
	if err != nil {
		return publishedImage{}, err
	}
	offers := ""
	if len(inputs.Offers) > 0 {
		offers = commit
	}
	release, err = release.WithOffered(inputs.Description, inputs.APIPath, offers)
	return publishedImage{tag: tag, release: release, declaration: declaration}, err
}

// createdRecords is the files a create writes: the app record and the environment's pair, the encrypted seed its
// instance signs in with, and the documents it publishes.
func createdRecords(inputs CreateInputs, spec kernel.InstanceSpec, app kernel.AppSpec, release kernel.ReleaseBundle,
	secret []byte) ([]kernel.File, error) {
	appDocument, err := kernel.EncodeApp(app)
	if err != nil {
		return nil, err
	}
	releaseDocument, err := kernel.EncodeRelease(release)
	if err != nil {
		return nil, err
	}
	instanceDocument, err := kernel.EncodeInstance(spec)
	if err != nil {
		return nil, err
	}
	return recordFiles(inputs.RecordsRoot, spec.Application, spec.Environment, appDocument, releaseDocument,
		instanceDocument, secret, inputs.Offers), nil
}
