// Package appsourcetest provides the app-source behavioural fake and the common reader contract.
package appsourcetest

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// Fake holds already decoded facts and raw files for one checkout, and can inject an unavailable source.
type Fake struct {
	Root          string
	Source        producer.Object
	EventMappings map[string]producer.Object
	Files         map[string][]byte
	Err           error
	Reads         []string
}

var _ appsource.Reader = (*Fake)(nil)

// Declaration returns the source or the configured failure, recording the read.
func (f *Fake) Declaration(root string) (producer.Object, error) {
	f.Reads = append(f.Reads, "swhurl.yaml")
	if err := f.check(root, "swhurl.yaml"); err != nil {
		return nil, err
	}
	if f.Source == nil {
		return nil, fmt.Errorf("swhurl.yaml: missing")
	}
	return f.Source, nil
}

// API returns an owned copy of a known document, or a named refusal.
func (f *Fake) API(root, name string) ([]byte, error) {
	f.Reads = append(f.Reads, name)
	if err := f.check(root, name); err != nil {
		return nil, err
	}
	data, ok := f.Files[name]
	if !ok {
		return nil, fmt.Errorf("%s, which swhurl.yaml names: missing", name)
	}
	return bytes.Clone(data), nil
}

// Events returns a known decoded mapping and owned raw bytes, or no partial result on failure.
func (f *Fake) Events(root, name string) (producer.Object, []byte, error) {
	data, err := f.API(root, name)
	if err != nil {
		return nil, nil, err
	}
	o, ok := f.EventMappings[name]
	if !ok {
		return nil, nil, fmt.Errorf("%s: not valid YAML", name)
	}
	return o, data, nil
}
func (f *Fake) check(root, name string) error {
	if f.Err != nil {
		return f.Err
	}
	if root != f.Root {
		return fmt.Errorf("%s: checkout unavailable", name)
	}
	return nil
}

// Scenario is the decoded source in testdata/swhurl.yaml.
func Scenario() producer.Object {
	return producer.Object{{Name: "version", Value: int64(1)}, {Name: "kind", Value: "worker"}, {Name: "provides", Value: producer.Object{{Name: "eventsFile", Value: "asyncapi.yaml"}}}}
}

// EventScenario is the decoded testdata/asyncapi.yaml, including an explicitly empty mapping.
func EventScenario() producer.Object {
	return producer.Object{{Name: "asyncapi", Value: "3.0.0"}, {Name: "channels", Value: producer.Object{}}}
}

// Contract is the read boundary both implementations obey: ordered typed facts, opaque bytes, owned byte
// results, missing names refused and no partial result. The factory supplies the raw fixture checkout.
func Contract(t *testing.T, open func(*testing.T) (appsource.Reader, string)) {
	t.Helper()
	r, root := open(t)
	o, err := r.Declaration(root)
	if err != nil || !reflect.DeepEqual(o, Scenario()) {
		t.Fatalf("source = %#v, %v", o, err)
	}
	o, data, err := r.Events(root, "asyncapi.yaml")
	if err != nil || !reflect.DeepEqual(o, EventScenario()) || string(data) != "asyncapi: 3.0.0\r\nchannels: {}\r\n" {
		t.Fatalf("events = %#v, %q, %v", o, data, err)
	}
	contractBytes(t, r, root)
	contractMissing(t, r, root)
}
func contractMissing(t *testing.T, r appsource.Reader, root string) {
	if data, err := r.API(root, "missing"); err == nil || data != nil {
		t.Fatal("missing API must fail atomically")
	}
	if o, data, err := r.Events(root, "missing"); err == nil || o != nil || data != nil {
		t.Fatal("missing events must fail atomically")
	}
	if o, err := r.Declaration(root + "/missing"); err == nil || o != nil {
		t.Fatal("missing checkout must fail")
	}
}

func contractBytes(t *testing.T, r appsource.Reader, root string) {
	want := []byte{255, 0, 'A', 'P', 'I', '\r', '\n'}
	data, err := r.API(root, "api.bin")
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("API = %q, %v", data, err)
	}
	data[0] = 0
	again, err := r.API(root, "api.bin")
	if err != nil || !bytes.Equal(again, want) {
		t.Fatal("API bytes not owned")
	}
}
