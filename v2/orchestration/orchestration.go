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
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// Result is what a use case produced: the compilation, and the files it wrote, when it wrote any. Written is
// nil for a use case that performs no effect, and it holds the files completed before a failure when one
// happened part-way.
type Result struct {
	Compilation kernel.Compilation
	Written     []files.Outcome
}

// CompileInto compiles the instance and writes its files under destination, reporting each file as written or
// unchanged. It is the one use case that performs an effect; the destination's policy — which directories an
// operator may write into — belongs to the caller that chose it.
func CompileInto(inputs provisioningrules.Inputs, destination string, writer files.Writer) (Result, error) {
	compiled, err := provisioningrules.Compile(inputs)
	if err != nil {
		return Result{}, err
	}
	files, err := instanceFiles(compiled, inputs)
	if err != nil {
		return Result{Compilation: compiled}, err
	}
	outcomes, err := writeCompiled(destination, compiled, files, writer)
	return Result{Compilation: compiled, Written: outcomes}, err
}

// compileComplete compiles one instance and adds the two declared inputs its folder holds: its seed and the documents
// it publishes. Every use case that writes an instance's folder calls it, so a folder is either complete or refused —
// the compiler lists the seed and never writes it, and the documents are copied, never produced.
func compileComplete(inputs provisioningrules.Inputs) (kernel.Compilation, []kernel.File, error) {
	compiled, err := provisioningrules.Compile(inputs)
	if err != nil {
		return kernel.Compilation{}, nil, err
	}
	files, err := instanceFiles(compiled, inputs)
	return compiled, files, err
}

// instanceFiles is the compilation's files plus the declared inputs the caller supplied.
func instanceFiles(compiled kernel.Compilation, inputs provisioningrules.Inputs) ([]kernel.File, error) {
	documents, err := WithEventsSecret(compiled, inputs.EventsSecret)
	if err != nil {
		return nil, err
	}
	return WithOffers(documents, inputs.Offers)
}

// writeCompiled writes a use case's files and then prunes the instance's compiled folder, so a file the records no
// longer render cannot outlive the change that removed it. Pruning names only the files the renderer produces and this
// compilation did not: a folder's declared inputs are not its to remove.
func writeCompiled(destination string, compiled kernel.Compilation, written []kernel.File,
	writer files.Writer) ([]files.Outcome, error) {
	outcomes, err := writer.Write(destination, written)
	if err != nil {
		return outcomes, err
	}
	pruned, err := prune(writer, destination, compiled)
	return append(outcomes, pruned...), err
}

// prune removes the rendered names one compilation stopped producing from the instance's own folder.
func prune(writer files.Writer, destination string, compiled kernel.Compilation) ([]files.Outcome, error) {
	folder := instanceFolder(compiled.Files)
	if folder == "" {
		return nil, nil
	}
	rendered := make([]string, 0, len(compiled.Files))
	for _, file := range compiled.Files {
		name := strings.TrimPrefix(file.Path, folder+"/")
		if name == file.Path || strings.Contains(name, "/") {
			continue // the unit and anything in a subfolder are not this folder's names
		}
		rendered = append(rendered, name)
	}
	return writer.Prune(destination, folder, rendered)
}

// WithEventsSecret adds an instance's seed to the files a use case writes beside its manifests. The compiler
// lists the file; this is what puts it there, so a folder is either complete or refused: an instance that signs
// in to the broker cannot be delivered without the private half of its key pair, and one that has no key cannot
// receive a secret.
func WithEventsSecret(compiled kernel.Compilation, secret []byte) ([]kernel.File, error) {
	files := compiled.Files
	folder := instanceFolder(files)
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

// refuseSecret is a use case's refusal about the instance's seed: a fact the caller supplies or does not, never a
// value v2 reads.
func refuseSecret(reason string) error { return &Refusal{Subject: "events-secret", Reason: reason} }

// WithOffers adds an instance's published documents to the files a use case writes beside its manifests. The
// compiler does not list them: they are not resources, and nothing in the cluster applies them. A document's path
// is its name inside the offers folder, so it cannot be written anywhere else, and the caller's order is kept.
func WithOffers(manifests []kernel.File, documents []kernel.File) ([]kernel.File, error) {
	if len(documents) == 0 {
		return manifests, nil
	}
	folder := instanceFolder(manifests)
	if folder == "" {
		return nil, refuseOffers("documents_have_no_instance_folder")
	}
	written := slices.Clone(manifests)
	for _, document := range documents {
		if !plainName(document.Path) {
			return nil, refuseOffers("document_names_are_file_names")
		}
		written = append(written, kernel.File{Path: folder + "/offers/" + document.Path, Document: document.Document})
	}
	return written, nil
}

// plainName is whether a name is one path segment: no separator, no relative step, nothing absolute.
func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`)
}

// instanceFolder is the folder a compilation wrote into, as its files name it.
func instanceFolder(files []kernel.File) string {
	for _, file := range files {
		if index := strings.LastIndex(file.Path, "/"); index > 0 {
			return file.Path[:index]
		}
	}
	return ""
}

// refuseOffers is a use case's refusal about the documents an instance publishes: what the caller supplied, and
// what the release states it was read at.
func refuseOffers(reason string) error { return &Refusal{Subject: "offers", Reason: reason} }
