package sopstest_test

import (
	"bytes"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
)

func TestContract(t *testing.T) {
	sopstest.Contract(t, func(*testing.T) sops.Encrypter { return sopstest.New() },
		[]byte(`{"stringData":{"SWHURL_EVENTS_SEED":"never-a-real-seed"}}`),
		"age1u79dp6zjkal33l7rguzqxy66rwykhq90msrw47tf0vy4mu2jh3hsrkmn7h")
}

// The fake records the plaintext it was handed, so a use case's test can assert what was encrypted, and it can
// fail on purpose.
func TestFakeRecordsTheCallAndCanFail(t *testing.T) {
	f := sopstest.New()
	if _, err := f.Encrypt([]byte("document"), "age1recipient"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || string(f.Calls[0].Document) != "document" || f.Calls[0].Recipient != "age1recipient" {
		t.Fatalf("the call: %+v", f.Calls)
	}
	if bytes.Contains(f.Calls[0].Document, []byte("fake")) {
		t.Fatal("the recorded document is not the plaintext")
	}
	f.Err = &sops.Error{Reason: "sops_exit_1"}
	if _, err := f.Encrypt([]byte("document"), "age1recipient"); err == nil {
		t.Fatal("a configured failure was not returned")
	}
}
