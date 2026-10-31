package kernel_test

import (
	"os"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// An instance's settings are changed one at a time, and each change carries exactly that setting.

func stagingInstance(t *testing.T) kernel.InstanceSpec {
	t.Helper()
	data, err := os.ReadFile("../testdata/staging.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := kernel.DecodeInstance(data)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// webRelease is a release whose kind lets an instance be exposed; a worker is the kind that cannot.
func webRelease(kind string) kernel.ReleaseBundle {
	return kernel.ReleaseBundle{Runtime: kernel.Runtime{Kind: kind}}
}

func TestWithReplicasSetsOnlyReplicas(t *testing.T) {
	spec := stagingInstance(t)
	changed, err := kernel.WithReplicas(spec, 5)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Configuration.Replicas != 5 {
		t.Errorf("replicas are %d, want 5", changed.Configuration.Replicas)
	}
	if changed.Configuration.MemoryMiB != spec.Configuration.MemoryMiB || changed.Grants != spec.Grants ||
		len(changed.Allows) != len(spec.Allows) {
		t.Error("the change carried a second setting")
	}
	if spec.Configuration.Replicas != 2 {
		t.Error("the record the caller gave was modified")
	}
	if _, err := kernel.WithReplicas(spec, -1); err == nil {
		t.Error("a negative replica count was accepted")
	}
}

func TestWithMemorySetsOnlyMemory(t *testing.T) {
	spec := stagingInstance(t)
	changed, err := kernel.WithMemory(spec, 512)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Configuration.MemoryMiB != 512 || changed.Configuration.Replicas != spec.Configuration.Replicas {
		t.Errorf("the change is %+v", changed.Configuration)
	}
	if _, err := kernel.WithMemory(spec, 0); err == nil {
		t.Error("a zero memory was accepted")
	}
}

// An exposure is the release's to permit: a worker serves nothing, so it has no route to sign in to.
func TestWithExposureRefusesAWorker(t *testing.T) {
	spec := stagingInstance(t)
	if _, err := kernel.WithExposure(spec, webRelease("worker"), "authenticated-web"); err == nil {
		t.Fatal("a worker was exposed")
	}
	private, err := kernel.WithExposure(spec, webRelease("worker"), "private")
	if err != nil || private.Grants.Exposure != "private" {
		t.Fatalf("a worker could not be private: %v", err)
	}
	exposed, err := kernel.WithExposure(spec, webRelease("web"), "authenticated-web")
	if err != nil || exposed.Grants.Exposure != "authenticated-web" {
		t.Fatalf("a web instance could not be exposed: %v", err)
	}
	if _, err := kernel.WithExposure(spec, webRelease("web"), "public"); err == nil {
		t.Error("a public exposure was accepted")
	}
}

// A declaration the resolution could never connect is refused at the edit: the resolver's rules are the record's.
func TestWithCallsAndAllows(t *testing.T) {
	spec := stagingInstance(t)
	calls, err := kernel.WithCalls(spec, []string{"demo-shop", "demo-shopper"})
	if err != nil || len(calls.Calls) != 2 {
		t.Fatalf("calls: %v, %v", calls.Calls, err)
	}
	if _, err := kernel.WithCalls(spec, []string{"demo-orders"}); err == nil {
		t.Error("an instance was allowed to call itself")
	}
	if _, err := kernel.WithCalls(spec, []string{"NotAnApp"}); err == nil {
		t.Error("a malformed app name was accepted")
	}
	allows, err := kernel.WithAllows(spec, []string{"team:demo", "demo-shop"})
	if err != nil || len(allows.Allows) != 2 {
		t.Fatalf("allows: %v, %v", allows.Allows, err)
	}
	if _, err := kernel.WithAllows(spec, []string{"demo-orders"}); err == nil {
		t.Error("an instance was allowed to admit itself")
	}
	cleared, err := kernel.WithCalls(calls, nil)
	if err != nil || cleared.Calls != nil {
		t.Errorf("clearing the calls left %v (%v)", cleared.Calls, err)
	}
}

// Subscribing needs an identity to sign in with: an instance without a key is refused rather than rendered a
// consumer it could not use.
func TestWithSubscriptionsNeedsAKey(t *testing.T) {
	spec := stagingInstance(t)
	if _, err := kernel.WithSubscriptions(spec, []string{"demo-orders.created"}); err == nil {
		t.Fatal("an instance without a key subscribed")
	}
	spec.Events.Key = "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI"
	subscribed, err := kernel.WithSubscriptions(spec, []string{"demo-orders.created"})
	if err != nil || len(subscribed.Events.Subscriptions) != 1 {
		t.Fatalf("subscriptions: %v, %v", subscribed.Events.Subscriptions, err)
	}
	if _, err := kernel.WithSubscriptions(spec, []string{"not-an-event"}); err == nil {
		t.Error("a malformed subscription was accepted")
	}
	cleared, err := kernel.WithSubscriptions(spec, nil)
	if err != nil || cleared.Events.Subscriptions != nil {
		t.Errorf("clearing the subscriptions left %v (%v)", cleared.Events.Subscriptions, err)
	}
}

// A team is the app's fact, so the change is the app record's and its identity follows the new team.
func TestWithTeam(t *testing.T) {
	app, err := kernel.DecodeApp([]byte(`{"schemaVersion":1,"application":"demo-orders","team":"demo"}`))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := kernel.WithTeam(app, "payments")
	if err != nil || changed.Team != "payments" {
		t.Fatalf("team: %v, %v", changed.Team, err)
	}
	before, err := app.ID()
	if err != nil {
		t.Fatal(err)
	}
	after, err := changed.ID()
	if err != nil || before == after {
		t.Fatalf("the app's identity did not follow its team: %v, %v", before, after)
	}
	if _, err := kernel.WithTeam(app, "Not A Team"); err == nil {
		t.Error("a malformed team was accepted")
	}
	if _, err := kernel.WithTeam(app, ""); err == nil {
		t.Error("an empty team was accepted")
	}
}
