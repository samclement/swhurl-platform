package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/adapters/otlp/otlptest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// counting is an identifier source that never repeats: each byte read is the next value.
type counting struct{ next byte }

func (c *counting) Read(out []byte) (int, error) {
	for i := range out {
		c.next++
		out[i] = c.next
	}
	return len(out), nil
}

// testTelemetry records into a fake collector, with a clock that moves a millisecond each time it is read.
func testTelemetry(log *jsonLog) (*consoleTelemetry, *otlptest.Fake) {
	fake := &otlptest.Fake{}
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	trace := newConsoleTelemetry(fake, func() time.Time { at = at.Add(time.Millisecond); return at }, log)
	trace.ids = &counting{}
	return trace, fake
}

// exported is the spans and metrics a collector received, decoded far enough to read.
type exported struct {
	spans   []exportedSpan
	metrics map[string][]map[string]any // a metric's data points, by name
}

type exportedSpan struct {
	TraceID, SpanID, ParentSpanID, Name string
	Kind                                int
	Attributes                          []struct {
		Key   string
		Value map[string]any
	}
	Status *struct{ Code int }
}

func (s exportedSpan) attribute(key string) any {
	for _, a := range s.Attributes {
		if a.Key == key {
			for _, v := range a.Value {
				return v
			}
		}
	}
	return nil
}

func received(t *testing.T, fake *otlptest.Fake) exported {
	t.Helper()
	got := exported{metrics: map[string][]map[string]any{}}
	for _, sent := range fake.Received() {
		var document struct {
			ResourceSpans []struct {
				ScopeSpans []struct{ Spans []exportedSpan }
			}
			ResourceMetrics []struct {
				ScopeMetrics []struct {
					Metrics []struct {
						Name      string
						Sum       struct{ DataPoints []map[string]any }
						Histogram struct{ DataPoints []map[string]any }
					}
				}
			}
		}
		if err := json.Unmarshal(sent.Document, &document); err != nil {
			t.Fatal(err)
		}
		for _, r := range document.ResourceSpans {
			got.spans = append(got.spans, r.ScopeSpans[0].Spans...)
		}
		for _, r := range document.ResourceMetrics {
			for _, m := range r.ScopeMetrics[0].Metrics {
				got.metrics[m.Name] = append(m.Sum.DataPoints, m.Histogram.DataPoints...)
			}
		}
	}
	return got
}

func (e exported) named(name string) []exportedSpan {
	var found []exportedSpan
	for _, span := range e.spans {
		if span.Name == name {
			found = append(found, span)
		}
	}
	return found
}

