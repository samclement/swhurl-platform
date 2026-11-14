// Package otlp sends a service's own traces and metrics to an OpenTelemetry collector over OTLP/HTTP with JSON
// bodies. It decides nothing about what to send or when: each call is one export, posted once. No error names the
// endpoint or repeats what the collector answered.
package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/samclement/swhurl-platform/v2/telemetry"
)

// The two kinds of failure: an export this adapter refuses to send, and one the collector did not accept.
const (
	RefusedRequest = "refused_request"
	Failed         = "failed"
)

// Error names a code, the signal it is about and a fixed reason.
type Error struct{ Code, Subject, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Code, e.Subject, e.Reason) }

// Exporter sends one export of each signal. Nil is the collector's 2xx with nothing rejected; a partial success
// that rejected anything is a failure, since the caller cannot tell which spans or points were kept.
type Exporter interface {
	Traces(ctx context.Context, source telemetry.Source, spans []telemetry.Span) error
	Metrics(ctx context.Context, source telemetry.Source, metrics telemetry.Metrics) error
}

// timeout bounds one export; maxAnswer bounds what is read of the collector's answer.
const (
	timeout   = 10 * time.Second
	maxAnswer = 64 << 10
)

// Collector posts to one collector's OTLP/HTTP receiver.
type Collector struct {
	client   *http.Client
	endpoint string
}

var _ Exporter = (*Collector)(nil)

// New sends to endpoint, the receiver's base URL as OTEL_EXPORTER_OTLP_ENDPOINT states it (`http://<host>:4318`):
// http or https, a host, and no user, query or fragment. The signal's path is appended. The client is copied, nil
// for the default one, limited to 10 seconds and following no redirect.
func New(client *http.Client, endpoint string) (*Collector, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || !baseURL(parsed) {
		return nil, &Error{RefusedRequest, "endpoint", "not_an_http_base_url"}
	}
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = timeout
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	parsed.Path = trimSlash(parsed.Path)
	return &Collector{client: &bounded, endpoint: parsed.String()}, nil
}

// baseURL is whether a URL names an http or https server and at most a path: no user, query or fragment.
func baseURL(u *url.URL) bool {
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" &&
		!u.ForceQuery && u.Fragment == "" && u.Opaque == ""
}

func trimSlash(path string) string {
	for len(path) > 0 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	return path
}

// Traces posts the spans as one export to /v1/traces.
func (c *Collector) Traces(ctx context.Context, source telemetry.Source, spans []telemetry.Span) error {
	document, err := telemetry.EncodeTraces(source, spans)
	if err != nil {
		return &Error{RefusedRequest, "traces", "unencodable"}
	}
	return c.post(ctx, "traces", document, "rejectedSpans")
}

// Metrics posts the metrics as one export to /v1/metrics.
func (c *Collector) Metrics(ctx context.Context, source telemetry.Source, metrics telemetry.Metrics) error {
	document, err := telemetry.EncodeMetrics(source, metrics)
	if err != nil {
		return &Error{RefusedRequest, "metrics", "unencodable"}
	}
	return c.post(ctx, "metrics", document, "rejectedDataPoints")
}

// post sends one document and reads the collector's partial success: its count of what it rejected is an int64,
// which OTLP/JSON writes as text (or, from some encoders, as a number).
func (c *Collector) post(ctx context.Context, signal string, document []byte, rejectedField string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/"+signal, bytes.NewReader(document))
	if err != nil {
		return &Error{RefusedRequest, signal, "unsendable"}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return &Error{Failed, signal, failureReason(err)}
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return &Error{Failed, signal, "status_" + strconv.Itoa(response.StatusCode)}
	}
	if rejected(answer, rejectedField) {
		return &Error{Failed, signal, "partly_rejected"}
	}
	return nil
}

// rejected is whether an answer's partial success counts anything rejected. An empty or unreadable answer rejected
// nothing: the status already said the export was accepted.
func rejected(answer []byte, field string) bool {
	var document struct {
		PartialSuccess map[string]json.RawMessage `json:"partialSuccess"`
	}
	if json.Unmarshal(answer, &document) != nil {
		return false
	}
	count := bytes.Trim(document.PartialSuccess[field], `"`)
	n, err := strconv.ParseInt(string(count), 10, 64)
	return err == nil && n > 0
}

// failureReason names an export that got no answer. The error itself is never kept: it repeats the endpoint.
func failureReason(err error) string {
	var timedOut interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timedOut) && timedOut.Timeout() {
		return "timed_out"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "unreachable"
}
