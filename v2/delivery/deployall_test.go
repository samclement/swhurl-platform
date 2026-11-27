package delivery

import (
	"context"
	"errors"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
)

// stagingRecord is the fixture staging instance as the records reader reads it.
func stagingRecord(t *testing.T) files.InstanceRecords {
	t.Helper()
	return files.InstanceRecords{Application: "demo-orders", Environment: "staging", App: fixture(t, "app.json"),
		Instance: fixture(t, "staging.json"), Release: fixture(t, "r42.json")}
}

// batchInputs is a batch over records, with the fixture profile.
func batchInputs(t *testing.T, records ...files.InstanceRecords) DeployAllInputs {
	t.Helper()
	return DeployAllInputs{Records: records, Profile: fixture(t, "profile.json")}
}

// registries answers each image name with the next of readers, in call order, and records the names it was asked.
type registries struct {
	asked   []string
	answers []func() (registry.Reader, error)
}

func (r *registries) open(image string) (registry.Reader, error) {
	r.asked = append(r.asked, image)
	answer := r.answers[0]
	r.answers = r.answers[1:]
	return answer()
}

func newer() (registry.Reader, error) { return images(declarationLabel(nil)), nil }

func TestDeployAllDeliversOnlyStagingInstances(t *testing.T) {
	prod := files.InstanceRecords{Application: "demo-orders", Environment: "prod", App: fixture(t, "app.json"),
		Instance: fixture(t, "prod.json"), Release: fixture(t, "r41.json")}
	opened := &registries{answers: []func() (registry.Reader, error){newer}}
	writer := filestest.New()
	deploys, err := DeployAll(context.Background(), batchInputs(t, prod, stagingRecord(t)), opened.open, t.TempDir(),
		writer)
	if err != nil {
		t.Fatal(err)
	}
	if len(deploys) != 1 || deploys[0].Environment != "staging" || deploys[0].Err != nil || !deploys[0].Deploy.Changed {
		t.Fatalf("deploys %+v", deploys)
	}
	if len(opened.asked) != 1 || opened.asked[0] != "ghcr.io/samclement/demo-orders" {
		t.Fatalf("registries opened for %v", opened.asked)
	}
	if _, found := writer.Contents["v2-records/demo-orders/prod/release.json"]; found {
		t.Fatal("a batch wrote production's records")
	}
}

// An instance whose registry cannot be opened keeps its image; the next instance is still delivered.
func TestDeployAllKeepsGoingPastAnInstanceThatFails(t *testing.T) {
	unavailable := &registry.Error{Code: registry.Unavailable, Subject: "registry", Reason: "no answer"}
	opened := &registries{answers: []func() (registry.Reader, error){
		func() (registry.Reader, error) { return nil, unavailable }, newer}}
	writer := filestest.New()
	deploys, err := DeployAll(context.Background(), batchInputs(t, stagingRecord(t), stagingRecord(t)), opened.open,
		t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if len(deploys) != 2 || !errors.Is(deploys[0].Err, unavailable) || deploys[0].Deploy.Changed {
		t.Fatalf("the failed instance: %+v", deploys)
	}
	if deploys[1].Err != nil || !deploys[1].Deploy.Changed || len(deploys[1].Deploy.Written) == 0 {
		t.Fatalf("the instance after it: %+v", deploys[1])
	}
}

// A batch that cannot be read whole refuses before any instance's registry is opened or anything is written.
func TestDeployAllRefusesTheBatchBeforeAnyDelivery(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*DeployAllInputs)
	}{
		{"a malformed allocation", func(inputs *DeployAllInputs) { inputs.Egress = []byte(`{}`) }},
		{"an instance record that does not decode", func(inputs *DeployAllInputs) {
			inputs.Records[0].Instance = []byte(`{"schemaVersion": 1`)
		}},
		{"a folder holding another instance", func(inputs *DeployAllInputs) {
			inputs.Records[0].Application = "elsewhere"
		}},
		{"a later instance whose release does not decode", func(inputs *DeployAllInputs) {
			broken := stagingRecord(t)
			broken.Release = []byte(`{}`)
			inputs.Records = append(inputs.Records, broken)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inputs := batchInputs(t, stagingRecord(t))
			test.change(&inputs)
			opened := &registries{}
			writer := filestest.New()
			deploys, err := DeployAll(context.Background(), inputs, opened.open, t.TempDir(), writer)
			if err == nil || deploys != nil {
				t.Fatalf("delivered %+v", deploys)
			}
			if len(opened.asked) != 0 || writer.Writes != 0 {
				t.Fatalf("opened %v, %d writes", opened.asked, writer.Writes)
			}
		})
	}
}
