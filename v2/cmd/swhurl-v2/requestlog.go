package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/telemetry"
)

// requestLine is the message of every request's line; its fields say what was asked and how it ended.
const requestLine = "Request completed"

// logRequests writes one line per answered request: its method, the route pattern the mux matched, its status and how
// long it took. The path, query, headers and body are never written, so a token or a form value cannot reach the log.
// Probe and asset requests write none: they say nothing about the service, and a probe every few seconds would bury
// the requests an operator made. With telemetry, each request is also a span, whose trace the line names, and is
// counted by route and status.
func logRequests(log *jsonLog, trace *consoleTelemetry, next http.Handler) http.Handler {
	if log == nil && trace == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		ctx, span := trace.root(r.Context(), r.Header.Get("traceparent"))
		// The mux sets the pattern on the request it is given, so the line reads it from that one.
		r = r.WithContext(ctx)
		next.ServeHTTP(recorder, r)
		took := time.Since(started)
		route, status := r.Pattern, recorder.code()
		if route == "" {
			route = "unmatched"
		}
		path := strings.TrimPrefix(route, r.Method+" ")
		span.name(r.Method + " " + path)
		span.end(statusFailure(status), telemetry.Attribute{Key: "http.request.method", Value: r.Method},
			telemetry.Attribute{Key: "http.route", Value: path}, telemetry.Attribute{Key: "http.response.status_code", Value: status})
		trace.request(r.Method, path, status, took)
		writeRequest(log, span, r.Method, route, status, took)
	})
}

// writeRequest is a request's line, naming its trace when it has one.
func writeRequest(log *jsonLog, span *liveSpan, method, route string, status int, took time.Duration) {
	if log == nil {
		return
	}
	level := "INFO"
	if status >= http.StatusInternalServerError {
		level = "ERROR"
	}
	fields := []logField{{"event", "request"}, {"method", method}, {"route", route}, {"status", status},
		{"duration_ms", took.Milliseconds()}}
	if traceID, spanID := span.ids(); traceID != "" {
		fields = append(fields, logField{"trace_id", traceID}, logField{"span_id", spanID})
	}
	log.writeFields(level, requestLine, fields...)
}

// statusFailure is a server error as a span's failure; anything below 500 is the client's outcome, not the span's.
func statusFailure(status int) error {
	if status >= http.StatusInternalServerError {
		return fmt.Errorf("status %d", status)
	}
	return nil
}

// statusRecorder is the status a handler answered, kept for its request's line. Flush passes through, so a streamed
// page still streams, and Unwrap lets http.ResponseController reach the writer underneath.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

func (r *statusRecorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// code is the status to report: a handler that wrote nothing answered 200.
func (r *statusRecorder) code() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}
