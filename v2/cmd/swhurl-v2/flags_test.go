package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/adapters/nats/natstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// flagsTestDocument is what a test instance's image declares: one flag of each kind, so the card's typing shows.
const flagsTestDocument = `{
  "checkout": {"type": "boolean", "description": "Offer the new checkout", "default": false, "prod": true},
  "batch-size": {"type": "integer", "default": 100, "min": 1, "max": 1000},
  "theme": {"type": "string", "default": "plain", "enum": ["plain", "dark"]}
}`

// flagsClock is the instant the card measures an override's age from.
var flagsClock = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// flagsRecords writes one instance's records tree, with the document its image offers.
func flagsRecords(t *testing.T, document string) string {
	t.Helper()
	root := t.TempDir()
	folder := filepath.Join(kernel.RecordFolder(root, "demo-shop", "staging"), "offers")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if document != "" {
		if err := os.WriteFile(filepath.Join(folder, kernel.DocumentRoles["flags"]), []byte(document), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// flagsCLI is the binary's own cli with the two effects a card has: an environment and a store.
func flagsCLI(seed string, store nats.KV) cli {
	return cli{readFile: os.ReadFile, getenv: func(name string) string {
		if name == "NATS_SEED" {
			return seed
		}
		return ""
	}, natsStore: func(_ context.Context, _, _, _ string) (nats.KV, error) { return store, nil },
		clock: func() time.Time { return flagsClock }}
}

// flagsRun runs one flags command line and is its report, its stdout, its diagnostic and its exit status.
func flagsRun(t *testing.T, root string, store nats.KV, arguments ...string) (flagsDocument, string, string, int) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	args := append([]string{"flags", "--records", root, "--app", "demo-shop", "--env", "staging", "--json"}, arguments...)
	status := runWith(args, &out, &diagnostic, flagsCLI("a-seed", store))
	var report flagsDocument
	if status == 0 {
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("the report: %v\n%s", err, out.String())
		}
	}
	return report, out.String(), diagnostic.String(), status
}

// The card states every row and the instance it is about.
func TestFlagsReportsTheCard(t *testing.T) {
	fake := natstest.New()
	fake.Entries[kernel.FlagSubject("staging", "demo-shop", "checkout")] = nats.Message{Value: []byte("true"),
		StoredAt: flagsClock.AddDate(0, 0, -14).Format(time.RFC3339Nano)}
	report, _, _, status := flagsRun(t, flagsRecords(t, flagsTestDocument), fake)
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	if report.Kind != "v2-flags" || report.Version != 1 || !report.Writable || report.Problem != "" {
		t.Fatalf("the document: %+v", report)
	}
	if report.Instance.Application != "demo-shop" || report.Instance.Environment != "staging" {
		t.Fatalf("the instance: %+v", report.Instance)
	}
	if len(report.Rows) != 3 || report.Rows[0].Name != "batch-size" || report.Rows[2].Name != "theme" {
		t.Fatalf("the rows: %+v", report.Rows)
	}
	checkout := report.Rows[1]
	if !checkout.Overridden || checkout.Effective != true || checkout.AgeDays == nil || *checkout.AgeDays != 14 ||
		!checkout.Stale || checkout.StoredAt == "" {
		t.Fatalf("the override: %+v", checkout)
	}
	if declared := checkout.Declaration; declared == nil || declared.Type != "boolean" || declared.Default != false ||
		declared.Description != "Offer the new checkout" {
		t.Fatalf("the declaration: %+v", checkout.Declaration)
	}
	if bounds := report.Rows[0].Declaration; bounds == nil || bounds.Min != float64(1) || bounds.Max != float64(1000) {
		t.Fatalf("an integer's bounds: %+v", report.Rows[0].Declaration)
	}
	if enum := report.Rows[2].Declaration; enum == nil || len(enum.Enum) != 2 {
		t.Fatalf("a string's enumeration: %+v", report.Rows[2].Declaration)
	}
}

// No seed is a card that lists the declaration, says it can write nothing, and needs no store.
func TestFlagsWithoutASeedStillListsTheDeclaration(t *testing.T) {
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"flags", "--records", flagsRecords(t, flagsTestDocument), "--app", "demo-shop", "--env",
		"staging", "--json"}, &out, &diagnostic, flagsCLI("", nil))
	if status != 0 {
		t.Fatalf("status %d: %s", status, diagnostic.String())
	}
	var report flagsDocument
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Writable || report.Problem != orchestration.NoFlagsProblem || len(report.Rows) != 3 {
		t.Fatalf("no seed: %+v", report)
	}
}

// A set writes the value the declaration types, and the report printed is the one read back.
func TestFlagsSetWritesAndReportsWhatIsStored(t *testing.T) {
	fake := natstest.New()
	root := flagsRecords(t, flagsTestDocument)
	report, _, _, status := flagsRun(t, root, fake, "--set", "theme=dark")
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	if len(fake.Writes) != 1 || string(fake.Writes[0].Value) != `"dark"` {
		t.Fatalf("the write: %+v", fake.Writes)
	}
	if row := report.Rows[2]; !row.Overridden || row.Effective != "dark" || row.Stale {
		t.Fatalf("the row after the write: %+v", row)
	}
}

