package kernel

import (
	"reflect"
	"testing"
)

// facts is one instance's input to the decision.
func facts(application string, port int32, grants []string, database bool) EgressFacts {
	return EgressFacts{Application: application, Environment: "staging", Port: port, Grants: grants, Database: database}
}

// A port, once given, is kept; a new instance takes the lowest free one; an instance that needs no way out
// gets none; and an instance the caller no longer lists keeps its allocation.
func TestAllocate(t *testing.T) {
	got, err := Allocate([]EgressFacts{
		facts("orders", 30105, nil, false),                       // holds a port: kept
		facts("shop", 0, []string{"api.example.com:443"}, false), // needs one: the lowest free
		facts("quiet", 0, nil, false),                            // nothing leaves: none
		facts("shopkeeper", 0, nil, true),                        // a database's backup needs one
	}, []EgressAllocation{{Application: "gone", Environment: "staging", Port: 30101, Grants: []string{"b.example:443"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []EgressAllocation{
		{Application: "gone", Environment: "staging", Port: 30101, Grants: []string{"b.example:443"}},
		{Application: "orders", Environment: "staging", Port: 30105, Grants: []string{}},
		{Application: "shop", Environment: "staging", Port: 30102, Grants: []string{"api.example.com:443"}},
		{Application: "shopkeeper", Environment: "staging", Port: 30103, Grants: []string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allocation:\n got %v\nwant %v", got, want)
	}
	// The same facts give the same bytes: the order is the environment and the application's name.
	again, err := Allocate([]EgressFacts{facts("shopkeeper", 0, nil, true), facts("quiet", 0, nil, false),
		facts("shop", 0, []string{"api.example.com:443"}, false), facts("orders", 30105, nil, false)},
		[]EgressAllocation{{Application: "gone", Environment: "staging", Port: 30101, Grants: []string{"b.example:443"}}})
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("the decision depends on input order: %v %v", again, err)
	}
}

// Two instances on one port is a refusal: the port names the caller, so one would answer for the other.
func TestAllocateRefusesASharedPort(t *testing.T) {
	_, err := Allocate([]EgressFacts{facts("orders", 30105, nil, false), facts("shop", 30105, nil, false)}, nil)
	refusal(t, err, Invalid, "egress.port")
}

// A grant without a need is still a need; only an instance with nothing to reach outside and no database is
// given no port.
func TestNeedsEgress(t *testing.T) {
	if facts("a", 0, nil, false).NeedsEgress() {
		t.Fatal("an instance with nothing to reach outside needs a port")
	}
	if !facts("a", 0, []string{"x.example:443"}, false).NeedsEgress() {
		t.Fatal("a declared grant needs a port")
	}
	if !facts("a", 0, nil, true).NeedsEgress() {
		t.Fatal("a database's backup needs a port")
	}
}
