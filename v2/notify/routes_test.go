package notify_test

import (
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/notify"
)

// routes is the routes document as platform/notify/routes.json and the Python tests write it.
const routes = `{"channels": {}, "teams": {"payments": ["payments-phone", "payments-desk"]}}`

// test_an_app_without_a_team_channel_or_namespace_is_accepted_for_no_channel: a team the routes do not name, and a
// namespace without a team label or that does not exist, have no channel.
func TestAnAppWithoutATeamChannelOrNamespaceHasNoChannel(t *testing.T) {
	decoded, err := notify.DecodeRoutes(routes)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.Channels("payments", true); !reflect.DeepEqual(got, team) {
		t.Fatalf("Channels = %v", got)
	}
	none, _ := notify.DecodeRoutes(`{"channels": {}, "teams": {}}`)
	empty, _ := notify.DecodeRoutes(`{}`)
	for name, got := range map[string][]string{"no route for the team": none.Channels("payments", true),
		"no routes at all": empty.Channels("payments", true), "a team the routes do not name": decoded.Channels("billing", true),
		"no team label": decoded.Channels("", false), "an empty team label": decoded.Channels("", true)} {
		if got == nil || len(got) != 0 {
			t.Fatalf("%s: %#v", name, got)
		}
	}
	decoded.Channels("payments", true)[0] = "changed"
	if decoded.Channels("payments", true)[0] != "payments-phone" {
		t.Fatal("Channels is a copy")
	}
	if _, err := notify.DecodeRoutes(`{"teams": {"payments": "payments-phone"}}`); err == nil {
		t.Fatal("a route that is not a list of channels is refused")
	}
}

// The team is the namespace's platform.swhurl.com/team label; a namespace without it has none.
func TestTheTeamIsTheNamespacesLabel(t *testing.T) {
	cases := map[string]struct {
		team string
		has  bool
	}{
		`{"metadata": {"name": "orders-staging", "labels": {"platform.swhurl.com/team": "payments"}}}`: {"payments", true},
		`{"metadata": {"name": "orders-staging", "labels": {"team": "payments"}}}`:                     {"", false},
		`{"metadata": {"name": "orders-staging"}}`:                                                     {"", false},
		`{}`: {"", false},
	}
	for doc, want := range cases {
		team, has, err := notify.TeamOf([]byte(doc))
		if err != nil || team != want.team || has != want.has {
			t.Fatalf("TeamOf(%s) = %q, %v, %v", doc, team, has, err)
		}
	}
	if _, _, err := notify.TeamOf([]byte(`{`)); err == nil {
		t.Fatal("a namespace that is not JSON is an error")
	}
}

// A channel's address is in the Secret channel-<name>; no other Secret names a channel.
func TestAChannelIsNamedByItsSecret(t *testing.T) {
	for name, want := range map[string]string{"channel-payments-phone": "payments-phone", "channel-x": "x"} {
		if channel, ok := notify.ChannelOf(name); !ok || channel != want {
			t.Fatalf("ChannelOf(%q) = %q, %v", name, channel, ok)
		}
	}
	for _, name := range []string{"notify-alerts", "channel-", "channels", "my-channel-x", ""} {
		if channel, ok := notify.ChannelOf(name); ok {
			t.Fatalf("ChannelOf(%q) = %q", name, channel)
		}
	}
}
