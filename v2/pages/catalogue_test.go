package pages_test

import (
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/pages"
)

// object is a document object with its keys in the order written.
func object(fields ...pages.ValueField) pages.Value {
	return pages.Value{Kind: pages.ValueObject, Fields: fields}
}

// array is a document array.
func array(items ...pages.Value) pages.Value {
	return pages.Value{Kind: pages.ValueArray, Items: items}
}

// text is a document string.
func text(said string) pages.Value { return pages.Value{Kind: pages.ValueString, Text: said} }

// field is one key of an object.
func field(name string, value pages.Value) pages.ValueField {
	return pages.ValueField{Name: name, Value: value}
}

// offerRow is one instance of the catalogue's fixture: a web app that publishes one event.
func offerRow(app, env string, publishing bool) pages.AppRow {
	inventory := &pages.Inventory{Team: "demo", Kind: "web", Port: 9000,
		Address: "http://" + app + "." + app + "-" + env + ".svc.cluster.local:9000", API: "http://api.example/openapi.json",
		Offers: "abc1234"}
	if publishing {
		inventory.Publishes = []string{"created"}
		inventory.Connections = []pages.Connection{{Role: "publishes", Event: "created"}}
	}
	return pages.AppRow{Instance: pages.Instance{App: app, Env: env}, Inventory: inventory, Teams: map[string]string{app: "demo"}}
}

// The catalogue words a payload schema's fields as the console's own cases do: one row per field, a nested field below
// its parent under its path, and a field of an optional parent not required. The schema and the example are printed as
// Python's json.dumps(indent=2) prints them.
func TestCataloguePayloadRows(t *testing.T) {
	idField := object(field("type", text("string")), field("format", text("uuid")), field("description", text("Its id")))
	callerField := object(
		field("type", text("object")),
		field("required", array(text("type"))),
		field("properties", object(
			field("type", object(field("enum", array(text("app"), text("user"))))),
			field("id", object(field("type", array(text("string"), text("null"))))),
		)),
	)
	itemField := object(field("type", text("string")))
	linesField := object(field("type", text("array")), field("items", object(
		field("type", text("object")),
		field("required", array(text("item"))),
		field("properties", object(field("item", itemField))),
	)))
	schema := object(
		field("type", text("object")),
		field("required", array(text("id"), text("caller"))),
		field("properties", object(
			field("id", idField),
			field("caller", callerField),
			field("lines", linesField),
		)),
	)
	rows := []pages.AppRow{offerRow("orders", "staging", true)}
	page := pages.CataloguePageOf(pages.CatalogueInput{Rows: rows, Offers: map[string]pages.Offers{"orders": {
		API: &pages.OfferedAPI{Description: "Create and read orders", Operations: []pages.Operation{
			{Method: "GET", Path: "/orders/{id}", Summary: "Read one order"}, {Method: "POST", Path: "/orders"},
			{Method: "GET", Path: "/orders", Summary: "List orders"}}},
		Events: []pages.OfferedEvent{{Name: "created", Description: "An order was <b>accepted</b>", Payload: schema,
			Example: object(field("id", text("o-1")))}}}}})
	if len(page.Groups) != 1 || len(page.Groups[0].Offers) != 2 {
		t.Fatalf("groups = %+v", page.Groups)
	}
	api, event := page.Groups[0].Offers[0], page.Groups[0].Offers[1]
	if api.RoutesLabel != "3 routes" || api.SamplePath != "/orders" {
		t.Fatalf("api = %q, %q", api.RoutesLabel, api.SamplePath)
	}
	want := []pages.PayloadField{
		{Name: "id", Type: "string (uuid)", Required: true, Meaning: "Its id"},
		{Name: "caller", Type: "object", Required: true},
		{Name: "caller.type", Type: "one of app, user", Required: true},
		{Name: "caller.id", Type: "string or null"},
		{Name: "lines", Type: "list of object"},
		{Name: "lines[].item", Type: "string"},
	}
	if !reflect.DeepEqual(event.Fields, want) {
		t.Fatalf("fields = %+v", event.Fields)
	}
	printedSchema := "{\n  \"type\": \"object\",\n  \"required\": [\n    \"id\",\n    \"caller\"\n  ],\n  \"properties\": {\n" +
		"    \"id\": {\n      \"type\": \"string\",\n      \"format\": \"uuid\",\n      \"description\": \"Its id\"\n    },\n" +
		"    \"caller\": {\n      \"type\": \"object\",\n      \"required\": [\n        \"type\"\n      ],\n      \"properties\": {\n" +
		"        \"type\": {\n          \"enum\": [\n            \"app\",\n            \"user\"\n          ]\n        },\n" +
		"        \"id\": {\n          \"type\": [\n            \"string\",\n            \"null\"\n          ]\n        }\n      }\n    },\n" +
		"    \"lines\": {\n      \"type\": \"array\",\n      \"items\": {\n        \"type\": \"object\",\n" +
		"        \"required\": [\n          \"item\"\n        ],\n        \"properties\": {\n          \"item\": {\n" +
		"            \"type\": \"string\"\n          }\n        }\n      }\n    }\n  }\n}"
	if event.Schema != printedSchema {
		t.Fatalf("schema = %s", event.Schema)
	}
	if event.Example != "{\n  \"id\": \"o-1\"\n}" {
		t.Fatalf("example = %s", event.Example)
	}
}

// A schema that is not an object with properties gives no rows, so the page shows the schema alone.
func TestCataloguePayloadRowsNeedAnObject(t *testing.T) {
	for _, schema := range []pages.Value{text("string"), object(field("type", text("string"))), object()} {
		if rows := pages.CataloguePageOf(pages.CatalogueInput{Rows: []pages.AppRow{offerRow("orders", "staging", true)},
			Offers: map[string]pages.Offers{"orders": {Events: []pages.OfferedEvent{{Name: "created", Payload: schema}}}}}).
			Groups[0].Offers[0].Fields; len(rows) != 0 {
			t.Fatalf("schema %+v gave %+v", schema, rows)
		}
	}
}

// The catalogue lists the apps that offer something first, then those that only use, and a chosen kind leaves out an
// app that offers nothing of it: the console's own cases.
func TestCatalogueGroupsAndFilters(t *testing.T) {
	rows := []pages.AppRow{offerRow("reports", "staging", false), offerRow("orders", "staging", true)}
	offers := map[string]pages.Offers{"orders": {API: &pages.OfferedAPI{Description: "Create and read orders"}}}
	page := pages.CataloguePageOf(pages.CatalogueInput{Rows: rows, Offers: offers})
	groups := []string{}
	for _, group := range page.Groups {
		groups = append(groups, group.App)
	}
	if !reflect.DeepEqual(groups, []string{"orders", "reports"}) {
		t.Fatalf("groups = %v", groups)
	}
	if page.Tally.APIs != 2 || page.Tally.Events != 1 || page.Tally.Described != 1 {
		t.Fatalf("tally = %+v", page.Tally)
	}
	events := pages.CataloguePageOf(pages.CatalogueInput{Rows: rows, Offers: offers, Kind: "event"})
	if len(events.Groups) != 1 || events.Groups[0].App != "orders" || events.Tally.APIs != 0 || events.Tally.Events != 1 {
		t.Fatalf("event kind = %+v", events)
	}
	nobody := pages.CataloguePageOf(pages.CatalogueInput{Rows: rows, Offers: offers, Env: "prod", Team: "nobody"})
	if len(nobody.Groups) != 0 || nobody.Env != "prod" || nobody.Team != "nobody" {
		t.Fatalf("empty = %+v", nobody)
	}
}
