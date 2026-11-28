package apps

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/registry"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// The apps use cases over a checkout: each reads what its change needs from the records tree and the documents the
// checkout names, then applies the change and writes under the checkout's root. A command and a console change call
// the same ones, so a line a person types and the change that quotes it run the same thing.

// CreateRequest is a create as its caller asks for it: the app and its team, the image reference the operator named,
// the instance's exposure, and the seed the caller obtained for its broker identity.
type CreateRequest struct {
	Application, Team, Image, Exposure string
	Seed                               [32]byte
}

// CreateIn states a new app's staging records in the checkout from the image its repository published. An app the
// records already hold anything of is refused: a create states what an app starts with, so a second one would
// overwrite a release an instance may be selecting.
func CreateIn(ctx context.Context, checkout files.Checkout, request CreateRequest,
	images func(image string) (registry.Reader, error), writer files.Writer, encrypter sops.Encrypter) (Created, error) {
	empty, err := files.Empty(filepath.Join(checkout.Records, request.Application), "records")
	if err != nil {
		return Created{}, err
	}
	if !empty {
		return Created{}, &Refusal{Subject: "app", Reason: "the_app_already_has_records"}
	}
	profile, err := files.Read(checkout.Profile, "profile")
	if err != nil {
		return Created{}, err
	}
	root, err := checkout.Relative(checkout.Records)
	if err != nil {
		return Created{}, err
	}
	return Create(ctx, CreateInputs{Application: request.Application, Team: request.Team, Image: request.Image,
		Profile: profile, RecordsRoot: root, Exposure: request.Exposure, Seed: request.Seed}, images, checkout.Root,
		writer, encrypter)
}

// EditIn applies one instance-level setting to one instance's records in the checkout.
func EditIn(checkout files.Checkout, application, environment string, setting Setting,
	writer files.Writer) (Edition, error) {
	records, err := checkout.Instances()
	if err != nil {
		return Edition{}, err
	}
	record, err := files.Find(records, application, environment)
	if err != nil {
		return Edition{}, err
	}
	instance, err := instanceIn(checkout, record)
	if err != nil {
		return Edition{}, err
	}
	return Edit(Editable{InstanceInputs: instance, Change: setting.Name, Apply: setting.Apply}, checkout.Root, writer)
}

// SetTeamIn gives an app a new team, re-rendering every environment the checkout's records hold for it.
func SetTeamIn(checkout files.Checkout, application, team string, writer files.Writer) (TeamChange, error) {
	appPath, err := checkout.Relative(filepath.Join(checkout.Records, application, kernel.AppRecordName))
	if err != nil {
		return TeamChange{}, err
	}
	records, err := checkout.Instances()
	if err != nil {
		return TeamChange{}, err
	}
	inputs := TeamInputs{AppPath: appPath, Team: team}
	for _, record := range records {
		if record.Application != application {
			continue
		}
		instance, err := instanceIn(checkout, record)
		if err != nil {
			return TeamChange{}, err
		}
		inputs.App = record.App
		inputs.Instances = append(inputs.Instances, instance)
	}
	if inputs.Instances == nil {
		return TeamChange{}, fmt.Errorf("invalid: records: %s holds no instance", application)
	}
	return SetTeam(inputs, checkout.Root, writer)
}

// instanceIn is one instance's folder with the documents a compilation of it takes, refused when its records name
// another instance than its path.
func instanceIn(checkout files.Checkout, record files.InstanceRecords) (InstanceInputs, error) {
	if _, _, err := provisioningrules.FolderHolds(record.Application, record.Environment, record.Instance,
		record.Release); err != nil {
		return InstanceInputs{}, err
	}
	folder, err := checkout.Folder(record)
	if err != nil {
		return InstanceInputs{}, err
	}
	inputs := provisioningrules.Inputs{Instance: folder.Instance, Release: folder.Release, App: folder.App,
		EventsSecret: folder.EventsSecret, Offers: folder.Offers}
	for _, read := range []struct {
		path, what string
		target     *[]byte
	}{
		{checkout.Profile, "profile", &inputs.Profile},
		{checkout.Calls, "calls", &inputs.Calls},
		{checkout.Egress, "egress", &inputs.Egress},
	} {
		if *read.target, err = files.Optional(read.path, read.what); err != nil {
			return InstanceInputs{}, err
		}
	}
	return InstanceInputs{Inputs: inputs, InstancePath: folder.InstancePath}, nil
}

