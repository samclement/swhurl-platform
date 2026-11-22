package orchestration

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// statusVariant is the prod fixture's records renamed to application and environment, optionally a database
// instance or exposed; the team scenario builds its instances from it.
func statusVariant(t *testing.T, application, environment string, database, exposed bool) provisioningrules.Inputs {
	t.Helper()
	inputs := input(t, nil, nil)
	release, err := kernel.DecodeRelease(inputs.Release)
	if err != nil {
		t.Fatal(err)
	}
	release.Application, release.ReleaseID = application, ""
	if database {
		release.Database = "sqlite"
	}
	id, err := release.ID()
	if err != nil {
		t.Fatal(err)
	}
	release.ReleaseID = id
	inputs.Release, err = kernel.EncodeRelease(release)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		t.Fatal(err)
	}
	spec.Application, spec.Environment, spec.Release = application, environment, id
	if database {
		spec.Storage.SizeMiB = 128
		inputs.Egress = []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[{"application":"` + application + `","environment":"` + environment + `","port":30102,"grants":["swhurl-platform-backups-110927251694.s3.eu-west-2.amazonaws.com:443"]}]}`)
	}
	if exposed {
		spec.Grants.Exposure = "authenticated-web"
	}
	inputs.Instance, err = kernel.EncodeInstance(spec)
	if err != nil {
		t.Fatal(err)
	}
	app, err := kernel.DecodeApp(inputs.App)
	if err != nil {
		t.Fatal(err)
	}
	app.Application, app.AppID = application, ""
	inputs.App, err = kernel.EncodeApp(app)
	if err != nil {
		t.Fatal(err)
	}
	return inputs
}
