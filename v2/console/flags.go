package console

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// FlagStore is the console's feature flag overrides in NATS, as the flags command reads them: one store, opened on
// first use and kept, since the store reconnects by itself. It is safe for concurrent use: requests share it.
type FlagStore struct {
	// Seed signs the console in; empty is a console with no key, which opens no store and has no error.
	Seed string
	URL  string
	// Open signs in to NATS at a URL with a seed, answering on an inbox prefix.
	Open func(ctx context.Context, url, seed, inbox string) (nats.KV, error)
	Now  func() time.Time

	mu      sync.Mutex
	kv      nats.KV
	triedAt time.Time
	err     error // why the last sign-in failed, until the next attempt
}

// open is the open store, nil when the console has no key. A failed sign-in is its reason, kept for a minute before
// the next attempt.
func (f *FlagStore) open(ctx context.Context) (nats.KV, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.kv != nil {
		return f.kv, nil
	}
	if f.Seed == "" {
		return nil, nil
	}
	if f.err != nil && f.Now().Sub(f.triedAt) < time.Minute {
		return nil, f.err
	}
	f.triedAt = f.Now()
	store, err := f.Open(ctx, f.URL, f.Seed, kernel.ConsoleInbox)
	if err != nil {
		f.err = err
		return nil, err
	}
	f.kv, f.err = store, nil
	return store, nil
}

// Card is an instance's card and whether its overrides can be changed: the store's overrides, or why there are none.
// It is the FlagCards an app page reads.
func (f *FlagStore) Card(ctx context.Context, instance pages.Instance, declaration []producer.Flag,
	problem string) (orchestration.FlagCard, bool) {
	store, err := f.open(ctx)
	if err != nil {
		return orchestration.UnreachableFlags(instance.Env, declaration, problem, err, f.Now()), false
	}
	return orchestration.Flags(ctx, store, instance.Env, instance.App, declaration, problem, f.Now()), store != nil
}

// FlagChange is one form's change to one flag's override: set to Value's text, or cleared.
type FlagChange struct {
	Name  string
	Set   bool
	Value string
}

// Change sets or clears one flag's override for an instance, typed by the declaration the pinned tree's offers state.
// It is not a pull request: the override is written to NATS at once. Done is what the write left, as the card reads it
// back ("cleared", or "= " and the stored value); a problem comes with the status the console answers it with.
func (f *FlagStore) Change(ctx context.Context, instance pages.Instance, tree map[string][]byte,
	change FlagChange) (done, problem string, status int) {
	store, err := f.open(ctx)
	if err != nil {
		return "", "Overrides could not be written: " + err.Error(), http.StatusBadGateway
	}
	declaration, declarationProblem := orchestration.FlagDeclaration(tree[FlagDocument(instance)])
	if change.Set {
		err = orchestration.SetFlag(ctx, store, instance.Env, instance.App, change.Name, change.Value, declaration)
	} else {
		err = orchestration.ClearFlag(ctx, store, instance.Env, instance.App, change.Name)
	}
	if err != nil {
		return "", change.Name + ": " + err.Error(), http.StatusBadRequest
	}
	if !change.Set {
		return "cleared", "", 0
	}
	card := orchestration.Flags(ctx, store, instance.Env, instance.App, declaration, declarationProblem, f.Now())
	return "= " + storedValue(card, change.Name), "", 0
}

// storedValue is the value the write left one flag at, as the card reads it back.
func storedValue(card orchestration.FlagCard, name string) string {
	for _, row := range card.Rows {
		if row.Name == name && row.Overridden {
			encoded, err := json.Marshal(row.Override.Value)
			if err != nil {
				return "unknown"
			}
			return string(encoded)
		}
	}
	return "unknown"
}
