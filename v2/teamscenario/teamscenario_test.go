package teamscenario

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var demo = map[string]App{
	"reports":  {Team: "insights", Calls: []string{"orders"}},
	"shopper":  {Team: "insights", Calls: []string{"orders"}},
	"orders":   {Team: "commerce", Publishes: []string{"paid", "created"}},
	"notifier": {Team: "payments", Subscriptions: []string{"orders.created"}},
	"audit":    {Team: "payments", Subscriptions: []string{"orders.created", "orders.paid"}},
}

var chain = Chain{Caller: "reports", Callee: "orders", Event: "created", Subscriber: "notifier"}

func TestChooseTakesTheFirstCalleeByNameWithItsFirstCallerAndSubscriber(t *testing.T) {
	got, err := Choose("staging", demo)
	// orders publishes paid first, and audit, the first subscriber by name, takes it.
	if want := (Chain{Caller: "reports", Callee: "orders", Event: "paid", Subscriber: "audit"}); err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	// An earlier callee by name wins once it has a caller and a subscribed event of its own.
	apps := map[string]App{"billing": {Publishes: []string{"due"}}}
	for name, app := range demo {
		apps[name] = app
	}
	apps["notifier"] = App{Calls: []string{"billing"}, Subscriptions: []string{"billing.due", "orders.created"}}
	if got, err := Choose("staging", apps); err != nil || got != (Chain{Caller: "notifier", Callee: "billing", Event: "due", Subscriber: "notifier"}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestChooseRefusesWithoutAChainOrWithANameAQueryCannotHold(t *testing.T) {
	for name, apps := range map[string]map[string]App{
		"none":                     {},
		"no caller":                {"orders": demo["orders"], "notifier": demo["notifier"]},
		"another app's event":      {"reports": demo["reports"], "orders": demo["orders"], "notifier": {Subscriptions: []string{"billing.created"}}},
		"event it doesn't publish": {"reports": demo["reports"], "orders": {Publishes: []string{"paid"}}, "notifier": demo["notifier"]},
	} {
		_, err := Choose("prod", apps)
		var preflight *Preflight
		if !errors.As(err, &preflight) || !strings.HasPrefix(err.Error(), "no prod instances form a chain: ") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, apps := range []map[string]App{
		{"reports": {Calls: []string{"or'ders"}}, "or'ders": {Publishes: []string{"created"}}, "notifier": {Subscriptions: []string{"or'ders.created"}}},
		{"reports": {Calls: []string{"orders"}}, "orders": {Publishes: []string{"c'"}}, "notifier": {Subscriptions: []string{"orders.c'"}}},
	} {
		var preflight *Preflight
		if _, err := Choose("staging", apps); !errors.As(err, &preflight) || !strings.Contains(err.Error(), "cannot go into a query") {
			t.Fatalf("got %v", err)
		}
	}
}

// The queries are the Python test's, byte for byte.
func TestQueriesAreThePythonTestsQueries(t *testing.T) {
	for got, want := range map[string]string{
		chain.TraceQuery():       "SELECT TraceId, groupBitOr((ServiceName = 'orders' AND SpanKind = 'Producer') + (ServiceName = 'notifier' AND SpanKind = 'Consumer' AND SpanName = 'process orders.created') * 2) AS seen FROM otel_traces WHERE Timestamp > now() - INTERVAL 10 MINUTE AND ServiceName IN ('orders', 'notifier') GROUP BY TraceId HAVING countIf(ServiceName = 'orders' AND SpanKind = 'Server' AND SpanAttributes['caller.type'] = 'app' AND SpanAttributes['caller.id'] = 'reports') > 0 ORDER BY seen = 3 DESC, max(Timestamp) DESC LIMIT 1 FORMAT TSV",
		chain.SpansQuery("ab12"): "SELECT ServiceName, ResourceAttributes['swhurl.team'] FROM otel_traces WHERE TraceId = 'ab12' AND ServiceName IN ('reports', 'orders', 'notifier') FORMAT TSV",
		chain.LogQuery("ab12"):   "SELECT count() FROM otel_logs WHERE TraceId = 'ab12' AND ServiceName = 'orders' AND LogAttributes['caller.type'] = 'app' AND LogAttributes['caller.id'] = 'reports' FORMAT TSV",
	} {
		if got != want {
			t.Fatalf("got  %s\nwant %s", got, want)
		}
	}
}

func TestServedJudgesTheTraceAndItsPublishAndProcessing(t *testing.T) {
	for answer, want := range map[string][]Line{
		"ab12\t3\n": {{OK, "orders served reports in trace ab12"}, {OK, "orders published orders.created and notifier processed it, in the same trace"}},
		"ab12\t1\n": {{OK, "orders served reports in trace ab12"}, {Bad, `no publish span from orders or no "process orders.created" span from notifier in the trace`}},
		"\n":        {{Bad, "no trace in the last 10 minutes in which orders served reports: does anything drive reports?"}},
		"x'y\t3\n":  {{Bad, `ClickHouse answered a trace ID that is not hex: "x'y"`}},
	} {
		trace, lines := chain.Served(answer)
		if !reflect.DeepEqual(lines, want) || (trace == "ab12") != (len(want) == 2) {
			t.Fatalf("%q: %q %+v", answer, trace, lines)
		}
	}
}

func TestTeamsAreEachAppsRecordedTeamExactly(t *testing.T) {
	lines := chain.Teams(demo, "reports\tplatform\norders\tcommerce\norders\tcommerce\n")
	want := []Line{{Bad, "reports's spans carry team ['platform'], its records say insights"}, {OK, "orders's spans carry its team: commerce"},
		{Bad, "notifier's spans carry team none, its records say payments"}}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("%+v", lines)
	}
	if lines := chain.Teams(demo, "orders\tsales\norders\tcommerce\n"); lines[1] != (Line{Bad, "orders's spans carry team ['commerce', 'sales'], its records say commerce"}) {
		t.Fatalf("%+v", lines[1])
	}
}

func TestLoggedAndTheReport(t *testing.T) {
	for answer, passed := range map[string]bool{"2\n": true, "0\n": false, "": false} {
		if lines := chain.Logged("ab12", answer); Passes(lines) != passed || lines[1] != (Line{Info, "In HyperDX, search for trace ab12"}) {
			t.Fatalf("%q: %+v", answer, lines)
		}
	}
	lines := []Line{{Section, chain.Section("staging")}, {OK, "a"}, {Info, "b"}}
	if got := Text(lines); got != "\n== The newest trace in which orders served reports (staging, last 10 minutes) ==\n[OK] a\n[INFO] b\n\nTeam scenario passed." {
		t.Fatalf("%q", got)
	}
	if got := Text(append(lines, QueryFailed("exit 1"))); strings.Contains(got, Passed) || !strings.HasSuffix(got, "[BAD] ClickHouse query failed: exit 1") {
		t.Fatalf("%q", got)
	}
}
