package producer

import (
	"reflect"
	"testing"
)

func send(ref string) Object { return Object{{"action", "send"}, {"channel", Object{{"$ref", ref}}}} }
func eventSource() Object {
	return Object{{"asyncapi", "3.0.0"}, {"channels", Object{{"z/channel", Object{{"address", "zed"}}}, {"first", Object{{"address", "alpha"}}}, {"ignored", Object{}}}}, {"operations", Object{{"second", send("#/channels/first")}, {"one", send("#/channels/z~1channel")}, {"again", send("#/channels/first")}}}}
}
func TestPublicationOrderAndRefs(t *testing.T) {
	doc := eventSource()
	pubs, err := Publications(doc, "events.yaml")
	if err != nil || !reflect.DeepEqual(pubs, []string{"zed", "alpha"}) {
		t.Fatal(pubs, err)
	}
	doc = append(doc[:2], Field{"operations", Object{{"ref", Object{{"$ref", "#/components/op"}}}}}, Field{"components", Object{{"op", send("#/channels/first")}}})
	pubs, err = Publications(doc, "events.yaml")
	if err != nil || !reflect.DeepEqual(pubs, []string{"alpha"}) {
		t.Fatal(pubs, err)
	}
	for _, doc := range []Object{{{"asyncapi", "3.0.0"}}, {{"asyncapi", "3.0.0"}, {"channels", Object{{"unreferenced", Object{}}}}}} {
		pubs, err := Publications(doc, "events.yaml")
		if err != nil || len(pubs) != 0 {
			t.Fatal(pubs, err)
		}
	}
}
func TestNumericAsyncAPIVersionKeepsSourceSemantics(t *testing.T) {
	doc := Object{{"asyncapi", 3.0}}
	if pubs, err := Publications(doc, "events.yaml"); err != nil || len(pubs) != 0 {
		t.Fatal(pubs, err)
	}
}
func TestPublicationRefusals(t *testing.T) {
	for _, f := range []Field{{"asyncapi", "2.6.0"}, {"channels", []any{}}, {"operations", nil}, {"operations", Object{{"bad", false}}}, {"operations", Object{{"bad", Object{{"action", "publish"}}}}}, {"operations", Object{{"bad", Object{{"action", "send"}}}}}, {"operations", Object{{"bad", send("#/channels/missing")}}}, {"operations", Object{{"bad", Object{{"$ref", "#/nothing"}}}}}, {"channels", Object{{"first", Object{{"$ref", "#/nothing"}}}, {"z/channel", Object{}}}}, {"channels", Object{{"first", Object{{"address", "bad.name"}}}, {"z/channel", Object{}}}}, {"channels", Object{{"first", Object{{"address", "same"}}}, {"z/channel", Object{{"address", "same"}}}}}, {"channels", Object{{"first", Object{{"messages", []any{}}}}, {"z/channel", Object{}}}}, {"channels", Object{{"first", Object{{"messages", Object{{"message", Object{{"$ref", "#/missing"}}}}}}}, {"z/channel", Object{}}}}} {
		doc := eventSource()
		for i := range doc {
			if doc[i].Name == f.Name {
				doc[i] = f
			}
		}
		if pubs, err := Publications(doc, "events.yaml"); err == nil || pubs != nil {
			t.Fatalf("accepted %#v: %v", f, pubs)
		}
	}
}

func TestPythonBooleanEventAddressText(t *testing.T) {
	if scalarText(true) != "True" || scalarText(false) != "False" {
		t.Fatal("booleans must retain Python text")
	}
	doc := Object{{"asyncapi", "3.0.0"}, {"channels", Object{{"native", Object{{"address", true}}}}}, {"operations", Object{{"send", send("#/channels/native")}}}}
	if pubs, err := Publications(doc, "events.yaml"); err == nil || pubs != nil {
		t.Fatal(pubs, err)
	}
}
