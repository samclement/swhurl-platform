package provisioning

import (
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// input is a compiled instance's documents: the fixture records, without the optional documents.
func input(t *testing.T) rules.Inputs {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile("../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return rules.Inputs{Instance: read("prod.json"), Release: read("r41.json"), App: read("app.json"),
		Profile: read("profile.json")}
}

// CompileInto writes the files through the adapter and reports each one; a refusal writes nothing.
func TestCompileIntoWritesAndReports(t *testing.T) {
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	result, err := CompileInto(input(t), "/checkout", writer)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) == 0 || len(result.Written) != len(result.Compilation.Files) {
		t.Fatalf("outcomes: %+v", result.Written)
	}
	if writer.Writes != 1 {
		t.Fatalf("the adapter was called %d times", writer.Writes)
	}
	refused := input(t)
	refused.Instance = []byte(`{"schemaVersion":1}`)
	empty := &filestest.Fake{Contents: map[string][]byte{}}
	result, err = CompileInto(refused, "/checkout", empty)
	if err == nil || len(result.Written) != 0 || empty.Writes != 0 {
		t.Fatalf("a refusal wrote something: %v %d", err, empty.Writes)
	}
}

// The documents an instance publishes are written beside its manifests, in one folder, and never listed as
// resources: nothing in the cluster applies them. A name cannot escape that folder.
func TestWithOffersWritesTheDocumentsBesideTheManifests(t *testing.T) {
	read := func(name string) []byte {
		data, err := os.ReadFile("../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	inputs := rules.Inputs{Instance: read("staging.json"), Release: read("r42.json"), App: read("app.json"),
		Profile: read("profile.json"), Offers: []kernel.File{
			{Path: "openapi.json", Document: []byte("{\"openapi\":\"3.0.0\"}\n")},
			{Path: "source.yaml", Document: []byte("commit: ccccccc\n")},
		}}
	writer := &filestest.Fake{Contents: map[string][]byte{}}
	if _, err := CompileInto(inputs, "/checkout", writer); err != nil {
		t.Fatal(err)
	}
	folder := "v2-apps/instances/demo-orders/staging"
	for _, name := range []string{"openapi.json", "source.yaml"} {
		if len(writer.Contents[folder+"/offers/"+name]) == 0 {
			t.Fatalf("%s was not written beside the manifests", name)
		}
	}
	if strings.Contains(string(writer.Contents[folder+"/kustomization.yaml"]), "offers/") {
		t.Fatal("a document was listed as a resource")
	}
	files := []kernel.File{{Path: folder + "/helmrelease.json", Document: []byte("{}")}}
	for _, name := range []string{"", ".", "..", "../escape.yaml", "nested/openapi.json"} {
		if _, err := withOffers(files, []kernel.File{{Path: name, Document: []byte("{}")}}); err == nil ||
			!strings.Contains(err.Error(), "document_names_are_file_names") {
			t.Fatalf("%q was accepted as a document name: %v", name, err)
		}
	}
	// No documents is no change: a release with nothing to publish renders a folder without an offers directory.
	if same, err := withOffers(files, nil); err != nil || len(same) != len(files) {
		t.Fatalf("an instance with no documents: %v %d", err, len(same))
	}
}
