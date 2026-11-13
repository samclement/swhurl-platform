// Package route asks an app's public route what it answers: one HTTPS GET that follows no redirect and verifies no
// certificate, as `curl -k` did, so a route whose certificate is still being issued answers all the same. It reads;
// it sends no credential and no body.
package route

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// The two kinds of failure: a request this adapter refuses to make, and one that got no answer.
const (
	RefusedRequest = "refused_request"
	Failed         = "failed"
)

// Error names a code, the subject it is about and a fixed reason.
type Error struct{ Code, Subject, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Code, e.Subject, e.Reason) }

// Answer is what a route answered: its status and, for a redirect, where to.
type Answer struct {
	Status   int
	Location string
}

// Prober asks one route. A redirect is an answer, never followed; no answer is an error.
type Prober interface {
	Get(ctx context.Context, address string) (Answer, error)
}

// timeout bounds one request, connection and answer included; maxAnswer bounds what is read of the body, which only
// lets the connection be reused.
const (
	timeout   = 10 * time.Second
	maxAnswer = 64 << 10
)

// Routes asks over one HTTP client.
type Routes struct{ client *http.Client }

var _ Prober = (*Routes)(nil)

// New asks with a client limited to 10 seconds that follows no redirect and does not verify the server's
// certificate: the question is what the route answers, not whether its certificate has been issued yet.
func New() Prober {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // curl -k: see the package comment
	return &Routes{client: &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Get asks the address once and answers its status and Location header.
func (r *Routes) Get(ctx context.Context, address string) (Answer, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return Answer{}, &Error{RefusedRequest, "address", "not_an_https_url"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return Answer{}, &Error{RefusedRequest, "address", "not_an_https_url"}
	}
	response, err := r.client.Do(request)
	if err != nil {
		return Answer{}, &Error{Failed, "route", failureReason(err)}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAnswer))
	return Answer{Status: response.StatusCode, Location: response.Header.Get("Location")}, nil
}

// failureReason names a request that got no answer.
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
