package kernel

import (
	"slices"
	"testing"
)

// declaredSpec is one instance record whose relationships and exposure the tests vary.
func declaredSpec() InstanceSpec {
	return InstanceSpec{SchemaVersion: 1, Application: "demo-orders", Environment: "prod",
		Configuration: Configuration{Replicas: 1, MemoryMiB: 128},
		Grants:        Grants{Exposure: "authenticated-web"},
		Calls:         []string{"shop", "ledger", "orders"},
		Allows:        []string{"team:demo", "shopper"},
		Events:        Events{Subscriptions: []string{"ledger.created", "shop.updated"}}}
}

// declaredRecord is a release that states documents and publishes an event, so every declared fact has a value.
func declaredRecord(t *testing.T) ReleaseBundle {
	t.Helper()
	record := releaseFile(t, "r43.json")
	record.Documents, record.APIPath = []string{"api", "events"}, "/openapi.json"
	record.Publishes = []string{"created"}
	return record
}

// declaredResolutionDocument is one committed resolution: a pair, a wait, a refusal, a connected subscription and a waiting
// one, every answer the document can state.
const declaredResolutionDocument = `{"kind":"v2-calls","version":1,"revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
 "pairs":[{"caller":"demo-orders","callee":"shop","environment":"prod","port":8080},
          {"caller":"shopper","callee":"demo-orders","environment":"prod","port":8080}],
 "waiting":[{"caller":"demo-orders","callee":"ledger","environment":"prod","reason":"no_consent"}],
 "refused":[{"application":"demo-orders","callee":"orders","environment":"prod","reason":"callee_not_web"}],
 "subscriptions":[{"subscriber":"demo-orders","publisher":"ledger","event":"created","environment":"prod",
                   "stream":"prod-ledger","consumer":"demo-orders_created","subject":"prod.ledger.created",
                   "object":"ledger-created"},
                  {"subscriber":"notifier","publisher":"demo-orders","event":"created","environment":"prod",
                   "stream":"prod-demo-orders","consumer":"notifier_created","subject":"prod.demo-orders.created",
                   "object":"demo-orders-created"}],
 "eventWaiting":[{"caller":"demo-orders","callee":"shop","environment":"prod","reason":"publisher_not_ready"}]}
`

// declaredAllocationDocument is one committed allocation: a port with a grant the record does not name, as a database's
// backup host arrives.
const declaredAllocationDocument = `{"kind":"v2-egress","version":1,"revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
 "instances":[{"application":"demo-orders","environment":"prod","port":30111,
               "grants":["swhurl-platform-backups.example.com:443"]}]}
`

