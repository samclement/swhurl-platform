package kernel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The two keys the platform owns: the stream controller's and the console's, as the published files carry them.
const (
	testControllerKey = "UCEW5BT5FDDXPO475VJV6NYYS2GJ66C67KQJY6UAXIAFQOAJRHQTWMAP"
	testConsoleKey    = "UAGJUVYLYUYPDTZV7E5ODWOWL3YPXIMMRXXE7MAX4HV3HTWKBYGHZPGD"
)

func users(t *testing.T, instances ...NATSUser) NATSUsers {
	t.Helper()
	return NATSUsers{ControllerKey: testControllerKey, ConsoleKey: testConsoleKey, Instances: instances}
}

// userList decodes the user list the ConfigMap carries, as Helm parses it.
func userList(t *testing.T, rendered ResourceSet) []any {
	t.Helper()
	var document struct {
		Data map[string]string
	}
	if len(rendered.Files) != 1 {
		t.Fatalf("files %d, want the ConfigMap alone", len(rendered.Files))
	}
	if err := json.Unmarshal(rendered.Files[0].Document, &document); err != nil {
		t.Fatal(err)
	}
	var values struct {
		Config struct {
			Merge struct {
				Authorization struct {
					Users []any
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(document.Data["values.yaml"]), &values); err != nil {
		t.Fatalf("the data key is not JSON: %v", err)
	}
	return values.Config.Merge.Authorization.Users
}

// allowOf is one user's publish and subscribe allow lists, by the position it holds in the list.
func allowOf(t *testing.T, rendered ResourceSet, index int) (publish, subscribe []string) {
	t.Helper()
	entry := userList(t, rendered)[index].(map[string]any)["permissions"].(map[string]any)
	for field, into := range map[string]*[]string{"publish": &publish, "subscribe": &subscribe} {
		for _, value := range entry[field].(map[string]any)["allow"].([]any) {
			*into = append(*into, value.(string))
		}
	}
	return publish, subscribe
}

// A subscriber's permissions are the flag overrides first, then what reading one event needs: its consumer's
// info, next, ack and the stream's own description. The order is the file's, so the document is stable and it
// reads the same as the one NATS serves today.
func TestUserPermissions(t *testing.T) {
	rendered, err := RenderUsers(users(t, NATSUser{Application: "demo-notifier", Environment: "staging",
		NKey: "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI",
		Subscriptions: []NATSSubscription{{Stream: "staging-demo-orders", Consumer: "demo-notifier_created",
			Subject: "staging.demo-orders.created"}}}))
	if err != nil {
		t.Fatal(err)
	}
	publish, subscribe := allowOf(t, rendered, 2)
	for _, want := range []string{
		"$JS.API.INFO",
		"$JS.API.STREAM.INFO.KV_flags-staging",
		"$JS.API.CONSUMER.CREATE.KV_flags-staging.*.$KV.flags-staging.staging.demo-notifier.>",
		"$JS.API.CONSUMER.DELETE.KV_flags-staging.*",
		"$JS.API.CONSUMER.INFO.KV_flags-staging.*",
		"$JS.API.CONSUMER.INFO.staging-demo-orders.demo-notifier_created",
		"$JS.API.CONSUMER.MSG.NEXT.staging-demo-orders.demo-notifier_created",
		"$JS.ACK.staging-demo-orders.demo-notifier_created.>",
		"$JS.API.STREAM.INFO.staging-demo-orders",
	} {
		if !contains(publish, want) {
			t.Errorf("the publisher permissions lack %q: %v", want, publish)
		}
	}
	if len(publish) != 9 {
		t.Errorf("a subscriber's publish list: %v", publish)
	}
	if !reflect.DeepEqual(subscribe, []string{"_INBOX.staging.demo-notifier.>"}) {
		t.Errorf("a subscriber reads replies under its own inbox only: %v", subscribe)
	}
	// A publisher adds its whole prefix, not its list of events: an image can publish a new event before the
	// platform has read its description.
	publisher, err := RenderUsers(users(t, NATSUser{Application: "demo-orders", Environment: "staging",
		NKey: "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G", Publishes: true}))
	if err != nil {
		t.Fatal(err)
	}
	publish, _ = allowOf(t, publisher, 2)
	if publish[len(publish)-1] != "staging.demo-orders.>" || len(publish) != 6 {
		t.Fatalf("a publisher's publish list: %v", publish)
	}
	// Two subscriptions to one stream read its description once.
	twice, err := RenderUsers(users(t, NATSUser{Application: "notifier", Environment: "staging",
		NKey: "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI",
		Subscriptions: []NATSSubscription{
			{Stream: "staging-orders", Consumer: "notifier_created", Subject: "staging.orders.created"},
			{Stream: "staging-orders", Consumer: "notifier_cancelled", Subject: "staging.orders.cancelled"}}}))
	if err != nil {
		t.Fatal(err)
	}
	publish, _ = allowOf(t, twice, 2)
	if count(publish, "$JS.API.STREAM.INFO.staging-orders") != 1 {
		t.Fatalf("the stream's description is read once: %v", publish)
	}
}

// The platform's own two users are the list's first two entries, ahead of every instance.
func TestPlatformUsersAreFirst(t *testing.T) {
	rendered, err := RenderUsers(users(t, NATSUser{Application: "orders", Environment: "staging",
		NKey: "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G", Publishes: true}))
	if err != nil {
		t.Fatal(err)
	}
	entries := userList(t, rendered)
	if len(entries) != 3 {
		t.Fatalf("%d users, want the controller, the console and one instance", len(entries))
	}
	if entries[0].(map[string]any)["nkey"] != testControllerKey || entries[1].(map[string]any)["nkey"] != testConsoleKey {
		t.Fatalf("the platform's keys are not first: %v", entries)
	}
	publish, subscribe := allowOf(t, rendered, 0)
	if !reflect.DeepEqual(publish, []string{"$JS.API.>"}) || !reflect.DeepEqual(subscribe, []string{"_INBOX.>"}) {
		t.Fatalf("the controller writes the JetStream API only: %v %v", publish, subscribe)
	}
	publish, subscribe = allowOf(t, rendered, 1)
	if publish[0] != "$JS.API.INFO" || !contains(publish, "$KV.flags-prod.>") || len(publish) != 15 {
		t.Fatalf("the console reads and writes both environments' buckets: %v", publish)
	}
	if !reflect.DeepEqual(subscribe, []string{"_INBOX.console.>"}) {
		t.Fatalf("the console's inbox: %v", subscribe)
	}
}

// The list's order is fixed: staging's instances before production's, each environment by app name, whatever order
// the caller supplies.
func TestUsersOrderIsFixed(t *testing.T) {
	key := func(letter string) string { return "U" + strings.Repeat(letter, 55) }
	forward := []NATSUser{
		{Application: "alpha", Environment: "staging", NKey: key("A")},
		{Application: "beta", Environment: "staging", NKey: key("B")},
		{Application: "alpha", Environment: "prod", NKey: key("C")},
	}
	backward := []NATSUser{forward[2], forward[1], forward[0]}
	first, err := RenderUsers(users(t, forward...))
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderUsers(users(t, backward...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Files, second.Files) {
		t.Fatal("the list's order depends on the caller's order")
	}
	entries := userList(t, first)
	want := []string{testControllerKey, testConsoleKey, key("A"), key("B"), key("C")}
	for index, expected := range want {
		if entries[index].(map[string]any)["nkey"] != expected {
			t.Fatalf("entry %d is %v, want %s", index, entries[index].(map[string]any)["nkey"], expected)
		}
	}
}

// The document is the ConfigMap the NATS HelmRelease reads, and the platform owns it.
func TestUsersDocument(t *testing.T) {
	rendered, err := RenderUsers(users(t, NATSUser{Application: "orders", Environment: "prod",
		NKey: "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G", Publishes: true}))
	if err != nil {
		t.Fatal(err)
	}
	document := string(rendered.Files[0].Document)
	for _, want := range []string{`"kind":"ConfigMap"`, `"name":"nats-users"`, `"namespace":"messaging"`,
		`"reconcile.fluxcd.io/watch":"Enabled"`, `"values.yaml":"{\"config\":{\"merge\":{\"authorization\"`} {
		if !strings.Contains(document, want) {
			t.Errorf("the ConfigMap lacks %s:\n%s", want, document)
		}
	}
	if !strings.HasPrefix(rendered.Files[0].Path, "nats/") {
		t.Errorf("the file's place: %s", rendered.Files[0].Path)
	}
	want := []Claim{{Object{"", "ConfigMap", "messaging", "nats-users"}, "platform"}}
	if !reflect.DeepEqual(rendered.Inventory, want) {
		t.Errorf("claims %v", rendered.Inventory)
	}
	// Two renders do not share a slice or a byte.
	again, err := RenderUsers(users(t, NATSUser{Application: "orders", Environment: "prod",
		NKey: "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G", Publishes: true}))
	if err != nil || !reflect.DeepEqual(rendered, again) {
		t.Fatalf("not repeatable: %v", err)
	}
	rendered.Files[0].Document[0] = '!'
	rendered.Inventory[0].Owner = "changed"
	if !strings.HasPrefix(string(again.Files[0].Document), "{") || again.Inventory[0].Owner != "platform" {
		t.Fatal("calls alias their output")
	}
}

// The keys and names a permission is spelled with are checked, so a broken key or name is refused rather than
// published to the server.
func TestUsersRefusals(t *testing.T) {
	valid := NATSUser{Application: "orders", Environment: "staging",
		NKey: "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G", Publishes: true}
	for _, tt := range []struct {
		name  string
		users NATSUsers
		field string
	}{
		{"a controller key that is not one", NATSUsers{ControllerKey: "not-a-key", ConsoleKey: testConsoleKey}, "users.controllerKey"},
		{"a console key that is not one", NATSUsers{ControllerKey: testControllerKey, ConsoleKey: "SU" + strings.Repeat("A", 54)}, "users.consoleKey"},
		{"no controller key", NATSUsers{ConsoleKey: testConsoleKey}, "users.controllerKey"},
		{"an instance key that is not one", NATSUsers{ControllerKey: testControllerKey, ConsoleKey: testConsoleKey,
			Instances: []NATSUser{{Application: "orders", Environment: "staging", NKey: "U"}}}, "users.nkey"},
		{"two instances on one key", NATSUsers{ControllerKey: testControllerKey, ConsoleKey: testConsoleKey,
			Instances: []NATSUser{valid, {Application: "other", Environment: "prod", NKey: valid.NKey}}}, "users.nkey"},
		{"an environment that is not one", NATSUsers{ControllerKey: testControllerKey, ConsoleKey: testConsoleKey,
			Instances: []NATSUser{{Application: "orders", Environment: "preview", NKey: valid.NKey}}}, "users.instance"},
		{"a subscription without its names", NATSUsers{ControllerKey: testControllerKey, ConsoleKey: testConsoleKey,
			Instances: []NATSUser{{Application: "orders", Environment: "staging", NKey: valid.NKey,
				Subscriptions: []NATSSubscription{{Stream: "staging-orders"}}}}}, "users.subscription"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendered, err := RenderUsers(tt.users)
			refusal(t, err, Invalid, tt.field)
			if !reflect.DeepEqual(rendered, ResourceSet{}) {
				t.Fatal("partial resources on refusal")
			}
		})
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func count(values []string, want string) int {
	found := 0
	for _, value := range values {
		if value == want {
			found++
		}
	}
	return found
}
