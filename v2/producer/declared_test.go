package producer

import (
	"strings"
	"testing"
)

// sampleFlags is the block a test source declares: one of each type, one with an environment value and bounds.
func sampleFlags() Object {
	return Object{
		{"shout", Object{{"type", "boolean"}, {"default", false}, {"staging", true}, {"description", "Say hello in capitals"}}},
		{"max-items", Object{{"type", "integer"}, {"default", int64(20)}, {"min", int64(1)}, {"max", int64(100)}}},
		{"ratio", Object{{"type", "number"}, {"default", 0.5}, {"min", 0.0}, {"max", 1.0}}},
		{"banner", Object{{"type", "string"}, {"default", "none"}, {"enum", []any{"none", "sale"}}}},
	}
}

// declaredNames is the flags a test source declares, in name order.
func declaredNames(t *testing.T) ([]Flag, Source) {
	t.Helper()
	source, err := Parse(worker(Field{"flags", sampleFlags()}))
	if err != nil {
		t.Fatal(err)
	}
	flags, err := Declarations(FlagsDocument(source.Flags))
	if err != nil {
		t.Fatal(err)
	}
	return flags, source
}

// The document the producer writes is the document it reads back: the same flags, the same values, in name order.
func TestDeclarationsReadsBackWhatTheSourceDeclared(t *testing.T) {
	flags, _ := declaredNames(t)
	var names []string
	for _, flag := range flags {
		names = append(names, flag.Name)
	}
	if got := strings.Join(names, ","); got != "banner,max-items,ratio,shout" {
		t.Fatalf("names: %s", got)
	}
	byName := map[string]Flag{}
	for _, flag := range flags {
		byName[flag.Name] = flag
	}
	if shout := byName["shout"]; shout.Type() != "boolean" || shout.Description() != "Say hello in capitals" ||
		shout.Declared("staging") != true || shout.Declared("prod") != false {
		t.Fatalf("shout: %+v", shout)
	}
	if items := byName["max-items"]; items.Declared("prod") != int64(20) {
		t.Fatalf("max-items: %+v", items)
	}
}

// A document is refused with the file it was read from, and the rule it broke: a document swhurl.yaml could not
// have declared never reaches a card.
func TestDeclarationsRefusesADocumentTheSourceRulesRefuse(t *testing.T) {
	for _, refused := range []struct{ name, document string }{
		{"not a mapping", `[]`},
		{"not JSON", `flags: {`},
		{"trailing content", `{} {}`},
		{"a duplicate flag", `{"shout": {"type": "boolean", "default": false}, "shout": {"type": "boolean", "default": true}}`},
		{"a duplicate field", `{"shout": {"type": "boolean", "type": "boolean", "default": false}}`},
		{"an unknown type", `{"shout": {"type": "choice", "default": "a"}}`},
		{"no default", `{"shout": {"type": "boolean"}}`},
		{"a default outside its bounds", `{"max-items": {"type": "integer", "default": 200, "max": 100}}`},
		{"a value of the wrong type", `{"shout": {"type": "boolean", "default": "yes"}}`},
		{"an unknown field", `{"shout": {"type": "boolean", "default": false, "team": "demo"}}`},
		{"a name that is not kebab case", `{"Shout": {"type": "boolean", "default": false}}`},
	} {
		t.Run(refused.name, func(t *testing.T) {
			_, err := Declarations([]byte(refused.document))
			if err == nil || !strings.Contains(err.Error(), "flags.json") {
				t.Fatalf("must refuse: %v", err)
			}
		})
	}
	if flags, err := Declarations([]byte(`{}`)); err != nil || len(flags) != 0 {
		t.Fatalf("an empty document declares no flags: %v %v", flags, err)
	}
}

// A form's text becomes the declared type, or the card's own refusal: the words are the ones a person already reads.
func TestFlagTextTypesTheFormsValue(t *testing.T) {
	flags, _ := declaredNames(t)
	byName := map[string]Flag{}
	for _, flag := range flags {
		byName[flag.Name] = flag
	}
	for _, accepted := range []struct {
		flag, text string
		want       any
	}{
		{"shout", "true", true}, {"shout", " false ", false},
		{"max-items", "42", int64(42)}, {"max-items", "1", int64(1)}, {"max-items", "100", int64(100)},
		{"ratio", "0.25", 0.25}, {"ratio", "1", int64(1)}, {"ratio", "5e-1", 0.5},
		{"banner", "sale", "sale"}, {"banner", " none ", "none"},
	} {
		value, err := byName[accepted.flag].Text(accepted.text)
		if err != nil || value != accepted.want {
			t.Errorf("%s=%q: %#v, %v", accepted.flag, accepted.text, value, err)
		}
	}
	for _, refused := range []struct{ flag, text, wording string }{
		{"shout", "yes", "choose true or false"},
		{"max-items", "1.5", "give a whole number"},
		{"max-items", "", "give a whole number"},
		{"max-items", "2147483648", "an integer"},
		{"max-items", "200", "outside min and max"},
		{"max-items", "0", "outside min and max"},
		{"ratio", "half", "give a number"},
		{"ratio", "NaN", "a number"},
		{"ratio", "1.5", "outside min and max"},
		{"banner", "loud", "must be one of none, sale"},
	} {
		_, err := byName[refused.flag].Text(refused.text)
		if err == nil || !strings.Contains(err.Error(), refused.wording) {
			t.Errorf("%s=%q: %v, want %q", refused.flag, refused.text, err, refused.wording)
		}
	}
}

// A name is kebab case, each part starting with a letter, at most 40 characters: clearing an override needs no
// declaration, so the name is all that is checked.
func TestValidFlagName(t *testing.T) {
	for _, valid := range []string{"shout", "max-items", "a1", "new-checkout"} {
		if !ValidFlagName(valid) {
			t.Errorf("refused %q", valid)
		}
	}
	for _, refused := range []string{"", "Shout", "max--items", "a-1", "1a", "a" + strings.Repeat("b", 40), "a b", "a_b"} {
		if ValidFlagName(refused) {
			t.Errorf("accepted %q", refused)
		}
	}
}
