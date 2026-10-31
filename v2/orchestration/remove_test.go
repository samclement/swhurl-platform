package orchestration_test

import (
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

const removalRevision = "8ae21b491dcbbe7306bc56073460fff24d61a7f2"

// resolution is a call or event edge the removal has to respect, or none.
func resolution(t *testing.T, pairs, subscriptions string) []byte {
	t.Helper()
	return []byte(`{"kind":"v2-calls","version":1,"revision":"` + removalRevision + `","pairs":[` + pairs +
		`],"waiting":[],"refused":[],"subscriptions":[` + subscriptions + `]}`)
}

// registration is a platform kustomization that lists the instance's files.
func registration(path, entry string) orchestration.Registration {
	return orchestration.Registration{Path: path, Document: []byte("resources:\n  - infra.yaml\n  - " + entry + "\n")}
}

func removal(t *testing.T, calls []byte) orchestration.RemovalInputs {
	t.Helper()
	return orchestration.RemovalInputs{
		Instance: testdata(t, "staging.json"), Release: testdata(t, "r42.json"), Calls: [][]byte{calls},
		Records: "v2-records/demo-orders/staging", Compiled: "v2-apps/instances/demo-orders/staging",
		Unit: "clusters/home/app-demo-orders-staging.json", AppRecord: "v2-records/demo-orders/app.json",
		LastEnvironment: true, AuthFile: "platform/auth/demo-orders-staging.json",
		UnitRegistration: registration("clusters/home/kustomization.yaml", "app-demo-orders-staging.json"),
		AuthRegistration: orchestration.Registration{Path: "platform/auth/kustomization.yaml",
			Document: []byte(`{"apiVersion":"kustomize.config.k8s.io/v1beta1","kind":"Kustomization",` +
				`"resources":["swhurl-auth-demo-orders-prod.json","swhurl-auth-demo-orders-staging.json"]}`)}}
}

// A removal deletes the instance's own files and the entries that named them, and writes the lists without them first.
func TestRemoveDeletesWhatItNames(t *testing.T) {
	writer := filestest.New()
	writer.Contents["v2-records/demo-orders/staging/instance.json"] = []byte("{}")
	writer.Contents["v2-records/demo-orders/staging/events.sops.yaml"] = []byte("sops:\n")
	writer.Contents["v2-records/demo-orders/app.json"] = []byte("{}")
	writer.Contents["v2-apps/instances/demo-orders/staging/namespace.json"] = []byte("{}")
	writer.Contents["clusters/home/app-demo-orders-staging.json"] = []byte("{}")
	writer.Contents["platform/auth/demo-orders-staging.json"] = []byte("{}")
	result, err := orchestration.Remove(removal(t, resolution(t, "", "")), t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"v2-records/demo-orders/staging/instance.json",
		"v2-records/demo-orders/app.json", "v2-apps/instances/demo-orders/staging/namespace.json",
		"clusters/home/app-demo-orders-staging.json", "platform/auth/demo-orders-staging.json"} {
		if _, kept := writer.Contents[path]; kept {
			t.Errorf("%s survived the removal", path)
		}
	}
	unitList, written := writer.Contents["clusters/home/kustomization.yaml"]
	if !written || strings.Contains(string(unitList), "app-demo-orders-staging.json") ||
		!strings.Contains(string(unitList), "infra.yaml") {
		t.Errorf("the unit's registration is %s", unitList)
	}
	authList, written := writer.Contents["platform/auth/kustomization.yaml"]
	if !written || strings.Contains(string(authList), "swhurl-auth-demo-orders-staging.json") ||
		!strings.Contains(string(authList), "swhurl-auth-demo-orders-prod.json") {
		t.Errorf("the auth registration is %s", authList)
	}
	if len(result.Removed) == 0 || len(result.Notes) == 0 {
		t.Errorf("the report is %+v", result)
	}
}

// An instance the resolution still connects is not removed: another instance's files render that edge.
func TestRemoveRefusesAConnectedInstance(t *testing.T) {
	writer := filestest.New()
	pair := `{"caller":"demo-shop","callee":"demo-orders","environment":"staging","port":8080}`
	if _, err := orchestration.Remove(removal(t, resolution(t, pair, "")), t.TempDir(), writer); err == nil ||
		!strings.Contains(err.Error(), "called_by demo-shop") {
		t.Fatalf("a called instance was removed: %v", err)
	}
	subscription := `{"subscriber":"demo-notifier","publisher":"demo-orders","event":"created","environment":"staging",` +
		`"stream":"staging-demo-orders","consumer":"demo-notifier_created","subject":"staging.demo-orders.created",` +
		`"object":"demo-orders-created"}`
	if _, err := orchestration.Remove(removal(t, resolution(t, "", subscription)), t.TempDir(), writer); err == nil ||
		!strings.Contains(err.Error(), "subscribed_by demo-notifier") {
		t.Fatalf("a published-to instance was removed: %v", err)
	}
	if writer.Writes != 0 || writer.Removals != 0 {
		t.Error("a refused removal changed something")
	}
}

// The app record outlives the app's first environment: only the last removal takes it.
func TestRemoveKeepsTheAppRecordWhileAnEnvironmentRemains(t *testing.T) {
	writer := filestest.New()
	writer.Contents["v2-records/demo-orders/app.json"] = []byte("{}")
	inputs := removal(t, resolution(t, "", ""))
	inputs.LastEnvironment = false
	if _, err := orchestration.Remove(inputs, t.TempDir(), writer); err != nil {
		t.Fatal(err)
	}
	if _, kept := writer.Contents["v2-records/demo-orders/app.json"]; !kept {
		t.Error("the app record was removed while production remains")
	}
}

// A registration that lists the instance's file twice is refused: v2 does not know which entry is the instance's.
func TestRemoveRefusesADoubledRegistration(t *testing.T) {
	writer := filestest.New()
	inputs := removal(t, resolution(t, "", ""))
	inputs.UnitRegistration.Document = []byte("resources:\n  - app-demo-orders-staging.json\n  - app-demo-orders-staging.json\n")
	if _, err := orchestration.Remove(inputs, t.TempDir(), writer); err == nil {
		t.Fatal("a doubled registration was accepted")
	}
	if writer.Removals != 0 {
		t.Error("a refused removal deleted something")
	}
}
