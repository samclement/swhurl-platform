// Package natstest is the key-value store without a server: one in-memory bucket, and the behaviour every
// implementation of it must have.
package natstest

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
)

// The keys a contract run writes: one instance's value and its clear, under the bucket's own prefix.
const (
	Prefix     = "$KV.flags-staging.staging.demo-shop."
	StoredKey  = Prefix + "shout"
	ClearedKey = Prefix + "banner"
	OtherKey   = "$KV.flags-staging.staging.demo-orders.shout"
	Stream     = "KV_flags-staging"
)

// StoredAt is the time the fake gives a value it stored: a fixed stamp, so a test's rows do not move.
const StoredAt = "2026-11-09T09:30:00.123456789Z"

// Fake is one bucket in memory: the values a test's page reads, and the writes it made, in order.
type Fake struct {
	Entries map[string]nats.Message
	// Fail is returned by every call when it is set: a store that answers nothing is not a store that holds
	// nothing.
	Fail   error
	Closed bool
	Writes []Write
}

// Write is one Put: the subject, the bytes and the headers it came with.
type Write struct {
	Subject string
	Value   []byte
	Headers map[string]string
}

var _ nats.KV = (*Fake)(nil)

// New is an empty bucket.
func New() *Fake { return &Fake{Entries: map[string]nats.Message{}} }

// Keys is the subjects under a prefix, sorted, as the server lists them.
func (f *Fake) Keys(_ context.Context, _ string, prefix string) ([]string, error) {
	if err := f.state(); err != nil {
		return nil, err
	}
	var keys []string
	for key := range f.Entries {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Get is one stored value; a key the bucket does not hold is ErrAbsent.
func (f *Fake) Get(_ context.Context, _ string, key string) (nats.Message, error) {
	if err := f.state(); err != nil {
		return nats.Message{}, err
	}
	message, found := f.Entries[key]
	if !found {
		return nats.Message{}, nats.ErrAbsent
	}
	return message, nil
}

// Put stores one value: a clear is a value with the DEL operation header and no bytes, as the server keeps it.
func (f *Fake) Put(_ context.Context, subject string, value []byte, headers map[string]string) error {
	if err := f.state(); err != nil {
		return err
	}
	copied := make(map[string]string, len(headers))
	for name, header := range headers {
		copied[name] = header
	}
	f.Entries[subject] = nats.Message{Value: slices.Clone(value), Headers: copied, StoredAt: StoredAt}
	f.Writes = append(f.Writes, Write{Subject: subject, Value: slices.Clone(value), Headers: copied})
	return nil
}

// Close ends the bucket: a reader after it fails rather than finding nothing.
func (f *Fake) Close() error {
	f.Closed = true
	return nil
}

// state is what every call shares: a bucket that failed, or was closed, answers with an error.
func (f *Fake) state() error {
	if f.Fail != nil {
		return f.Fail
	}
	if f.Closed {
		return errors.New("the store is closed")
	}
	return nil
}

// Contract is the behaviour every store has: a value is written and read back with its headers and the server's
// time, a missing key is absence, a clear is a live message that states the DEL operation, the keys are the ones
// under the prefix, and a closed store refuses rather than holding nothing.
func Contract(t *testing.T, open func(*testing.T) nats.KV) {
	t.Helper()
	ctx := context.Background()
	store := open(t)
	t.Cleanup(func() { store.Close() })
	valuesRoundTrip(t, ctx, store)
	keysAreThePrefix(t, ctx, store)
	closedRefuses(t, open)
}

// valuesRoundTrip writes a value and a clear, and reads both back.
func valuesRoundTrip(t *testing.T, ctx context.Context, store nats.KV) {
	t.Helper()
	if _, err := store.Get(ctx, Stream, StoredKey); !errors.Is(err, nats.ErrAbsent) {
		t.Fatalf("an unwritten key: %v, want absence", err)
	}
	if err := store.Put(ctx, StoredKey, []byte("true"), nil); err != nil {
		t.Fatal(err)
	}
	message, err := store.Get(ctx, Stream, StoredKey)
	if err != nil || string(message.Value) != "true" || message.StoredAt == "" {
		t.Fatalf("the value: %+v, %v", message, err)
	}
	if err := store.Put(ctx, ClearedKey, nil, map[string]string{"KV-Operation": "DEL"}); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.Get(ctx, Stream, ClearedKey)
	if err != nil || cleared.Headers["KV-Operation"] != "DEL" || len(cleared.Value) != 0 {
		t.Fatalf("a clear: %+v, %v", cleared, err)
	}
}

// keysAreThePrefix lists one instance's keys, and never another's.
func keysAreThePrefix(t *testing.T, ctx context.Context, store nats.KV) {
	t.Helper()
	keys, err := store.Keys(ctx, Stream, Prefix)
	if err != nil || !equal(keys, []string{ClearedKey, StoredKey}) {
		t.Fatalf("the keys under %s: %v, %v", Prefix, keys, err)
	}
	if keys, err := store.Keys(ctx, Stream, OtherKey); err != nil || len(keys) != 0 {
		t.Fatalf("another instance's keys: %v, %v", keys, err)
	}
}

// closedRefuses is a store that answers with an error, not with absence.
func closedRefuses(t *testing.T, open func(*testing.T) nats.KV) {
	t.Helper()
	store := open(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), Stream, StoredKey); err == nil || errors.Is(err, nats.ErrAbsent) {
		t.Fatalf("a closed store answered %v", err)
	}
}

// equal compares two string lists.
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
