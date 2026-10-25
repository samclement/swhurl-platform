package observe_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// helmRelease is the HelmRelease file of one golden compilation, and the application it belongs to.
func helmRelease(t *testing.T, name string) ([]byte, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "compiled", name))
	if err != nil {
		t.Fatal(err)
	}
	var compiled struct {
		Files []struct {
			Path     string
			Document json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &compiled); err != nil {
		t.Fatal(err)
	}
	for _, file := range compiled.Files {
		if filepath.Base(file.Path) == "helmrelease.json" {
			return file.Document, filepath.Base(filepath.Dir(file.Path))
		}
	}
	t.Fatalf("%s holds no HelmRelease", name)
	return nil, ""
}

// unitRef is a compiled unit's identity, as the caller supplies it.
func unitRef() observe.Ref {
	return observe.Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization",
		Namespace: "flux-system", Name: "app-demo-orders-prod"}
}

// The target comes from the compiled HelmRelease: its objects, its selector, its container, the pinned
// digest and the intended replica count. A caller supplies only what the document cannot say.
func TestTargetOfDerivesTheCompiledInstance(t *testing.T) {
	for _, tt := range []struct {
		name     string
		replicas int32
		digest   string
	}{
		{"prod.json", 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"staging.json", 2, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			document, environment := helmRelease(t, tt.name)
			target, err := observe.TargetOf(document, unitRef(), commit)
			if err != nil {
				t.Fatal(err)
			}
			want := observe.Target{
				Namespace: "demo-orders-" + environment,
				Unit:      unitRef(),
				Release: observe.Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease",
					Namespace: "demo-orders-" + environment, Name: "demo-orders"},
				Workload: observe.Ref{APIVersion: "apps/v1", Kind: "Deployment",
					Namespace: "demo-orders-" + environment, Name: "demo-orders"},
				Pods:      observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: "demo-orders-" + environment},
				Selector:  "app.kubernetes.io/instance=demo-orders,app.kubernetes.io/controller=main",
				Container: "main",
				Digest:    "sha256:" + tt.digest,
				Replicas:  tt.replicas,
				Revision:  commit,
			}
			if target != want {
				t.Fatalf("derived target:\n%+v\nwant:\n%+v", target, want)
			}
			// The derived target must be one the projection accepts: with no evidence it reports
			// every fact unassessed rather than refusing the target.
			if _, err := observe.Explain(target, observe.Evidence{}); err != nil {
				t.Fatalf("the derived target is not a valid target: %v", err)
			}
		})
	}
}

// A document that is not the shape the compiler renders is refused, with a fixed reason: describing a
// workload the instance does not have is worse than reporting nothing.
func TestTargetOfRefusesUnrecognisedShapes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(map[string]any)
		discard string // a whole document to use instead
		field   string
		reason  string
	}{
		{"not JSON", nil, "not json", "release", "single_utf8_json_required"},
		{"no metadata", func(document map[string]any) { document["metadata"] = map[string]any{} }, "", "release", "metadata_required"},
		{"no controller", func(document map[string]any) { values(document)["controllers"] = map[string]any{} }, "", "release", "single_controller_required"},
		{"two controllers", func(document map[string]any) {
			values(document)["controllers"] = map[string]any{"main": controller(document), "worker": controller(document)}
		}, "", "release", "single_controller_required"},
		{"a named controller", func(document map[string]any) {
			values(document)["controllers"] = map[string]any{"web": controller(document)}
		}, "", "release", "single_controller_required"},
		{"a stateful controller", func(document map[string]any) { controller(document)["type"] = "statefulset" }, "", "release", "controller_type_unsupported"},
		{"no container", func(document map[string]any) { controller(document)["containers"] = map[string]any{} }, "", "release", "container_required"},
		{"no digest", func(document map[string]any) {
			image(document)["digest"] = ""
			delete(image(document), "digest")
		}, "", "release", "digest_required"},
		{"a tag instead of a digest", func(document map[string]any) { image(document)["digest"] = "1.2.3" }, "", "release", "digest_required"},
		{"no replica count", func(document map[string]any) { delete(controller(document), "replicas") }, "", "release", "replicas_required"},
		{"a negative replica count", func(document map[string]any) { controller(document)["replicas"] = -1 }, "", "release", "replicas_required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			document, _ := helmRelease(t, "prod.json")
			if tt.discard != "" {
				document = []byte(tt.discard)
			} else {
				decoded := map[string]any{}
				if err := json.Unmarshal(document, &decoded); err != nil {
					t.Fatal(err)
				}
				tt.change(decoded)
				document = encode(t, decoded)
			}
			_, err := observe.TargetOf(document, unitRef(), commit)
			if !refusal(err, tt.field, tt.reason) {
				t.Fatalf("got %v, want %s/%s", err, tt.field, tt.reason)
			}
		})
	}
}

// A unit document states its own identity, so the caller does not repeat the unit naming convention.
func TestUnitRefReadsTheCompiledUnit(t *testing.T) {
	document := encode(t, map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]any{"name": "app-demo-orders-prod", "namespace": "flux-system"},
	})
	ref, err := observe.UnitRef(document)
	if err != nil {
		t.Fatal(err)
	}
	if ref != unitRef() {
		t.Fatalf("unit ref %+v", ref)
	}
	if _, err := observe.UnitRef([]byte("not json")); err == nil {
		t.Fatal("a document that is not JSON was accepted")
	}
}

// The helpers reach into the decoded document, so a fixture change names what broke.
func values(document map[string]any) map[string]any {
	return document["spec"].(map[string]any)["values"].(map[string]any)
}

func controller(document map[string]any) map[string]any {
	return values(document)["controllers"].(map[string]any)["main"].(map[string]any)
}

func image(document map[string]any) map[string]any {
	return controller(document)["containers"].(map[string]any)["main"].(map[string]any)["image"].(map[string]any)
}
