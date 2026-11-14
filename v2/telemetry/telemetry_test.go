package telemetry_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/telemetry"
)

var update = flag.Bool("update", false, "rewrite the golden documents")

var (
	trace  = telemetry.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36}
	root   = telemetry.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}
	child  = telemetry.SpanID{0x53, 0x99, 0x5c, 0x3f, 0x42, 0xcd, 0x8a, 0xd8}
	source = telemetry.Source{Resource: []telemetry.Attribute{{"service.name", "console"}}, Scope: "swhurl-v2/console"}
	at     = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)
)

// golden compares a document with testdata/<name>, rewriting it under -update; both must be valid JSON.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, got, "", "  "); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(pretty.Bytes(), '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, want); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compact.Bytes(), got) {
		t.Fatalf("%s differs:\n%s", name, got)
	}
}

// A root span and its failed child: hex identifiers, nanosecond texts, typed attributes, the child's parent and
// error status, and no parent on the root.
func TestTracesAreOTLPJSON(t *testing.T) {
	spans := []telemetry.Span{
		{Trace: trace, ID: child, Parent: root, Name: "github.Tarball", Kind: telemetry.Client,
			Start: at.Add(5 * time.Millisecond), End: at.Add(705 * time.Millisecond),
			Attributes: []telemetry.Attribute{{"github.repository", "samclement/swhurl-platform"}},
			Failed:     true, Message: "failed: tarball: status_502"},
		{Trace: trace, ID: root, Name: "GET /apps", Kind: telemetry.Server, Start: at, End: at.Add(time.Second),
			Attributes: []telemetry.Attribute{{"http.route", "/apps"}, {"http.response.status_code", 200},
				{"console.cold", true}, {"ratio", 0.5}, {"bytes", int64(12)}, {"other", time.Second}}},
	}
	got, err := telemetry.EncodeTraces(source, spans)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "traces.json", got)
	if _, err := telemetry.EncodeTraces(source, []telemetry.Span{{Name: "nameless"}}); err == nil {
		t.Fatal("a span without identifiers was encoded")
	}
}

// Counters and histograms keep one point per attribute set, in a stable order, with a value on a bound counted
// in that bound's bucket; a later Record does not change a document already taken.
func TestMetricsAreCumulativeOTLPJSON(t *testing.T) {
	requests := telemetry.Counter{Name: "console.requests", Unit: "{request}", Description: "Requests answered"}
	duration := telemetry.Histogram{Name: "http.server.request.duration", Unit: "s", Bounds: []float64{0.1, 1, 10}}
	ok := []telemetry.Attribute{{"http.route", "/apps"}, {"http.response.status_code", 200}}
	failed := []telemetry.Attribute{{"http.route", "/activity"}, {"http.response.status_code", 502}}
	requests.Add(1, ok...)
	requests.Add(2, ok...)
	requests.Add(1, failed...)
	for _, seconds := range []float64{0.05, 0.1, 0.5, 1, 30} {
		duration.Record(seconds, ok...)
	}
	duration.Record(2, failed...)
	metrics := telemetry.Metrics{Start: at, At: at.Add(time.Minute), Sums: []telemetry.Sum{requests.Sum()},
		Histograms: []telemetry.Distribution{duration.Distribution()}}
	duration.Record(3, ok...)
	got, err := telemetry.EncodeMetrics(source, metrics)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "metrics.json", got)
}

// Nothing recorded is an export with no points, never null.
func TestEmptyMetricsHaveNoNulls(t *testing.T) {
	got, err := telemetry.EncodeMetrics(source, telemetry.Metrics{Start: at, At: at,
		Sums:       []telemetry.Sum{(&telemetry.Counter{Name: "c"}).Sum()},
		Histograms: []telemetry.Distribution{(&telemetry.Histogram{Name: "h"}).Distribution()}})
	if err != nil || bytes.Contains(got, []byte("null")) {
		t.Fatalf("%s %v", got, err)
	}
}

// Only a version-00 header with lowercase, non-zero identifiers continues a trace.
func TestTraceparent(t *testing.T) {
	parent, ok := telemetry.ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if !ok || parent.Trace != trace || parent.Span != root {
		t.Fatalf("%v %v", parent, ok)
	}
	for _, header := range []string{"", "garbage",
		"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-zz",
		"00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra"} {
		if _, ok := telemetry.ParseTraceparent(header); ok {
			t.Fatalf("%q was accepted", header)
		}
	}
}
