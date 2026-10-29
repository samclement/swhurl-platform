package orchestration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
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
	if err == nil || !strings.Contains(err.Error(), "the_profile_states_no_recipient") {
		t.Fatalf("no recipient: %v", err)
	}
	if len(writer.Contents) != 0 || len(encrypter.Calls) != 0 {
		t.Fatal("a refused mint generated or wrote something")
	}
}

// An encrypter that hands back the plaintext is refused, so a broken adapter cannot put a seed in Git.
func TestMintRefusesAPlaintextCiphertext(t *testing.T) {
	inputs := mintInputs(t, 1, false)
	encrypter := sopstest.New()
	encrypter.Output = []byte("stringData: {SWHURL_EVENTS_SEED: " + kernel.NewUserKey(inputs.Seed).Seed + "}")
	_, err := Mint(inputs, t.TempDir(), filestest.New(), encrypter)
	if err == nil || !strings.Contains(err.Error(), "plaintext seed") {
		t.Fatalf("a plaintext ciphertext: %v", err)
	}
}

// errorString is an error's text, or "" for nil, so an assertion can name what a refusal said.
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
