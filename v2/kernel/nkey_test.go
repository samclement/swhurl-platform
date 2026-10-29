package kernel

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// The published vectors: RFC 8032's first three Ed25519 seeds, in NATS's text form. The expected strings were
// computed by v1's independent Python implementation (tools/swhurl/apps/nkeys.py), so this pins the encoding
// against a second implementation rather than against itself.
func TestNewUserKeyMatchesThePublishedVectors(t *testing.T) {
	for _, vector := range []struct{ seed, seedText, public string }{
		{"9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
			"SUAJ2YNRTXX72WTAXKCEV5ES5QWMIRCJYVUXWMTJDFYDXLADDSXH6YALCA",
			"UDLVVGABQKYQVN6VJP7NHSLEA45A5YLS6PNKMIZFV4BBU2HXA5IRUVAL"},
		{"4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
			"SUAEZTIITMUP7FW2TW3MGRXMCFHA6W4KGGPTLK5GETNIZ5XNJ64KN63OVI",
			"UA6UAF6D5BBYSWUSW4FKOTI3P26JZGBMZ4XMJFUMYDGVL4JK6RTAYUDN"},
		{"c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7",
			"SUAMLKUN6Q7Z7A335W3UILZR3S33CZWTQU2QO3YJJOC44OROBNCFR54NIM",
			"UD6FDTMOMIMKDI4NUR7NAARQ6BMAQFXNCO5DGA5MLXVZCFKISCACL4HR"},
	} {
		raw, err := hex.DecodeString(vector.seed)
		if err != nil || len(raw) != 32 {
			t.Fatalf("the vector's seed: %v", err)
		}
		pair := NewUserKey([32]byte(raw))
		if pair.Seed != vector.seedText || pair.Public != vector.public {
			t.Errorf("seed %s: got %s / %s, want %s / %s", vector.seed, pair.Seed, pair.Public,
				vector.seedText, vector.public)
		}
		if !nkeyPattern().MatchString(pair.Public) {
			t.Errorf("the public key is not what a record accepts: %s", pair.Public)
		}
	}
}

// The Secret holds the seed under the name the image reads, in the instance's own namespace, and nothing else: it
// states the public key nowhere, so a leaked record cannot be confused with a leaked seed.
func TestEventsSecret(t *testing.T) {
	instance := instanceSpec(t)
	var seed [32]byte
	copy(seed[:], "a seed that is never a credential")
	pair := NewUserKey(seed)
	document, err := EventsSecret(instance, pair)
	if err != nil {
		t.Fatal(err)
	}
	var secret struct {
		APIVersion string
		Kind       string
		Type       string
		Metadata   struct{ Name, Namespace string }
		StringData map[string]string
	}
	if err := json.Unmarshal(document, &secret); err != nil {
		t.Fatal(err)
	}
	if secret.APIVersion != "v1" || secret.Kind != "Secret" || secret.Type != "Opaque" ||
		secret.Metadata.Name != instance.Application+"-events" ||
		secret.Metadata.Namespace != instance.Application+"-"+instance.Environment {
		t.Fatalf("the object: %+v", secret)
	}
	if len(secret.StringData) != 1 || secret.StringData["SWHURL_EVENTS_SEED"] != pair.Seed {
		t.Fatalf("stringData: %v", secret.StringData)
	}
	if bytes.Contains(document, []byte(pair.Public)) {
		t.Fatal("the Secret states the public key")
	}
	_, err = EventsSecret(instance, UserKey{})
	refusal(t, err, Invalid, "events.secret")
}

// A key is created when there is none, replaced only when a rotation is asked for, and a rotation of nothing is a
// mistake.
func TestWithBrokerKey(t *testing.T) {
	instance := instanceSpec(t)
	var seed [32]byte
	pair := NewUserKey(seed)
	updated, err := WithBrokerKey(instance, pair, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Events.Key != pair.Public || instance.Events.Key != "" {
		t.Fatalf("the record: %+v, and the input changed: %+v", updated.Events, instance.Events)
	}
	_, err = WithBrokerKey(updated, pair, false)
	refusal(t, err, Unsupported, "events.key")
	rotated, err := WithBrokerKey(updated, pair, true)
	if err != nil || rotated.Events.Key != pair.Public {
		t.Fatalf("a rotation: %+v, %v", rotated.Events, err)
	}
	_, err = WithBrokerKey(instance, pair, true)
	refusal(t, err, Unsupported, "events.key")
}

// What v2 can say about a ciphertext it cannot decrypt: it must not hold the seed, and it must hold an encrypted
// value.
func TestCheckEncryptedSeed(t *testing.T) {
	seed := "SUAMLKUN6Q7Z7A335W3UILZR3S33CZWTQU2QO3YJJOC44OROBNCFR54NIM"
	if err := CheckEncryptedSeed([]byte("stringData:\n  SEED: ENC[AES256_GCM,data:abc]\n"), seed); err != nil {
		t.Fatal(err)
	}
	err := CheckEncryptedSeed([]byte("stringData:\n  SEED: "+seed+"\n"), seed)
	refusal(t, err, Invalid, "events.secret")
	err = CheckEncryptedSeed([]byte("stringData:\n  SEED: abc\n"), seed)
	refusal(t, err, Invalid, "events.secret")
}

// instanceSpec is a minimal instance record for the renderer tests.
func instanceSpec(t *testing.T) InstanceSpec {
	t.Helper()
	return InstanceSpec{SchemaVersion: 1, Application: "example", Environment: "staging",
		Configuration: Configuration{Replicas: 1, MemoryMiB: 128}}
}
