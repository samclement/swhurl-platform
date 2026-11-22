package delivery

// The write plumbing promotion and deployment share. Each declaration is a copy of orchestration's, named at its
// origin; the two copies stay until slice 5 moves the rest of orchestration's writers.

import (
	"errors"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// Refusal is the use case's own refusal: it will not start, and nothing was changed. A failure
// after the first change is never a refusal, so a caller can tell "not started" from "stopped part-way" by
// the type alone. (Copied from orchestration/usecase.go.)
type Refusal struct{ Subject, Reason string }

func (r *Refusal) Error() string { return "refused: " + r.Subject + ": " + r.Reason }

// reasonOf is the fixed reason a read states, or the malformed document it could not be read from.
// (Copied from orchestration/usecase.go.)
func reasonOf(read observe.Read) string {
	if read.Reason != "" {
		return read.Reason
	}
	return observe.ReasonNotFound
}

// unavailable is a read that failed part-way through a use case: a failure of this run, never a refusal,
// because the run may already have changed something. (Copied from orchestration/usecase.go.)
func unavailable(subject, reason string) error {
	if reason == "" {
		reason = observe.ReasonNotFound
	}
	return errors.New("unavailable: " + subject + ": " + reason)
}

// recordsRoot is where a v2-owned instance keeps its own records, under the checkout the caller writes into. It is
// this use case's own output and becomes a profile fact only if a second writer needs it.
// (Copied from orchestration/records.go.)
const recordsRoot = "v2-records"

// writeCompiled writes a use case's files and then prunes the instance's compiled folder, so a file the records no
// longer render cannot outlive the change that removed it. Pruning names only the files the renderer produces and this
// compilation did not: a folder's declared inputs are not its to remove. (Copied from orchestration/orchestration.go.)
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
// (Copied from orchestration/orchestration.go.)
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

// documents reduces the optional documents to the instance's own facts. A document that was given is read
// even when the instance turns out to need nothing from it, so a malformed one is refused rather than
// ignored. (Copied from orchestration/orchestration.go, taking the two documents rather than orchestration's
// Inputs.)
func documents(calls, egressDocument []byte, application, environment string) (kernel.Connections, *kernel.Egress,
	error) {
	connections := kernel.Connections{}
	if calls != nil {
		read, err := kernel.ConnectionsOf(calls, application, environment)
		if err != nil {
			return kernel.Connections{}, nil, err
		}
		connections = read
	}
	egress, err := kernel.InstanceEgress(egressDocument, application, environment)
	if err != nil {
		return kernel.Connections{}, nil, err
	}
	return connections, egress, nil
}

// WithOffers adds an instance's published documents to the files a use case writes beside its manifests. The
// compiler does not list them: they are not resources, and nothing in the cluster applies them. A document's path
// is its name inside the offers folder, so it cannot be written anywhere else, and the caller's order is kept.
// (Copied from orchestration/orchestration.go.)
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
// (Copied from orchestration/orchestration.go.)
func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`)
}

// instanceFolder is the folder a compilation wrote into, as its files name it.
// (Copied from orchestration/orchestration.go.)
func instanceFolder(files []kernel.File) string {
	for _, file := range files {
		if index := strings.LastIndex(file.Path, "/"); index > 0 {
			return file.Path[:index]
		}
	}
	return ""
}

// refuseOffers is a use case's refusal about the documents an instance publishes: what the caller supplied, and
// what the release states it was read at. (Copied from orchestration/orchestration.go.)
func refuseOffers(reason string) error { return &Refusal{Subject: "offers", Reason: reason} }

// instanceDocuments is what a delivery or a promotion writes: the release first, then the instance that selects it,
// then the compiled files, each with the documents the release's image states. A failure leaves at worst a release
// nothing selects. (Copied from orchestration/deploy.go.)
func instanceDocuments(spec kernel.InstanceSpec, release kernel.ReleaseBundle,
	compiled kernel.Compilation, offers []kernel.File) ([]kernel.File, error) {
	releaseDocument, err := kernel.EncodeRelease(release)
	if err != nil {
		return nil, err
	}
	instanceDocument, err := kernel.EncodeInstance(spec)
	if err != nil {
		return nil, err
	}
	folder := recordsRoot + "/" + spec.Application + "/" + spec.Environment
	records, err := WithOffers([]kernel.File{
		{Path: folder + "/release.json", Document: releaseDocument},
		{Path: folder + "/instance.json", Document: instanceDocument},
	}, offers)
	if err != nil {
		return nil, err
	}
	compiledDocuments, err := WithOffers(compiled.Files, offers)
	if err != nil {
		return nil, err
	}
	return append(records, compiledDocuments...), nil
}
