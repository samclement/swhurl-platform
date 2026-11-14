package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/otlp"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/telemetry"
)

// The console's own spans are kept until the next export, at most spanLimit of them: past that a span is dropped
// and counted, so a collector that is down costs memory once, not forever. Spans go every traceEvery, metrics every
// metricsEvery, and both once more when the pod stops. An export that fails is not retried.
const (
	spanLimit    = 2048
	spanBatch    = 512
	traceEvery   = 5 * time.Second
	metricsEvery = 30 * time.Second
	messageLimit = 200
)

// consoleTelemetry is the console's traces and metrics: the spans a request makes, a counter and duration of each
// request by route and status, and of each job by action and state. A nil one records nothing, which is the console
// without OTEL_EXPORTER_OTLP_ENDPOINT.
type consoleTelemetry struct {
	exporter otlp.Exporter
	source   telemetry.Source
	now      func() time.Time
	ids      io.Reader // where span and trace identifiers come from
	log      *jsonLog  // one line when exports start or stop failing; nil writes none
	started  time.Time

	mu        sync.Mutex
	spans     []telemetry.Span
	dropped   int
	failing   bool
	requests  telemetry.Counter
	durations telemetry.Histogram
	jobs      telemetry.Counter
	jobTimes  telemetry.Histogram
}

// consoleTelemetryOf is the console's telemetry sent to endpoint (OTEL_EXPORTER_OTLP_ENDPOINT, the node's collector),
// or nil, recording nothing, when no endpoint is set. Its own failures go to out as console log lines.
func consoleTelemetryOf(endpoint string, out io.Writer) (*consoleTelemetry, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, nil
	}
	exporter, err := otlp.New(nil, strings.TrimSpace(endpoint))
	if err != nil {
		return nil, err
	}
	return newConsoleTelemetry(exporter, time.Now, consoleLog(out, time.Now)), nil
}

func newConsoleTelemetry(exporter otlp.Exporter, now func() time.Time, log *jsonLog) *consoleTelemetry {
	return &consoleTelemetry{exporter: exporter, now: now, ids: rand.Reader, log: log, started: now(),
		source: telemetry.Source{Scope: "swhurl-v2/console",
			Resource: []telemetry.Attribute{{Key: "service.name", Value: "console"}}},
		requests: telemetry.Counter{Name: "console.requests", Unit: "{request}",
			Description: "Requests the console answered, by route and status"},
		durations: telemetry.Histogram{Name: "http.server.request.duration", Unit: "s",
			Description: "How long the console took to answer a request",
			Bounds:      []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}},
		jobs: telemetry.Counter{Name: "console.jobs", Unit: "{job}",
			Description: "Console jobs that ended, by action and state"},
		jobTimes: telemetry.Histogram{Name: "console.job.duration", Unit: "s",
			Description: "How long a console job ran",
			Bounds:      []float64{1, 5, 10, 30, 60, 120, 300, 600, 900}}}
}

// liveSpan is a span that has started and not ended. Its end is recorded once.
type liveSpan struct {
	owner *consoleTelemetry
	span  telemetry.Span
}

type spanKey struct{}

// root starts a request's span: a child of the caller's when the request carries a valid traceparent (Traefik's
// span, so the edge and the console are one trace), otherwise a trace of its own.
func (t *consoleTelemetry) root(ctx context.Context, traceparent string) (context.Context, *liveSpan) {
	if t == nil {
		return ctx, nil
	}
	parent, continued := telemetry.ParseTraceparent(traceparent)
	if !continued {
		parent = telemetry.Parent{Trace: t.traceID()}
	}
	return t.begin(ctx, parent, "request", telemetry.Server)
}

// child starts a span inside the one ctx carries. Without one it starts nothing: a read made outside a request (a
// job's own work, the registry loading) is not a trace of its own.
func (t *consoleTelemetry) child(ctx context.Context, name string, attributes ...telemetry.Attribute) (context.Context, *liveSpan) {
	current, ok := ctx.Value(spanKey{}).(*liveSpan)
	if t == nil || !ok {
		return ctx, nil
	}
	ctx, span := t.begin(ctx, telemetry.Parent{Trace: current.span.Trace, Span: current.span.ID}, name, telemetry.Client)
	span.span.Attributes = attributes
	return ctx, span
}

func (t *consoleTelemetry) begin(ctx context.Context, parent telemetry.Parent, name string, kind telemetry.Kind) (context.Context, *liveSpan) {
	span := &liveSpan{owner: t, span: telemetry.Span{Trace: parent.Trace, Parent: parent.Span, ID: t.spanID(),
		Name: name, Kind: kind, Start: t.now()}}
	return context.WithValue(ctx, spanKey{}, span), span
}

// end records the span with its outcome and any attributes learned on the way. A failure's text is kept, short:
// the adapters' errors name a code and a fixed reason, never a token or a body.
func (s *liveSpan) end(err error, attributes ...telemetry.Attribute) {
	if s == nil {
		return
	}
	s.span.End = s.owner.now()
	s.span.Attributes = append(s.span.Attributes, attributes...)
	if err != nil {
		s.span.Failed, s.span.Message = true, shorten(err.Error(), messageLimit)
	}
	s.owner.keep(s.span)
}

