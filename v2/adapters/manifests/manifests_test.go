package manifests_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// nestedAliases is levels anchors, each a list of ten aliases of the one before: it expands to 10^levels scalars.
func nestedAliases(levels int) string {
	text := "a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n"
	for i := 1; i < levels; i++ {
		ref := fmt.Sprintf("*a%d", i-1)
		text += fmt.Sprintf("a%d: &a%d [%s]\n", i, i, strings.TrimSuffix(strings.Repeat(ref+", ", 10), ", "))
	}
	return text
}

// Nested repeated aliases read in full while small and are refused once they would expand past the budget, not
// expanded exponentially.
func TestNestedAliasesAreBounded(t *testing.T) {
	documents, err := (manifests.YAML{}).Documents([]byte(nestedAliases(3)))
	if err != nil || len(documents[0].Value["a2"].([]any)[9].([]any)[9].([]any)) != 10 {
		t.Fatalf("documents %v, error %v", documents, err)
	}
	documents, err = (manifests.YAML{}).Documents([]byte(nestedAliases(9)))
	if err == nil || documents != nil || !strings.Contains(err.Error(), "expand to more than") {
		t.Fatalf("documents %v, error %v; want a refusal", documents, err)
	}
	// The budget is per document: each of these expands to about 75,000 nodes, together more than the budget.
	document := nestedAliases(4) + "b: [*a3, *a3, *a3, *a3, *a3]\n"
	if _, err := (manifests.YAML{}).Documents([]byte(document + "---\n" + document)); err != nil {
		t.Fatal(err)
	}
}
