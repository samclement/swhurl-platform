// Package orchestration runs the platform's use cases. A use case assembles the inputs it needs, calls the
// kernel's decisions in order, performs its one effect through an adapter, and reports either the result or
// where it stopped.
//
// It decides nothing itself: every rule it applies is a kernel rule, and a refusal is the kernel's own. It
// prints nothing, parses no arguments, chooses no exit code and names no flag — the command above it does
// those — so the same use case can be called by a command, a test or a future console handler.
package orchestration

import (
	"bytes"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

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
}

// Result is what a use case produced: the compilation, and the files it wrote, when it wrote any. Written is
// nil for a use case that performs no effect, and it holds the files completed before a failure when one
// happened part-way.
type Result struct {
	Compilation kernel.Compilation
	Written     []files.Outcome
}

// Compile assembles the instance's inputs and compiles them. It performs no effect: nothing is written and
// nothing is read but the documents it was given.
func Compile(inputs Inputs) (kernel.Compilation, error) {
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		return kernel.Compilation{}, err
	}
	release, err := kernel.DecodeRelease(inputs.Release)
	if err != nil {
		return kernel.Compilation{}, err
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		return kernel.Compilation{}, err
	}
	app := kernel.AppSpec{}
	if inputs.App != nil {
		if app, err = kernel.DecodeApp(inputs.App); err != nil {
			return kernel.Compilation{}, err
		}
	}
	connections, egress, err := documents(inputs, spec.Application, spec.Environment)
	if err != nil {
		return kernel.Compilation{}, err
	}
	return kernel.CompileWith(spec, release, app, profile, connections, egress)
}

// CompileInto compiles the instance and writes its files under destination, reporting each file as written or
// unchanged. It is the one use case that performs an effect; the destination's policy — which directories an
// operator may write into — belongs to the caller that chose it.
func CompileInto(inputs Inputs, destination string, writer files.Writer) (Result, error) {
	compiled, err := Compile(inputs)
	if err != nil {
		return Result{}, err
	}
	documents, err := WithEventsSecret(compiled, inputs.EventsSecret)
	if err != nil {
		return Result{Compilation: compiled}, err
	}
	outcomes, err := writer.Write(destination, documents)
	return Result{Compilation: compiled, Written: outcomes}, err
}

// WithEventsSecret adds an instance's seed to the files a use case writes beside its manifests. The compiler
// lists the file; this is what puts it there, so a folder is either complete or refused: an instance that signs
// in to the broker cannot be delivered without the private half of its key pair, and one that has no key cannot
// receive a secret.
func WithEventsSecret(compiled kernel.Compilation, secret []byte) ([]kernel.File, error) {
	files := compiled.Files
	folder := ""
	for _, file := range files {
		if index := strings.LastIndex(file.Path, "/"); index > 0 {
			folder = file.Path[:index]
			break
		}
	}
	// The unit and the kustomization list the secret exactly when the instance has a broker identity.
	listed := folder != "" && slices.ContainsFunc(files, func(file kernel.File) bool {
		return strings.HasSuffix(file.Path, "/kustomization.yaml") &&
			bytes.Contains(file.Document, []byte(`"events.sops.yaml"`))
	})
	switch {
	case listed && len(secret) == 0:
		return nil, refuseSecret("an_instance_with_a_key_needs_its_seed")
	case !listed && len(secret) > 0:
		return nil, refuseSecret("an_instance_with_no_key_has_no_secret")
	case !listed:
		return files, nil
	}
	return append(slices.Clone(files), kernel.File{Path: folder + "/events.sops.yaml", Document: secret}), nil
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
	egress, err := egressOf(inputs.Egress, application, environment)
	if err != nil {
		return kernel.Connections{}, nil, err
	}
	return connections, egress, nil
}

// egressOf reads the allocation's place for one instance. A caller with no allocation passes nil and gets
// nil; whether an instance may be without one is the renderer's refusal, not this helper's.
func egressOf(document []byte, application, environment string) (*kernel.Egress, error) {
	if document == nil {
		return nil, nil
	}
	allocation, err := kernel.DecodeEgressDocument(document)
	if err != nil {
		return nil, err
	}
	return allocation.EgressOf(application, environment)
}

// refuseSecret is a use case's refusal about the instance's seed: a fact the caller supplies or does not, never a
// value v2 reads.
func refuseSecret(reason string) error { return &Refusal{Subject: "events-secret", Reason: reason} }