// name renames the span once its name is known: a request's route is known only after the mux has matched it.
func (s *liveSpan) name(name string) {
	if s != nil {
		s.span.Name = name
	}
}

// ids is the span's trace and its own identifier, for the request's log line; empty without a span.
func (s *liveSpan) ids() (string, string) {
	if s == nil {
		return "", ""
	}
	return s.span.Trace.String(), s.span.ID.String()
}

func (t *consoleTelemetry) keep(span telemetry.Span) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) >= spanLimit {
		t.dropped++
		return
	}
	t.spans = append(t.spans, span)
}

// request counts one answered request.
func (t *consoleTelemetry) request(method, route string, status int, took time.Duration) {
	if t == nil {
		return
	}
	attributes := []telemetry.Attribute{{Key: "http.request.method", Value: method},
		{Key: "http.route", Value: route}, {Key: "http.response.status_code", Value: status}}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests.Add(1, attributes...)
	t.durations.Record(took.Seconds(), attributes...)
}

// job counts a job that ended and records its span, a trace of its own that started when the job did. A job's
// start writes nothing here: its end carries how long it ran.
func (t *consoleTelemetry) job(event orchestration.JobEvent) {
	if t == nil || event.State == "started" {
		return
	}
	end := t.now()
	attributes := []telemetry.Attribute{{Key: "console.job.action", Value: event.Action},
		{Key: "console.job.state", Value: event.State}}
	span := telemetry.Span{Trace: t.traceID(), ID: t.spanID(), Name: "job " + event.Action, Kind: telemetry.Internal,
		Start: end.Add(-event.Duration), End: end, Failed: event.State != "succeeded",
		Attributes: append(attributes, telemetry.Attribute{Key: "console.job.target", Value: event.Target},
			telemetry.Attribute{Key: "console.job.id", Value: event.ID})}
	if span.Failed {
		span.Message = event.State
	}
	t.keep(span)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.jobs.Add(1, attributes...)
	t.jobTimes.Record(event.Duration.Seconds(), attributes...)
}

// run exports until ctx ends: spans every traceEvery and metrics every metricsEvery. The last export is the
// caller's, through flush, once the server has stopped answering.
func (t *consoleTelemetry) run(ctx context.Context) {
	if t == nil {
		return
	}
	spans, metrics := time.NewTicker(traceEvery), time.NewTicker(metricsEvery)
	defer spans.Stop()
	defer metrics.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-spans.C:
			t.exportSpans(ctx)
		case <-metrics.C:
			t.exportMetrics(ctx)
		}
	}
}

// flush exports what is kept now: the spans, then the metrics.
func (t *consoleTelemetry) flush(ctx context.Context) {
	if t == nil {
		return
	}
	t.exportSpans(ctx)
	t.exportMetrics(ctx)
}

func (t *consoleTelemetry) exportSpans(ctx context.Context) {
	t.mu.Lock()
	spans, dropped := t.spans, t.dropped
	t.spans, t.dropped = nil, 0
	t.mu.Unlock()
	if dropped > 0 && t.log != nil {
		t.log.writeFields("WARN", fmt.Sprintf("Dropped %d spans: the buffer was full", dropped),
			logField{"event", "telemetry"}, logField{"dropped", dropped})
	}
	if len(spans) == 0 {
		return
	}
	var err error
	for len(spans) > 0 && err == nil {
		batch := spans[:min(len(spans), spanBatch)]
		spans = spans[len(batch):]
		err = t.exporter.Traces(ctx, t.source, batch)
	}
	t.outcome(err)
}

func (t *consoleTelemetry) exportMetrics(ctx context.Context) {
	t.mu.Lock()
	metrics := telemetry.Metrics{Start: t.started, At: t.now(),
		Sums:       []telemetry.Sum{t.requests.Sum(), t.jobs.Sum()},
		Histograms: []telemetry.Distribution{t.durations.Distribution(), t.jobTimes.Distribution()}}
	t.mu.Unlock()
	t.outcome(t.exporter.Metrics(ctx, t.source, metrics))
}

// outcome writes a line when exports start failing and when they recover, not one per export.
func (t *consoleTelemetry) outcome(err error) {
	t.mu.Lock()
	changed := (err != nil) != t.failing
	t.failing = err != nil
	t.mu.Unlock()
	switch {
	case t.log == nil || !changed:
	case err != nil:
		t.log.writeFields("WARN", "Telemetry export failed: "+err.Error(), logField{"event", "telemetry"})
	default:
		t.log.writeFields("INFO", "Telemetry export recovered", logField{"event", "telemetry"})
	}
}

// traceID and spanID are random identifiers; the all-zero one OTLP refuses is never answered.
func (t *consoleTelemetry) traceID() telemetry.TraceID {
	var id telemetry.TraceID
	_, _ = io.ReadFull(t.ids, id[:])
	if id.IsZero() {
		id[15] = 1
	}
	return id
}

func (t *consoleTelemetry) spanID() telemetry.SpanID {
	var id telemetry.SpanID
	_, _ = io.ReadFull(t.ids, id[:])
	if id.IsZero() {
		id[7] = 1
	}
	return id
}

func shorten(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "…"
}
