package sops_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// The real adapter decrypts what it encrypted, with throwaway identities made for the test: the platform's own key
// is never read by a test.
func TestDecryptContract(t *testing.T) {
	if _, err := exec.LookPath("sops"); err != nil {
		if os.Getenv("REQUIRE_SOPS") == "1" {
			t.Fatalf("sops is required here: %v", err)
		}
		t.Skip("sops is not installed (REQUIRE_SOPS=1 requires it)")
	}
	dir := t.TempDir()
	key, public := identity(t, dir, "key")
	wrong, _ := identity(t, dir, "wrong")
	encrypted, err := sops.New().Encrypt(plaintext(t), public)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secret.sops.yaml")
	if err := os.WriteFile(path, encrypted, 0o600); err != nil {
		t.Fatal(err)
	}
	sopstest.DecryptContract(t, sops.New(), path, key, wrong, "never-a-real-seed")
}

// identity writes a new age X25519 identity under dir, as age-keygen would, and returns its path and recipient.
func identity(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	secret := strings.ToUpper(bech32(t, "age-secret-key-", private.Bytes()))
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, bech32(t, "age", private.PublicKey().Bytes())
}

// bech32 is BIP 173's encoding of data under hrp, which age writes its keys in.
func bech32(t *testing.T, hrp string, data []byte) string {
	t.Helper()
	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	var groups []byte // 8-bit bytes regrouped into 5-bit values
	accumulator, bits := 0, 0
	for _, b := range data {
		accumulator, bits = accumulator<<8|int(b), bits+8
		for bits >= 5 {
			bits -= 5
			groups = append(groups, byte(accumulator>>bits&31))
		}
	}
	if bits > 0 {
		groups = append(groups, byte(accumulator<<(5-bits)&31))
	}
	var values []byte
	for _, c := range hrp {
		values = append(values, byte(c>>5))
	}
	values = append(values, 0)
	for _, c := range hrp {
		values = append(values, byte(c&31))
	}
	values = append(append(values, groups...), 0, 0, 0, 0, 0, 0)
	checksum := 1
	for _, v := range values {
		top := checksum >> 25
		checksum = (checksum&0x1ffffff)<<5 ^ int(v)
		for i, generator := range []int{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3} {
			if top>>i&1 == 1 {
				checksum ^= generator
			}
		}
	}
	checksum ^= 1
	out := []byte(hrp + "1")
	for _, g := range groups {
		out = append(out, charset[g])
	}
	for i := 0; i < 6; i++ {
		out = append(out, charset[checksum>>(5*(5-i))&31])
	}
	return string(out)
}

func TestDecryptMissingOrFailingCommand(t *testing.T) {
	_, err := (&sops.Command{Binary: "sops-that-is-not-installed"}).Decrypt("a.sops.yaml", "key")
	if refusal, ok := err.(*sops.DecryptError); !ok || refusal.Reason != "sops_not_available" {
		t.Fatalf("a missing binary: %v", err)
	}
	_, err = (&sops.Command{Binary: "false"}).Decrypt("a.sops.yaml", "key")
	if refusal, ok := err.(*sops.DecryptError); !ok || refusal.Reason != "sops_exit_1" {
		t.Fatalf("a failing command: %v", err)
	}
	_, err = (&sops.Command{Binary: "true"}).Decrypt("a.sops.yaml", "key")
	if refusal, ok := err.(*sops.DecryptError); !ok || refusal.Reason != "empty_output" {
		t.Fatalf("an empty answer: %v", err)
	}
}
