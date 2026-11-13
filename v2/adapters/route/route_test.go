package route_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/route"
	"github.com/samclement/swhurl-platform/v2/adapters/route/routetest"
)

// signIn is oauth2-proxy's recorded answer to a browser that is not signed in.
type signIn struct {
	Status   int    `json:"status"`
	Location string `json:"location"`
	Body     string `json:"body"`
}

func fixture(t *testing.T) signIn {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sign-in.json"))
	var answer signIn
	if err != nil || json.Unmarshal(data, &answer) != nil {
		t.Fatalf("testdata/sign-in.json: %v", err)
	}
	return answer
}

// server is an app's route over TLS with a certificate no client trusts: / answers the recorded sign-in redirect,
// /healthz a page, and /followed counts a client that followed the redirect.
func server(t *testing.T, answer signIn, followed *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Location", answer.Location)
			w.WriteHeader(answer.Status)
			_, _ = w.Write([]byte(answer.Body))
		case "/moved":
			http.Redirect(w, r, "/followed", http.StatusFound)
		case "/followed":
			followed.Add(1)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// unreachable is an HTTPS address nothing listens on.
func unreachable(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "https://" + listener.Addr().String() + "/"
	_ = listener.Close()
	return address
}

func TestContract(t *testing.T) {
	answer := fixture(t)
	var followed atomic.Int32
	s := server(t, answer, &followed)
	scenario := routetest.Scenario{Redirecting: s.URL + "/", Serving: s.URL + "/healthz", Unreachable: unreachable(t),
		Location: answer.Location}
	routetest.Contract(t, scenario, func(*testing.T, routetest.Scenario) route.Prober { return route.New() })
}

// A redirect is the answer, never followed, and the server's certificate is not verified.
func TestRedirectIsNotFollowed(t *testing.T) {
	var followed atomic.Int32
	s := server(t, fixture(t), &followed)
	answer, err := route.New().Get(context.Background(), s.URL+"/moved")
	if err != nil || answer.Status != http.StatusFound || answer.Location != "/followed" || followed.Load() != 0 {
		t.Fatalf("%+v %v followed %d", answer, err, followed.Load())
	}
}

// A refused connection and a cancelled request are failures that name why, never an answer.
func TestNoAnswerIsAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var followed atomic.Int32
	s := server(t, fixture(t), &followed)
	for address, reason := range map[string]string{unreachable(t): "unreachable", s.URL + "/": "cancelled"} {
		call := context.Background()
		if reason == "cancelled" {
			call = ctx
		}
		_, err := route.New().Get(call, address)
		var failure *route.Error
		if !errors.As(err, &failure) || failure.Code != route.Failed || failure.Reason != reason {
			t.Fatalf("%s: %v", address, err)
		}
	}
}