// A page is one trace: the request's server span, continuing the caller's traceparent, with the cluster's and the
// pinned tree's reads, the GitHub calls inside them and the template's render as its descendants (the empty cluster
// makes the page an error; its shape is what is checked). The request's line names the trace, and
// the request is counted by route and status. The health probe makes no span.
func TestAPageIsOneTraceWithItsReadsInside(t *testing.T) {
	var out bytes.Buffer
	trace, fake := testTelemetry(nil)
	server, _ := githubServer(t)
	server.telemetry, server.requests, server.identity = trace, requestLogger(&out, "console"), devIdentity
	server.github = trace.traceGitHub(server.github)
	cluster := kubetest.New(kubetest.Answers{})
	server.reader, server.collector = trace.traceReader(cluster), trace.traceCollector(cluster)
	server.pin(trace.traceSource(orchestration.ConsoleSourceOf{Console: server.github, Repository: "owner/repo", Token: "tok"}))
	handler := logRequests(server.requests, trace, server.identityMiddleware(server.routes()))
	request := httptest.NewRequest("GET", "/catalogue", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	trace.flush(context.Background())

	got := received(t, fake)
	roots := got.named("GET /catalogue")
	if len(roots) != 1 || len(got.named("GET /healthz")) != 0 {
		t.Fatalf("spans %+v", got.spans)
	}
	root := roots[0]
	if root.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || root.ParentSpanID != "00f067aa0ba902b7" || root.Kind != 2 ||
		root.attribute("http.route") != "/catalogue" || root.attribute("http.response.status_code") == nil {
		t.Fatalf("root %+v", root)
	}
	parents := map[string]string{}
	for _, span := range got.spans {
		if span.TraceID != root.TraceID {
			t.Fatalf("%s is in another trace", span.Name)
		}
		parents[span.SpanID] = span.ParentSpanID
	}
	tree, tarball, render := got.named("source.Tree"), got.named("github.Tarball"), got.named("render")
	if cluster := got.named("kube.ListNamespace"); len(cluster) == 0 || cluster[0].ParentSpanID != root.SpanID {
		t.Fatalf("spans %+v", got.spans)
	}
	if len(tree) != 1 || len(tarball) != 1 || len(render) != 1 || tree[0].ParentSpanID != root.SpanID ||
		tarball[0].ParentSpanID != tree[0].SpanID || render[0].ParentSpanID != root.SpanID ||
		render[0].attribute("console.template") != "error.html" {
		t.Fatalf("spans %+v", got.spans)
	}
	if tarball[0].attribute("github.repository") != "owner/repo" || strings.Contains(string(fake.Received()[0].Document), "tok\"") {
		t.Fatalf("tarball %+v", tarball[0])
	}
	logged := line(t, out.String())
	if logged["trace_id"] != root.TraceID || logged["span_id"] != root.SpanID {
		t.Fatalf("line %v", logged)
	}
	counted := got.metrics["console.requests"]
	if len(counted) != 1 || counted[0]["asInt"] != "1" || len(got.metrics["http.server.request.duration"]) != 1 {
		t.Fatalf("metrics %v", got.metrics)
	}
}

// Without a traceparent a request starts its own trace; a server error is a failed span.
func TestARequestWithoutACallerStartsItsOwnTrace(t *testing.T) {
	trace, fake := testTelemetry(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /broken", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	request := httptest.NewRequest("GET", "/broken", nil)
	request.Header.Set("traceparent", "garbage")
	logRequests(nil, trace, mux).ServeHTTP(httptest.NewRecorder(), request)
	logRequests(nil, trace, mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/nowhere", nil))
	trace.flush(context.Background())
	got := received(t, fake)
	broken, unmatched := got.named("GET /broken"), got.named("POST unmatched")
	if len(broken) != 1 || broken[0].ParentSpanID != "" || broken[0].Status == nil || broken[0].Status.Code != 2 ||
		len(unmatched) != 1 || unmatched[0].Status != nil || unmatched[0].TraceID == broken[0].TraceID {
		t.Fatalf("spans %+v", got.spans)
	}
}

// A read outside any request (a job's own work) records no span, and nothing at all is recorded without telemetry.
func TestAReadOutsideARequestIsNotASpan(t *testing.T) {
	trace, fake := testTelemetry(nil)
	source := trace.traceSource(localSource{})
	if _, err := source.Revision(context.Background()); err != nil {
		t.Fatal(err)
	}
	trace.flush(context.Background())
	if spans := received(t, fake).spans; len(spans) != 0 {
		t.Fatalf("spans %+v", spans)
	}
	var none *consoleTelemetry
	if none.traceSource(localSource{}) != (localSource{}) || none.traceReader(nil) != nil {
		t.Fatal("a nil telemetry decorated a read")
	}
	none.flush(context.Background())
	none.job(orchestration.JobEvent{State: "failed"})
}

type localSource struct{}

func (localSource) Revision(context.Context) (string, error) { return "abc", nil }
func (localSource) Tree(context.Context, string) (map[string][]byte, error) {
	return nil, errors.New("not here")
}
func (localSource) Files(context.Context, string, []string) (map[string][]byte, error) {
	return nil, nil
}

// A read that fails is a failed span whose message is the adapter's error, short.
func TestAFailedReadIsAFailedSpan(t *testing.T) {
	trace, fake := testTelemetry(nil)
	ctx, root := trace.root(context.Background(), "")
	if _, err := trace.traceSource(localSource{}).Tree(ctx, "abc"); err == nil {
		t.Fatal("the tree was read")
	}
	root.end(nil)
	trace.flush(context.Background())
	tree := received(t, fake).named("source.Tree")
	if len(tree) != 1 || tree[0].Status == nil || tree[0].attribute("vcs.ref.head.revision") != "abc" {
		t.Fatalf("spans %+v", received(t, fake).spans)
	}
	if !strings.Contains(string(fake.Received()[0].Document), `"message":"not here"`) {
		t.Fatalf("%s", fake.Received()[0].Document)
	}
}

// A job that ended is one span of its own and is counted by action and state; its start records nothing.
func TestAJobThatEndedIsASpanAndCounted(t *testing.T) {
	trace, fake := testTelemetry(nil)
	trace.job(orchestration.JobEvent{ID: 68, Action: "scale", Target: "web/prod", State: "started"})
	trace.job(orchestration.JobEvent{ID: 68, Action: "scale", Target: "web/prod", State: "failed", Duration: 3 * time.Second})
	trace.flush(context.Background())
	got := received(t, fake)
	jobs := got.named("job scale")
	if len(got.spans) != 1 || len(jobs) != 1 || jobs[0].Status == nil || jobs[0].attribute("console.job.id") != "68" ||
		jobs[0].attribute("console.job.target") != "web/prod" {
		t.Fatalf("spans %+v", got.spans)
	}
	counted := got.metrics["console.jobs"]
	if len(counted) != 1 || counted[0]["asInt"] != "1" || got.metrics["console.job.duration"][0]["sum"] != float64(3) {
		t.Fatalf("metrics %v", got.metrics)
	}
}

// The buffer holds spanLimit spans and counts the rest; exports go in batches. A collector that fails writes one
// line when it starts failing and one when it recovers, not one per export.
func TestTheBufferIsBoundedAndFailuresAreLoggedOnce(t *testing.T) {
	var out bytes.Buffer
	trace, fake := testTelemetry(requestLogger(&out, "console"))
	ctx, _ := trace.root(context.Background(), "")
	for range spanLimit + 3 {
		_, span := trace.child(ctx, "kube.Read")
		span.end(nil)
	}
	trace.exportSpans(context.Background())
	if attempts := fake.Attempts(); attempts != spanLimit/spanBatch || !strings.Contains(out.String(), "Dropped 3 spans") {
		t.Fatalf("%d exports, log %s", attempts, out.String())
	}
	out.Reset()
	fake.Refusing = true
	for range 3 {
		trace.flush(context.Background())
	}
	if lines := strings.Count(out.String(), "\n"); lines != 1 || !strings.Contains(out.String(), "Telemetry export failed: failed: metrics: status_503") {
		t.Fatalf("log %s", out.String())
	}
	out.Reset()
	fake.Refusing = false
	trace.flush(context.Background())
	if !strings.Contains(out.String(), "Telemetry export recovered") {
		t.Fatalf("log %s", out.String())
	}
}

// The endpoint is the environment's: none records nothing, and one that is not an http base URL stops the console.
func TestTheEndpointComesFromTheEnvironment(t *testing.T) {
	if trace, err := consoleTelemetryOf(" ", nil); trace != nil || err != nil {
		t.Fatalf("%v %v", trace, err)
	}
	if _, err := consoleTelemetryOf("10.0.0.1:4318", nil); err == nil {
		t.Fatal("an endpoint without a scheme was accepted")
	}
	if trace, err := consoleTelemetryOf("http://10.0.0.1:4318", nil); trace == nil || err != nil {
		t.Fatalf("%v %v", trace, err)
	}
}

// The server wraps what it reads with when it records telemetry: the cluster, GitHub and the pinned tree's source.
func TestTheServerTracesItsReads(t *testing.T) {
	trace, _ := testTelemetry(nil)
	cluster := kubetest.New(kubetest.Answers{})
	server, err := newConsoleServer(consoleConfig{Telemetry: trace, Reader: cluster, Collector: cluster, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	_, reader := server.reader.(tracedReader)
	_, collector := server.collector.(tracedCollector)
	_, github := server.github.(tracedGitHub)
	_, apps := server.appConsole.(tracedGitHub)
	_, source := server.bundle.Source.(tracedSource)
	if !reader || !collector || !github || !apps || !source || server.telemetry != trace {
		t.Fatalf("reader %v, collector %v, github %v, app repositories %v, source %v", reader, collector, github, apps, source)
	}
	plain, err := newConsoleServer(consoleConfig{Reader: cluster, Collector: cluster, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, traced := plain.reader.(tracedReader); traced {
		t.Fatal("a server without telemetry traced its reads")
	}
}