// A clear writes the tombstone, and the declared value is effective again.
func TestFlagsClearRemovesTheOverride(t *testing.T) {
	fake := natstest.New()
	fake.Entries[kernel.FlagSubject("staging", "demo-shop", "theme")] = nats.Message{Value: []byte(`"dark"`),
		StoredAt: flagsClock.Format(time.RFC3339Nano)}
	report, _, _, status := flagsRun(t, flagsRecords(t, flagsTestDocument), fake, "--clear", "theme")
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	if len(fake.Writes) != 1 || fake.Writes[0].Headers["KV-Operation"] != "DEL" {
		t.Fatalf("the clear: %+v", fake.Writes)
	}
	if row := report.Rows[2]; row.Overridden || row.Effective != "plain" {
		t.Fatalf("the row after the clear: %+v", row)
	}
}

// A malformed declaration is the card's problem, and the overrides are still listed.
func TestFlagsShowsAProblemAndStillListsOverrides(t *testing.T) {
	fake := natstest.New()
	fake.Entries[kernel.FlagSubject("staging", "demo-shop", "old-banner")] = nats.Message{Value: []byte("true"),
		StoredAt: flagsClock.Format(time.RFC3339Nano)}
	report, _, _, status := flagsRun(t, flagsRecords(t, `{"checkout": {"type": "flag"}}`), fake)
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	if !strings.Contains(report.Problem, "flags.checkout.type") {
		t.Fatalf("the problem: %q", report.Problem)
	}
	if len(report.Rows) != 1 || !report.Rows[0].Undeclared || report.Rows[0].Name != "old-banner" {
		t.Fatalf("the rows: %+v", report.Rows)
	}
}

// An instance that offers no flags document declares none, which is not a problem.
func TestFlagsWithoutADocumentDeclaresNothing(t *testing.T) {
	report, out, _, status := flagsRun(t, flagsRecords(t, ""), natstest.New())
	if status != 0 || report.Problem != "" || len(report.Rows) != 0 {
		t.Fatalf("status %d, %+v", status, report)
	}
	if !strings.Contains(out, `"rows": []`) {
		t.Fatalf("an empty card: %s", out)
	}
	var text, diagnostic bytes.Buffer
	status = runWith([]string{"flags", "--records", flagsRecords(t, ""), "--app", "demo-shop", "--env", "staging"},
		&text, &diagnostic, flagsCLI("a-seed", natstest.New()))
	if status != 0 || text.String() != "demo-shop/staging declares no feature flags.\n" {
		t.Fatalf("text output: status %d, %q", status, text.String())
	}
}

// Every refusal is the card's own words and exit 2: nothing is written, and no report replaces the reason.
func TestFlagsRefusesInTheCardsOwnWords(t *testing.T) {
	for _, refused := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{"an undeclared flag", []string{"--set", "old-banner=true"},
			"demo-shop/staging runs an image that does not declare old-banner"},
		{"a value outside its declaration", []string{"--set", "batch-size=0"},
			"the value is outside min and max"},
		{"a value of the wrong type", []string{"--set", "checkout=yes"}, "choose true or false"},
		{"a set without a value", []string{"--set", "checkout"}, "--set takes FLAG=TEXT"},
		{"a name that is not a flag's", []string{"--clear", "Not A Flag"}, "not a flag name"},
		{"a store that cannot be reached", []string{"--set", "theme=dark"}, "cannot reach NATS"},
	} {
		fake := natstest.New()
		if refused.name == "a store that cannot be reached" {
			fake.Fail = errors.New("cannot reach NATS at nats://nats.messaging.svc.cluster.local:4222: refused")
		}
		_, _, diagnostic, status := flagsRun(t, flagsRecords(t, flagsTestDocument), fake, refused.arguments...)
		if status != 2 || !strings.Contains(diagnostic, refused.want) {
			t.Fatalf("%s: status %d, %q, want %q", refused.name, status, diagnostic, refused.want)
		}
		if len(fake.Writes) != 0 {
			t.Fatalf("%s wrote %+v", refused.name, fake.Writes)
		}
	}
}

// A usage error is the usage text, twice over when a set and a clear are asked for at once.
func TestFlagsTakesOneWriteAtMost(t *testing.T) {
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"flags", "--records", flagsRecords(t, flagsTestDocument), "--app", "demo-shop", "--env",
		"staging", "--set", "theme=dark", "--clear", "theme"}, &out, &diagnostic, flagsCLI("a-seed", natstest.New()))
	if status != 2 || !strings.Contains(diagnostic.String(), "usage:") {
		t.Fatalf("status %d, %q", status, diagnostic.String())
	}
}

// An environment the platform does not have is refused before anything is read.
func TestFlagsRefusesAnEnvironmentItDoesNotHave(t *testing.T) {
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"flags", "--records", "nowhere", "--app", "demo-shop", "--env", "qa"}, &out, &diagnostic,
		flagsCLI("a-seed", natstest.New()))
	if status != 2 || !strings.Contains(diagnostic.String(), "qa is not an environment: staging or prod") {
		t.Fatalf("status %d, %q", status, diagnostic.String())
	}
}
