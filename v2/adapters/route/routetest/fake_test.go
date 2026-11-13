package routetest_test

import (
	"context"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/route"
	"github.com/samclement/swhurl-platform/v2/adapters/route/routetest"
)

func TestContract(t *testing.T) {
	scenario := routetest.Scenario{Redirecting: "https://smoke-web.example/", Serving: "https://smoke-web.example/healthz",
		Unreachable: "https://gone.example/", Location: "https://accounts.google.com/o/oauth2/auth"}
	routetest.Contract(t, scenario, func(t *testing.T, scenario routetest.Scenario) route.Prober {
		fake := routetest.New()
		fake.Answers[scenario.Redirecting] = []route.Answer{{Status: 302, Location: scenario.Location}}
		fake.Answers[scenario.Serving] = []route.Answer{{Status: 200}}
		return fake
	})
}

// The fake gives an address's answers in turn, repeating the last, and records every address it was asked.
func TestFakeAnswersInTurn(t *testing.T) {
	fake := routetest.New()
	fake.Answers["https://a.example/"] = []route.Answer{{Status: 503}, {Status: 302, Location: "https://x/"}}
	var got []int
	for range 3 {
		answer, err := fake.Get(context.Background(), "https://a.example/")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, answer.Status)
	}
	if len(fake.Asked) != 3 || got[0] != 503 || got[1] != 302 || got[2] != 302 {
		t.Fatalf("%v %v", got, fake.Asked)
	}
}
