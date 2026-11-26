package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/provisioning"
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
	encrypted, err := provisioning.EncryptedSeed(updated, pair, profile, encrypter)
	if err != nil {
		return MintOutcome{}, err
	}
	record, err := kernel.EncodeInstance(updated)
	if err != nil {
		return MintOutcome{}, err
	}
	folder := kernel.RecordFolder(kernel.RecordsRoot, updated.Application, updated.Environment)
	written, err := writer.Write(destination, []kernel.File{
		{Path: folder + "/" + kernel.EventsSecretFile, Document: encrypted},
		{Path: kernel.InstanceRecordPath(kernel.RecordsRoot, updated.Application, updated.Environment), Document: record},
	})
	outcome := MintOutcome{Application: updated.Application, Environment: updated.Environment,
		Public: pair.Public, Previous: previous, Written: written}
	return outcome, err
}
