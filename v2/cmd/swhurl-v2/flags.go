package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// flags is the console's Feature flags card and its two writes, without the console: the rows an app declares and the
// overrides NATS holds for it, and one set or clear. It reads the declaration from the pinned records tree, so a page
// cannot show a declaration from a different commit than the rows beside it.
//
// It writes to NATS, and only there: nothing in Git changes, no cluster object is touched, and the app watches its own
// keys, so a set reaches it without a new image or a restart.

// flagsDocument is the report: the card's rows, and whether this process could write them at all.
type flagsDocument struct {
	Kind     string `json:"kind"`
	Version  int    `json:"version"`
	Instance struct {
		Application string `json:"application"`
		Environment string `json:"environment"`
	} `json:"instance"`
	Writable bool               `json:"writable"`
	Problem  string             `json:"problem"`
	Rows     []flagsDocumentRow `json:"rows"`
}

// flagsDocumentRow is one line of the card, in the console's own order of facts.
type flagsDocumentRow struct {
	Name        string              `json:"name"`
	Declaration *flagDeclarationOut `json:"declaration,omitempty"`
	Undeclared  bool                `json:"undeclared"`
	Overridden  bool                `json:"overridden"`
	Override    *overrideOut        `json:"override,omitempty"`
	StoredAt    string              `json:"storedAt,omitempty"`
	AgeDays     *int                `json:"ageDays,omitempty"`
	Stale       bool                `json:"stale"`
	Effective   any                 `json:"effective"`
}

// flagDeclarationOut is what a flag's declaration says, each fact omitted when it states none.
type flagDeclarationOut struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default"`
	Value       any    `json:"value"`
	Min         any    `json:"min,omitempty"`
	Max         any    `json:"max,omitempty"`
	Enum        []any  `json:"enum,omitempty"`
}

// overrideOut is one stored override: its value, or `readable: false` when the stored bytes are not JSON.
type overrideOut struct {
	Value    any  `json:"value,omitempty"`
	Readable bool `json:"readable"`
}

// flags reads one instance's rows, and writes one override when the caller asks for it.
func (c cli) flags(r request) int {
	environment, application := r.env, r.app
	if environment != "staging" && environment != "prod" {
		return fail(c.diagnostic, fmt.Errorf("%s is not an environment: staging or prod", environment))
	}
	document, status := c.offeredFlags(kernel.RecordFolder(r.records, application, environment)+"/offers/"+
		kernel.DocumentRoles["flags"], "the instance's offered flags document")
	if status != 0 {
		return status
	}
	declarations, problem := orchestration.FlagDeclaration(document)
	ctx := context.Background()
	store, refusal := c.flagStore(ctx)
	if refusal != nil {
		return fail(c.diagnostic, refusal)
	}
	if store != nil {
		defer store.Close()
	}
	if r.change != "" || r.cleared != "" {
		if err := writeFlag(ctx, store, environment, application, r.change, r.cleared, declarations); err != nil {
			return fail(c.diagnostic, err)
		}
	}
	card := orchestration.Flags(ctx, store, environment, application, declarations, problem, c.now())
	return emitFlags(c, r, environment, application, store != nil, card)
}

// writeFlag is the one write a command line asks for: a set, whose text is typed by the declaration, or a clear.
func writeFlag(ctx context.Context, store nats.KV, environment, application, set, clear string,
	declarations []producer.Flag) error {
	if clear != "" {
		return orchestration.ClearFlag(ctx, store, environment, application, clear)
	}
	name, text, found := splitSetting(set)
	if !found {
		return errors.New("--set takes FLAG=TEXT")
	}
	return orchestration.SetFlag(ctx, store, environment, application, name, text, declarations)
}

// emitFlags prints the card: the rows as text, or the document the console reads.
func emitFlags(c cli, r request, environment, application string, writable bool, card orchestration.FlagCard) int {
	document := flagsReport(application, environment, writable, card)
	if r.json {
		data, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return c.failure(err)
		}
		return emit(c.out, c.diagnostic, data)
	}
	var text []byte
	if len(document.Rows) == 0 {
		text = []byte(fmt.Sprintf("%s/%s declares no feature flags.\n", application, environment))
	}
	for _, row := range document.Rows {
		text = append(text, flagsLine(row)...)
	}
	if document.Problem != "" {
		fmt.Fprintf(c.diagnostic, "flags: %s\n", document.Problem)
	}
	return emit(c.out, c.diagnostic, text)
}

