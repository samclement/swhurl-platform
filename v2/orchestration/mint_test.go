package orchestration

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/provisioning"
)

// mintInstance is one v2-owned instance with no broker identity, as its record states it.
func mintInstance(t *testing.T) []byte {
	t.Helper()
	record, err := kernel.EncodeInstance(kernel.InstanceSpec{SchemaVersion: 1, Application: "example",
		Environment: "staging", Release: kernel.ReleaseID("rel-sha256:" + strings.Repeat("a", 64)),
		Grants:        kernel.Grants{Exposure: "private"},
		Configuration: kernel.Configuration{Replicas: 1, MemoryMiB: 128}})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// mintProfile is the fixture profile, optionally without its SOPS recipient.
func mintProfile(t *testing.T, recipient bool) []byte {
	t.Helper()
	data, err := os.ReadFile("../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	if recipient {
		return data
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "sops")
	edited, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return edited
}

func mintInputs(t *testing.T, seed byte, rotate bool) MintInputs {
	t.Helper()
	var bytes [32]byte
	for index := range bytes {
		bytes[index] = seed
	}
	return MintInputs{Instance: mintInstance(t), Profile: mintProfile(t, true), Seed: bytes, Rotate: rotate}
}

// A mint writes the encrypted seed first and the record second, states the public key in the record, and hands
// the use case's plaintext — the seed under the image's variable — to the encrypter, never to the writer.
func TestMintWritesTheSeedThenTheRecord(t *testing.T) {
	writer, encrypter := filestest.New(), sopstest.New()
	outcome, err := Mint(mintInputs(t, 1, false), t.TempDir(), writer, encrypter)
	if err != nil {
		t.Fatal(err)
	}
	pair := kernel.NewUserKey(mintInputs(t, 1, false).Seed)
	if outcome.Application != "example" || outcome.Environment != "staging" || outcome.Public != pair.Public ||
		outcome.Previous != "" {
		t.Fatalf("the outcome: %+v", outcome)
	}
	if len(outcome.Written) != 2 ||
		outcome.Written[0].Path != "v2-records/example/staging/events.sops.yaml" ||
		outcome.Written[1].Path != "v2-records/example/staging/instance.json" {
		t.Fatalf("the order: %+v", outcome.Written)
	}
	record, found := writer.Contents["v2-records/example/staging/instance.json"]
	if !found {
		t.Fatal("the record was not written")
	}
	spec, err := kernel.DecodeInstance(record)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Events.Key != pair.Public {
		t.Fatalf("the record states %q, want %q", spec.Events.Key, pair.Public)
	}
	if len(encrypter.Calls) != 1 {
		t.Fatalf("encryptions: %d", len(encrypter.Calls))
	}
	if !strings.Contains(string(encrypter.Calls[0].Document), pair.Seed) ||
		!strings.Contains(string(encrypter.Calls[0].Document), "example-events") {
		t.Fatalf("what was encrypted: %s", encrypter.Calls[0].Document)
	}
	if encrypter.Calls[0].Recipient == "" {
		t.Fatal("the encrypter was given no recipient")
	}
	// The writer never sees the plaintext: the only seed-shaped bytes it holds are the encrypter's output.
	for path, content := range writer.Contents {
		if strings.Contains(string(content), pair.Seed) {
			t.Fatalf("%s holds the plaintext seed", path)
		}
	}
}

// A key that exists is replaced only when a rotation is asked for, and a rotation of nothing is refused.
func TestMintRefusesAnUnaskedRotation(t *testing.T) {
	writer := filestest.New()
	first, err := Mint(mintInputs(t, 1, false), t.TempDir(), writer, sopstest.New())
	if err != nil {
		t.Fatal(err)
	}
	inputs := mintInputs(t, 2, false)
	inputs.Instance = writer.Contents["v2-records/example/staging/instance.json"]
	_, err = Mint(inputs, t.TempDir(), filestest.New(), sopstest.New())
	refusal := errorString(err)
	if !strings.Contains(refusal, "unsupported: events.key") || !strings.Contains(refusal, "rotation") {
		t.Fatalf("a second mint without --rotate: %v", err)
	}
	inputs.Rotate = true
	rotated, err := Mint(inputs, t.TempDir(), filestest.New(), sopstest.New())
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Previous != first.Public || rotated.Public == first.Public {
		t.Fatalf("the rotation: %+v, after %s", rotated, first.Public)
	}
	none := mintInputs(t, 3, true)
	_, err = Mint(none, t.TempDir(), filestest.New(), sopstest.New())
	if !strings.Contains(errorString(err), "no broker identity to rotate") {
		t.Fatalf("a rotation of no key: %v", err)
	}
}

// A profile that states no recipient is refused before anything is generated or written.
func TestMintNeedsARecipient(t *testing.T) {
	writer, encrypter := filestest.New(), sopstest.New()
	inputs := mintInputs(t, 1, false)
	inputs.Profile = mintProfile(t, false)
	_, err := Mint(inputs, t.TempDir(), writer, encrypter)
	if err == nil || err.Error() != "refused: sops: the_profile_states_no_recipient" {
		t.Fatalf("no recipient: %v", err)
	}
	if _, ok := err.(*provisioning.Refusal); !ok {
		t.Fatalf("missing recipient must surface provisioning's refusal directly: %T %v", err, err)
	}
	if writer.Writes != 0 || len(encrypter.Calls) != 0 {
		t.Fatal("a refused mint generated or wrote something")
	}
}

// Encryption failures and unsafe ciphertext never reach the record writer.
func TestMintStopsBeforeWritingAnUnsafeSeed(t *testing.T) {
	inputs := mintInputs(t, 1, false)
	failure := errors.New("encryption failed")
	for _, tt := range []struct {
		name   string
		output []byte
		err    error
		reason string
	}{
		{name: "encryption failed", err: failure, reason: failure.Error()},
		{name: "plaintext seed even with encrypted values", output: []byte("ENC[fake] " + kernel.NewUserKey(inputs.Seed).Seed), reason: "plaintext seed"},
		{name: "no encrypted value", output: []byte("not encrypted"), reason: "no encrypted value"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writer, encrypter := filestest.New(), sopstest.New()
			encrypter.Output, encrypter.Err = tt.output, tt.err
			outcome, err := Mint(inputs, t.TempDir(), writer, encrypter)
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error = %v, want %q", err, tt.reason)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Fatalf("encryption error was not propagated: %v", err)
			}
			if len(encrypter.Calls) != 1 || writer.Writes != 0 || len(outcome.Written) != 0 {
				t.Fatal("an unsafe seed reached the writer or was not encrypted once")
			}
		})
	}
}

// errorString is an error's text, or "" for nil, so an assertion can name what a refusal said.
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
