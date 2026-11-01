package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// MintInputs is one mint's inputs: the instance record, the profile that states the platform's recipient, the
// seed — which the caller obtains, because randomness is an effect outside the model — and whether replacing an
// existing identity, a rotation, was asked for.
type MintInputs struct {
	Instance, Profile []byte
	Seed              [32]byte
	Rotate            bool
}

// MintOutcome is what a mint produced: the instance, the public key its record now states, the key it replaced
// and every file it wrote. The seed is not here: it leaves this use case only inside the ciphertext.
type MintOutcome struct {
	Application, Environment string
	Public, Previous         string
	Written                  []files.Outcome
}

// Mint gives one instance a broker identity: it derives the pair from the supplied seed, encrypts the Secret that
// holds the seed to the platform's recipient, and writes the seed beside the instance's record, then the record
// that states the public key. The seed is written first so a failure leaves a record that states no key rather
// than a key with no seed — a state `compile` refuses rather than delivers.
func Mint(inputs MintInputs, destination string, writer files.Writer, encrypter sops.Encrypter) (MintOutcome, error) {
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		return MintOutcome{}, err
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return MintOutcome{}, err
	}
	pair := kernel.NewUserKey(inputs.Seed)
	previous := spec.Events.Key
	updated, err := kernel.WithBrokerKey(spec, pair, inputs.Rotate)
	if err != nil {
		return MintOutcome{}, err
	}
	encrypted, err := encryptedSeed(updated, pair, profile, encrypter)
	if err != nil {
		return MintOutcome{}, err
	}
	record, err := kernel.EncodeInstance(updated)
	if err != nil {
		return MintOutcome{}, err
	}
	folder := recordsRoot + "/" + updated.Application + "/" + updated.Environment
	written, err := writer.Write(destination, []kernel.File{
		{Path: folder + "/" + kernel.EventsSecretFile, Document: encrypted},
		{Path: folder + "/instance.json", Document: record},
	})
	outcome := MintOutcome{Application: updated.Application, Environment: updated.Environment,
		Public: pair.Public, Previous: previous, Written: written}
	return outcome, err
}

// encryptedSeed is one instance's seed Secret, encrypted to the platform's recipient and checked as far as v2 can
// check a ciphertext it never decrypts. A mint and a create both write it before the record that states the key, so a
// failure leaves a record that states no identity rather than one with no seed.
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
