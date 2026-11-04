package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// platformRoot is where, from the destination, the platform's documents live.
const platformRoot = "platform"

// RegistryInputs is one change to the team registry: the registry record and where it is written, every app and
// instance record (a rename or a removal must not leave one naming a team that is gone), and for a channel the
// profile that states the platform's recipient and the address, which the caller read from its standard input.
type RegistryInputs struct {
	Registry        []byte
	RegistryPath    string
	Apps, Instances [][]byte
	Profile         []byte
	Change          kernel.TeamChange
	Address         string
}

// RegistryOutcome is what a change did: the team, the channel it gave or removed, and every file it wrote or removed.
// The address is not here: it leaves this use case only inside the ciphertext.
type RegistryOutcome struct {
	Team    string
	Action  kernel.TeamAction
	Channel string
	Written []files.Outcome
}

// ChangeRegistry applies one change to the registry and writes the registry with the notification routes and the
// channels' kustomization rendered from it. A channel's Secret is encrypted and written first, so a failure leaves a
// file nothing lists rather than a list naming a file that is not there; a dropped channel's Secret is removed last,
// once nothing lists it. Everything is decided before anything is written.
func ChangeRegistry(inputs RegistryInputs, destination string, writer files.Writer,
	encrypter sops.Encrypter) (RegistryOutcome, error) {
	registry, apps, instances, err := decodeRegistryInputs(inputs)
	if err != nil {
		return RegistryOutcome{}, err
	}
	changed, err := kernel.ChangeTeams(registry, inputs.Change, apps, instances)
	if err != nil {
		return RegistryOutcome{}, err
	}
	written, err := registryFiles(inputs.RegistryPath, changed)
	if err != nil {
		return RegistryOutcome{}, err
	}
	outcome := RegistryOutcome{Team: inputs.Change.Team, Action: inputs.Change.Action}
	var secret []byte
	switch inputs.Change.Action {
	case kernel.GiveChannel:
		channel := *changed.Teams[inputs.Change.Team].Channel
		outcome.Channel = channel.Name
		if secret, err = encryptedChannel(channel, inputs, encrypter); err != nil {
			return RegistryOutcome{}, err
		}
	case kernel.DropChannel:
		outcome.Channel = registry.Teams[inputs.Change.Team].Channel.Name
	}
	return outcome.write(destination, writer, secret, written)
}

// write writes the channel's Secret when there is one, then the registry and what it renders, then removes the
// Secret of a dropped channel.
func (o RegistryOutcome) write(destination string, writer files.Writer, secret []byte,
	written []kernel.File) (RegistryOutcome, error) {
	secretPath := platformRoot + "/" + kernel.ChannelSecretPath(o.Channel)
	if secret != nil {
		outcomes, err := writer.Write(destination, []kernel.File{{Path: secretPath, Document: secret}})
		o.Written = append(o.Written, outcomes...)
		if err != nil {
			return o, err
		}
	}
	outcomes, err := writer.Write(destination, written)
	o.Written = append(o.Written, outcomes...)
	if err != nil || o.Action != kernel.DropChannel {
		return o, err
	}
	outcomes, err = writer.Remove(destination, secretPath)
	o.Written = append(o.Written, outcomes...)
	return o, err
}

// decodeRegistryInputs reads the registry and every record a change is judged against.
func decodeRegistryInputs(inputs RegistryInputs) (kernel.TeamRegistry, []kernel.AppSpec, []kernel.InstanceSpec, error) {
	registry, err := kernel.DecodeTeams(inputs.Registry)
	if err != nil {
		return kernel.TeamRegistry{}, nil, nil, err
	}
	apps := make([]kernel.AppSpec, 0, len(inputs.Apps))
	for _, data := range inputs.Apps {
		app, err := kernel.DecodeApp(data)
		if err != nil {
			return kernel.TeamRegistry{}, nil, nil, err
		}
		apps = append(apps, app)
	}
	instances := make([]kernel.InstanceSpec, 0, len(inputs.Instances))
	for _, data := range inputs.Instances {
		spec, err := kernel.DecodeInstance(data)
		if err != nil {
			return kernel.TeamRegistry{}, nil, nil, err
		}
		instances = append(instances, spec)
	}
	return registry, apps, instances, nil
}

// registryFiles is the registry record at its path and the notification files rendered from it, under the platform
// folder.
func registryFiles(path string, registry kernel.TeamRegistry) ([]kernel.File, error) {
	record, err := kernel.EncodeTeams(registry)
	if err != nil {
		return nil, err
	}
	notify, err := kernel.RenderNotify(registry)
	if err != nil {
		return nil, err
	}
	written := []kernel.File{{Path: path, Document: record}}
	for _, file := range notify.Files {
		written = append(written, kernel.File{Path: platformRoot + "/" + file.Path, Document: file.Document})
	}
	return written, nil
}

// encryptedChannel is one channel's Secret, encrypted to the platform's recipient and checked as far as v2 can check
// a ciphertext it never decrypts.
func encryptedChannel(channel kernel.Channel, inputs RegistryInputs, encrypter sops.Encrypter) ([]byte, error) {
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return nil, err
	}
	if profile.SOPS.Recipient == "" {
		return nil, &Refusal{"sops", "the_profile_states_no_recipient"}
	}
	plaintext, err := kernel.ChannelSecret(channel, inputs.Address)
	if err != nil {
		return nil, err
	}
	encrypted, err := encrypter.Encrypt(plaintext, profile.SOPS.Recipient)
	if err != nil {
		return nil, err
	}
	if err := kernel.CheckEncryptedAddress(encrypted, inputs.Address); err != nil {
		return nil, err
	}
	return encrypted, nil
}
