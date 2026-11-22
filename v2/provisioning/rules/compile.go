// Package rules is provisioning's pure decisions: what an instance's records compile to, and what they declare.
// It assembles the documents it is given into the kernel's values and applies the kernel's compiler; it reads no
// file and writes none, so a use case that writes an instance's folder and one that reports its status compile the
// same records the same way.
package rules

import "github.com/samclement/swhurl-platform/v2/kernel"

// Inputs is one instance's compilation inputs, as the documents read them: its records, the platform
// profile, and the two optional documents — the resolution that names its pairs and the allocation that
// names its port. A nil document means the caller was given none; whether the instance needed one is the
// kernel's to refuse, not this package's.
type Inputs struct {
	Instance, Release, App, Profile []byte
	Calls, Egress                   []byte
	// EventsSecret is an instance's own events.sops.yaml, byte for byte, for a use case that writes its files.
	// v2 neither reads nor decrypts it: it is delivered beside the manifests, which list it.
	EventsSecret []byte
	// Offers are the documents an instance publishes for others to read — the app's OpenAPI and AsyncAPI
	// documents and its flags file — each named by its name in the instance's offers folder. They are a declared
	// input, never output: v2 copies them and parses none of them. The release's offers commit is their provenance.
	Offers []kernel.File
}

// Declaration is one instance's identity and the declaration its records state.
type Declaration struct {
	Application, Environment string
	Declared                 kernel.Declared
}

// Compile assembles the instance's inputs and compiles them. Nothing is read but the documents it was given.
func Compile(inputs Inputs) (kernel.Compilation, error) {
	spec, release, app, profile, err := decode(inputs)
	if err != nil {
		return kernel.Compilation{}, err
	}
	connections, egress, err := documents(inputs, spec.Application, spec.Environment)
	if err != nil {
		return kernel.Compilation{}, err
	}
	return kernel.CompileWith(spec, release, app, profile, connections, egress)
}

// Declare is the instance's identity and the declaration its records state: the compiler's own facts and the two
// committed resolution documents, never a read of the cluster.
func Declare(inputs Inputs) (Declaration, error) {
	spec, release, app, profile, err := decode(inputs)
	if err != nil {
		return Declaration{}, err
	}
	declared, err := kernel.Declare(spec, release, app, profile, inputs.Calls, inputs.Egress)
	if err != nil {
		return Declaration{}, err
	}
	return Declaration{Application: spec.Application, Environment: spec.Environment, Declared: declared}, nil
}

// decode reads the records and the profile; an absent app record is the empty app.
func decode(inputs Inputs) (kernel.InstanceSpec, kernel.ReleaseBundle, kernel.AppSpec, kernel.PlatformProfile, error) {
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, kernel.AppSpec{}, kernel.PlatformProfile{}, err
	}
	release, err := kernel.DecodeRelease(inputs.Release)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, kernel.AppSpec{}, kernel.PlatformProfile{}, err
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, kernel.AppSpec{}, kernel.PlatformProfile{}, err
	}
	app := kernel.AppSpec{}
	if inputs.App != nil {
		if app, err = kernel.DecodeApp(inputs.App); err != nil {
			return kernel.InstanceSpec{}, kernel.ReleaseBundle{}, kernel.AppSpec{}, kernel.PlatformProfile{}, err
		}
	}
	return spec, release, app, profile, nil
}

// documents reduces the optional documents to the instance's own facts. A document that was given is read
// even when the instance turns out to need nothing from it, so a malformed one is refused rather than
// ignored.
func documents(inputs Inputs, application, environment string) (kernel.Connections, *kernel.Egress, error) {
	connections := kernel.Connections{}
	if inputs.Calls != nil {
		read, err := kernel.ConnectionsOf(inputs.Calls, application, environment)
		if err != nil {
			return kernel.Connections{}, nil, err
		}
		connections = read
	}
	egress, err := kernel.InstanceEgress(inputs.Egress, application, environment)
	if err != nil {
		return kernel.Connections{}, nil, err
	}
	return connections, egress, nil
}
