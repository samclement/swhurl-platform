package kubetest

import (
	"bytes"
	"context"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
)

// SigningKeys answers Signing, as a copy, unless the scenario fails it ("signing-keys").
func (f *Fake) SigningKeys(context.Context) (kube.SigningKeys, error) {
	f.Calls = append(f.Calls, "signing-keys")
	if err := f.failing("signing-keys"); err != nil {
		return kube.SigningKeys{}, err
	}
	return kube.SigningKeys{Issuer: f.Signing.Issuer, Keys: bytes.Clone(f.Signing.Keys)}, nil
}

// IssuerContract is the behaviour every kube.Issuer must have: the issuer and the key set come back as the cluster
// serves them, and the caller owns the bytes it is given.
func IssuerContract(t *testing.T, want kube.SigningKeys, open func(*testing.T, kube.SigningKeys) kube.Issuer) {
	t.Helper()
	issuer := open(t, want)
	expected := bytes.Clone(want.Keys)
	found, err := issuer.SigningKeys(context.Background())
	if err != nil || found.Issuer != want.Issuer || !bytes.Equal(found.Keys, expected) {
		t.Fatalf("signing keys: %+v, %v", found, err)
	}
	if len(found.Keys) > 0 {
		found.Keys[0] ^= 0xff
	}
	again, err := issuer.SigningKeys(context.Background())
	if err != nil || !bytes.Equal(again.Keys, expected) {
		t.Fatalf("a caller changed a later answer: %v", err)
	}
}