// flagsReport is the card as a document: each row's facts, and the problem that kept any of them out.
func flagsReport(application, environment string, writable bool, card orchestration.FlagCard) flagsDocument {
	document := flagsDocument{Kind: "v2-flags", Version: 1, Writable: writable, Problem: card.Problem,
		Rows: make([]flagsDocumentRow, 0, len(card.Rows))}
	document.Instance.Application, document.Instance.Environment = application, environment
	for _, row := range card.Rows {
		document.Rows = append(document.Rows, flagsReportRow(environment, row))
	}
	return document
}

// flagsReportRow is one row's document: the declaration's facts, the override, and what the app runs with.
func flagsReportRow(environment string, row orchestration.FlagRow) flagsDocumentRow {
	out := flagsDocumentRow{Name: row.Name, Undeclared: row.Undeclared, Overridden: row.Overridden,
		Stale: row.Stale, Effective: row.Effective}
	if !row.Undeclared {
		out.Declaration = declaredFlag(environment, row.Flag)
	}
	if row.Overridden {
		out.Override = &overrideOut{Value: row.Override.Value, Readable: row.Override.Readable}
		if !row.Override.StoredAt.IsZero() {
			out.StoredAt = row.Override.StoredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		if row.AgeDays >= 0 {
			age := row.AgeDays
			out.AgeDays = &age
		}
	}
	return out
}

// declaredFlag is one declaration as the card states it, with the value it has in this environment.
func declaredFlag(environment string, flag producer.Flag) *flagDeclarationOut {
	out := &flagDeclarationOut{Type: flag.Type(), Description: flag.Description(),
		Default: flag.Declaration.Get("default"), Value: flag.Declared(environment)}
	if bound := flag.Declaration.Get("min"); bound != nil {
		out.Min = bound
	}
	if bound := flag.Declaration.Get("max"); bound != nil {
		out.Max = bound
	}
	if values, ok := flag.Declaration.Get("enum").([]any); ok {
		out.Enum = values
	}
	return out
}

// flagsLine is one row as text: the flag, what the app runs with, and the override it came from.
func flagsLine(row flagsDocumentRow) []byte {
	line := fmt.Sprintf("%s = %s", row.Name, rendered(row.Effective))
	if row.Overridden {
		line += " (override " + jsonText(row.Override.Value) + ")"
		if row.AgeDays != nil {
			line += ", set " + days(*row.AgeDays)
		}
		if row.Stale {
			line += ", stale"
		}
	}
	if row.Undeclared {
		line += " (not declared: the app ignores this override)"
	}
	return []byte(line + "\n")
}

// jsonText is a value exactly as JSON states it, a string in its quotes.
func jsonText(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// rendered is a JSON value as a line shows it: a string bare, anything else as JSON, nothing as "none".
func rendered(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if value == nil {
		return "none"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// days is an age as a card says it: today, one day ago, or a count of days ago.
func days(age int) string {
	switch age {
	case 0:
		return "today"
	case 1:
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", age)
}

// splitSetting splits --set at its first = and is the name and the form's text, which the declaration types.
func splitSetting(set string) (string, string, bool) {
	name, text, found := strings.Cut(set, "=")
	return name, text, found
}

// offeredFlags reads the document an instance's offers folder holds: no file at all is an instance that declares no
// flags, and any other failure is the read's own.
func (c cli) offeredFlags(path, what string) ([]byte, int) {
	data, err := c.readFile(path)
	switch {
	case err == nil:
		return data, 0
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0
	}
	fmt.Fprintf(c.diagnostic, "io: %s: cannot read input file\n", what)
	return nil, 1
}

// flagStore signs this process in to NATS with the seed in its environment: no seed is no store, which the card states
// rather than failing, since it can still show what the app declares.
func (c cli) flagStore(ctx context.Context) (nats.KV, error) {
	seed := c.environment("NATS_SEED")
	if seed == "" {
		return nil, nil
	}
	url := c.environment("NATS_URL")
	if url == "" {
		url = defaultNATSURL
	}
	open := c.natsStore
	if open == nil {
		open = nats.Open
	}
	store, err := open(ctx, url, seed, kernel.ConsoleInbox)
	if err != nil {
		return nil, err
	}
	return store, nil
}
