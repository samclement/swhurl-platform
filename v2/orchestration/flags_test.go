package orchestration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/adapters/nats/natstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// The declaration these tests card: a boolean with an environment value, a bounded integer, an enumerated string and
// a number, so every kind the form types is covered.
const flagsDocument = `{
  "checkout": {"type": "boolean", "description": "Offer the new checkout", "default": false, "prod": true},
  "batch-size": {"type": "integer", "description": "Rows per batch", "default": 100, "min": 1, "max": 1000},
  "theme": {"type": "string", "default": "plain", "enum": ["plain", "dark"]},
  "ratio": {"type": "number", "default": 0.5, "min": 0, "max": 1}
}`

// flagsAt is the instant these tests measure an override's age from.
var flagsAt = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// declared is the document's flags, and the problem that was not there.
func declared(t *testing.T) []producer.Flag {
	t.Helper()
	flags, problem := orchestration.FlagDeclaration([]byte(flagsDocument))
	if problem != "" {
		t.Fatalf("the document declares a problem: %s", problem)
	}
	return flags
}

// store with one override stored for the instance, at a given age in days.
func stored(t *testing.T, name string, value string, age int) *natstest.Fake {
	t.Helper()
	fake := natstest.New()
	at := flagsAt.AddDate(0, 0, -age).UTC().Format(time.RFC3339Nano)
	fake.Entries[kernel.FlagSubject("staging", "demo-shop", name)] = nats.Message{Value: []byte(value),
		StoredAt: at}
	return fake
}

// card is the rows for the declaration and one store, as a card reads them.
func card(t *testing.T, store nats.KV, problem string, now time.Time) orchestration.FlagCard {
	t.Helper()
	flags := declared(t)
	return orchestration.Flags(context.Background(), store, "staging", "demo-shop", flags, problem, now)
}

// A declaration with no override: every flag is listed in name order, each with the value its environment states.
func TestFlagsListsEveryDeclaredFlagWithItsValue(t *testing.T) {
	found := card(t, natstest.New(), "", flagsAt)
	if found.Problem != "" {
		t.Fatalf("problem: %s", found.Problem)
	}
	names := make([]string, 0, len(found.Rows))
	for _, row := range found.Rows {
		names = append(names, row.Name)
		if row.Overridden || row.Undeclared || row.AgeDays != -1 || row.Stale {
			t.Fatalf("%s: %+v", row.Name, row)
		}
	}
	if strings.Join(names, ",") != "batch-size,checkout,ratio,theme" {
		t.Fatalf("the order: %v", names)
	}
	checkout := found.Rows[1]
	if checkout.Effective != false || checkout.Flag.Declared("prod") != true {
		t.Fatalf("checkout: %+v", checkout)
	}
	if found.Rows[0].Flag.Declaration.Get("max") != int64(1000) {
		t.Fatalf("the declaration's bound: %v", found.Rows[0].Flag.Declaration.Get("max"))
	}
}

// An override replaces the declared value, and its age is whole days from the server's stamp.
func TestFlagsPutsAnOverrideOverTheDeclaredValue(t *testing.T) {
	found := card(t, stored(t, "checkout", "true", 13), "", flagsAt)
	row := found.Rows[1]
	switch {
	case !row.Overridden || row.Override.Value != true || row.Effective != true:
		t.Fatalf("the override: %+v", row)
	case row.AgeDays != 13 || row.Stale:
		t.Fatalf("an override of 13 days: age %d, stale %v", row.AgeDays, row.Stale)
	case row.Override.StoredAt.IsZero():
		t.Fatalf("the stored time: %+v", row.Override)
	}
}

// Fourteen days is the mark, and a stamp that cannot be read is no age rather than a guessed one.
func TestFlagsMarksAnOverrideAtFourteenDays(t *testing.T) {
	found := card(t, stored(t, "checkout", "true", 14), "", flagsAt)
	if row := found.Rows[1]; row.AgeDays != 14 || !row.Stale {
		t.Fatalf("14 days: age %d, stale %v", row.AgeDays, row.Stale)
	}
	unreadable := natstest.New()
	unreadable.Entries[kernel.FlagSubject("staging", "demo-shop", "checkout")] = nats.Message{
		Value: []byte("true"), StoredAt: "yesterday"}
	row := card(t, unreadable, "", flagsAt).Rows[1]
	if row.AgeDays != -1 || row.Stale || row.Effective != true {
		t.Fatalf("an unreadable stamp: %+v", row)
	}
}

