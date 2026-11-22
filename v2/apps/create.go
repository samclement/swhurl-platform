package apps

import (
	"context"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/provisioning"
)

// Refusal is a create's own refusal: it will not start, and nothing was written.
type Refusal struct{ Subject, Reason string }

func (r *Refusal) Error() string { return "refused: " + r.Subject + ": " + r.Reason }

// CreateInputs is what a create takes: the app and its team, the image reference the operator named, the platform
// profile, the records root the pair is written under, the instance's exposure, and the seed the caller obtained for
// its broker identity. Every other fact the instance starts with is the image's own declaration: what it needs and
// what it offers, documents included.
type CreateInputs struct {
	Application, Team string
	Image             string // REPO:<run>-<commit>, as the app's repository publishes it
	Profile           []byte
	// RecordsRoot is the tree the records are written under, as the destination addresses it. A create states its own
	// records; the compiled files and the unit are the whole tree's, which the next render decides.
	RecordsRoot string
	Exposure    string
	Seed        [32]byte
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
		Declaration: published.declaration, Exposure: inputs.Exposure, Key: pair.Public}
	spec, app, err := creation.Instance()
	if err != nil {
		return Created{}, err
	}
	secret, err := encryptedSeed(spec, pair, profile, encrypter)
	if err != nil {
		return Created{}, err
	}
	records, err := createdRecords(inputs, spec, app, published.release, published.declaration, secret)
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
// path, which is the platform's convention for an app image, so nothing else names it; what the app offers is part of
// the declaration, so the release states it and the documents travel with the digest.
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
	return publishedImage{tag: tag, release: release, declaration: declaration}, nil
}

// createdRecords is the files a create writes: the app record and the environment's pair, the encrypted seed its
// instance signs in with, and the documents the image states.
func createdRecords(inputs CreateInputs, spec kernel.InstanceSpec, app kernel.AppSpec, release kernel.ReleaseBundle,
	declaration kernel.Declaration, secret []byte) ([]kernel.File, error) {
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
	return provisioning.RecordFiles(inputs.RecordsRoot, spec.Application, spec.Environment, appDocument, releaseDocument,
		instanceDocument, secret, declaration.OfferedFiles()), nil
}

// encryptedSeed is the new instance's seed Secret, encrypted to the platform's recipient and checked as far as v2 can
// check a ciphertext it never decrypts. It is written before the record that states the key, so a failure leaves no
// record that states an identity without its seed. (Copied from orchestration/mint.go.)
func encryptedSeed(spec kernel.InstanceSpec, pair kernel.UserKey, profile kernel.PlatformProfile,
	encrypter sops.Encrypter) ([]byte, error) {
	if profile.SOPS.Recipient == "" {
		return nil, &Refusal{"sops", "the_profile_states_no_recipient"}
	}
	plaintext, err := kernel.EventsSecret(spec, pair)
	if err != nil {
		return nil, err
	}
	encrypted, err := encrypter.Encrypt(plaintext, profile.SOPS.Recipient)
	if err != nil {
		return nil, err
	}
	if err := kernel.CheckEncryptedSeed(encrypted, pair.Seed); err != nil {
		return nil, err
	}
	return encrypted, nil
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

// declarationOf reads the image's declaration label. An image without one is refused rather than guessed:
// the platform cannot apply facts the build did not state.
func declarationOf(published registry.Image) (kernel.Declaration, error) {
	value, found := published.Labels[kernel.DeclarationLabel]
	if !found || value == "" {
		return kernel.Declaration{}, &Refusal{"declaration", "absent"}
	}
	return kernel.DecodeDeclaration([]byte(value))
}
