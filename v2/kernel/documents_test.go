package kernel

import (
	"strings"
	"testing"
)

const revision = "0123456789abcdef0123456789abcdef01234567"

// An allocation document is read only when this platform wrote it, at a commit, with usable allocations.
func TestDecodeEgressDocument(t *testing.T) {
	good := `{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[
		{"application":"shop","environment":"staging","port":30101,"grants":["api.example.com:443"]}]}`
	document, err := DecodeEgressDocument([]byte(good))
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
			_, err := DecodeEgressDocument([]byte(tt.document))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	// The bytes round-trip through this platform's own encoding.
	encoded, err := EncodeEgressDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := DecodeEgressDocument(encoded); err != nil || again.Revision != revision {
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
	connections, err := ConnectionsOf(data, "orders", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections.Calls) != 0 || len(connections.Callers) != 2 ||
		connections.Callers[0] != "shop" || connections.Callers[1] != "stock" {
		t.Fatalf("the callee's edges: %+v", connections)
	}
	connections, err = ConnectionsOf(data, "shop", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections.Calls) != 2 || connections.Calls[0].Callee != "billing" || connections.Calls[1].Callee != "orders" {
		t.Fatalf("the caller's edges: %+v", connections)
	}
	if _, err := ConnectionsOf([]byte(`{"kind":"v2-egress","version":1,"revision":"`+revision+`"}`), "shop", "staging"); err == nil {
		t.Fatal("another document kind was accepted")
	}
}
