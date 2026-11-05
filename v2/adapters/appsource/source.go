// Package appsource reads only swhurl.yaml and the documents it explicitly names. YAML decoding is confined
// here; source policy and encoding belong to producer. No subprocess, registry or platform files are used.
package appsource

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/samclement/swhurl-platform/v2/producer"
	"go.yaml.in/yaml/v3"
)

// Reader is the app checkout boundary. API bytes are opaque; declarations and events are YAML mappings.
type Reader interface {
	Declaration(root string) (producer.Object, error)
	API(root, name string) ([]byte, error)
	Events(root, name string) (producer.Object, []byte, error)
}

// Local reads only explicitly named paths, following symlinks as the original local producer did.
type Local struct{}

// Declaration reads the source file, never a platform profile or settings manifest.
func (Local) Declaration(root string) (producer.Object, error) {
	data, err := read(root, "swhurl.yaml")
	if err != nil {
		return nil, err
	}
	return decode(data, "swhurl.yaml")
}

// API returns exactly the explicitly named API document bytes, with no OpenAPI/creation validation.
func (Local) API(root, name string) ([]byte, error) { return read(root, name) }

// Events returns the explicitly named events mapping and its unchanged bytes in one read.
func (Local) Events(root, name string) (producer.Object, []byte, error) {
	data, err := read(root, name)
	if err != nil {
		return nil, nil, err
	}
	o, err := decode(data, name)
	if err != nil {
		return nil, nil, err
	}
	return o, data, nil
}
func read(root, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return nil, fmt.Errorf("%s, which swhurl.yaml names: %w", name, err)
	}
	return data, nil
}
func decode(data []byte, name string) (producer.Object, error) {
	fail := func(err error) (producer.Object, error) { return nil, fmt.Errorf("%s: not valid YAML: %w", name, err) }
	if !utf8.Valid(data) {
		return fail(fmt.Errorf("invalid UTF-8"))
	}
	d := yaml.NewDecoder(strings.NewReader(string(data)))
	var node yaml.Node
	if err := d.Decode(&node); err != nil {
		return fail(err)
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return fail(fmt.Errorf("expected one YAML document"))
	}
	if len(node.Content) != 1 {
		return fail(fmt.Errorf("must be a mapping"))
	}
	v, err := value(node.Content[0], map[*yaml.Node]bool{})
	if err != nil {
		return fail(err)
	}
	o, ok := v.(producer.Object)
	if !ok {
		return nil, fmt.Errorf("%s: must be a mapping", name)
	}
	return o, nil
}

// active follows only the current expansion path: repeated aliases are valid, cycles are not.
func value(n *yaml.Node, active map[*yaml.Node]bool) (any, error) {
	if n == nil {
		return nil, fmt.Errorf("missing alias target")
	}
	if active[n] {
		return nil, fmt.Errorf("cyclic YAML alias")
	}
	active[n] = true
	defer delete(active, n)
	switch n.Kind {
	case yaml.AliasNode:
		return value(n.Alias, active)
	case yaml.MappingNode:
		if n.Tag != "!!map" {
			return nil, fmt.Errorf("unsupported YAML tag %s", n.Tag)
		}
		return mapping(n, active)
	case yaml.SequenceNode:
		if n.Tag != "!!seq" {
			return nil, fmt.Errorf("unsupported YAML tag %s", n.Tag)
		}
		return sequence(n, active)
	case yaml.ScalarNode:
		return scalar(n)
	default:
		return nil, fmt.Errorf("unsupported YAML node")
	}
}
func sequence(n *yaml.Node, active map[*yaml.Node]bool) ([]any, error) {
	a := []any{}
	for _, c := range n.Content {
		v, err := value(c, active)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
	}
	return a, nil
}

// PyYAML flattens all merges before explicit fields. Sequence merges are flattened in reverse order,
// so the first mapping wins; explicit fields always win. Assignment keeps a key's first insertion slot.
func mapping(n *yaml.Node, active map[*yaml.Node]bool) (producer.Object, error) {
	o, err := mergedFields(n, active)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Tag == "!!merge" {
			continue
		}
		key, err := value(k, active)
		if err != nil {
			return nil, err
		}
		switch key.(type) {
		case producer.Object, []any:
			return nil, fmt.Errorf("unhashable YAML mapping key")
		}
		v, err := value(n.Content[i+1], active)
		if err != nil {
			return nil, err
		}
		o = assign(o, producer.Field{Name: key, Value: v})
	}
	return o, nil
}
func mergedFields(n *yaml.Node, active map[*yaml.Node]bool) (producer.Object, error) {
	o := producer.Object{}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Tag != "!!merge" {
			continue
		}
		v, err := value(n.Content[i+1], active)
		if err != nil {
			return nil, err
		}
		fields, err := merge(v)
		if err != nil {
			return nil, err
		}
		for _, f := range fields {
			o = assign(o, f)
		}
	}
	return o, nil
}
func merge(v any) (producer.Object, error) {
	if o, ok := v.(producer.Object); ok {
		return o, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("merge value must be a mapping or sequence of mappings")
	}
	o := producer.Object{}
	for i := len(a) - 1; i >= 0; i-- {
		fields, ok := a[i].(producer.Object)
		if !ok {
			return nil, fmt.Errorf("merge sequence entries must be mappings")
		}
		o = append(o, fields...)
	}
	return o, nil
}
func assign(o producer.Object, f producer.Field) producer.Object {
	for i := range o {
		if equalKey(o[i].Name, f.Name) {
			o[i].Value = f.Value
			return o
		}
	}
	return append(o, f)
}
