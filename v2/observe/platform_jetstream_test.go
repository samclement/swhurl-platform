package observe

import (
	"reflect"
	"strings"
	"testing"
)

func TestJetStreamStorageRequiresCapacityEvidence(t *testing.T) {
	for _, output := range []string{
		`not JSON`, `{}`, `null`,
		`{"config":{"max_storage":1073741824}}`,
		`{"config":{"max_storage":1073741824},"reserved_storage":null}`,
		`{"config":{"max_storage":1073741824},"reserved_storage":-1}`,
		`{"config":{"max_storage":0},"reserved_storage":0}`,
	} {
		t.Run(output, func(t *testing.T) {
			entries := jetStreamEntries(JetStreamStorage{Output: []byte(output)}, nil)
			if len(entries) != 1 || entries[0].Level != CheckWarn || !strings.Contains(entries[0].Message, "unchecked") {
				t.Fatalf("incomplete or invalid storage evidence must warn: %+v", entries)
			}
		})
	}
}

func TestJetStreamStorageCapacityBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		reserved int
		compiled []CompiledInstance
		level    CheckLevel
	}{
		{"exactly one cap fits", 960, []CompiledInstance{compiledHello()}, CheckOK},
		{"one MiB short", 961, []CompiledInstance{compiledHello()}, CheckWarn},
		{"no room", 1024, nil, CheckWarn},
		{"empty store and no instances", 0, nil, CheckOK},
		{"no instances still needs one app cap", 961, nil, CheckWarn},
		{"larger declared cap", 900, []CompiledInstance{{Files: map[string][]byte{
			"stream.json": []byte(`{"spec":{"name":"large","maxBytes":134217728}}`),
		}}}, CheckWarn},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := jetStreamEntries(JetStreamStorage{Output: jetStream(1024, test.reserved)}, test.compiled)
			if len(entries) != 1 || entries[0].Level != test.level {
				t.Fatalf("capacity finding: %+v", entries)
			}
		})
	}
}

func TestJetStreamLeftoversAreSortedAndKeepMessageCounts(t *testing.T) {
	output := []byte(`{"config":{"max_storage":1073741824},"reserved_storage":201326592,
		"account_details":[{"stream_detail":[
			{"name":"staging-z","config":{"max_bytes":67108864},"state":{"messages":7}},
			{"name":"staging-hello","config":{"max_bytes":67108864},"state":{"messages":9}},
			{"name":"KV_flags-staging","config":{"max_bytes":1048576},"state":{"messages":1}}
		]},{"stream_detail":[
			{"name":"staging-a","config":{"max_bytes":33554432},"state":{"messages":3}},
			{"name":"KV_flags-prod","config":{"max_bytes":1048576},"state":{"messages":2}}
		]}]}`)
	entries := jetStreamEntries(JetStreamStorage{Output: output}, []CompiledInstance{compiledHello()})
	want := []PlatformEntry{
		ok("JetStream has 832 of 1024 MiB unreserved"),
		warn("2 stream(s) in NATS that no instance declares reserve 96 MiB: staging-a (3 messages), staging-z (7 messages) " +
			"(docs/operations.md#leftover-event-streams)"),
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("findings: %+v", entries)
	}
}
