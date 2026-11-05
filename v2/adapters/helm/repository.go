package helm

import (
	"bytes"
	"io"

	"go.yaml.in/yaml/v3"
)

// Repository resolves exactly one named HelmRepository from the supplied YAML stream. Parsing stays
// at the Helm boundary; no YAML values escape. A malformed later document never yields a partial answer.
func Repository(data []byte, name string) (string, error) {
	if name == "" || len(data) > MaxValues {
		return "", chartFailure("invalid_repositories")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	found := ""
	for {
		address, err := nextRepository(decoder, name)
		if err == io.EOF {
			break
		}
		if err != nil || (address != "" && found != "") {
			return "", chartFailure("invalid_repositories")
		}
		if address != "" {
			found = address
		}
	}
	if found == "" {
		return "", chartFailure("repository_not_found")
	}
	return found, nil
}

// nextRepository validates a whole document even when it is not the requested repository.
func nextRepository(decoder *yaml.Decoder, name string) (string, error) {
	var document yaml.Node
	err := decoder.Decode(&document)
	if err == io.EOF {
		return "", io.EOF
	}
	if err != nil || len(document.Content) != 1 {
		return "", chartFailure("invalid_repositories")
	}
	value, err := jsonValue(document.Content[0], 0)
	if err != nil {
		return "", chartFailure("invalid_repositories")
	}
	object, okay := value.(map[string]any)
	if !okay && value != nil {
		return "", chartFailure("invalid_repositories")
	}
	return repositoryURL(object, name)
}

func repositoryURL(object map[string]any, name string) (string, error) {
	if object["kind"] != "HelmRepository" {
		return "", nil
	}
	metadata, _ := object["metadata"].(map[string]any)
	if metadata["name"] != name {
		return "", nil
	}
	spec, _ := object["spec"].(map[string]any)
	address, _ := spec["url"].(string)
	if address == "" {
		return "", chartFailure("invalid_repositories")
	}
	return address, nil
}
