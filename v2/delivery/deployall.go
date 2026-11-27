package delivery

import (
	"context"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	connectivityrules "github.com/samclement/swhurl-platform/v2/connectivity/rules"
	"github.com/samclement/swhurl-platform/v2/kernel"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// deployEnvironment is the one environment a batch delivers to: production takes a release only by promotion.
const deployEnvironment = "staging"

// DeployAllInputs is a batch delivery's inputs: every instance the records reader read, the platform profile and the
// two optional documents every instance shares.
type DeployAllInputs struct {
	Records                []files.InstanceRecords
	Profile, Calls, Egress []byte
}

// InstanceDeploy is one instance's part of a batch: what its delivery produced, or the error that left it on the
// image it runs.
type InstanceDeploy struct {
	Application, Environment string
	Deploy                   Deploy
	Err                      error
}

// stagingRecords is one staging instance's records, described before anything runs: the revision a delivery is
// reviewed against and the image whose registry it reads.
type stagingRecords struct {
	records  files.InstanceRecords
	revision kernel.InstanceRevision
	image    string
}

// DeployAll brings every staging instance in the records to the newest image its own repository publishes, in the
// records' order. A records tree or allocation a delivery cannot take refuses the whole batch before anything runs;
// once it runs, an instance that refuses or fails keeps the image it runs and does not stop the others.
func DeployAll(ctx context.Context, inputs DeployAllInputs, images func(image string) (registry.Reader, error),
	destination string, writer files.Writer) ([]InstanceDeploy, error) {
	staging, err := describeStaging(inputs.Records)
	if err != nil {
		return nil, err
	}
	if inputs.Egress != nil {
		if _, err := connectivityrules.DecodeEgressDocument(inputs.Egress); err != nil {
			return nil, err
		}
	}
	deploys := make([]InstanceDeploy, 0, len(staging))
	for _, one := range staging {
		deploys = append(deploys, deployOne(ctx, inputs, one, images, destination, writer))
	}
	return deploys, nil
}

// deployOne is one instance's delivery against the registry its image is published to.
func deployOne(ctx context.Context, inputs DeployAllInputs, one stagingRecords,
	images func(image string) (registry.Reader, error), destination string, writer files.Writer) InstanceDeploy {
	result := InstanceDeploy{Application: one.records.Application, Environment: one.records.Environment}
	reader, err := images(one.image)
	if err != nil {
		result.Err = err
		return result
	}
	result.Deploy, result.Err = DeployImage(ctx, DeployInputs{Instance: one.records.Instance,
		Release: one.records.Release, App: one.records.App, Profile: inputs.Profile, Calls: inputs.Calls,
		Egress: inputs.Egress, Expected: one.revision}, reader, destination, writer)
	return result
}

// describeStaging is every staging instance's records described; a record that does not hold the instance its
// folder names refuses the batch.
func describeStaging(records []files.InstanceRecords) ([]stagingRecords, error) {
	staging := []stagingRecords{}
	for _, record := range records {
		if record.Environment != deployEnvironment {
			continue
		}
		one, err := describeRecords(record)
		if err != nil {
			return nil, err
		}
		staging = append(staging, one)
	}
	return staging, nil
}

// describeRecords reads what a delivery needs before it runs: the instance's own name and revision, and the image
// its registry is addressed by.
func describeRecords(record files.InstanceRecords) (stagingRecords, error) {
	spec, release, err := provisioningrules.FolderHolds(record.Application, record.Environment, record.Instance,
		record.Release)
	if err != nil {
		return stagingRecords{}, err
	}
	revision, err := spec.Revision()
	if err != nil {
		return stagingRecords{}, err
	}
	return stagingRecords{records: record, revision: revision, image: release.Artifact.Image}, nil
}
