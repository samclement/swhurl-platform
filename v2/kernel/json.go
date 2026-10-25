package kernel

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"unicode/utf8"
)

// Check before struct decoding: encoding/json otherwise accepts duplicate keys,
// case-insensitive field matches, and null for scalar fields.
func document(data []byte) error {
	if !utf8.Valid(data) || !json.Valid(data) {
		return refuse(Invalid, "document", "expected one valid UTF-8 JSON document")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return uniqueKeys(decoder)
}

func uniqueKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return refuse(Invalid, "document", "invalid JSON")
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return refuse(Invalid, "document", "invalid object key")
			}
			key := token.(string) // json.Valid established object grammar
			if seen[key] {
				return refuse(Invalid, "document", "duplicate object key")
			}
			seen[key] = true
			if err := uniqueKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	case json.Delim('['):
		for decoder.More() {
			if err := uniqueKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	}
	return err
}

func fields(data []byte, where string, required []string, optional ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, refuse(Invalid, where, "expected an object")
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return nil, refuse(Invalid, where+"."+key, "required field is missing")
		}
	}
	// Unsupported fields before nulls, each in key order: one document always gets one refusal.
	keys := slices.Sorted(maps.Keys(object))
	for _, key := range keys {
		if !slices.Contains(required, key) && !slices.Contains(optional, key) {
			return nil, refuse(Unsupported, where, "unsupported field")
		}
	}
	for _, key := range keys {
		if bytes.Equal(bytes.TrimSpace(object[key]), []byte("null")) {
			return nil, refuse(Invalid, where+"."+key, "null is not supported")
		}
	}
	return object, nil
}

func DecodeRelease(data []byte) (ReleaseBundle, error) {
	if err := document(data); err != nil {
		return ReleaseBundle{}, err
	}
	root, err := fields(data, "release", []string{"schemaVersion", "application", "source", "artifact", "runtime"}, "releaseId", "database")
	if err != nil {
		return ReleaseBundle{}, err
	}
	for _, part := range []struct {
		key      string
		required []string
	}{
		{"source", []string{"repository", "commit"}},
		{"artifact", []string{"image", "digest", "mediaType", "platform"}},
		{"runtime", []string{"kind", "port", "healthPath", "uid"}},
	} {
		if _, err := fields(root[part.key], part.key, part.required); err != nil {
			return ReleaseBundle{}, err
		}
	}
	var release ReleaseBundle
	if err := json.Unmarshal(data, &release); err != nil {
		return ReleaseBundle{}, refuse(Invalid, "release", "incorrect field type or integer range")
	}
	if _, present := root["releaseId"]; present && !releasePattern.MatchString(string(release.ReleaseID)) {
		return ReleaseBundle{}, refuse(Invalid, "releaseId", "expected a release ID")
	}
	release.ReleaseID, err = release.ID()
	if err != nil {
		return ReleaseBundle{}, err
	}
	return release, nil
}

func DecodeInstance(data []byte) (InstanceSpec, error) {
	if err := document(data); err != nil {
		return InstanceSpec{}, err
	}
	root, err := fields(data, "instance", []string{"schemaVersion", "application", "environment", "release", "configuration", "grants", "bindings"}, "storage")
	if err != nil {
		return InstanceSpec{}, err
	}
	for _, part := range []struct {
		key      string
		required []string
	}{
		{"configuration", []string{"replicas", "memoryMiB"}},
		{"grants", []string{"exposure"}},
		{"bindings", nil},
	} {
		if _, err := fields(root[part.key], part.key, part.required); err != nil {
			return InstanceSpec{}, err
		}
	}
	var spec InstanceSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return InstanceSpec{}, refuse(Invalid, "instance", "incorrect field type or integer range")
	}
	if err := spec.validate(); err != nil {
		return InstanceSpec{}, err
	}
	return spec, nil
}

func EncodeRelease(release ReleaseBundle) ([]byte, error) {
	id, err := release.ID()
	if err != nil {
		return nil, err
	}
	release.ReleaseID = id
	return json.MarshalIndent(release, "", "  ")
}

func EncodeInstance(spec InstanceSpec) ([]byte, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(spec, "", "  ")
}
