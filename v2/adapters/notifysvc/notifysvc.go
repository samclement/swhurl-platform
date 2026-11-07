// Package notifysvc posts an app notice to the notify service. The caller supplies the service URL and freshly
// read audience-bound token for this pass. Neither credentials, addresses nor response bodies appear in errors.
package notifysvc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Request is the existing POST /v1/notify wire document. Priority is normal or high; Click is present even empty.
type Request struct {
	App      string `json:"app"`
	Env      string `json:"env"`
	Key      string `json:"key"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority string `json:"priority"`
	Click    string `json:"click"`
}

// Sender returns nil only for 202. Other answers (including redirects) are refusal; no answer is unreachable.
type Sender interface {
	Send(ctx context.Context, address, token string, request Request) error
}

// Error has a fixed reason: invalid_request, refused or unreachable, never server or transport diagnostics.
type Error struct{ Reason string }

func (e *Error) Error() string { return "notify service: " + e.Reason }

// Service is a five-second, redirect-refusing HTTP sender.
type Service struct{ client *http.Client }

var _ Sender = (*Service)(nil)

// New uses a bounded copy of the client. HTTP is permitted for the cluster-local Service, as in Python.
func New(client *http.Client) Sender {
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = 5 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Service{&bounded}
}

// Send posts one copy, reading at most 64 KiB of the unused answer to permit connection reuse.
func (s *Service) Send(ctx context.Context, address, token string, request Request) error {
	if !ValidAddress(address) || strings.TrimSpace(token) == "" {
		return &Error{"invalid_request"}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return &Error{"invalid_request"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(address, "/")+"/v1/notify", bytes.NewReader(raw))
	if err != nil {
		return &Error{"invalid_request"}
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return &Error{"unreachable"}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode != http.StatusAccepted {
		return &Error{"refused"}
	}
	return nil
}

// ValidAddress is whether an address is a plain HTTP(S) endpoint without userinfo, query or fragment.
func ValidAddress(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == ""
}
