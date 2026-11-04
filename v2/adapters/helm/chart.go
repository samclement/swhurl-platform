package helm

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Chart identity belongs to the local chart, not its folder/cache name. No parser escapes this adapter.
func chartMatches(request Request) error {
	file, err := os.Open(filepath.Join(request.Chart, "Chart.yaml"))
	if err != nil {
		return refuse("invalid_chart")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return refuse("invalid_chart")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if decoder.Decode(&document) != nil || len(document.Content) != 1 {
		return refuse("invalid_chart")
	}
	metadata, err := chartMetadata(decoder, &document)
	if err != nil {
		return err
	}
	return chartIdentity(metadata, request)
}
func chartIdentity(metadata map[string]any, request Request) error {
	if request.ChartName == "" || request.ChartVersion == "" {
		return refuse("chart_mismatch")
	}
	if metadata["name"] != request.ChartName || metadata["version"] != request.ChartVersion {
		return refuse("chart_mismatch")
	}
	return nil
}

func chartMetadata(decoder *yaml.Decoder, document *yaml.Node) (map[string]any, error) {
	value, err := jsonValue(document.Content[0], 0)
	metadata, ok := value.(map[string]any)
	if err != nil || !ok {
		return nil, refuse("invalid_chart")
	}
	if decoder.Decode(document) != io.EOF {
		return nil, refuse("invalid_chart")
	}
	return metadata, nil
}
