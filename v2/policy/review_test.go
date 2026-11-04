package policy

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestReviewForeignNamespacesCannotSatisfyRelationships(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		source bool
		rules  []string
	}{
		{"Deployment", false, []string{"identity", "workload"}},
		{"CronJob", true, []string{"identity", "call-tokens"}},
		{"Service", false, []string{"identity", "runtime"}},
		{"PersistentVolumeClaim", false, []string{"identity", "storage"}},
		{"NetworkPolicy", true, []string{"identity", "network-policy"}},
		{"Ingress", true, []string{"identity", "exposure"}},
		{"ResourceQuota", true, []string{"identity", "quota"}},
		{"ConfigMap", true, []string{"identity"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			fixture := fixtureScenario(t)
			objects := fixture.Rendered
			if tc.source {
				objects = fixture.Source
			}
			mutate(t, objects, tc.kind, func(o map[string]any) { o["metadata"].(map[string]any)["namespace"] = "unprotected" })
			got := rules(t, fixture)
			for _, want := range tc.rules {
				if !slices.Contains(got, want) {
					t.Errorf("foreign %s missing %s: %v", tc.kind, want, got)
				}
			}
		})
	}
}
func TestReviewClusterOnlyServiceExternalIPs(t *testing.T) {
	fixture := fixtureScenario(t)
	mutate(t, fixture.Rendered, "Service", func(o map[string]any) { spec(o)["externalIPs"] = []any{"203.0.113.1"} })
	if got := rules(t, fixture); !slices.Contains(got, "exposure") {
		t.Fatalf("external Service IP passed: %v", got)
	}
}
func TestReviewServiceSelectorMustSelectMain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selector any
	}{
		{"wrong", map[string]any{"app": "no-such-pod"}}, {"empty", map[string]any{}}, {"missing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fixtureScenario(t)
			mutate(t, fixture.Rendered, "Service", func(o map[string]any) { spec(o)["selector"] = tc.selector })
			if got := rules(t, fixture); !slices.Contains(got, "runtime") {
				t.Fatalf("nonselecting Service passed: %v", got)
			}
		})
	}
}
func TestReviewWholeDirectoryMountsRejectSubpaths(t *testing.T) {
	for _, field := range []string{"subPath", "subPathExpr"} {
		for _, volume := range []struct{ name, rule string }{{"tokens", "call-tokens"}, {"auth", "identity"}, {"callers", "identity"}} {
			t.Run(field+"/"+volume.name, func(t *testing.T) {
				fixture := fixtureScenario(t)
				mutate(t, fixture.Rendered, "Deployment", func(o map[string]any) {
					found := false
					for _, raw := range mainMap(o)["volumeMounts"].([]any) {
						mount := raw.(map[string]any)
						if mount["name"] == volume.name {
							mount[field] = "notify"
							found = true
						}
					}
					if !found {
						t.Fatalf("no fixture mount %s", volume.name)
					}
				})
				if got := rules(t, fixture); !slices.Contains(got, volume.rule) {
					t.Fatalf("partial directory mounted as whole: %v", got)
				}
			})
		}
	}
}
func TestReviewHTTPProbeRequestSemantics(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"scheme", "scheme", "HTTPS"}, {"host", "host", "127.0.0.1"},
		{"headers", "httpHeaders", []any{map[string]any{"name": "Host", "value": "wrong.example"}}},
		{"disable-default-accept", "httpHeaders", []any{map[string]any{"name": "Accept", "value": ""}}},
		{"disable-default-agent", "httpHeaders", []any{map[string]any{"name": "User-Agent", "value": ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fixtureScenario(t)
			mutate(t, fixture.Rendered, "Deployment", func(o map[string]any) {
				mainMap(o)["readinessProbe"].(map[string]any)["httpGet"].(map[string]any)[tc.field] = tc.value
			})
			if got := rules(t, fixture); !slices.Contains(got, "runtime") {
				t.Fatalf("changed HTTP request passed: %v", got)
			}
		})
	}
}
func TestReviewBaselineDefaultsAndUnknownScopes(t *testing.T) {
	fixture := fixtureScenario(t)
	mutate(t, fixture.Rendered, "Deployment", func(o map[string]any) {
		get := mainMap(o)["readinessProbe"].(map[string]any)["httpGet"].(map[string]any)
		get["scheme"] = "HTTP"
		get["host"] = ""
		get["httpHeaders"] = []any{map[string]any{"name": "aCcEpT", "value": "*/*"}}
	})
	fixture.Rendered = append(fixture.Rendered, json.RawMessage(`{"apiVersion":"example.test/v1","kind":"UnknownScope","metadata":{"name":"retained"}}`))
	if got := rules(t, fixture); len(got) > 0 {
		t.Fatalf("equivalent defaults or absent unknown namespace rejected: %v", got)
	}
}

func TestReviewOmittedKnownNamespacesUseHelmTarget(t *testing.T) {
	fixture := fixtureScenario(t)
	for _, kind := range []string{"Deployment", "Service", "PersistentVolumeClaim"} {
		mutate(t, fixture.Rendered, kind, func(o map[string]any) { delete(o["metadata"].(map[string]any), "namespace") })
	}
	if got := rules(t, fixture); len(got) > 0 {
		t.Fatalf("implicit target namespace rejected: %v", got)
	}
}

func TestReviewCompilerFluxNamespaceIsNotChartPermission(t *testing.T) {
	fixture := fixtureScenario(t)
	automation := json.RawMessage(`{"apiVersion":"image.toolkit.fluxcd.io/v1","kind":"ImageRepository","metadata":{"name":"automation","namespace":"flux-system"}}`)
	fixture.Source = append(fixture.Source, automation)
	if got := rules(t, fixture); len(got) > 0 {
		t.Fatalf("compiler-owned cross-namespace Flux object rejected: %v", got)
	}
	fixture.Rendered = append(fixture.Rendered, automation)
	if got := rules(t, fixture); !slices.Contains(got, "identity") {
		t.Fatalf("chart could emit explicit foreign namespace: %v", got)
	}
}
