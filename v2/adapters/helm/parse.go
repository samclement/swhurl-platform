package helm

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// objects consumes the entire stream before returning anything. A bad later document cannot become a pass.
func objects(raw []byte) ([]json.RawMessage, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var out []json.RawMessage
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil || len(document.Content) != 1 {
			return nil, refuse("invalid_yaml")
		}
		encoded, err := objectJSON(document.Content[0])
		if err != nil {
			return nil, err
		}
		if encoded == nil {
			continue
		}
		out = append(out, encoded)
	}
	if len(out) == 0 {
		return nil, refuse("empty_output")
	}
	return out, nil
}

func objectJSON(node *yaml.Node) (json.RawMessage, error) {
	// Only truly empty/comment documents are absent; explicit null/~/quoted empty is not an object.
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" && node.Value == "" && node.Style == 0 {
		return nil, nil
	}
	value, err := jsonValue(node, 0)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || !identity(object) {
		return nil, refuse("invalid_object")
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, refuse("non_json_value")
	}
	return encoded, nil
}

func jsonValue(node *yaml.Node, depth int) (any, error) {
	if depth > 100 {
		return nil, refuse("yaml_too_deep")
	}
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return nil, refuse("non_json_value")
		}
		return mapping(node, depth)
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return nil, refuse("non_json_value")
		}
		out := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := jsonValue(child, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case yaml.ScalarNode:
		return scalar(node)
	default:
		return nil, refuse("non_json_value") // aliases/merges are not JSON values
	}
}

func mapping(node *yaml.Node, depth int) (map[string]any, error) {
	out := map[string]any{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || implicitNumber(key) != "" {
			return nil, refuse("non_string_key")
		}
		if _, duplicate := out[key.Value]; duplicate {
			return nil, refuse("duplicate_key")
		}
		value, err := jsonValue(node.Content[i+1], depth+1)
		if err != nil {
			return nil, err
		}
		out[key.Value] = value
	}
	return out, nil
}

func scalar(node *yaml.Node) (any, error) {
	if numeric := implicitNumber(node); numeric != "" {
		return number(numeric)
	}
	switch node.Tag {
	case "!!str":
		return node.Value, nil
	case "!!null":
		return nullScalar(node.Value)
	case "!!bool":
		if strings.EqualFold(node.Value, "true") {
			return true, nil
		}
		if strings.EqualFold(node.Value, "false") {
			return false, nil
		}
	case "!!int":
		return integer(node.Value)
	case "!!float":
		return floating(node.Value)
	}
	return nil, refuse("non_json_scalar") // timestamps, binary and custom tags are not JSON scalars
}

func nullScalar(text string) (any, error) {
	if text == "" || text == "~" || strings.EqualFold(text, "null") {
		return nil, nil
	}
	return nil, refuse("non_json_scalar")
}

// Preserve decimal/exponent spelling as a JSON number, never a binary floating-point round trip.
func floating(text string) (any, error) {
	// The YAML resolver falls back to !!float when an integer overflows uint64. Never round it.
	if !strings.ContainsAny(text, ".eE") {
		return nil, refuse("non_json_scalar")
	}
	text = strings.ReplaceAll(text, "_", "")
	if !regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?$`).MatchString(text) {
		return nil, refuse("non_json_scalar")
	}
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimLeft(text, "+-")
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(text), "e")
	whole, fraction, hasFraction := strings.Cut(mantissa, ".")
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	text = whole
	if hasFraction {
		if fraction == "" {
			fraction = "0"
		}
		text += "." + fraction
	}
	if hasExponent {
		text += "e" + exponent
	}
	if negative {
		text = "-" + text
	}
	return json.Number(text), nil
}

// Integers are never decoded through float64: YAML's decimal/base-prefixed forms are normalised exactly.
func integer(text string) (any, error) {
	text = strings.ReplaceAll(text, "_", "")
	if value, err := strconv.ParseInt(text, 0, 64); err == nil {
		return value, nil
	}
	if value, err := strconv.ParseUint(strings.TrimPrefix(text, "+"), 0, 64); err == nil {
		return value, nil
	}
	return nil, refuse("non_json_scalar")
}

func identity(object map[string]any) bool {
	api, _ := object["apiVersion"].(string)
	kind, _ := object["kind"].(string)
	metadata, _ := object["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	if !regexp.MustCompile(`^([a-z0-9]+([.-][a-z0-9]+)*/)?[a-z][a-z0-9]*$`).MatchString(api) ||
		!regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`).MatchString(kind) ||
		!objectName(name) {
		return false
	}
	if namespace, present := metadata["namespace"]; present {
		text, ok := namespace.(string)
		return ok && len(text) <= 63 && regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(text)
	}
	return true
}

// Object kinds have different DNS restrictions (an RBAC name may contain ':'). At this format boundary,
// validate the common path-segment identity, not a guessed schema for every Kubernetes/extension kind.
func objectName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/%\x00")
}

// YAML's resolver may label overflows as !!str. Classify implicit plain lexemes independently of that hint.
// A quoted, block or explicitly tagged string remains a string, whatever its spelling.
func implicitNumber(node *yaml.Node) string {
	if node.Style != 0 {
		return ""
	}
	text := strings.ReplaceAll(node.Value, "_", "")
	if regexp.MustCompile(`^[+-]?(0[xX][0-9a-fA-F]+|0[oO][0-7]+|0[bB][01]+)$`).MatchString(text) {
		return text
	}
	if regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?$`).MatchString(text) {
		return text
	}
	return ""
}

func number(text string) (any, error) {
	unsigned := strings.TrimLeft(text, "+-")
	if strings.HasPrefix(strings.ToLower(unsigned), "0x") {
		return integer(text)
	}
	if strings.ContainsAny(text, ".eE") {
		return floating(text)
	}
	return integer(text)
}
