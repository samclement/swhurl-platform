// Package sopstest is the fake for sops.Encrypter and the contract suite both implementations run.
package sopstest

import (
	"bytes"
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/sops"
)

// Call is one encryption a fake was asked to perform, with the plaintext it was given.
type Call struct {
	Document  []byte
	Recipient string
}

// Fake records what it was asked to encrypt and returns a configured answer. It never runs a command, so a use
// case's tests do not need sops installed; the contract suite is what keeps its behaviour honest.
type Fake struct {
	Output []byte
	Err    error
	Calls  []Call
}

// New returns an empty fake whose output looks encrypted and holds no plaintext.
func New() *Fake {
	return &Fake{Output: []byte("apiVersion: v1\nkind: Secret\nstringData:\n  SEED: ENC[fake]\nsops:\n  version: 3.12.1\n")}
}

var _ sops.Encrypter = (*Fake)(nil)

// Encrypt records the call and returns the configured answer. An empty recipient is refused exactly as the
// real adapter refuses it.
func (f *Fake) Encrypt(document []byte, recipient string) ([]byte, error) {
	f.Calls = append(f.Calls, Call{Document: bytes.Clone(document), Recipient: recipient})
	if recipient == "" {
		return nil, &sops.Error{Reason: "no_recipient"}
	}
	if f.Err != nil {
		return nil, f.Err
	}
	if f.Output == nil {
		return nil, &sops.Error{Reason: "empty_output"}
	}
	return bytes.Clone(f.Output), nil
}

// Contract is the behaviour every sops.Encrypter must have: the plaintext goes in, ciphertext comes out, and it
// holds neither the document nor an empty recipient. The real adapter runs it on the raw fixtures in
// sops/testdata and the fake runs it here, so the fake cannot drift from the adapter.
func Contract(t *testing.T, open func(*testing.T) sops.Encrypter, document []byte, recipient string) {
	t.Helper()
	encrypter := open(t)
	out, err := encrypter.Encrypt(document, recipient)
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	switch {
	case len(out) == 0:
		t.Fatal("an empty ciphertext")
	case bytes.Equal(out, document):
		t.Fatal("the ciphertext is the plaintext")
	case !bytes.Contains(out, []byte("ENC[")):
		t.Fatalf("no SOPS-encrypted value:\n%s", out)
	case bytes.Contains(out, document):
		t.Fatal("the ciphertext holds the whole plaintext")
	}
	_, err = encrypter.Encrypt(document, "")
	var refusal *sops.Error
	if !errors.As(err, &refusal) || refusal.Reason != "no_recipient" {
		t.Fatalf("an empty recipient = %v, want refused_encryption: no_recipient", err)
	}
}
