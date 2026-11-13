// Package routetest is the fake for route.Prober and the contract suite both implementations run.
package routetest

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/route"
)

// Scenario is what the contract runs against both implementations: an address that redirects to Location, one that
// answers 200, and one nothing answers.
type Scenario struct {
	Redirecting, Serving, Unreachable string
	Location                          string
}

// Fake answers each address it is given, in turn: Answers[address] is the answers it gives, the last one repeated.
// An address it has no answer for gets no answer, as an unreachable route does.
type Fake struct {
	Answers map[string][]route.Answer
	Asked   []string
}

var _ route.Prober = (*Fake)(nil)

// New returns a fake that answers nothing.
func New() *Fake { return &Fake{Answers: map[string][]route.Answer{}} }

// Get records the address and answers the next of its answers, refusing what the adapter refuses.
func (f *Fake) Get(_ context.Context, address string) (route.Answer, error) {
	f.Asked = append(f.Asked, address)
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return route.Answer{}, &route.Error{Code: route.RefusedRequest, Subject: "address", Reason: "not_an_https_url"}
	}
	answers := f.Answers[address]
	if len(answers) == 0 {
		return route.Answer{}, &route.Error{Code: route.Failed, Subject: "route", Reason: "unreachable"}
	}
	answer := answers[0]
	if len(answers) > 1 {
		f.Answers[address] = answers[1:]
	}
	return answer, nil
}

// Contract is the behaviour every route.Prober must have: a redirect is answered with its status and Location and
// not followed, a page is answered with its status, no answer is a failure, and an address that is not an HTTPS URL
// is refused before anything is sent.
func Contract(t *testing.T, scenario Scenario, open func(*testing.T, Scenario) route.Prober) {
	t.Helper()
	prober := open(t, scenario)
	ctx := context.Background()
	answer, err := prober.Get(ctx, scenario.Redirecting)
	if err != nil || answer != (route.Answer{Status: 302, Location: scenario.Location}) {
		t.Fatalf("the redirecting route: %+v %v", answer, err)
	}
	if answer, err := prober.Get(ctx, scenario.Serving); err != nil || answer != (route.Answer{Status: 200}) {
		t.Fatalf("the serving route: %+v %v", answer, err)
	}
	if _, err := prober.Get(ctx, scenario.Unreachable); !failed(err, route.Failed) {
		t.Fatalf("the unreachable route: %v", err)
	}
	for _, address := range []string{"http://smoke-web.example/", "smoke-web.example", "https:///",
		"https://user:pass@smoke-web.example/", "https://%zz/"} {
		if _, err := prober.Get(ctx, address); !failed(err, route.RefusedRequest) {
			t.Fatalf("%q: %v", address, err)
		}
	}
}

// failed is whether err is the adapter's error with this code.
func failed(err error, code string) bool {
	var failure *route.Error
	return errors.As(err, &failure) && failure.Code == code
}
