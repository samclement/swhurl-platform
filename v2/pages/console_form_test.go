package pages_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// form is a posted form: one value per field, unless a field holds a list.
func form(values map[string]string) map[string][]string {
	found := make(map[string][]string, len(values))
	for name, value := range values {
		found[name] = strings.Split(value, "\u0000")
		if value == "" {
			found[name] = []string{""}
		}
	}
	return found
}

// TestConsoleFormChange keeps each app form's flags and its refusals: the console decides these before a job starts, so
// the words are its own, and a value v2 would refuse is never reached.
func TestConsoleFormChange(t *testing.T) {
	declared := &pages.Inventory{Team: "shop", Allows: []string{"demo-api", "team:shop"}}
	teams := map[string]pages.TeamInfo{"shop": {}, "ops": {}}
	cases := []struct {
		name, kind string
		fields     map[string][]string
		want       pages.ConsoleFormFlags
		problem    string
	}{
		{name: "replicas and memory", kind: "scale", fields: form(map[string]string{"replicas": "3", "memory": "256Mi"}),
			want: pages.ConsoleFormFlags{Settings: []string{"replicas=3", "memoryMiB=256"}}},
		{name: "memory without Mi", kind: "scale", fields: form(map[string]string{"memory": "512"}),
			want: pages.ConsoleFormFlags{Settings: []string{"memoryMiB=512"}}},
		{name: "replicas not a number", kind: "scale", fields: form(map[string]string{"replicas": "3x"}),
			problem: "Replicas '3x' is not a whole number"},
		{name: "memory not a number", kind: "scale", fields: form(map[string]string{"memory": "lots"}),
			problem: "Memory 'lots' is not a whole number of MiB, such as 256 or 256Mi"},
		{name: "cpu is not modelled", kind: "scale", fields: form(map[string]string{"replicas": "1", "cpu": "100m"}),
			problem: "v2 does not model CPU requests or memory limits: change replicas and memory only"},
		{name: "an empty scale", kind: "scale", fields: form(map[string]string{}), problem: "change replicas or memory"},
		{name: "signed out", kind: "expose", fields: form(map[string]string{"exposure": "private"}),
			want: pages.ConsoleFormFlags{Settings: []string{"exposure=private"}}},
		{name: "no such exposure", kind: "expose", fields: form(map[string]string{"exposure": "wide-open"}),
			problem: "exposure must be one of private, authenticated-web, public"},
		{name: "a host is derived", kind: "expose", fields: form(map[string]string{"exposure": "private", "host": "x.example.com"}),
			problem: "v2 derives the address from the app name and the exposure: it does not model a host"},
		{name: "connections", kind: "connect", fields: form(map[string]string{"calls": "demo-api,other", "subscribe": "demo-api.order"}),
			want: pages.ConsoleFormFlags{Calls: []string{"demo-api", "other"}, Subscriptions: []string{"demo-api.order"}}},
		{name: "a bad call", kind: "connect", fields: form(map[string]string{"calls": "Demo"}),
			problem: "Demo: a call names an app, a subscription <app>.<event>"},
		{name: "nothing to connect", kind: "connect", fields: form(map[string]string{}),
			problem: "choose an app to call or an event to receive"},
		{name: "drop a call", kind: "disconnect", fields: form(map[string]string{"role": "calls", "app": "demo-api"}),
			want: pages.ConsoleFormFlags{DropCalls: []string{"demo-api"}}},
		{name: "drop a subscription", kind: "disconnect", fields: form(map[string]string{"role": "subscribes", "app": "demo-api", "event": "order"}),
			want: pages.ConsoleFormFlags{DropSubscriptions: []string{"demo-api.order"}}},
		{name: "a publication is not a connection", kind: "disconnect", fields: form(map[string]string{"role": "publishes"}),
			problem: "publications come from the release: v2 does not remove one on the platform"},
		{name: "nor is an egress grant", kind: "disconnect", fields: form(map[string]string{"role": "reaches"}),
			problem: "an egress grant is set with swhurl-v2 edit --set egress=, so this cannot be removed here"},
		{name: "allow and disallow", kind: "allow", fields: form(map[string]string{"add": "other", "remove": "team:shop"}),
			want: pages.ConsoleFormFlags{Allows: []string{"other"}, Disallows: []string{"team:shop"}}},
		{name: "nothing allowed", kind: "allow", fields: form(map[string]string{}),
			problem: "name an app or team:<team> to allow or stop allowing"},
		{name: "stopping a caller that is not allowed", kind: "allow", fields: form(map[string]string{"remove": "other"}),
			problem: "that changes nothing: name an app or team:<team> to allow or stop allowing"},
		{name: "a registered team", kind: "team", fields: form(map[string]string{"team": "ops"}),
			want: pages.ConsoleFormFlags{Settings: []string{"team=ops"}}},
		{name: "an unregistered team", kind: "team", fields: form(map[string]string{"team": "nobody"}),
			problem: "choose a registered team: one of ops, shop"},
		{name: "the team it has", kind: "team", fields: form(map[string]string{"team": "shop"}),
			problem: "demo already belongs to shop: that changes nothing"},
		{name: "a team name that is not one", kind: "team", fields: form(map[string]string{"team": "Sh op"}),
			problem: "choose the team that owns the app"},
		{name: "removal needs nothing", kind: "remove", fields: form(map[string]string{})},
		{name: "a promotion needs the image reviewed", kind: "promote", fields: form(map[string]string{}),
			problem: "refresh the staging app page and review the image before promotion"},
		{name: "a promotion", kind: "promote", fields: form(map[string]string{"image": "ghcr.io/x/y:1-abcdefg@sha256:abc"})},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			found, err := pages.ConsoleFormChange(one.kind, "demo", one.fields, declared, teams)
			if one.problem != "" {
				if err == nil || err.Error() != one.problem {
					t.Fatalf("refusal = %v, want %q", err, one.problem)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(found, one.want) {
				t.Fatalf("flags = %+v, want %+v", found, one.want)
			}
		})
	}
}
