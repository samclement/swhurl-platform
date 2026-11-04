package helm

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func rawFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/objects.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLosslessCompleteObjects(t *testing.T) {
	result, err := objects(rawFixture(t))
	if err != nil || len(result) != 2 {
		t.Fatalf("%v, %d objects", err, len(result))
	}
	for _, literal := range []string{"9007199254740993", "9223372036854775807", "18446744073709551615", "-9223372036854775808", `"nested":[true,null,"unchanged"]`} {
		if !bytes.Contains(result[1], []byte(literal)) {
			t.Errorf("lost %s", literal)
		}
	}
	if !bytes.Contains(result[1], []byte(`"kind":"Unfamiliar"`)) {
		t.Fatal("unknown kind filtered")
	}
	if bytes.Contains(result[1], []byte(`"namespace"`)) {
		t.Fatal("invented a namespace")
	}
	for _, doc := range result {
		if !json.Valid(doc) {
			t.Fatal("not JSON")
		}
	}
}

func TestRejectsWholeStream(t *testing.T) {
	good := string(rawFixture(t))
	invalid := []string{
		"null", "~", `""`, "[]", "42", "metadata: []", "apiVersion: v1\nkind: Pod\nmetadata: {name: [bad]}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good, namespace: 7}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: bad/name}",
		"apiVersion: v1\nkind: 1\nmetadata: {name: good}",
		"apiVersion: []\nkind: Pod\nmetadata: {name: good}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: 1, a: 2}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {1: bad}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: .nan}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: !!null invalid}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: !!bool 1}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: .inf}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: 18446744073709551616}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: 2026-11-04}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: !!binary c2VjcmV0}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: !custom value}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {a: &a [1], b: *a}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: {<<: {a: b}}",
		"apiVersion: v1\nkind: Pod\nmetadata: {name: good}\nspec: [unclosed",
		"...\ntrailing garbage",
	}
	for i, text := range invalid {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			out, err := objects([]byte(good + "\n---\n" + text))
			if out != nil || err == nil {
				t.Fatal("accepted partial/invalid stream")
			}
			if strings.Contains(err.Error(), text) {
				t.Fatal("raw diagnostic escaped")
			}
		})
	}
	for _, empty := range []string{"", "# comment", "---\n# empty\n---"} {
		if out, err := objects([]byte(empty)); out != nil || err == nil {
			t.Fatal("empty success")
		}
	}
}

func TestBaseIntegersAndDepthBound(t *testing.T) {
	for text, want := range map[string]string{"0x20000000000001": "9007199254740993", "0o777": "511", "0777": "511", "+1_024": "1024", "0b101": "5"} {
		value, err := integer(text)
		out, _ := json.Marshal(value)
		if err != nil || string(out) != want {
			t.Fatalf("%s -> %s, %v", text, out, err)
		}
	}
	raw := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: boundary}\nspec: " + strings.Repeat("[", 102) + "0" + strings.Repeat("]", 102)
	if out, err := objects([]byte(raw)); out != nil || err == nil {
		t.Fatal("unbounded depth")
	}
}

func TestDecimalNumbersNeverRoundThroughFloat64(t *testing.T) {
	for text, want := range map[string]string{
		"9007199254740993.0": "9007199254740993.0", "1.234567890123456789": "1.234567890123456789",
		"+.5": "0.5", "001.5": "1.5", "1.": "1.0", "1e+99": "1e+99",
	} {
		value, err := floating(text)
		out, _ := json.Marshal(value)
		if err != nil || string(out) != want {
			t.Fatalf("%s -> %s, %v", text, out, err)
		}
	}
}

func TestIdentityDoesNotInventPerKindDNSRules(t *testing.T) {
	raw := "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata: {name: 'system:boundary'}\nrules: []"
	if result, err := objects([]byte(raw)); err != nil || len(result) != 1 {
		t.Fatal("valid RBAC identity refused")
	}
}

func TestImplicitNumericFallbackCannotBecomeAString(t *testing.T) {
	prefix := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: probe}\ndata:\n  value: "
	for _, number := range []string{"0x10000000000000000", "0o2000000000000000000000", strings.Repeat("9", 400)} {
		for _, earlier := range []string{"", string(rawFixture(t)) + "\n---\n"} {
			result, err := objects([]byte(earlier + prefix + number))
			if err == nil || result != nil {
				t.Errorf("accepted overflowing numeric lexeme %s", number)
			}
		}
		for _, text := range []string{`"` + number + `"`, "!!str " + number} {
			result, err := objects([]byte(prefix + text))
			if err != nil || len(result) != 1 {
				t.Errorf("explicit string refused: %v", err)
			}
		}
	}
	// JSON exponents are not float64: retain their exact JSON number spelling, not a fallback string.
	result, err := objects([]byte(prefix + "1e999"))
	if err != nil || len(result) != 1 || !bytes.Contains(result[0], []byte(`"value":1e999`)) {
		t.Fatal("exponent lost its numeric type")
	}
}

func TestJSONValuesUnicodeIsNotReparsedAsYAML(t *testing.T) {
	for _, text := range []string{`{"text":"\ud83d\ude00"}`, `{"text":"😀"}`} {
		if !validValues([]byte(text)) {
			t.Errorf("valid Unicode JSON refused: %s", text)
		}
	}
	for _, text := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `[]`, `null`, `{} {}`,
		`{"number":18446744073709551616}`, `{"nested":` + strings.Repeat("[", 101) + "0" + strings.Repeat("]", 101) + "}"} {
		if validValues([]byte(text)) {
			t.Errorf("invalid values accepted: %s", text)
		}
	}
}

func TestNormalizedValuesRetainExactNumbers(t *testing.T) {
	raw := []byte(`{"emoji":"\ud83d\ude00","unsigned":18446744073709551615,"signed":-9223372036854775808,"decimal":1.234567890123456789,"exponent":1e999}`)
	if !validValues(raw) {
		t.Fatal("valid exact JSON refused")
	}
	normalized, err := normalizedValues(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range []string{`"emoji":"😀"`, `18446744073709551615`, `-9223372036854775808`, `1.234567890123456789`, `1e999`} {
		if !bytes.Contains(normalized, []byte(literal)) {
			t.Fatalf("normalization lost %s", literal)
		}
	}
}
