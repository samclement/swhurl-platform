package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// StaleDays is the age at which the card marks an override: a mark, never a refusal, because only a person knows
// whether an override is still meant.
const StaleDays = 14

// FlagOverride is one stored override: what it holds, whether that is JSON the app can read, and when NATS stored it
// (the zero time when the server's stamp could not be read).
type FlagOverride struct {
	Value    any
	Readable bool
	StoredAt time.Time
}

// FlagRow is one line of the card: a flag the image declares with the override stored for it, or an override of a
// flag the image does not declare, which the app therefore ignores and only a clear can reach. AgeDays is -1 when the
// stored time could not be read.
type FlagRow struct {
	Name       string
	Flag       producer.Flag
	Undeclared bool
	Overridden bool
	Override   FlagOverride
	AgeDays    int
	Stale      bool
	Effective  any
}

// FlagCard is what the card shows, and why it cannot show everything: Problem is empty when the declaration and the
// overrides were both read. One unreadable half never hides the other, because an operator's way out of a bad
// declaration is a clear, and a declaration is what a card shows while NATS is down.
type FlagCard struct {
	Rows    []FlagRow
	Problem string
}

// NoFlagsProblem is the card's problem when the caller has no seed to sign in with: the console's own words for the
// state it is in, which also disables its Set and Clear buttons.
const NoFlagsProblem = "This console has no NATS key (console-nats Secret), so overrides cannot be read or set."

// FlagDeclaration is the declarations an instance's offers folder states. A document that cannot be read is the
// card's problem; an instance that offers none declares no flags, which is not a problem.
func FlagDeclaration(document []byte) ([]producer.Flag, string) {
	if len(bytes.TrimSpace(document)) == 0 {
		return nil, ""
	}
	flags, err := producer.Declarations(document)
	if err != nil {
		return nil, err.Error()
	}
	return flags, ""
}

// Flags is the card for one instance: the declaration's flags first, in name order, then the overrides of flags the
// image does not declare. The store is nil when the caller has no seed.
func Flags(ctx context.Context, store nats.KV, environment, application string, flags []producer.Flag,
	declarationProblem string, now time.Time) FlagCard {
	problems := []string{}
	if declarationProblem != "" {
		problems = append(problems, declarationProblem)
	}
	overrides := map[string]FlagOverride{}
	if store == nil {
		problems = append(problems, NoFlagsProblem)
	} else {
		stored, err := storedFlags(ctx, store, environment, application)
		if err != nil {
			problems = append(problems, "Overrides could not be read: "+err.Error())
		}
		overrides = stored
	}
	return FlagCard{Rows: flagRows(environment, flags, overrides, now), Problem: strings.Join(problems, " ")}
}

// SetFlag writes one declared flag's override: the declaration types the form's text first, so a value the app could
// not read is never stored. The write is idempotent, so a lost answer is safe to retry.
func SetFlag(ctx context.Context, store nats.KV, environment, application, name, text string,
	flags []producer.Flag) error {
	if err := writable(store, environment, application, name); err != nil {
		return err
	}
	flag, declared := declaration(flags, name)
	if !declared {
		return fmt.Errorf("%s/%s runs an image that does not declare %s", application, environment, name)
	}
	value, err := flag.Text(text)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return store.Put(ctx, kernel.FlagSubject(environment, application, name), encoded, nil)
}

// ClearFlag writes the tombstone that removes one flag's override, declared or not: a flag the image stopped declaring
// keeps its override otherwise, and the app keeps ignoring it.
func ClearFlag(ctx context.Context, store nats.KV, environment, application, name string) error {
	if err := writable(store, environment, application, name); err != nil {
		return err
	}
	return store.Put(ctx, kernel.FlagSubject(environment, application, name), nil, map[string]string{
		"KV-Operation": "DEL"})
}

