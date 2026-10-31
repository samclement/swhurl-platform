package orchestration_test

import (
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// testdata is one of the module's raw fixtures.
func testdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// instanceInputs is one instance's documents as an edit reads them from the tree.
func instanceInputs(t *testing.T, instance, release string) orchestration.Inputs {
	t.Helper()
	return orchestration.Inputs{Instance: testdata(t, instance), Release: testdata(t, release),
		App: testdata(t, "app.json"), Profile: testdata(t, "profile.json")}
}

// applyFor is a kernel change bound to one value, as a command binds it.
func applyFor(rule func(kernel.InstanceSpec, int32) (kernel.InstanceSpec, error), value int32) func(kernel.InstanceSpec, kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
	return func(spec kernel.InstanceSpec, _ kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
		return rule(spec, value)
	}
}

func editable(t *testing.T, rule func(kernel.InstanceSpec, int32) (kernel.InstanceSpec, error), value int32) orchestration.Editable {
	t.Helper()
	return orchestration.Editable{
		InstanceInputs: orchestration.InstanceInputs{Inputs: instanceInputs(t, "staging.json", "r42.json"),
			InstancePath: "v2-records/demo-orders/staging/instance.json"},
		Change: "replicas", Apply: applyFor(rule, value)}
}

// One change writes the record it changed and the files it renders, in one write, and prunes the folder.
func TestEditWritesTheRecordAndItsFiles(t *testing.T) {
	writer := filestest.New()
	result, err := orchestration.Edit(editable(t, kernel.WithReplicas, 3), t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if result.After.Configuration.Replicas != 3 {
		t.Fatalf("the report's record has %d replicas", result.After.Configuration.Replicas)
	}
	if writer.Writes != 1 || writer.Prunes != 1 {
		t.Fatalf("%d writes and %d prunes, want one of each", writer.Writes, writer.Prunes)
	}
	record, written := writer.Contents["v2-records/demo-orders/staging/instance.json"]
	if !written {
		t.Fatal("the record was not written")
	}
	decoded, err := kernel.DecodeInstance(record)
	if err != nil || decoded.Configuration.Replicas != 3 {
		t.Fatalf("the written record is %s (%v)", record, err)
	}
	if _, written := writer.Contents["v2-apps/instances/demo-orders/staging/helmrelease.json"]; !written {
		t.Error("the compiled files were not written")
	}
	if result.ResolutionStale {
		t.Error("a replica change made the resolution stale")
	}
}

// A file the records no longer render does not outlive the change: the folder is pruned, and the change reports it.
func TestEditPrunesWhatTheRecordsStoppedRendering(t *testing.T) {
	writer := filestest.New()
	writer.Contents["v2-apps/instances/demo-orders/staging/callers.json"] = []byte(`{"kind":"ConfigMap"}`)
	writer.Contents["v2-apps/instances/demo-orders/staging/events.sops.yaml"] = []byte("sops:\n")
	result, err := orchestration.Edit(editable(t, kernel.WithReplicas, 4), t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := writer.Contents["v2-apps/instances/demo-orders/staging/callers.json"]; kept {
		t.Error("the stale callers.json survived the change")
	}
	if _, kept := writer.Contents["v2-apps/instances/demo-orders/staging/events.sops.yaml"]; !kept {
		t.Error("the prune removed the folder's seed")
	}
	removed := false
	for _, outcome := range result.Written {
		if strings.HasSuffix(outcome.Path, "callers.json") {
			removed = outcome.State == "removed"
		}
	}
	if !removed {
		t.Errorf("the report does not name the removal: %+v", result.Written)
	}
}

// A change that leaves the record as it was is refused: repeating a command is not a rewrite.
func TestEditRefusesAChangeThatChangesNothing(t *testing.T) {
	writer := filestest.New()
	_, err := orchestration.Edit(editable(t, kernel.WithReplicas, 2), t.TempDir(), writer)
	if err == nil || !strings.Contains(err.Error(), "already_set") {
		t.Fatalf("the repeated change was not refused: %v", err)
	}
	if writer.Writes != 0 {
		t.Error("a refused change wrote something")
	}
}

// A relationship change leaves the committed resolution older than the record, and the report says so.
func TestEditSaysTheResolutionIsStale(t *testing.T) {
	writer := filestest.New()
	one := editable(t, kernel.WithReplicas, 3)
	one.Change = "calls"
	one.Apply = func(spec kernel.InstanceSpec, _ kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
		return kernel.WithCalls(spec, []string{"demo-shop"})
	}
	result, err := orchestration.Edit(one, t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ResolutionStale {
		t.Fatal("a calls change did not mark the resolution stale")
	}
	if len(result.After.Calls) != 1 {
		t.Errorf("the record states %v", result.After.Calls)
	}
}

// A team is the app's fact: one change writes the app record and re-renders every environment, and no instance record
// is rewritten.
func TestSetTeamReachesEveryEnvironment(t *testing.T) {
	writer := filestest.New()
	inputs := orchestration.TeamInputs{App: testdata(t, "app.json"), AppPath: "v2-records/demo-orders/app.json",
		Team: "payments"}
	for _, pair := range []struct{ instance, release string }{{"staging.json", "r42.json"}, {"prod.json", "r41.json"}} {
		inputs.Instances = append(inputs.Instances, orchestration.InstanceInputs{
			Inputs:       instanceInputs(t, pair.instance, pair.release),
			InstancePath: "v2-records/demo-orders/" + pair.instance})
	}
	result, err := orchestration.SetTeam(inputs, t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if result.After.Team != "payments" || len(result.Instances) != 2 {
		t.Fatalf("the report is %+v", result)
	}
	app, written := writer.Contents["v2-records/demo-orders/app.json"]
	if !written {
		t.Fatal("the app record was not written")
	}
	decoded, err := kernel.DecodeApp(app)
	if err != nil || decoded.Team != "payments" {
		t.Fatalf("the written app record is %s (%v)", app, err)
	}
	for _, environment := range []string{"staging", "prod"} {
		if _, written := writer.Contents["v2-apps/instances/demo-orders/"+environment+"/namespace.json"]; !written {
			t.Errorf("%s was not re-rendered", environment)
		}
		if _, written := writer.Contents["v2-records/demo-orders/"+environment+"/instance.json"]; written {
			t.Errorf("%s's instance record was rewritten", environment)
		}
	}
	if writer.Prunes != 2 {
		t.Errorf("%d prunes, want one per environment", writer.Prunes)
	}
	// The fixture's app record already names the team "demo", so asking for it is a change that changes nothing.
	if _, err := orchestration.SetTeam(orchestration.TeamInputs{App: testdata(t, "app.json"), Team: "demo",
		AppPath: "v2-records/demo-orders/app.json", Instances: inputs.Instances}, t.TempDir(), writer); err == nil {
		t.Error("the same team again was accepted")
	}
}

// A tree whose record names another app than its release is refused before anything is written.
func TestEditRefusesRecordsThatDisagree(t *testing.T) {
	writer := filestest.New()
	one := orchestration.Editable{
		InstanceInputs: orchestration.InstanceInputs{Inputs: orchestration.Inputs{Instance: testdata(t, "staging.json"),
			Release: testdata(t, "r41.json"), App: testdata(t, "app.json"), Profile: testdata(t, "profile.json")},
			InstancePath: "v2-records/demo-orders/staging/instance.json"},
		Change: "replicas", Apply: applyFor(kernel.WithReplicas, 3)}
	// r41 is another release of the same app, so the pair agrees; a release of another app disagrees.
	other := one
	other.Inputs.Release = []byte(strings.Replace(string(testdata(t, "r42.json")), `"application": "demo-orders"`,
		`"application": "demo-shop"`, 1))
	if _, err := orchestration.Edit(other, t.TempDir(), writer); err == nil {
		t.Fatal("a release for another app was accepted")
	}
	if writer.Writes != 0 {
		t.Error("a refused edit wrote something")
	}
}
