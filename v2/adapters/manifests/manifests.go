// Package manifests decodes the Kubernetes YAML a checkout holds into the documents checkrepo decides about. It is
// the third approved importer of go.yaml.in/yaml/v3 (docs/iteration-2-plan.md#guardrails): it reads no file, starts
// no process and judges nothing; it only turns bytes into documents.
package manifests

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/samclement/swhurl-platform/v2/checkrepo"
	"go.yaml.in/yaml/v3"
)

// Decoder turns one file's text into its documents. An empty stream has none; an empty document, a list or a scalar
// is a document with no Value. Text that is not YAML is an error and no documents.
type Decoder interface {
	Documents(text []byte) ([]checkrepo.Document, error)
}

// YAML decodes with go.yaml.in/yaml/v3. A plain scalar reads as Kubernetes's YAML 1.1 reads it where that differs for
// a boolean (yes, no, on, off), so a setting written `on` is not a string; JSON is YAML and reads the same way.
type YAML struct{}

var _ Decoder = YAML{}

// Documents decodes every document in text, in order.
func (YAML) Documents(text []byte) ([]checkrepo.Document, error) {
	if !utf8.Valid(text) {
		return nil, errors.New("invalid UTF-8")
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(text)))
	var documents []checkrepo.Document
	for {
		var node yaml.Node
		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			return documents, nil
		}
		if err != nil {
			return nil, err
		}
		document, err := documentOf(&node)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
}

// documentOf is one decoded document; a document that is not a mapping has no Value.
func documentOf(node *yaml.Node) (checkrepo.Document, error) {
	if len(node.Content) == 0 {
		return checkrepo.Document{}, nil
	}
	decoded, err := value(node.Content[0], map[*yaml.Node]bool{})
	if err != nil {
		return checkrepo.Document{}, err
	}
	mapping, _ := decoded.(map[string]any)
	if mapping == nil {
		return checkrepo.Document{}, nil
	}
	kind, _ := mapping["kind"].(string)
	metadata, _ := mapping["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return checkrepo.Document{Kind: kind, Name: name, Value: mapping}, nil
}

// value is a node as plain Go values: a mapping as map[string]any, a sequence as []any, a scalar as a string, bool,
// int, float64 or nil. active guards the aliases being expanded, so a repeated alias reads twice and a cycle fails.
func value(node *yaml.Node, active map[*yaml.Node]bool) (any, error) {
	switch node.Kind {
	case yaml.AliasNode:
		if node.Alias == nil || active[node.Alias] {
			return nil, errors.New("an alias that refers to itself")
		}
		active[node.Alias] = true
		defer delete(active, node.Alias)
		return value(node.Alias, active)
	case yaml.MappingNode:
		return mapping(node, active)
	case yaml.SequenceNode:
		items := []any{}
		for _, item := range node.Content {
			decoded, err := value(item, active)
			if err != nil {
				return nil, err
			}
			items = append(items, decoded)
		}
		return items, nil
	case yaml.ScalarNode:
		return scalar(node)
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", node.Line)
}

// mapping is a mapping node's keys and values; a merge key (<<) supplies the keys the mapping does not state itself.
func mapping(node *yaml.Node, active map[*yaml.Node]bool) (map[string]any, error) {
	stated, merged := map[string]any{}, map[string]any{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, item := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: a mapping key that is not a scalar", key.Line)
		}
		decoded, err := value(item, active)
		if err != nil {
			return nil, err
		}
		if key.Tag == "!!merge" {
			if err := merge(merged, decoded, key.Line); err != nil {
				return nil, err
			}
			continue
		}
		stated[key.Value] = decoded
	}
	for key, item := range merged {
		if _, own := stated[key]; !own {
			stated[key] = item
		}
	}
	return stated, nil
}

// merge adds a merge key's mapping, or each mapping of its list, to into; an earlier mapping wins.
func merge(into map[string]any, decoded any, line int) error {
	sources, isList := decoded.([]any)
	if !isList {
		sources = []any{decoded}
	}
	for _, source := range sources {
		fields, isMapping := source.(map[string]any)
		if !isMapping {
			return fmt.Errorf("line %d: a merge key that is not a mapping", line)
		}
		for key, item := range fields {
			if _, seen := into[key]; !seen {
				into[key] = item
			}
		}
	}
	return nil
}

// yaml11Booleans are the plain scalars YAML 1.1 reads as booleans and YAML 1.2 as strings.
var yaml11Booleans = map[string]bool{"yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true,
	"no": false, "No": false, "NO": false, "off": false, "Off": false, "OFF": false}

// scalar is one scalar's value by its resolved tag.
func scalar(node *yaml.Node) (any, error) {
	if truth, isBoolean := yaml11Booleans[node.Value]; isBoolean && node.Style == 0 {
		return truth, nil
	}
	switch node.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool", "!!int", "!!float":
		return number(node)
	}
	return node.Value, nil
}

// number is a boolean, integer or float scalar decoded as yaml/v3 resolves it; an integer too large for int is a
// float, as JSON would read it.
func number(node *yaml.Node) (any, error) {
	var decoded any
	if err := node.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("line %d: %w", node.Line, err)
	}
	switch v := decoded.(type) {
	case int64:
		return int(v), nil
	case uint64:
		parsed, _ := strconv.ParseFloat(node.Value, 64)
		return parsed, nil
	}
	return decoded, nil
}
