package provisioning

import (
	"bytes"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Provisioning hands the instance's plaintext Secret only to the encrypter and returns its checked output unchanged.
func TestEncryptedSeedConstructsAndEncryptsTheSecret(t *testing.T) {
	spec := kernel.InstanceSpec{Application: "example", Environment: "staging"}
	pair := kernel.NewUserKey([32]byte{1})
	profile := kernel.PlatformProfile{SOPS: kernel.SOPS{Recipient: "age1recipient"}}
	encrypter := sopstest.New()
	ciphertext, err := EncryptedSeed(spec, pair, profile, encrypter)
	if err != nil {
		t.Fatal(err)
	}
	if len(encrypter.Calls) != 1 {
		t.Fatalf("encryptions: %d", len(encrypter.Calls))
	}
	call := encrypter.Calls[0]
	if call.Recipient != profile.SOPS.Recipient || !bytes.Contains(call.Document, []byte(pair.Seed)) ||
		!bytes.Contains(call.Document, []byte("SWHURL_EVENTS_SEED")) ||
		!bytes.Contains(call.Document, []byte("example-events")) {
		t.Fatal("the encrypter did not receive the instance's seed Secret and recipient")
	}
	if !bytes.Equal(ciphertext, encrypter.Output) {
		t.Fatal("the checked ciphertext was not returned unchanged")
	}
}

// Invalid Secret inputs fail before encryption, and return no document for a caller to write.
func TestEncryptedSeedValidatesBeforeEncrypting(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec kernel.InstanceSpec
		pair kernel.UserKey
	}{
		{name: "invalid instance", spec: kernel.InstanceSpec{Application: "../example", Environment: "staging"},
			pair: kernel.NewUserKey([32]byte{1})},
		{name: "missing key pair", spec: kernel.InstanceSpec{Application: "example", Environment: "staging"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			encrypter := sopstest.New()
			ciphertext, err := EncryptedSeed(tt.spec, tt.pair,
				kernel.PlatformProfile{SOPS: kernel.SOPS{Recipient: "age1recipient"}}, encrypter)
			if err == nil || ciphertext != nil || len(encrypter.Calls) != 0 {
				t.Fatal("invalid Secret inputs must fail without encryption or a returned document")
			}
		})
	}
}
