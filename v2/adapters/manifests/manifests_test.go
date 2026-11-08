package manifests_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests/manifeststest"
	"github.com/samclement/swhurl-platform/v2/checkrepo"
)

func TestContract(t *testing.T) {
	manifeststest.Contract(t, func(*testing.T) manifests.Decoder { return manifests.YAML{} })
}

func decode(t *testing.T, name string) []checkrepo.Document {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	documents, err := manifests.YAML{}.Documents(text)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return documents
}

// A SOPS Secret as Git holds one: its kind and name, the ciphertext as written and the metadata the check reads.
func TestASOPSSecret(t *testing.T) {
	documents := decode(t, "sops-secret.yaml")
	if len(documents) != 1 || documents[0].Kind != "Secret" || documents[0].Name != "example" {
		t.Fatalf("documents = %#v", documents)
	}
	secret := documents[0].Value
	sops, _ := secret["sops"].(map[string]any)
	data, _ := secret["stringData"].(map[string]any)
	if sops["encrypted_regex"] != "^(data|stringData)$" || sops["mac"] == nil || len(sops["age"].([]any)) != 1 {
		t.Errorf("sops = %#v", sops)
	}
	if data["address"] != "ENC[AES256_GCM,data:c3ludGhldGlj,iv:aXY=,tag:dGFn,type:str]" {
		t.Errorf("stringData = %#v", data)
	}
}

// A unit v2 compiles to JSON reads like a YAML one, its optional source a boolean.
func TestAJSONUnit(t *testing.T) {
	documents := decode(t, "unit.json")
	want := []any{map[string]any{"kind": "Secret", "name": "backup-writer", "optional": false}}
	spec, _ := documents[0].Value["spec"].(map[string]any)
	postBuild, _ := spec["postBuild"].(map[string]any)
	if documents[0].Name != "app-example-staging" || !reflect.DeepEqual(postBuild["substituteFrom"], want) {
		t.Fatalf("documents = %#v", documents)
	}
}

// A unit file of several documents: each in order, one of another kind, and the empty document a trailing separator
// leaves, which is not a mapping.
func TestAUnitFileOfSeveralDocuments(t *testing.T) {
	documents := decode(t, "units.yaml")
	var kinds, names []string
	for _, document := range documents {
		kinds, names = append(kinds, document.Kind), append(names, document.Name)
	}
	if !reflect.DeepEqual(names, []string{"platform-example", "not-a-unit", "platform-other", ""}) ||
		!reflect.DeepEqual(kinds, []string{"Kustomization", "ConfigMap", "Kustomization", ""}) || documents[3].Value != nil {
		t.Fatalf("kinds %q, names %q", kinds, names)
	}
	data, _ := documents[1].Value["data"].(map[string]any)
	if data["enabled"] != true || data["quoted"] != "on" {
		t.Errorf("data = %#v: a plain on is YAML 1.1's boolean, a quoted one a string", data)
	}
}

// An alias that refers to itself cannot be expanded and is refused rather than read forever.
func TestASelfReferringAliasIsRefused(t *testing.T) {
	if documents, err := (manifests.YAML{}).Documents([]byte("a: &x [*x]\n")); err == nil || documents != nil {
		t.Fatalf("documents %v, error %v; want a refusal", documents, err)
	}
	if _, err := (manifests.YAML{}).Documents([]byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 was read")
	}
}
