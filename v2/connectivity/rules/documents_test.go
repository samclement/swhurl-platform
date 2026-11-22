package rules_test

import (
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/connectivity/rules"
)

const revision = "0123456789abcdef0123456789abcdef01234567"

// An allocation document is read only when this platform wrote it, at a commit, with usable allocations.
func TestDecodeEgressDocument(t *testing.T) {
	good := `{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[
		{"application":"shop","environment":"staging","port":30101,"grants":["api.example.com:443"]}]}`
	document, err := rules.DecodeEgressDocument([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	egress, err := document.EgressOf("shop", "staging")
	if err != nil || egress.Port != 30101 || egress.Grants[0] != "api.example.com:443" {
		t.Fatalf("the allocation: %+v %v", egress, err)
	}
	if _, err := document.EgressOf("absent", "staging"); err == nil {
		t.Fatal("an instance with no allocation was accepted")
	}
	for _, tt := range []struct{ name, document, want string }{
		{"another document", `{"kind":"v2-calls","version":1,"revision":"` + revision + `"}`, "unsupported: egress"},
		{"no revision", `{"kind":"v2-egress","version":1,"instances":[]}`, "invalid: egress: revision_required"},
		{"not an object", `[]`, "invalid: egress: single_json_object_required"},
		{"a privileged port", `{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[{"application":"shop","environment":"staging","port":80}]}`, "invalid: egress: allocation_required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rules.DecodeEgressDocument([]byte(tt.document))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	// The bytes round-trip through this platform's own encoding.
	encoded, err := rules.EncodeEgressDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := rules.DecodeEgressDocument(encoded); err != nil || again.Revision != revision {
		t.Fatalf("round trip: %v %v", again, err)
	}
}

// A resolution is reduced to one instance's own edges in its own environment; another environment's pair is
// never cross-applied.
func TestConnectionsOf(t *testing.T) {
	data := []byte(`{"kind":"v2-calls","version":1,"revision":"` + revision + `","pairs":[
		{"caller":"shop","callee":"orders","environment":"staging","port":8080},
		{"caller":"shop","callee":"billing","environment":"staging","port":8081},
		{"caller":"shop","callee":"orders","environment":"prod","port":9090},
		{"caller":"stock","callee":"orders","environment":"staging","port":8080}]}`)
	connections, err := rules.ConnectionsOf(data, "orders", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections.Calls) != 0 || len(connections.Callers) != 2 ||
		connections.Callers[0] != "shop" || connections.Callers[1] != "stock" {
		t.Fatalf("the callee's edges: %+v", connections)
	}
	connections, err = rules.ConnectionsOf(data, "shop", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections.Calls) != 2 || connections.Calls[0].Callee != "billing" || connections.Calls[1].Callee != "orders" {
		t.Fatalf("the caller's edges: %+v", connections)
	}
	if _, err := rules.ConnectionsOf([]byte(`{"kind":"v2-egress","version":1,"revision":"`+revision+`"}`), "shop", "staging"); err == nil {
		t.Fatal("another document kind was accepted")
	}
}

// An instance takes its allocation only when the allocation grants it something to reach; no document, no entry
// and an entry without grants all mean no egress, and a malformed document is refused.
func TestInstanceEgress(t *testing.T) {
	data := []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[
		{"application":"shop","environment":"staging","port":30101,"grants":["api.example.com:443"]},
		{"application":"notes","environment":"staging","port":30102,"grants":[]}]}`)
	for _, tt := range []struct {
		name, application string
		document          []byte
		port              int32
	}{
		{"granted", "shop", data, 30101},
		{"a port with nothing to reach", "notes", data, 0},
		{"not allocated", "absent", data, 0},
		{"no document", "shop", nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			egress, err := rules.InstanceEgress(tt.document, tt.application, "staging")
			if err != nil {
				t.Fatal(err)
			}
			if (tt.port == 0) != (egress == nil) || (egress != nil && egress.Port != tt.port) {
				t.Fatalf("egress = %+v, want port %d", egress, tt.port)
			}
		})
	}
	if _, err := rules.InstanceEgress([]byte(`[]`), "shop", "staging"); err == nil {
		t.Fatal("a malformed allocation was accepted")
	}
}

// The subscribers of an instance's events are the connected subscriptions it publishes, in its own environment.
func TestSubscribersOf(t *testing.T) {
	data := []byte(`{"kind":"v2-calls","version":1,"revision":"` + revision + `","subscriptions":[
		{"subscriber":"notifier","publisher":"shop","event":"created","environment":"staging"},
		{"subscriber":"ledger","publisher":"shop","event":"created","environment":"prod"},
		{"subscriber":"shop","publisher":"orders","event":"placed","environment":"staging"}]}`)
	subscribers, err := rules.SubscribersOf(data, "shop", "staging")
	if err != nil || len(subscribers) != 1 || subscribers[0] != "notifier" {
		t.Fatalf("subscribers = %v %v", subscribers, err)
	}
	connections, err := rules.ConnectionsOf(data, "shop", "staging")
	if err != nil || len(connections.Subscriptions) != 1 || connections.Subscriptions[0].Publisher != "orders" {
		t.Fatalf("the subscriber's subscriptions: %+v %v", connections, err)
	}
}