// writable is the state both writes need: a store to write to, and a name an override can carry. Clearing needs no
// declaration, so the name alone is checked.
func writable(store nats.KV, environment, application, name string) error {
	if store == nil {
		return errors.New(NoFlagsProblem)
	}
	if !producer.ValidFlagName(name) {
		return errors.New("not a flag name")
	}
	return nil
}

// declaration is the flag a declaration states, and whether it states it at all.
func declaration(flags []producer.Flag, name string) (producer.Flag, bool) {
	for _, flag := range flags {
		if flag.Name == name {
			return flag, true
		}
	}
	return producer.Flag{}, false
}

// storedFlags is one instance's overrides by flag name, as NATS holds them: a key that was cleared is not an override,
// and a value that is not JSON is one the app ignores rather than a reason to fail the read.
func storedFlags(ctx context.Context, store nats.KV, environment, application string) (map[string]FlagOverride, error) {
	prefix := kernel.FlagsPrefix(environment, application)
	keys, err := store.Keys(ctx, kernel.FlagsStream(environment), prefix)
	if err != nil {
		return nil, err
	}
	overrides := make(map[string]FlagOverride, len(keys))
	for _, key := range keys {
		name := strings.TrimPrefix(key, prefix)
		message, err := store.Get(ctx, kernel.FlagsStream(environment), key)
		if errors.Is(err, nats.ErrAbsent) || cleared(message.Headers) {
			continue
		}
		if err != nil {
			return nil, err
		}
		overrides[name] = flagOverride(message)
	}
	return overrides, nil
}

// cleared says whether a message is a tombstone: a clear states its operation in the header, and carries no value.
func cleared(headers map[string]string) bool {
	operation, stated := headers["KV-Operation"]
	return stated && operation != ""
}

// flagOverride is one stored message as an override: its value when it is JSON, and the server's own time.
func flagOverride(message nats.Message) FlagOverride {
	override := FlagOverride{StoredAt: storedTime(message.StoredAt)}
	if err := json.Unmarshal(message.Value, &override.Value); err != nil {
		return override
	}
	override.Readable = true
	return override
}

// storedTime is NATS's message time, which it states with nanoseconds; a stamp this reads nothing from is the zero
// time, and the card shows no age rather than a guessed one.
func storedTime(text string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// flagRows is the card's lines: the declaration's flags in its own name order, then the undeclared overrides by name.
func flagRows(environment string, flags []producer.Flag, overrides map[string]FlagOverride, now time.Time) []FlagRow {
	rows := make([]FlagRow, 0, len(flags)+len(overrides))
	declared := make(map[string]bool, len(flags))
	for _, flag := range flags {
		declared[flag.Name] = true
		override, overridden := overrides[flag.Name]
		rows = append(rows, flagRow(environment, flag.Name, flag, override, overridden, now))
	}
	undeclared := make([]string, 0, len(overrides))
	for name := range overrides {
		if !declared[name] {
			undeclared = append(undeclared, name)
		}
	}
	sort.Strings(undeclared)
	for _, name := range undeclared {
		rows = append(rows, flagRow(environment, name, producer.Flag{}, overrides[name], true, now))
	}
	return rows
}

// flagRow is one line: the declared value, the override over it when there is one, and how old that override is. An
// override the app cannot read keeps the declared value effective, because the app ignores it too.
func flagRow(environment, name string, flag producer.Flag, override FlagOverride, overridden bool, now time.Time) FlagRow {
	row := FlagRow{Name: name, Flag: flag, Undeclared: flag.Name == "", Overridden: overridden, Override: override,
		AgeDays: -1}
	if !row.Undeclared {
		row.Effective = flag.Declared(environment)
	}
	if !overridden {
		return row
	}
	if override.Readable {
		row.Effective = override.Value
	}
	if override.StoredAt.IsZero() {
		return row
	}
	row.AgeDays = max(int(now.Sub(override.StoredAt).Hours()/24), 0)
	row.Stale = row.AgeDays >= StaleDays
	return row
}
