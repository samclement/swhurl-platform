package kernel

import (
	"strings"
	"testing"
)

// The platform's verification facts are rendered once for every managed namespace: one identical ConfigMap per
// namespace, the kustomization that lists them, and a claim by the platform — never by an instance.
func TestRenderAuthCoversEveryManagedNamespace(t *testing.T) {
	rendered, err := RenderAuth([]string{"orders-staging", "billing-prod", "orders-staging"}, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, file := range rendered.Files {
		paths = append(paths, file.Path)
	}
	want := []string{"auth/swhurl-auth-billing-prod.json", "auth/swhurl-auth-orders-staging.json", "auth/kustomization.yaml"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("the files are %v, want %v", paths, want)
	}
	keys := profile(t).Auth.Keys
	for _, namespace := range []string{"billing-prod", "orders-staging"} {
		document := documentOf(t, rendered, "swhurl-auth-"+namespace+".json")
		if document["kind"] != "ConfigMap" || document["metadata"].(map[string]any)["name"] != "swhurl-auth" ||
			document["metadata"].(map[string]any)["namespace"] != namespace {
			t.Fatalf("%s's object is %v", namespace, document)
		}
		data := document["data"].(map[string]any)
		if data["SWHURL_AUTH_ISSUER"] != profile(t).Auth.Issuer || data["SWHURL_AUTH_KEYS"] != keys {
			t.Fatalf("%s's data is %v", namespace, data)
		}
	}
	if len(rendered.Inventory) != 2 {
		t.Fatalf("the claims are %v", rendered.Inventory)
	}
	for _, claim := range rendered.Inventory {
		if claim.Kind != "ConfigMap" || claim.Name != "swhurl-auth" || claim.Owner != "platform" {
			t.Fatalf("a claim is %v", claim)
		}
	}
	kustomization := documentOf(t, rendered, "kustomization.yaml")
	resources := kustomization["resources"].([]any)
	if len(resources) != 2 || resources[0] != "swhurl-auth-billing-prod.json" {
		t.Fatalf("the kustomization lists %v", resources)
	}
}

// Nothing to render for, or no keys to render, is refused rather than written as an empty or unverifiable unit.
func TestRenderAuthRefusals(t *testing.T) {
	withoutKeys := profile(t)
	withoutKeys.Auth = Auth{}
	if _, err := RenderAuth([]string{"orders-staging"}, withoutKeys); err == nil {
		t.Fatal("a profile with no keys rendered")
	} else {
		refusal(t, err, Unsupported, "auth")
	}
	if _, err := RenderAuth(nil, profile(t)); err == nil {
		t.Fatal("no namespaces rendered")
	} else {
		refusal(t, err, Unsupported, "auth")
	}
	if _, err := RenderAuth([]string{"Not_A_Namespace"}, profile(t)); err == nil {
		t.Fatal("a namespace that is not a label rendered")
	} else {
		refusal(t, err, Invalid, "auth.namespace")
	}
}
