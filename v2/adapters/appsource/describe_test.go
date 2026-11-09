package appsource_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The two documents an instance keeps beside it, as a stack template writes them: the API's one route, and the
// event it publishes with a description, a payload schema and an example.
const (
	api    = `{"openapi": "3.1.0", "info": {"title": "orders", "description": "Create and read orders"}, "paths": {"/orders/{id}": {"get": {"summary": "Read one order"}, "post": {"description": "Replace one order"}}}}`
	events = `asyncapi: 3.0.0
info: {title: orders, version: 0.1.0}
channels:
  created:
    address: created
    description: An order was accepted
    messages:
      created:
        payload: {type: object, required: [id], properties: {id: {type: string}}}
        examples: [{payload: {id: o-1}}]
operations:
  sendCreated: {action: send, channel: {$ref: '#/channels/created'}}
`
)

// Describe reads what the catalogue shows of an instance's own documents: its routes in document order, and each
// event with the description, schema and example its document states.
func TestDescribeOffersDocuments(t *testing.T) {
	offers, err := appsource.Describe([]byte(api), []byte(events))
	if err != nil {
		t.Fatal(err)
	}
	if offers.API == nil || offers.API.Description != "Create and read orders" {
		t.Fatalf("api = %+v", offers.API)
	}
	want := []pages.Operation{{Method: "GET", Path: "/orders/{id}", Summary: "Read one order"},
		{Method: "POST", Path: "/orders/{id}", Summary: "Replace one order"}}
	if !reflect.DeepEqual(offers.API.Operations, want) {
		t.Fatalf("operations = %+v", offers.API.Operations)
	}
	if len(offers.Events) != 1 || offers.Events[0].Name != "created" || offers.Events[0].Description != "An order was accepted" {
		t.Fatalf("events = %+v", offers.Events)
	}
	payload := offers.Events[0].Payload
	if payload.Kind != pages.ValueObject || payload.Fields[0].Name != "type" {
		t.Fatalf("payload = %+v", payload)
	}
	if example := offers.Events[0].Example; example.Fields[0].Value.Text != "o-1" {
		t.Fatalf("example = %+v", example)
	}
	if offers.Problem != "" {
		t.Fatalf("problem = %q", offers.Problem)
	}
}

// An instance with neither document offers nothing, which is not a problem; a document that is not the kind its name
// says is refused, so the page can say its description was not shown.
func TestDescribeRefusesWhatItCannotRead(t *testing.T) {
	if offers, err := appsource.Describe(nil, nil); err != nil || offers.API != nil || len(offers.Events) != 0 {
		t.Fatalf("nothing offered: %+v, %v", offers, err)
	}
	for _, one := range []struct{ api, events, says string }{
		{`{"openapi": "2.0", "paths": {}}`, "", "must be an OpenAPI 3 document"},
		{`{"openapi": "3.1.0"}`, "", "must be an OpenAPI 3 document"},
		{"", "asyncapi: 2.6.0\n", "must be an AsyncAPI 3 document"},
	} {
		offers, err := appsource.Describe([]byte(one.api), []byte(one.events))
		if err == nil || !strings.Contains(err.Error(), one.says) || offers.API != nil {
			t.Fatalf("%q: %+v, %v", one.says, offers, err)
		}
	}
}
