package notifysvc_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/notifysvc"
	"github.com/samclement/swhurl-platform/v2/adapters/notifysvc/notifysvctest"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/request.json")
	if err != nil {
		t.Fatal(err)
	}
	var request notifysvc.Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	for _, fake := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "fake"}[fake], func(t *testing.T) {
			notifysvctest.Contract(t, request, func(t *testing.T, status int) (notifysvc.Sender, func() notifysvctest.Sent) {
				if fake {
					f := &notifysvctest.Fake{}
					if status == 0 {
						f.Fail = &notifysvc.Error{Reason: "unreachable"}
					} else if status != 202 {
						f.Fail = &notifysvc.Error{Reason: "refused"}
					}
					return f, func() notifysvctest.Sent { return f.Sent[0] }
				}
				var got notifysvctest.Sent
				client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
					if status == 0 {
						return nil, errors.New("synthetic-private-token")
					}
					if r.Method != "POST" || r.URL.String() != "http://notify.example.test/v1/notify" || r.Header.Get("Content-Type") != "application/json" {
						t.Fatal("wrong endpoint or media type")
					}
					got.Token = r.Header.Get("Authorization")[len("Bearer "):]
					if err := json.NewDecoder(r.Body).Decode(&got.Request); err != nil {
						t.Fatal(err)
					}
					body, err := os.ReadFile("testdata/accepted.json")
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{"Location": {"http://elsewhere.test"}}, Request: r}, nil
				})}
				return notifysvc.New(client), func() notifysvctest.Sent { return got }
			})
		})
	}
}
