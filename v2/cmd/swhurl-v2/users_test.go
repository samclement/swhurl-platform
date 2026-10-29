package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// eventInstances is a snapshot with a publisher, its subscriber, an instance that only reads its flags, and a
// v2-owned instance whose records cannot state publications yet.
func eventInstances(t *testing.T) map[string]any {
	t.Helper()
	instance := func(application, format string, settings, release map[string]any) map[string]any {
		return map[string]any{"application": application, "environment": "staging", "format": format,
			"missing": []any{}, "objects": []any{},
			"records": map[string]any{"instance": settings, "release": release}}
	}
	key := func(letter string) string { return "U" + strings.Repeat(letter, 55) }
	publisher := map[string]any{"publishes": []any{"created"}}
	// notifier reads orders' event, and one from the v2-owned app whose publications this slice cannot state.
	subscriber := map[string]any{"subscriptions": []any{"orders.created", "quiet.something"},
		"allocations": map[string]any{"eventsKey": key("B")}}
	return map[string]any{"version": 1, "revision": strings.Repeat("a", 40), "residual": []any{},
		"instances": []any{
			instance("orders", "v1", map[string]any{"allocations": map[string]any{"eventsKey": key("A")}}, publisher),
			instance("notifier", "v1", subscriber, map[string]any{}),
			// A v2-owned instance publishes nothing this model can state, so notifier's second subscription to it
			// would wait rather than being refused.
			instance("quiet", "v2", map[string]any{}, map[string]any{}),
		}}
}

// The command renders the file the platform's NATS server reads, and reports what it rendered.
func TestUsersRendersTheServerList(t *testing.T) {
	snapshot, profile := writeSnapshot(t, eventInstances(t)), profilePath(t)
	var out, diagnostic bytes.Buffer
	status := run([]string{"users", "--snapshot", snapshot, "--profile", profile}, &out, &diagnostic,
		os.ReadFile, files.New())
	if status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic.String())
	}
	var document struct {
		Kind     string
		Version  int
		Revision string
		Users    int
		Files    []struct {
			Path     string
			Document json.RawMessage
		}
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out.String())
	}
	if document.Kind != "v2-users" || document.Version != 1 || document.Revision != strings.Repeat("a", 40) {
		t.Fatalf("envelope: %+v", document)
	}
	// The controller, the console, and the two instances that have a key: a v2-owned instance has none yet.
	if document.Users != 4 || len(document.Files) != 1 || document.Files[0].Path != "nats/users.json" {
		t.Fatalf("report: %+v", document)
	}
	var configMap struct {
		Metadata struct{ Name, Namespace string }
		Data     map[string]string
	}
	if err := json.Unmarshal(document.Files[0].Document, &configMap); err != nil {
		t.Fatal(err)
	}
	if configMap.Metadata.Name != "nats-users" || configMap.Metadata.Namespace != "messaging" {
		t.Fatalf("the object: %+v", configMap.Metadata)
	}
	values := configMap.Data["values.yaml"]
	for _, want := range []string{`"users"`, "UCEW5BT5FDDXPO475VJV6NYYS2GJ66C67KQJY6UAXIAFQOAJRHQTWMAP",
		"UAGJUVYLYUYPDTZV7E5ODWOWL3YPXIMMRXXE7MAX4HV3HTWKBYGHZPGD",
		`staging.orders.>`, `$JS.API.CONSUMER.MSG.NEXT.staging-orders.notifier_created`} {
		if !strings.Contains(values, want) {
			t.Errorf("the user list lacks %s:\n%s", want, values)
		}
	}
	// A subscription whose publisher's publications cannot be stated yet waits: it has no consumer calls.
	for _, unwanted := range []string{"notifier_something", "staging-quiet", "quiet.something"} {
		if strings.Contains(values, unwanted) {
			t.Errorf("a waiting subscription was granted %q:\n%s", unwanted, values)
		}
	}
	if !strings.Contains(diagnostic.String(), "users: 4 user(s), 1 file(s)") {
		t.Errorf("the summary: %s", diagnostic.String())
	}
}

// profilePath is the fixture profile, which states the broker and the platform's two public keys.
func profilePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, fixture(t, "profile.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// What the command refuses before it renders anything.
func TestUsersRefusals(t *testing.T) {
	snapshot := writeSnapshot(t, eventInstances(t))
	// withRecords changes one instance's records: 0 is the publisher, 1 the subscriber.
	withRecords := func(index int, change func(map[string]any)) string {
		document := eventInstances(t)
		instances := document["instances"].([]any)
		change(instances[index].(map[string]any)["records"].(map[string]any))
		return writeSnapshot(t, document)
	}
	for _, tt := range []struct {
		name   string
		args   []string
		status int
		want   string
	}{
		{"no profile", []string{"users", "--snapshot", snapshot}, 2, "usage"},
		{"a subscription to a publisher that states its events and has none",
			[]string{"users", "--snapshot", withRecords(0, func(records map[string]any) {
				records["release"] = map[string]any{"publishes": []any{}}
			}), "--profile", profilePath(t)}, 2, "not_published"},
		{"an instance that declares events and has no key",
			[]string{"users", "--snapshot", withRecords(1, func(records map[string]any) {
				records["instance"] = map[string]any{"subscriptions": []any{"orders.created"}}
			}), "--profile", profilePath(t)}, 2,
			"unsupported: users.key: notifier/staging declares events and has no key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			status := run(tt.args, &out, &diagnostic, os.ReadFile, files.New())
			if status != tt.status || !strings.Contains(diagnostic.String(), tt.want) || out.Len() != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", status, out.String(), diagnostic.String())
			}
		})
	}
}

// --out writes the rendered file and nothing else, and a second run leaves it unchanged.
func TestUsersWritesItsFile(t *testing.T) {
	snapshot, profile := writeSnapshot(t, eventInstances(t)), profilePath(t)
	destination := t.TempDir()
	for range 2 {
		var out, diagnostic bytes.Buffer
		status := run([]string{"users", "--snapshot", snapshot, "--profile", profile, "--out", destination},
			&out, &diagnostic, os.ReadFile, files.New())
		if status != 0 {
			t.Fatalf("exit %d: %s", status, diagnostic.String())
		}
		if diagnostic.String() != "users: 4 user(s), 1 file(s)\n" ||
			(out.String() != "written nats/users.json\n" && out.String() != "unchanged nats/users.json\n") {
			t.Fatalf("stdout %q, diagnostic %q", out.String(), diagnostic.String())
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "nats", "users.json")); err != nil {
		t.Fatal(err)
	}
}
