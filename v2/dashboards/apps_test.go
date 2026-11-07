package dashboards

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type releaseSpec struct {
	name, env, unit, kind string
	unowned               bool
}

func helmRelease(t *testing.T, r releaseSpec) []byte {
	t.Helper()
	unit := r.unit
	if unit == "" {
		unit = "app-" + r.name + "-" + r.env
	}
	labels := map[string]string{"kustomize.toolkit.fluxcd.io/name": unit, "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
	if r.unowned {
		labels = nil
	}
	values := map[string]any{}
	switch r.kind {
	case "", Web:
		values["service"] = map[string]any{}
	case "job":
		values["controllers"] = map[string]any{"main": map[string]any{"type": "cronjob"}}
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{"name": r.name, "namespace": r.name + "-" + r.env,
		"labels": labels}, "spec": map[string]any{"values": values}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func namespaceDoc(name, alerts string) []byte {
	annotations := map[string]string{}
	if alerts != "" {
		annotations[AlertsAnnotation] = alerts
	}
	raw, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": name, "annotations": annotations}})
	return raw
}

// Python's discovery cases: only a release applied by its app's own unit, named for the app in the app's namespace,
// is an app; shared services, mismatched names and releases installed by hand are not.
func TestDiscoveryExcludesSharedServicesAndMismatchedNames(t *testing.T) {
	releases := [][]byte{
		helmRelease(t, releaseSpec{name: "job", env: "staging", kind: Worker}),
		helmRelease(t, releaseSpec{name: "console", env: "prod", unit: "platform-console"}),
		helmRelease(t, releaseSpec{name: "wrong", env: "prod", unit: "app-real-prod"}),
		helmRelease(t, releaseSpec{name: "manual", env: "prod", unowned: true}),
	}
	apps, err := Discover(releases, nil)
	want := []App{{Name: "job", Kind: Worker, Environments: []string{"staging"}}}
	if err != nil || !reflect.DeepEqual(apps, want) {
		t.Fatalf("%+v, %v", apps, err)
	}
}

// Only an app's release has its values read, and a stated Service makes it web before its controller is looked at,
// as Python's helmrelease.kind did.
func TestOnlyAnAppReleasesValuesAreReadServiceFirst(t *testing.T) {
	shared, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": "console", "namespace": "console",
		"labels": map[string]string{"kustomize.toolkit.fluxcd.io/name": "platform-console", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}},
		"spec": map[string]any{"values": map[string]any{"controllers": map[string]any{"main": "not an object"}}}})
	both, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": "web", "namespace": "web-staging",
		"labels": map[string]string{"kustomize.toolkit.fluxcd.io/name": "app-web-staging", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}},
		"spec": map[string]any{"values": map[string]any{"service": nil, "controllers": map[string]any{"main": map[string]any{"type": "cronjob"}}}}})
	apps, err := Discover([][]byte{shared, both}, nil)
	if err != nil || len(apps) != 1 || apps[0].Kind != Web {
		t.Fatalf("%+v, %v", apps, err)
	}
}

// An app's environments are staging first; its alert settings come from its namespaces; production joining an app
// adds an environment to the same dashboard.
func TestTheClusterJobReadsAnAppsSettingsFromItsNamespaces(t *testing.T) {
	releases := [][]byte{helmRelease(t, releaseSpec{name: "web", env: "prod"}), helmRelease(t, releaseSpec{name: "web", env: "staging"})}
	namespaces := [][]byte{namespaceDoc("web-staging", "errors=none"), namespaceDoc("other", "")}
	apps, err := Discover(releases, namespaces)
	want := []App{{Name: "web", Kind: Web, Environments: []string{"staging", "prod"}, Alerts: "errors=none"}}
	if err != nil || !reflect.DeepEqual(apps, want) {
		t.Fatalf("%+v, %v", apps, err)
	}
}

// Staging decides an app's kind while production still runs another release.
func TestStagingDecidesTheKindMidPromotion(t *testing.T) {
	releases := [][]byte{helmRelease(t, releaseSpec{name: "web", env: "prod", kind: Worker}), helmRelease(t, releaseSpec{name: "web", env: "staging"})}
	apps, err := Discover(releases, nil)
	if err != nil || len(apps) != 1 || apps[0].Kind != Web {
		t.Fatalf("%+v, %v", apps, err)
	}
}

// Deliberately changed: a scheduled job has no dashboard (v2 runs none), and an app release that is one stops the
// pass rather than losing its dashboard to pruning. A document that does not decode is a failed read.
func TestAJobOrAnUnreadableDocumentStopsDiscovery(t *testing.T) {
	for _, run := range []func() ([]App, error){
		func() ([]App, error) {
			return Discover([][]byte{helmRelease(t, releaseSpec{name: "nightly", env: "staging", kind: "job"})}, nil)
		},
		func() ([]App, error) { return Discover([][]byte{[]byte("{")}, nil) },
		func() ([]App, error) { return Discover(nil, [][]byte{[]byte("[")}) },
	} {
		if apps, err := run(); err == nil || apps != nil {
			t.Fatalf("%+v, %v", apps, err)
		}
	}
	if apps, err := Discover(nil, nil); err != nil || len(apps) != 0 {
		t.Fatalf("an empty cluster is no apps: %+v, %v", apps, err)
	}
}

// Deliberately changed: the records are v2's, and a missing or bad record is a failure, never an app left out.
func TestAppsComeFromTheRecordsOrNothingIsDone(t *testing.T) {
	apps, err := FromRecords([]Instance{{"hello", "prod", Web, ""}, {"hello", "staging", Web, ""}, {"bye", "staging", Worker, ""}})
	want := []App{{Name: "bye", Kind: Worker, Environments: []string{"staging"}}, {Name: "hello", Kind: Web, Environments: []string{"staging", "prod"}}}
	if err != nil || !reflect.DeepEqual(apps, want) {
		t.Fatalf("%+v, %v", apps, err)
	}
	if _, err := FromRecords(nil); !errors.Is(err, ErrNoRecords) {
		t.Fatalf("no records: %v", err)
	}
	for _, bad := range []Instance{{"Bad", "staging", Web, ""}, {"hello", "dev", Web, ""}, {"hello", "staging", "job", ""}} {
		if _, err := FromRecords([]Instance{bad}); err == nil || !strings.Contains(err.Error(), "no change made") {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}
