package natstest

import (
	"context"
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
)

// TestFake is the fake held to the contract, so it cannot answer differently from the server the real adapter is
// tested against.
func TestFake(t *testing.T) { Contract(t, func(*testing.T) nats.KV { return New() }) }

// A store that failed is a store that answered nothing: absence is a message the bucket does not hold.
func TestAFailedStoreIsNotAnEmptyOne(t *testing.T) {
	store := New()
	store.Fail = errors.New("NATS did not answer in time")
	if _, err := store.Get(context.Background(), Stream, StoredKey); err == nil || errors.Is(err, nats.ErrAbsent) {
		t.Fatalf("a failed store answered %v", err)
	}
	if _, err := store.Keys(context.Background(), Stream, Prefix); err == nil {
		t.Fatal("a failed store listed its keys")
	}
	if err := store.Put(context.Background(), StoredKey, []byte("true"), nil); err == nil {
		t.Fatal("a failed store took a write")
	}
}