// RemoveIn deletes one instance from the checkout: its records, its compiled files and its unit, with the
// registrations that named them.
func RemoveIn(checkout files.Checkout, application, environment string, writer files.Writer) (Removal, error) {
	inputs, err := removalIn(checkout, application, environment)
	if err != nil {
		return Removal{}, err
	}
	return Remove(inputs, checkout.Root, writer)
}

// removalIn assembles one removal: the places its own files are, the platform's two registrations, and whether the
// app record goes with it.
func removalIn(checkout files.Checkout, application, environment string) (RemovalInputs, error) {
	records, err := checkout.Instances()
	if err != nil {
		return RemovalInputs{}, err
	}
	record, err := files.Find(records, application, environment)
	if err != nil {
		return RemovalInputs{}, err
	}
	// The dependencies and registrations are checked from the record and the paths deleted from its folder's name.
	if _, _, err := provisioningrules.FolderHolds(application, environment, record.Instance, record.Release); err != nil {
		return RemovalInputs{}, err
	}
	folder, err := checkout.Relative(filepath.Join(checkout.Records, application, environment))
	if err != nil {
		return RemovalInputs{}, err
	}
	appRecord, err := checkout.Relative(filepath.Join(checkout.Records, application, kernel.AppRecordName))
	if err != nil {
		return RemovalInputs{}, err
	}
	data, err := files.Read(checkout.Profile, "profile")
	if err != nil {
		return RemovalInputs{}, err
	}
	profile, err := kernel.DecodeProfile(data)
	if err != nil {
		return RemovalInputs{}, err
	}
	calls, err := files.Read(checkout.Calls, "calls")
	if err != nil {
		return RemovalInputs{}, err
	}
	inputs := RemovalInputs{Instance: record.Instance, Release: record.Release, Calls: [][]byte{calls},
		Records: folder, AppRecord: appRecord, LastEnvironment: environmentsOf(records, application) == 1,
		Compiled: kernel.InstanceFolder(application, environment, profile),
		Unit:     kernel.UnitPath(application, environment, profile),
		AuthFile: "platform/auth/" + kernel.AuthFileName(kernel.NamespaceOf(application, environment))}
	return withRegistrations(checkout, inputs)
}

// withRegistrations reads the two kustomizations a removal edits.
func withRegistrations(checkout files.Checkout, inputs RemovalInputs) (RemovalInputs, error) {
	var err error
	if inputs.UnitRegistration, err = registrationIn(checkout, "clusters/home/kustomization.yaml"); err != nil {
		return RemovalInputs{}, err
	}
	if inputs.AuthRegistration, err = registrationIn(checkout, "platform/auth/kustomization.yaml"); err != nil {
		return RemovalInputs{}, err
	}
	return inputs, nil
}

// registrationIn is one kustomization a removal edits, by its repository path. A tree without it holds no entry in it
// either.
func registrationIn(checkout files.Checkout, path string) (Registration, error) {
	document, err := checkout.Present(path, "registration")
	return Registration{Path: path, Document: document}, err
}

// environmentsOf is how many of an app's environments the records hold.
func environmentsOf(records []files.InstanceRecords, application string) int {
	count := 0
	for _, record := range records {
		if record.Application == application {
			count++
		}
	}
	return count
}

// Summary is the edit's one line: the instance, the setting and how many files it wrote.
func (e Edition) Summary() string {
	return fmt.Sprintf("%s/%s %s: %d files", e.Application, e.Environment, e.Change, len(e.Written))
}

// Summary is the team change's one line.
func (t TeamChange) Summary() string {
	return fmt.Sprintf("%s team %s: %d instances, %d files", t.Application, t.After.Team, len(t.Instances),
		len(t.Written))
}

// Summary is the removal's one line.
func (r Removal) Summary() string {
	return fmt.Sprintf("%s/%s removed: %d files removed, %d written", r.Application, r.Environment, len(r.Removed),
		len(r.Written))
}

// Summary is the creation's one line: the instance, the image tag it was created from and how many files it wrote.
func (c Created) Summary() string {
	return fmt.Sprintf("%s/%s created from %s: %d files", c.Instance.Application, c.Instance.Environment, c.Tag,
		len(c.Written))
}