// An override of a flag the image does not declare is listed last, unused, and can only be cleared.
func TestFlagsListsAnUndeclaredOverrideLast(t *testing.T) {
	fake := stored(t, "old-banner", `"on"`, 1)
	fake.Entries[kernel.FlagSubject("staging", "demo-shop", "legacy")] = nats.Message{Value: []byte("1"),
		StoredAt: flagsAt.Format(time.RFC3339Nano)}
	found := card(t, fake, "", flagsAt)
	if got := len(found.Rows); got != 6 {
		t.Fatalf("rows: %d", got)
	}
	if found.Rows[4].Name != "legacy" || found.Rows[5].Name != "old-banner" {
		t.Fatalf("the undeclared rows: %v, %v", found.Rows[4].Name, found.Rows[5].Name)
	}
	row := found.Rows[5]
	if !row.Undeclared || !row.Overridden || row.Effective != "on" || row.Override.Value != "on" {
		t.Fatalf("an undeclared override: %+v", row)
	}
}

// A stored value that is not JSON is an override the app ignores, so the declared value stays effective.
func TestFlagsIgnoresAnOverrideTheAppCannotRead(t *testing.T) {
	fake := natstest.New()
	fake.Entries[kernel.FlagSubject("staging", "demo-shop", "theme")] = nats.Message{Value: []byte("dark"),
		StoredAt: flagsAt.Format(time.RFC3339Nano)}
	row := card(t, fake, "", flagsAt).Rows[3]
	if !row.Overridden || row.Override.Readable || row.Effective != "plain" {
		t.Fatalf("a value that is not JSON: %+v", row)
	}
}

// A clear is a tombstone, not an override: the declared value is what the app runs with.
func TestFlagsSkipsAClearedOverride(t *testing.T) {
	fake := stored(t, "checkout", "true", 1)
	fake.Entries[kernel.FlagSubject("staging", "demo-shop", "checkout")] = nats.Message{
		Headers: map[string]string{"KV-Operation": "DEL"}, StoredAt: flagsAt.Format(time.RFC3339Nano)}
	row := card(t, fake, "", flagsAt).Rows[1]
	if row.Overridden || row.Effective != false {
		t.Fatalf("a cleared override: %+v", row)
	}
}

// A store that cannot be read states its problem and still lists what the image declares; a declaration that cannot
// be read states its problem and still lists the overrides, clearable.
func TestFlagsKeepsTheOtherHalfWhenOneCannotBeRead(t *testing.T) {
	fake := stored(t, "old-banner", `"on"`, 1)
	fake.Fail = errors.New("NATS did not answer in time")
	deaf := card(t, fake, "", flagsAt)
	if !strings.Contains(deaf.Problem, "Overrides could not be read: NATS did not answer in time") || len(deaf.Rows) != 4 {
		t.Fatalf("a store that cannot be read: %+v", deaf)
	}
	broken := orchestration.Flags(context.Background(), stored(t, "old-banner", `"on"`, 1), "staging", "demo-shop",
		nil, "flags.json: flags.checkout.type: one of boolean, integer, number, string", flagsAt)
	if !strings.Contains(broken.Problem, "flags.json:") || len(broken.Rows) != 1 || !broken.Rows[0].Undeclared {
		t.Fatalf("a declaration that cannot be read: %+v", broken)
	}
}

// No seed is a card that lists the declaration and says why it can write nothing.
func TestFlagsWithoutASeedStatesItsProblem(t *testing.T) {
	found := card(t, nil, "", flagsAt)
	if found.Problem != orchestration.NoFlagsProblem || len(found.Rows) != 4 {
		t.Fatalf("no store: %+v", found)
	}
	if err := orchestration.ClearFlag(context.Background(), nil, "staging", "demo-shop", "checkout"); err == nil ||
		err.Error() != orchestration.NoFlagsProblem {
		t.Fatalf("a clear with no seed: %v", err)
	}
}

