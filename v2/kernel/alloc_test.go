package kernel

import (
	"reflect"
	"testing"
)

// facts is one instance's input to the decision.
func facts(application string, port int32, grants []string, database bool) EgressFacts {
	return EgressFacts{Application: application, Environment: "staging", Port: port, Grants: grants, Database: database}
}

// backupHost is the platform's backup endpoint, as the profile's Backup.Host gives it.
const backupHost = "backups.s3.eu-west-2.amazonaws.com:443"

// A port, once given, is kept; a new instance takes the lowest free one; an instance that needs no way out
// gets none; a database's instance is granted the backup host; and an instance the caller no longer lists
// keeps its allocation.
func TestAllocate(t *testing.T) {
	got, err := Allocate([]EgressFacts{
		facts("orders", 30105, nil, false),                       // holds a port: kept
		facts("shop", 0, []string{"api.example.com:443"}, false), // needs one: the lowest free
		facts("quiet", 0, nil, false),                            // nothing leaves: none
		facts("shopkeeper", 0, nil, true),                        // a database's backup needs one
	}, []EgressAllocation{{Application: "gone", Environment: "staging", Port: 30101, Grants: []string{"b.example:443"}}}, backupHost)
	if err != nil {
		t.Fatal(err)
	}
	want := []EgressAllocation{
		{Application: "gone", Environment: "staging", Port: 30101, Grants: []string{"b.example:443"}},
		{Application: "orders", Environment: "staging", Port: 30105, Grants: []string{}},
		{Application: "shop", Environment: "staging", Port: 30102, Grants: []string{"api.example.com:443"}},
		{Application: "shopkeeper", Environment: "staging", Port: 30103, Grants: []string{backupHost}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allocation:\n got %v\nwant %v", got, want)
	}
	// The same facts give the same bytes: the order is the environment and the application's name.
	again, err := Allocate([]EgressFacts{facts("shopkeeper", 0, nil, true), facts("quiet", 0, nil, false),
		facts("shop", 0, []string{"api.example.com:443"}, false), facts("orders", 30105, nil, false)},
		[]EgressAllocation{{Application: "gone", Environment: "staging", Port: 30101, Grants: []string{"b.example:443"}}}, backupHost)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("the decision depends on input order: %v %v", again, err)
	}
}

// Two instances on one port is a refusal: the port names the caller, so one would answer for the other.
func TestAllocateRefusesASharedPort(t *testing.T) {
	_, err := Allocate([]EgressFacts{facts("orders", 30105, nil, false), facts("shop", 30105, nil, false)}, nil, backupHost)
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

// The backup host is the platform's to grant: a database's instance gets it without declaring it, once even
// when a v1 record also lists it, an instance without a database never gets it, and a profile with no backup
// grants nothing.
func TestAllocateGrantsTheBackupHost(t *testing.T) {
	got, err := Allocate([]EgressFacts{
		facts("declared", 0, []string{backupHost, "api.example.com:443"}, true),
		facts("keeper", 0, nil, true),
		facts("stateless", 0, []string{"api.example.com:443"}, false),
	}, nil, backupHost)
	if err != nil {
		t.Fatal(err)
	}
	want := []EgressAllocation{
		{Application: "declared", Environment: "staging", Port: 30101, Grants: []string{"api.example.com:443", backupHost}},
		{Application: "keeper", Environment: "staging", Port: 30102, Grants: []string{backupHost}},
		{Application: "stateless", Environment: "staging", Port: 30103, Grants: []string{"api.example.com:443"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allocation:\n got %v\nwant %v", got, want)
	}
	none, err := Allocate([]EgressFacts{facts("keeper", 0, nil, true)}, nil, "")
	if err != nil || len(none) != 1 || len(none[0].Grants) != 0 {
		t.Fatalf("no backup host still granted one: %v %v", none, err)
	}
}
