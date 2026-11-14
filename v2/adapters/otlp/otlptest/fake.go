// Package otlptest is the fake for otlp.Exporter and the contract suite both implementations run.
package otlptest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/otlp"
	"github.com/samclement/swhurl-platform/v2/telemetry"
)

// Sent is one export a collector accepted: the signal's path and the document posted to it.
type Sent struct {
	Path     string
	Document []byte
}

// Fake is a collector that accepts every export and keeps the documents, or one that refuses them all. It is safe
// for concurrent use, as an exporter running beside a server must be.
type Fake struct {
	Refusing bool  // answers as a collector whose status is 503
	Fail     error // returned instead of exporting, after the call is counted
	mu       sync.Mutex
	attempts int
	sent     []Sent
}

var _ otlp.Exporter = (*Fake)(nil)

func (f *Fake) Traces(_ context.Context, source telemetry.Source, spans []telemetry.Span) error {
	document, err := telemetry.EncodeTraces(source, spans)
	if err != nil {
		return &otlp.Error{Code: otlp.RefusedRequest, Subject: "traces", Reason: "unencodable"}
	}
	return f.keep("traces", document)
}

func (f *Fake) Metrics(_ context.Context, source telemetry.Source, metrics telemetry.Metrics) error {
	document, err := telemetry.EncodeMetrics(source, metrics)
	if err != nil {
		return &otlp.Error{Code: otlp.RefusedRequest, Subject: "metrics", Reason: "unencodable"}
	}
	return f.keep("metrics", document)
}

func (f *Fake) keep(signal string, document []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.Fail != nil {
		return f.Fail
	}
	if f.Refusing {
		return &otlp.Error{Code: otlp.Failed, Subject: signal, Reason: "status_503"}
	}
	f.sent = append(f.sent, Sent{"/v1/" + signal, document})
	return nil
}

// Received is the documents accepted, in order.
func (f *Fake) Received() []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Sent{}, f.sent...)
}

// Attempts is how many exports were asked for, accepted or not.
func (f *Fake) Attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// Scenario is what the contract sends: one span and one counter.
func Scenario() (telemetry.Source, []telemetry.Span, telemetry.Metrics) {
	at := time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)
	source := telemetry.Source{Resource: []telemetry.Attribute{{Key: "service.name", Value: "console"}}, Scope: "contract"}
	spans := []telemetry.Span{{Trace: telemetry.TraceID{1}, ID: telemetry.SpanID{2}, Name: "GET /", Kind: telemetry.Server,
		Start: at, End: at.Add(time.Second)}}
	counter := telemetry.Counter{Name: "console.requests"}
	counter.Add(1, telemetry.Attribute{Key: "http.route", Value: "/"})
	return source, spans, telemetry.Metrics{Start: at, At: at.Add(time.Minute), Sums: []telemetry.Sum{counter.Sum()}}
}

// Contract is the behaviour every otlp.Exporter must have: an accepting collector is posted each signal's encoded
// document at its own path, a refusing one is a failure for each signal, and a span without identifiers is refused
// before anything is sent. open returns an exporter to a collector that accepts or refuses, and what it received.
func Contract(t *testing.T, open func(t *testing.T, refusing bool) (otlp.Exporter, func() []Sent)) {
	t.Helper()
	ctx := context.Background()
	source, spans, metrics := Scenario()
	exporter, received := open(t, false)
	if err := exporter.Traces(ctx, source, spans); err != nil {
		t.Fatalf("traces: %v", err)
	}
	if err := exporter.Metrics(ctx, source, metrics); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if err := exporter.Traces(ctx, source, []telemetry.Span{{Name: "nameless"}}); !failed(err, otlp.RefusedRequest) {
		t.Fatalf("a span without identifiers: %v", err)
	}
	traces, _ := telemetry.EncodeTraces(source, spans)
	points, _ := telemetry.EncodeMetrics(source, metrics)
	got := received()
	if len(got) != 2 || got[0].Path != "/v1/traces" || !sameJSON(got[0].Document, traces) ||
		got[1].Path != "/v1/metrics" || !sameJSON(got[1].Document, points) {
		t.Fatalf("the collector received %q", got)
	}
	refuses(t, open)
}

// refuses: a collector that refuses is a failure for each signal.
func refuses(t *testing.T, open func(t *testing.T, refusing bool) (otlp.Exporter, func() []Sent)) {
	t.Helper()
	ctx := context.Background()
	source, spans, metrics := Scenario()
	refusing, _ := open(t, true)
	if err := refusing.Traces(ctx, source, spans); !failed(err, otlp.Failed) {
		t.Fatalf("traces to a refusing collector: %v", err)
	}
	if err := refusing.Metrics(ctx, source, metrics); !failed(err, otlp.Failed) {
		t.Fatalf("metrics to a refusing collector: %v", err)
	}
}

func sameJSON(a, b []byte) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

func failed(err error, code string) bool {
	var failure *otlp.Error
	return errors.As(err, &failure) && failure.Code == code
}
