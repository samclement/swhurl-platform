package orchestration_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource/appsourcetest"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/producer"
)

func TestImageDeclarationReadsOnlyNamedDocumentsAndStopsOnFailure(t *testing.T) {
	fake := &appsourcetest.Fake{Root: "app", Source: appsourcetest.Scenario(), Files: map[string][]byte{"asyncapi.yaml": []byte("raw events"), "unused": []byte("not read")}, EventMappings: map[string]producer.Object{"asyncapi.yaml": appsourcetest.EventScenario()}}
	label, err := orchestration.ImageDeclaration(fake, "app")
	if err != nil || !strings.Contains(label, `"events"`) {
		t.Fatal(label, err)
	}
	if !reflect.DeepEqual(fake.Reads, []string{"swhurl.yaml", "asyncapi.yaml"}) {
		t.Fatal(fake.Reads)
	}
	fake.Err = errors.New("swhurl.yaml: unavailable")
	fake.Reads = nil
	if label, err := orchestration.ImageDeclaration(fake, "app"); err == nil || label != "" || len(fake.Reads) != 1 {
		t.Fatal(label, err, fake.Reads)
	}
	fake.Err = nil
	fake.Source = append(fake.Source, producer.Field{Name: "colour", Value: "blue"})
	fake.Reads = nil
	if label, err := orchestration.ImageDeclaration(fake, "app"); err == nil || label != "" || len(fake.Reads) != 1 {
		t.Fatal(label, err, fake.Reads)
	}
}
