// Package ntfy publishes one notification to one ntfy topic, over ntfy's JSON publish API. A topic's address is the
// credential that lets anyone publish to it, so no error this adapter returns names the address, the topic or what
// the server answered.
package ntfy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/notify"
)

// The two kinds of failure: a publication this adapter refuses to send, and one the server did not accept.
const (
	RefusedRequest = "refused_request"
	Failed         = "failed"
)

// Error names a code, the subject it is about and a fixed reason. It never carries the address, the topic or a
// response body.
type Error struct{ Code, Subject, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Code, e.Subject, e.Reason) }

// Publisher publishes one publication to one topic address, an HTTPS URL whose path is the topic
// (`https://ntfy.sh/<topic>`). Nil is the server's 2xx; anything else, a redirect included, is an error.
type Publisher interface {
	Publish(ctx context.Context, address string, publication notify.Publication) error
}

// timeout bounds one publication, connection and answer included; maxAnswer bounds what is read of the answer,
// which only lets the connection be reused.
const (
	timeout   = 10 * time.Second
	maxAnswer = 64 << 10
)

// Topics publishes over one HTTP client.
type Topics struct{ client *http.Client }

var _ Publisher = (*Topics)(nil)

// New publishes with a copy of the client, nil for the default one, limited to 10 seconds and following no
// redirect: a redirect is the server's answer, so the publication and its topic go only to the address's server.
func New(client *http.Client) Publisher {
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = timeout
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Topics{client: &bounded}
}

// Publish posts the publication's document, which names the topic, to the root of the address's server.
func (t *Topics) Publish(ctx context.Context, address string, publication notify.Publication) error {
	server, topic, err := target(address)
	if err != nil {
		return err
	}
	document, err := publication.Document(topic)
	if err != nil {
		return &Error{RefusedRequest, "publication", "unencodable"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server, bytes.NewReader(document))
	if err != nil {
		return &Error{RefusedRequest, "address", "not_an_https_topic"}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := t.client.Do(request)
	if err != nil {
		return &Error{Failed, "publish", failureReason(err)}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAnswer))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return &Error{Failed, "publish", "status_" + strconv.Itoa(response.StatusCode)}
	}
	return nil
}

// target is an address's server root and topic: an HTTPS URL with a host and a one-segment path, and nothing else.
func target(address string) (string, string, error) {
	parsed, err := url.Parse(address)
	if err != nil || !plain(parsed) {
		return "", "", &Error{RefusedRequest, "address", "not_an_https_topic"}
	}
	topic := strings.Trim(parsed.EscapedPath(), "/")
	if topic == "" || strings.Contains(topic, "/") {
		return "", "", &Error{RefusedRequest, "address", "not_an_https_topic"}
	}
	return "https://" + parsed.Host + "/", topic, nil
}

// plain is whether a URL names an HTTPS server and a path only: no user, query or fragment.
func plain(u *url.URL) bool {
	return u.Scheme == "https" && u.Host != "" && u.User == nil && u.Opaque == "" && u.RawQuery == "" &&
		!u.ForceQuery && u.Fragment == ""
}

// failureReason names a publication that got no answer. The error itself is never kept: it repeats the address.
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
