// Package manifeststest is the fake for manifests.Decoder and the contract suite both implementations run.
package manifeststest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/checkrepo"
)

// Fake answers the texts it was given the documents of, and refuses any other text as YAML it cannot read. It parses
// nothing, so a use case's tests do not need a YAML parser; the contract suite keeps it honest.
type Fake struct {
	Answers map[string][]checkrepo.Document
	Calls   int
}

var _ manifests.Decoder = (*Fake)(nil)

// errNotYAML is the fake's refusal of a text it holds no answer for.
var errNotYAML = errors.New("not YAML the fake holds an answer for")

// Documents answers a known text, or refuses it.
func (f *Fake) Documents(text []byte) ([]checkrepo.Document, error) {
	f.Calls++
	documents, known := f.Answers[string(text)]
	if !known {
		return nil, errNotYAML
	}
	return documents, nil
}

// Case is one text the contract decodes: the documents it holds, or that it is not YAML.
type Case struct {
	Name, Text string
	Documents  []checkrepo.Document
	Invalid    bool
}

// Scenario is the contract's cases: an empty stream, a lone comment, a mapping, documents that are not mappings, an
// empty document between separators, YAML 1.1 booleans, JSON, aliases with a merge key, a kind and name that are not
// strings, and text that is not YAML.
func Scenario() []Case {
	secret := map[string]any{"kind": "Secret", "metadata": map[string]any{"name": "s"}, "stringData": map[string]any{"a": "ENC[x]"}}
	return []Case{
		{Name: "empty stream", Text: ""},
		{Name: "a comment alone", Text: "# nothing here\n"},
		{Name: "one mapping", Text: "kind: Secret\nmetadata: {name: s}\nstringData: {a: \"ENC[x]\"}\n",
			Documents: []checkrepo.Document{{Kind: "Secret", Name: "s", Value: secret}}},
		{Name: "documents that are not mappings", Text: "- a\n---\nscalar\n---\nnull\n",
			Documents: []checkrepo.Document{{}, {}, {}}},
		{Name: "an empty document and a trailing separator", Text: "a: 1\n---\n---\nb: 2\n---\n",
			Documents: []checkrepo.Document{{Value: map[string]any{"a": 1}}, {}, {Value: map[string]any{"b": 2}}, {}}},
		{Name: "YAML 1.1 booleans, quoted strings, numbers and null", Text: "a: on\nb: 'on'\nc: 1\nd: ~\ne: 1.5\nf: no\n",
			Documents: []checkrepo.Document{{Value: map[string]any{"a": true, "b": "on", "c": 1, "d": nil, "e": 1.5, "f": false}}}},
		{Name: "JSON", Text: `{"kind": "Kustomization", "metadata": {"name": "u"}, "spec": {"optional": false, "list": [1, "x"]}}`,
			Documents: []checkrepo.Document{{Kind: "Kustomization", Name: "u", Value: map[string]any{"kind": "Kustomization",
				"metadata": map[string]any{"name": "u"}, "spec": map[string]any{"optional": false, "list": []any{1, "x"}}}}}},
		{Name: "aliases and a merge key", Text: "a: &x {k: v, z: 0}\nb: *x\nc: {<<: *x, z: 1}\n",
			Documents: []checkrepo.Document{{Value: map[string]any{"a": map[string]any{"k": "v", "z": 0},
				"b": map[string]any{"k": "v", "z": 0}, "c": map[string]any{"k": "v", "z": 1}}}}},
		{Name: "a kind and name that are not strings", Text: "kind: 1\nmetadata: {name: [x]}\n",
			Documents: []checkrepo.Document{{Value: map[string]any{"kind": 1, "metadata": map[string]any{"name": []any{"x"}}}}}},
		{Name: "not YAML", Text: "a: [1\n", Invalid: true},
	}
}

// Known is a fake that answers every Scenario case it can decode.
func Known() *Fake {
	f := &Fake{Answers: map[string][]checkrepo.Document{}}
	for _, c := range Scenario() {
		if !c.Invalid {
			f.Answers[c.Text] = c.Documents
		}
	}
	return f
}

// Contract is the behaviour every manifests.Decoder must have: each Scenario case decoded to exactly its documents,
// and text that is not YAML refused with no documents.
func Contract(t *testing.T, open func(*testing.T) manifests.Decoder) {
	t.Helper()
	decoder := open(t)
	for _, c := range Scenario() {
		documents, err := decoder.Documents([]byte(c.Text))
		switch {
		case c.Invalid && (err == nil || documents != nil):
			t.Errorf("%s: documents %v, error %v; want a refusal and no documents", c.Name, documents, err)
		case !c.Invalid && err != nil:
			t.Errorf("%s: %v", c.Name, err)
		case !c.Invalid && !reflect.DeepEqual(documents, c.Documents):
			t.Errorf("%s: documents\n  %#v\nwant\n  %#v", c.Name, documents, c.Documents)
		}
	}
}
