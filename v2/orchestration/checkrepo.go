package orchestration

import (
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/checkrepo"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// CheckRepo runs check-repo's decisions over one checkout: Git says which files it holds, read supplies the text of
// each one checkrepo.Needs names, decoder turns its manifests into documents, the names catalogue answers which
// platform names it lists, and checkrepo.Check judges them all. It writes nothing and reads no cluster. A refused Git
// read, a failed file read or a catalogue v2 cannot decode stops it with that error; findings are the report's.
func CheckRepo(root string, reader git.Reader, decoder manifests.Decoder, read func(path string) ([]byte, error)) (checkrepo.Report, error) {
	checkout, err := reader.Read(root)
	if err != nil {
		return checkrepo.Report{}, err
	}
	text, files := checkrepo.Needs(checkout.Present)
	repository := checkrepo.Repository{Present: checkout.Present, Text: map[string]string{}}
	for _, path := range text {
		data, err := read(path)
		if err != nil {
			return checkrepo.Report{}, err
		}
		repository.Text[path] = string(data)
	}
	if repository.Manifests, err = decodeAll(files, decoder, read); err != nil {
		return checkrepo.Report{}, err
	}
	catalogue, err := kernel.DecodeNameCatalogue([]byte(repository.Text[checkrepo.Catalogue]))
	if err != nil {
		return checkrepo.Report{}, fmt.Errorf("%s: %w", checkrepo.Catalogue, err)
	}
	return checkrepo.Check(repository, catalogue), nil
}

// RenderPath is what Flux applies from one active render path, given what Kustomize rendered there: the folder's
// substitution applied, and each document checked complete. It reads only the unit files and the platform settings.
// An unresolved substitution or an incomplete document is a *checkrepo.Finding; a path Flux does not render, a refused
// Git read or a failed file read is any other error.
func RenderPath(root string, reader git.Reader, decoder manifests.Decoder, read func(path string) ([]byte, error),
	path string, rendered []byte) (string, error) {
	checkout, err := reader.Read(root)
	if err != nil {
		return "", err
	}
	repository := checkrepo.Repository{Present: checkout.Present}
	if repository.Manifests, err = decodeAll(checkrepo.PathNeeds(checkout.Present), decoder, read); err != nil {
		return "", err
	}
	active, err := activePath(repository, path)
	if err != nil {
		return "", err
	}
	substituted, err := checkrepo.Substitute(active, string(rendered))
	if err != nil {
		return "", err
	}
	documents, err := decoder.Documents([]byte(substituted))
	if err != nil {
		return "", &checkrepo.Finding{Problem: fmt.Sprintf("%s: rendered manifests are not YAML: %v", path, err)}
	}
	if err := checkrepo.Complete(active, documents); err != nil {
		return "", err
	}
	return substituted, nil
}

// activePath is the named path among the checkout's active render paths, or why it is not one.
func activePath(repository checkrepo.Repository, path string) (checkrepo.Path, error) {
	paths, problems := checkrepo.Paths(repository)
	for _, active := range paths {
		if active.Path == path {
			return active, nil
		}
	}
	if len(problems) > 0 {
		return checkrepo.Path{}, fmt.Errorf("%s is not an active render path (%s)", path, problems[0])
	}
	return checkrepo.Path{}, fmt.Errorf("%s is not an active render path", path)
}

// decodeAll reads and decodes each file; a file that is not YAML is a finding the checks report, not an error here.
func decodeAll(files []string, decoder manifests.Decoder, read func(path string) ([]byte, error)) (map[string]checkrepo.Decoded, error) {
	decoded := map[string]checkrepo.Decoded{}
	for _, path := range files {
		data, err := read(path)
		if err != nil {
			return nil, err
		}
		documents, err := decoder.Documents(data)
		if err != nil {
			decoded[path] = checkrepo.Decoded{Err: err.Error()}
			continue
		}
		decoded[path] = checkrepo.Decoded{Documents: documents}
	}
	return decoded, nil
}
