package appsource

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/pages"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// Describe is what one instance's two offered documents say, for the catalogue: its API's routes, and the events it
// publishes with the payload schema and first example a subscriber reads. A document an instance does not have is
// not an error, so an instance that offers nothing describes nothing. Python's json prints a payload, so a schema's
// fields are worded by pages; this file only decodes.
func Describe(api, events []byte) (pages.Offers, error) {
	offers := pages.Offers{}
	if len(api) > 0 {
		described, err := describedAPI(api)
		if err != nil {
			return pages.Offers{}, err
		}
		offers.API = &described
	}
	if len(events) > 0 {
		described, err := describedEvents(events)
		if err != nil {
			return pages.Offers{}, err
		}
		offers.Events = described
	}
	return offers, nil
}

// apiMethods are the methods an OpenAPI path item documents, lowercased as an OpenAPI document writes them.
var apiMethods = []string{"get", "put", "post", "delete", "patch", "head", "options"}

// describedAPI is what an OpenAPI 3 document says its app answers: each path and method with its summary. Python's
// check_api: the document must state an OpenAPI 3 version and a paths mapping.
func describedAPI(data []byte) (pages.OfferedAPI, error) {
	name := kernel.DocumentRoles["api"]
	document, err := Parse(data, name)
	if err != nil {
		return pages.OfferedAPI{}, err
	}
	paths, listed := document.Get("paths").(producer.Object)
	if version, _ := document.Get("openapi").(string); !strings.HasPrefix(version, "3.") || !listed {
		return pages.OfferedAPI{}, fmt.Errorf("%s must be an OpenAPI 3 document (openapi: 3.x, with paths)", name)
	}
	offered := pages.OfferedAPI{Description: description(document.Get("info"))}
	for _, path := range paths {
		offered.Operations = append(offered.Operations, operationsOf(path)...)
	}
	return offered, nil
}

// operationsOf is one path item's documented methods, in the order the document writes them.
func operationsOf(path producer.Field) []pages.Operation {
	item, listed := path.Value.(producer.Object)
	if !listed {
		return nil
	}
	found := []pages.Operation{}
	for _, route := range item {
		method, named := route.Name.(string)
		operation, documented := route.Value.(producer.Object)
		if !named || !documented || !slices.Contains(apiMethods, method) {
			continue
		}
		found = append(found, pages.Operation{Method: strings.ToUpper(method), Path: keyText(path.Name),
			Summary: summaryOf(operation)})
	}
	return found
}

// summaryOf is what an operation says it does: its summary, else its description, else nothing.
func summaryOf(operation producer.Object) string {
	if summary, _ := operation.Get("summary").(string); summary != "" {
		return summary
	}
	said, _ := operation.Get("description").(string)
	return said
}

// description is an OpenAPI document's info block description: its text, or nothing when it has none.
func description(info any) string {
	said, _ := info.(producer.Object).Get("description").(string)
	return said
}

// describedEvents is the events an AsyncAPI 3 document says its app sends, with what a catalogue shows of each.
func describedEvents(data []byte) ([]pages.OfferedEvent, error) {
	name := kernel.DocumentRoles["events"]
	document, err := Parse(data, name)
	if err != nil {
		return nil, err
	}
	emitted, err := producer.EmittedEvents(document, name)
	if err != nil {
		return nil, err
	}
	found := make([]pages.OfferedEvent, 0, len(emitted))
	for _, event := range emitted {
		found = append(found, pages.OfferedEvent{Name: event.Name, Description: event.Description,
			Payload: documentValue(event.Payload), Example: documentValue(event.Example)})
	}
	return found, nil
}

// documentValue is a decoded document value as a page keeps it: an object's keys in document order, and a scalar as
// JSON prints it. A value that is not JSON (a YAML timestamp) is its own text.
func documentValue(value any) pages.Value {
	if object, listed := value.(producer.Object); listed {
		return objectValue(object)
	}
	if items, listed := value.([]any); listed {
		return arrayValue(items)
	}
	return scalarValue(value)
}

// objectValue is a mapping's value, its keys in document order.
func objectValue(object producer.Object) pages.Value {
	found := pages.Value{Kind: pages.ValueObject}
	for _, field := range object {
		found.Fields = append(found.Fields, pages.ValueField{Name: keyText(field.Name), Value: documentValue(field.Value)})
	}
	return found
}

// arrayValue is a sequence's value.
func arrayValue(items []any) pages.Value {
	found := pages.Value{Kind: pages.ValueArray}
	for _, item := range items {
		found.Items = append(found.Items, documentValue(item))
	}
	return found
}

// scalarValue is a scalar as JSON prints it.
func scalarValue(value any) pages.Value {
	switch value := value.(type) {
	case string:
		return pages.Value{Kind: pages.ValueString, Text: value}
	case bool:
		return pages.Value{Kind: pages.ValueBool, Text: strconv.FormatBool(value)}
	case int64:
		return pages.Value{Kind: pages.ValueNumber, Text: strconv.FormatInt(value, 10)}
	case json.Number:
		return pages.Value{Kind: pages.ValueNumber, Text: string(value)}
	case float64:
		return pages.Value{Kind: pages.ValueNumber, Text: floatText(value)}
	case nil:
		return pages.Value{Kind: pages.ValueNull}
	}
	return pages.Value{Kind: pages.ValueString, Text: fmt.Sprint(value)}
}

// keyText is an object key as JSON writes it: a document's keys are strings, and a source may number one.
func keyText(name any) string {
	said, isText := name.(string)
	if isText {
		return said
	}
	return fmt.Sprint(name)
}

// floatText is a number as Python's json prints it: the shortest text that reads back as the same number, with ".0" on
// an integral value so a float stays a float.
func floatText(value float64) string {
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if strings.ContainsAny(text, ".eE") {
		return text
	}
	return text + ".0"
}