// A set stores the form's text as the declared type: a boolean as JSON true, a whole number as a number.
func TestSetFlagStoresTheDeclaredValue(t *testing.T) {
	flags := declared(t)
	fake := natstest.New()
	if err := orchestration.SetFlag(context.Background(), fake, "staging", "demo-shop", "batch-size", " 250 ",
		flags); err != nil {
		t.Fatal(err)
	}
	if len(fake.Writes) != 1 || fake.Writes[0].Subject != kernel.FlagSubject("staging", "demo-shop", "batch-size") ||
		string(fake.Writes[0].Value) != "250" {
		t.Fatalf("the write: %+v", fake.Writes)
	}
	if err := orchestration.SetFlag(context.Background(), fake, "staging", "demo-shop", "theme", "dark", flags); err != nil {
		t.Fatal(err)
	}
	if string(fake.Writes[1].Value) != `"dark"` {
		t.Fatalf("a string's JSON: %s", fake.Writes[1].Value)
	}
}

// A clear writes the DEL operation and no value, declared or not.
func TestClearFlagWritesATombstone(t *testing.T) {
	fake := natstest.New()
	if err := orchestration.ClearFlag(context.Background(), fake, "staging", "demo-shop", "old-banner"); err != nil {
		t.Fatal(err)
	}
	written := fake.Writes[0]
	if written.Headers["KV-Operation"] != "DEL" || len(written.Value) != 0 {
		t.Fatalf("the clear: %+v", written)
	}
}

// Every refusal says what is wrong in the card's own words, and nothing is written.
func TestFlagWritesRefuseWhatTheDeclarationRefuses(t *testing.T) {
	flags := declared(t)
	for _, refused := range []struct{ name, text, want string }{
		{"checkout", "yes", "choose true or false"},
		{"batch-size", "ten", "give a whole number"},
		{"ratio", "half", "give a number"},
		{"batch-size", "0", "the value is outside min and max"},
		{"theme", "neon", "the value must be one of plain, dark"},
	} {
		fake := natstest.New()
		err := orchestration.SetFlag(context.Background(), fake, "staging", "demo-shop", refused.name, refused.text,
			flags)
		if err == nil || err.Error() != refused.want {
			t.Fatalf("%s = %q: %v, want %q", refused.name, refused.text, err, refused.want)
		}
		if len(fake.Writes) != 0 {
			t.Fatalf("%s = %q wrote %+v", refused.name, refused.text, fake.Writes)
		}
	}
	fake := natstest.New()
	if err := orchestration.SetFlag(context.Background(), fake, "staging", "demo-shop", "undeclared", "1", flags); err == nil ||
		err.Error() != "demo-shop/staging runs an image that does not declare undeclared" {
		t.Fatalf("an undeclared flag: %v", err)
	}
	if err := orchestration.ClearFlag(context.Background(), fake, "staging", "demo-shop", "Bad Name"); err == nil ||
		err.Error() != "not a flag name" {
		t.Fatalf("a name that is not a flag's: %v", err)
	}
}

// A declaration states what the card shows, and documents that cannot be read are a problem rather than a crash.
func TestFlagDeclarationReadsWhatTheImageOffers(t *testing.T) {
	flags, problem := orchestration.FlagDeclaration([]byte(flagsDocument))
	if problem != "" || len(flags) != 4 || flags[0].Name != "batch-size" {
		t.Fatalf("the declaration: %v, %s", flags, problem)
	}
	if flags, problem := orchestration.FlagDeclaration(nil); len(flags) != 0 || problem != "" {
		t.Fatalf("no document: %v, %q", flags, problem)
	}
	if _, problem := orchestration.FlagDeclaration([]byte(`{"nothing": {"type": "boolean"}}`)); !strings.Contains(
		problem, "flags.nothing.default") {
		t.Fatalf("a document that cannot be read: %q", problem)
	}
}

// The report is the JSON the console reads, not Go's spelling of a value.
func TestTheReportKeepsJSONValues(t *testing.T) {
	found := card(t, stored(t, "theme", `"dark"`, 2), "", flagsAt)
	encoded, err := json.Marshal(found)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"Effective":"dark"`) {
		t.Fatalf("the report: %s", encoded)
	}
}
