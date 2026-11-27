package orchestration

import "testing"

// An ordered document keeps an object's keys as written, which a map would not, and prints each scalar as the page
// that reads it always has.
func TestDecodeOrderedKeepsTheDocumentsOrder(t *testing.T) {
	document, err := DecodeOrdered([]byte(`{"z":1,"a":[true,false],"m":{"s":"text","n":null,"f":1.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	names := ""
	for _, field := range document.Fields {
		names += field.Name
	}
	if names != "zam" {
		t.Fatalf("keys in order %q, want zam", names)
	}
	cases := []struct {
		name  string
		value OrderedValue
		want  string
	}{
		{"number", document.Member("z"), "1"},
		{"true", document.Member("a").Items[0], "True"},
		{"false", document.Member("a").Items[1], "False"},
		{"string", document.Member("m").Member("s"), "text"},
		{"fraction", document.Member("m").Member("f"), "1.5"},
		{"null", document.Member("m").Member("n"), ""},
		{"absent", document.Member("none"), ""},
		{"an object is no scalar", document.Member("m"), ""},
	}
	for _, test := range cases {
		if got := test.value.Text(); got != test.want {
			t.Errorf("%s: Text() = %q, want %q", test.name, got, test.want)
		}
	}
	if _, err := DecodeOrdered([]byte(`{"a":`)); err == nil {
		t.Fatal("a truncated document decoded")
	}
}