// TestDeclareStatesWhatTheRecordsHold: every fact a page shows comes from the records, profile and committed
// documents, with no read of the cluster.
func TestDeclareStatesWhatTheRecordsHold(t *testing.T) {
	declared, err := Declare(declaredSpec(), declaredRecord(t), app(t), profile(t),
		[]byte(declaredResolutionDocument), []byte(declaredAllocationDocument))
	if err != nil {
		t.Fatal(err)
	}
	expected := Declared{
		Team: "demo", Kind: "web", Exposure: "authenticated-web", Port: 8080,
		Hosts:   []string{"demo-orders.homelab.swhurl.com"},
		Address: "http://demo-orders.demo-orders-prod.svc.cluster.local:8080",
		API:     "https://demo-orders.homelab.swhurl.com/openapi.json",
		Artifact: DeclaredArtifact{Image: "ghcr.io/samclement/demo-orders",
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Database:      DeclaredDatabase{Kind: "sqlite", Path: "/data/app.db"},
		Offers:        "aaaaaaa",
		Egress:        DeclaredEgress{Port: 30111, Grants: []string{"swhurl-platform-backups.example.com:443"}},
		Calls:         []string{"shop", "ledger", "orders"},
		Allows:        []string{"team:demo", "shopper"},
		Callers:       []string{"shopper"},
		Publishes:     []string{"created"},
		Subscriptions: []string{"ledger.created", "shop.updated"},
	}
	declared.Connections = nil
	if !sameDeclaration(declared, expected) {
		t.Fatalf("declared\n%+v\nwant\n%+v", declared, expected)
	}
}

// TestDeclareRowsFollowTheResolution: a row is connected, waiting or refused by what the resolution says, in the
// order a page lists them, and a declaration the document does not mention stays a row without an outcome.
func TestDeclareRowsFollowTheResolution(t *testing.T) {
	declared, err := Declare(declaredSpec(), declaredRecord(t), app(t), profile(t),
		[]byte(declaredResolutionDocument), []byte(declaredAllocationDocument))
	if err != nil {
		t.Fatal(err)
	}
	want := []DeclaredConnection{
		{Role: RoleCalls, App: "shop", Outcome: Connected},
		{Role: RoleCalls, App: "ledger", Outcome: Waiting, Reason: "no_consent"},
		{Role: RoleCalls, App: "orders", Outcome: Refused, Reason: "callee_not_web"},
		{Role: RoleSubscribes, App: "ledger", Event: "created", Outcome: Connected},
		{Role: RoleSubscribes, App: "shop", Event: "updated", Outcome: Waiting, Reason: "publisher_not_ready"},
		{Role: RolePublishes, Event: "created"},
		{Role: RoleReaches, Host: "swhurl-platform-backups.example.com:443"},
		{Role: RoleCalledBy, App: "shopper", Outcome: Connected},
		{Role: RoleSubscriber, App: "notifier", Event: "created", Outcome: Connected},
	}
	if !slices.Equal(declared.Connections, want) {
		t.Fatalf("rows\n%+v\nwant\n%+v", declared.Connections, want)
	}
}

// TestDeclareWithoutDocumentsStatesNoOutcome: a caller that supplies no resolution or allocation gets declarations
// without outcomes and a zero egress — never a connection nobody agreed to.
func TestDeclareWithoutDocumentsStatesNoOutcome(t *testing.T) {
	declared, err := Declare(declaredSpec(), declaredRecord(t), app(t), profile(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if declared.Egress.Port != 0 || len(declared.Egress.Grants) != 0 || len(declared.Callers) != 0 {
		t.Fatalf("egress or callers invented: %+v", declared)
	}
	for _, row := range declared.Connections {
		if row.Outcome != "" || row.Reason != "" {
			t.Fatalf("a row states an outcome no document gave: %+v", row)
		}
	}
	// Its own declarations only: what it calls, receives and publishes. Who calls it, who subscribes to it and what
	// it may reach are facts the two documents state.
	if len(declared.Connections) != 6 {
		t.Fatalf("rows: %+v", declared.Connections)
	}
}

// TestDeclareGivesAWorkerNoAddress: a worker renders no Service and is private, so it has no host, no address and no
// API address, whatever its exposure says.
func TestDeclareGivesAWorkerNoAddress(t *testing.T) {
	record := declaredRecord(t)
	record.Runtime.Kind = "worker"
	spec := declaredSpec()
	spec.Grants.Exposure = "private"
	declared, err := Declare(spec, record, app(t), profile(t), []byte(declaredResolutionDocument), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declared.Hosts) != 0 || declared.Address != "" || declared.API != "" {
		t.Fatalf("a worker has an address: %+v", declared)
	}
	if declared.Offers != "aaaaaaa" {
		t.Fatalf("a worker that offers documents lost its commit: %q", declared.Offers)
	}
}

// TestDeclareKeepsAPrivateInstanceOffItsRoute: private exposure renders no Ingress, so it has no host and therefore
// no way to reach its API description.
func TestDeclareKeepsAPrivateInstanceOffItsRoute(t *testing.T) {
	spec := declaredSpec()
	spec.Grants.Exposure = "private"
	declared, err := Declare(spec, declaredRecord(t), app(t), profile(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declared.Hosts) != 0 || declared.API != "" || declared.Address == "" {
		t.Fatalf("declared %+v", declared)
	}
}

// TestDeclareStatesNoDatabaseForAReleaseWithoutOne: the path follows the release, not the environment.
func TestDeclareStatesNoDatabaseForAReleaseWithoutOne(t *testing.T) {
	record := declaredRecord(t)
	record.Database = ""
	declared, err := Declare(declaredSpec(), record, app(t), profile(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if declared.Database.Kind != "" || declared.Database.Path != "" {
		t.Fatalf("a stateless release states a database: %+v", declared.Database)
	}
}

// sameDeclaration compares everything but the connection rows, which the tests of their own order compare.
func sameDeclaration(got, want Declared) bool {
	return got.Team == want.Team && got.Kind == want.Kind && got.Exposure == want.Exposure && got.Port == want.Port &&
		slices.Equal(got.Hosts, want.Hosts) && got.Address == want.Address && got.API == want.API &&
		got.Artifact == want.Artifact && got.Database == want.Database && got.Offers == want.Offers &&
		got.Egress.Port == want.Egress.Port && slices.Equal(got.Egress.Grants, want.Egress.Grants) &&
		slices.Equal(got.Calls, want.Calls) && slices.Equal(got.Allows, want.Allows) &&
		slices.Equal(got.Callers, want.Callers) && slices.Equal(got.Publishes, want.Publishes) &&
		slices.Equal(got.Subscriptions, want.Subscriptions)
}
