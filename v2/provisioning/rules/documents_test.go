package rules

import (
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Optional means absent, not unchecked: supplied documents are validated before their instance's facts are taken.
func TestDocuments(t *testing.T) {
	calls := []byte(`{"kind":"v2-calls","version":1,"revision":"` + revision + `","pairs":[
		{"caller":"demo-orders","callee":"billing","environment":"prod","port":8081},
		{"caller":"frontend","callee":"demo-orders","environment":"prod","port":8080}]}`)
	egress := []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[
		{"application":"demo-orders","environment":"prod","port":30111,"grants":["api.example.com:443"]}]}`)
	noGrants := []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[
		{"application":"demo-orders","environment":"prod","port":30111,"grants":[]}]}`)
	connected := kernel.Connections{Calls: []kernel.Call{{Callee: "billing", Port: 8081}},
		Callers: []string{"frontend"}, Subscriptions: []kernel.EventSubscription{}}
	empty := kernel.Connections{Subscriptions: []kernel.EventSubscription{}}
	allocated := &kernel.Egress{Port: 30111, Grants: []string{"api.example.com:443"}}
	for _, tt := range []struct {
		name, application, environment string
		calls, egress                  []byte
		wantConnections                kernel.Connections
		wantEgress                     *kernel.Egress
		wantError                      string
	}{
		{name: "absent"},
		{name: "matching", calls: calls, egress: egress, wantConnections: connected, wantEgress: allocated},
		{name: "calls only", calls: calls, wantConnections: connected},
		{name: "egress only", egress: egress, wantEgress: allocated},
		{name: "other application", application: "unrelated", calls: calls, egress: egress, wantConnections: empty},
		{name: "other environment", environment: "staging", calls: calls, egress: egress, wantConnections: empty},
		{name: "reserved port without grants", egress: noGrants},
		{name: "empty calls", calls: []byte{}, wantError: "invalid: calls: single_json_object_required"},
		{name: "empty egress", egress: []byte{}, wantError: "invalid: egress: single_json_object_required"},
		{name: "malformed calls", calls: []byte(`{`), wantError: "invalid: calls: single_json_object_required"},
		{name: "malformed egress discards calls", calls: calls, egress: []byte(`{`), wantError: "invalid: egress: single_json_object_required"},
		{name: "wrong calls kind", calls: egress, wantError: "unsupported: calls: only v2-calls version 1"},
		{name: "wrong egress kind", egress: calls, wantError: "unsupported: egress: only v2-egress version 1"},
		{name: "invalid calls revision", calls: []byte(`{"kind":"v2-calls","version":1}`), wantError: "invalid: calls: revision_required"},
		{name: "invalid egress revision", egress: []byte(`{"kind":"v2-egress","version":1}`), wantError: "invalid: egress: revision_required"},
		{name: "calls error first", calls: []byte(`{`), egress: []byte(`{`), wantError: "invalid: calls: single_json_object_required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			application, environment := tt.application, tt.environment
			if application == "" {
				application = "demo-orders"
			}
			if environment == "" {
				environment = "prod"
			}
			connections, egress, err := Documents(tt.calls, tt.egress, application, environment)
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(connections, tt.wantConnections) || !reflect.DeepEqual(egress, tt.wantEgress) {
				t.Fatalf("Documents = %#v, %#v; want %#v, %#v", connections, egress, tt.wantConnections, tt.wantEgress)
			}
		})
	}
}
