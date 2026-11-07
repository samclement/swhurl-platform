// Package notifysvctest provides a captured sender and the shared HTTP/fake contract.
package notifysvctest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/notifysvc"
)

// Sent is one offered copy, including its destination and synthetic test token.
type Sent struct {
	Address, Token string
	Request        notifysvc.Request
}

// Fake answers with a configured failure or acceptance, recording every offered copy.
type Fake struct {
	Sent []Sent
	Fail error
}

var _ notifysvc.Sender = (*Fake)(nil)

func (f *Fake) Send(ctx context.Context, address, token string, request notifysvc.Request) error {
	if !notifysvc.ValidAddress(address) || strings.TrimSpace(token) == "" {
		return &notifysvc.Error{Reason: "invalid_request"}
	}
	if ctx.Err() != nil {
		return &notifysvc.Error{Reason: "unreachable"}
	}
	f.Sent = append(f.Sent, Sent{address, strings.TrimSpace(token), request})
	return f.Fail
}

// Contract proves the exact JSON body and bearer header are sent, 202 alone accepts, errors expose no credentials,
// and malformed destination or empty token is refused. open supplies a sender and its captured accepted request.
func Contract(t *testing.T, request notifysvc.Request, open func(*testing.T, int) (notifysvc.Sender, func() Sent)) {
	t.Helper()
	ctx := context.Background()
	for _, status := range []int{202, 200, 403, 503, 302, 0} {
		sender, received := open(t, status)
		err := sender.Send(ctx, "http://notify.example.test/", "synthetic-private-token\n", request)
		if (err == nil) != (status == 202) {
			t.Fatalf("status %d: %v", status, err)
		}
		checkSafeError(t, err)
		if status == 202 {
			got := received()
			wantRaw, _ := json.Marshal(request)
			gotRaw, _ := json.Marshal(got.Request)
			if got.Token != "synthetic-private-token" || string(wantRaw) != string(gotRaw) {
				t.Fatalf("wire: %+v", got)
			}
		}
		if err := sender.Send(ctx, "http://notify.example.test/", "", request); err == nil {
			t.Fatal("empty token accepted")
		}
		if err := sender.Send(ctx, "http://user:secret@notify.example.test/", "t", request); err == nil {
			t.Fatal("userinfo accepted")
		}
	}
}

func checkSafeError(t *testing.T, err error) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), "notify.example")) {
		t.Fatalf("unsafe error: %v", err)
	}
}
