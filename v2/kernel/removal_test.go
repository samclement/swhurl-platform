package kernel_test

import (
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// A pruning writer has to tell a rendered file from one the folder keeps, so the vocabulary is the renderer's own:
// every name resourcesOf can produce, and nothing else.
func TestRendered(t *testing.T) {
	for _, name := range []string{"namespace.json", "helmrelease.json", "resourcequota.json", "networkpolicy.json",
		"image-repository.json", "image-policy.json", "ingress.json", "callers.json", "stream.json",
		"consumer-orders-created.json", "backup-secret.json", "backup-cronjob.json", "backup-networkpolicy.json",
		"kustomization.yaml"} {
		if !kernel.Rendered(name) {
			t.Errorf("%s is not a rendered name", name)
		}
	}
	// A declared input and anything else a folder holds are not the renderer's to remove.
	for _, name := range []string{"events.sops.yaml", "openapi.json", "consumer.json", "notes.txt",
		"asyncapi.yaml", "source.yaml", "flags.json"} {
		if kernel.Rendered(name) {
			t.Errorf("%s is treated as a rendered name", name)
		}
	}
}

// A registration is a list v2 edits one line of: the entry goes, and every other byte stays as it was.
func TestWithoutEntry(t *testing.T) {
	document := []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n" +
		"  - infra.yaml\n  - app-demo-orders-staging.json\n  - app-demo-orders-prod.yaml\n")
	edited, removed, err := kernel.WithoutEntry(document, "app-demo-orders-staging.json")
	if err != nil || !removed {
		t.Fatalf("removing an entry: %v, %v", removed, err)
	}
	if strings.Contains(string(edited), "app-demo-orders-staging.json") {
		t.Errorf("the entry survived:\n%s", edited)
	}
	for _, kept := range []string{"infra.yaml", "app-demo-orders-prod.yaml", "kind: Kustomization"} {
		if !strings.Contains(string(edited), kept) {
			t.Errorf("the edit dropped %s:\n%s", kept, edited)
		}
	}
	if _, removed, err := kernel.WithoutEntry(edited, "app-demo-orders-staging.json"); err != nil || removed {
		t.Error("an entry that is not there was reported as removed")
	}
	twice := append(append([]byte{}, document...), "- app-demo-orders-staging.json\n"...)
	if _, _, err := kernel.WithoutEntry(twice, "app-demo-orders-staging.json"); err == nil {
		t.Error("a doubled registration was accepted")
	}
}

// A rendered kustomization is JSON: dropping a resource is dropping it from the list, and every other byte of meaning is
// kept — the answer is what the renderer would write for the folder without it.
func TestWithoutResource(t *testing.T) {
	document := []byte(`{"apiVersion":"kustomize.config.k8s.io/v1beta1","kind":"Kustomization",` +
		`"resources":["swhurl-auth-a-staging.json","swhurl-auth-a-prod.json"]}`)
	edited, removed, err := kernel.WithoutResource(document, "swhurl-auth-a-staging.json")
	if err != nil || !removed {
		t.Fatalf("dropping a resource: %v, %v", removed, err)
	}
	want := `{"apiVersion":"kustomize.config.k8s.io/v1beta1","kind":"Kustomization",` +
		`"resources":["swhurl-auth-a-prod.json"]}`
	if string(edited) != want {
		t.Errorf("the edit is\n%s\nwant\n%s", edited, want)
	}
	if _, removed, err := kernel.WithoutResource(edited, "swhurl-auth-a-staging.json"); err != nil || removed {
		t.Error("a resource that is not listed was reported as removed")
	}
	twice := []byte(`{"kind":"Kustomization","resources":["a.json","a.json"]}`)
	if _, _, err := kernel.WithoutResource(twice, "a.json"); err == nil {
		t.Error("a doubled resource was accepted")
	}
	if _, _, err := kernel.WithoutResource([]byte("resources:\n"), "a.json"); err == nil {
		t.Error("a YAML document was accepted as a rendered one")
	}
}

// An instance's places are one convention: the compiled folder under the delivery root and the unit under the unit root.
func TestInstancePaths(t *testing.T) {
	data, err := os.ReadFile("../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := kernel.DecodeProfile(data)
	if err != nil {
		t.Fatal(err)
	}
	if folder := kernel.InstanceFolder("demo-orders", "staging", profile); folder != "v2-apps/instances/demo-orders/staging" {
		t.Errorf("the compiled folder is %q", folder)
	}
	if unit := kernel.UnitPath("demo-orders", "staging", profile); unit != "clusters/home/app-demo-orders-staging.json" {
		t.Errorf("the unit is %q", unit)
	}
}
