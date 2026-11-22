package provisioning

import (
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// EncryptedSeed constructs one instance's seed Secret, encrypts it to the platform's recipient and checks the
// ciphertext without decrypting it. A missing recipient is refused before encryption; no plaintext or unchecked
// ciphertext is returned, even on failure. The caller writes the result before the record that states the key.
func EncryptedSeed(spec kernel.InstanceSpec, pair kernel.UserKey, profile kernel.PlatformProfile,
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
