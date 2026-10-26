// Package orchestration runs the platform's use cases. A use case assembles the inputs it needs, calls the
// kernel's decisions in order, performs its one effect through an adapter, and reports either the result or
// where it stopped.
//
// It decides nothing itself: every rule it applies is a kernel rule, and a refusal is the kernel's own. It
// prints nothing, parses no arguments, chooses no exit code and names no flag — the command above it does
// those — so the same use case can be called by a command, a test or a future console handler.
package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Inputs is one instance's compilation inputs, as the documents read them: its records, the platform
// profile, and the two optional documents — the resolution that names its pairs and the allocation that
// names its port. A nil document means the caller was given none; whether the instance needed one is the
// kernel's to refuse, not this package's.
type Inputs struct {
	Instance, Release, Profile []byte
	Calls, Egress              []byte
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
	connections, egress, err := documents(inputs, spec.Application, spec.Environment)
	if err != nil {
		return kernel.Compilation{}, err
	}
	return kernel.CompileWith(spec, release, profile, connections, egress)
}

// CompileInto compiles the instance and writes its files under destination, reporting each file as written or
// unchanged. It is the one use case that performs an effect; the destination's policy — which directories an
// operator may write into — belongs to the caller that chose it.
func CompileInto(inputs Inputs, destination string, writer files.Writer) (Result, error) {
	compiled, err := Compile(inputs)
	if err != nil {
		return Result{}, err
	}
	outcomes, err := writer.Write(destination, compiled.Files)
	return Result{Compilation: compiled, Written: outcomes}, err
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
	if inputs.Egress == nil {
		return connections, nil, nil
	}
	allocation, err := kernel.DecodeEgressDocument(inputs.Egress)
	if err != nil {
		return kernel.Connections{}, nil, err
	}
	egress, err := allocation.EgressOf(application, environment)
	if err != nil {
		return kernel.Connections{}, nil, err
	}
	return connections, egress, nil
}
