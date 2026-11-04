package helm

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Values are JSON, not YAML. In particular JSON surrogate escapes must never pass through a YAML parser.
func validValues(raw []byte) bool {
	if !json.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	return err == nil && token == json.Delim('{') && jsonObject(decoder, 0)
}

func jsonObject(decoder *json.Decoder, depth int) bool {
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		if !jsonToken(decoder, depth+1) {
			return false
		}
	}
	_, err := decoder.Token()
	return err == nil
}

func jsonToken(decoder *json.Decoder, depth int) bool {
	if depth > 100 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	switch value := token.(type) {
	case json.Delim:
		if value == '{' {
			return jsonObject(decoder, depth)
		}
		return value == '[' && jsonArray(decoder, depth)
	case json.Number:
		if strings.ContainsAny(string(value), ".eE") {
			return true
		}
		_, err := integer(string(value))
		return err == nil
	}
	return true
}

func jsonArray(decoder *json.Decoder, depth int) bool {
	for decoder.More() {
		if !jsonToken(decoder, depth+1) {
			return false
		}
	}
	_, err := decoder.Token()
	return err == nil
}

// The token pass has already rejected duplicates, invalid roots/depth and integer overflow. Decode strings
// before Helm's YAML values reader sees them: JSON surrogate pairs are valid JSON, but not YAML escapes.
func normalizedValues(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, refuse("invalid_values")
	}
	normalized, err := json.Marshal(value)
	if err != nil || len(normalized) > MaxValues {
		return nil, refuse("invalid_values")
	}
	return normalized, nil
}
