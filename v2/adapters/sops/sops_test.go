package sops_test

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
)

// The platform's public age recipient: what every Secret in this repository is encrypted to.
const recipient = "age1u79dp6zjkal33l7rguzqxy66rwykhq90msrw47tf0vy4mu2jh3hsrkmn7h"

func plaintext(t *testing.T) []byte {
	t.Helper()
	document, err := os.ReadFile("testdata/secret.json")
	if err != nil {
		t.Fatal(err)
	}
	return document
}

// The real adapter runs the contract on the raw fixture: the plaintext goes in, ciphertext comes out.
func TestContract(t *testing.T) {
	if _, err := exec.LookPath("sops"); err != nil {
		if os.Getenv("REQUIRE_SOPS") == "1" {
			t.Fatalf("sops is required here: %v", err)
		}
		t.Skip("sops is not installed: the real encryption is exercised where it is (REQUIRE_SOPS=1 requires it)")
	}
	sopstest.Contract(t, func(*testing.T) sops.Encrypter { return sops.New() }, plaintext(t), recipient)
}

// Encrypting twice is not the same bytes: SOPS draws a fresh data key each time, so two minted seeds of the same
// document do not share a ciphertext that could be compared.
func TestEncryptDrawsAFreshDataKey(t *testing.T) {
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops is not installed")
	}
	first, err := sops.New().Encrypt(plaintext(t), recipient)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sops.New().Encrypt(plaintext(t), recipient)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two encryptions of one document are byte-identical")
	}
}

// A binary that is not there, and one that fails, are refusals with fixed reasons and no output.
func TestMissingOrFailingCommand(t *testing.T) {
	if _, err := (&sops.Command{Binary: "sops-that-is-not-installed"}).Encrypt(plaintext(t), recipient); !refused(t, err, "sops_not_available") {
		t.Fatalf("a missing binary: %v", err)
	}
	if _, err := (&sops.Command{Binary: "false"}).Encrypt(plaintext(t), recipient); !refused(t, err, "sops_exit_1") {
		t.Fatalf("a failing command: %v", err)
	}
}

// The encrypted fixture is what this adapter's output looks like: one encrypted value, the recipient, and the
// plaintext nowhere in it.
func TestEncryptedFixtureShape(t *testing.T) {
	encrypted, err := os.ReadFile("testdata/encrypted.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ENC[AES256_GCM", "recipient: " + recipient, "sops:"} {
		if !bytes.Contains(encrypted, []byte(want)) {
			t.Errorf("the fixture lacks %q", want)
		}
	}
	if bytes.Contains(encrypted, []byte("never-a-real-seed")) {
		t.Fatal("the fixture holds the plaintext")
	}
}

// refused reports whether an error is the adapter's refusal with that reason.
func refused(t *testing.T, err error, reason string) bool {
	t.Helper()
	refusal, ok := err.(*sops.Error)
	return ok && refusal.Reason == reason
}
