package main

import (
	"net/http"
	"strings"
	"time"
)

// requestLine is the message of every request's line; its fields say what was asked and how it ended.
const requestLine = "Request completed"

// logRequests writes one line per answered request: its method, the route pattern the mux matched, its status and how
// long it took. The path, query, headers and body are never written, so a token or a form value cannot reach the log.
// Probe and asset requests write none: they say nothing about the service, and a probe every few seconds would bury
// the requests an operator made.
func logRequests(log *jsonLog, next http.Handler) http.Handler {
	if log == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		level := "INFO"
		if recorder.status >= http.StatusInternalServerError {
			level = "ERROR"
		}
		log.writeFields(level, requestLine,
			logField{"event", "request"}, logField{"method", r.Method}, logField{"route", route},
			logField{"status", recorder.code()}, logField{"duration_ms", time.Since(started).Milliseconds()})
	})
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
