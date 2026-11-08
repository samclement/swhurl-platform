package producer

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
)

func worker(extra ...Field) Object {
	return append(Object{{"version", int64(1)}, {"kind", "worker"}}, extra...)
}
func TestSourceDefaultsAndNoCreationRules(t *testing.T) {
	for _, o := range []Object{worker(), worker(Field{"uid", int64(0)}, Field{"telemetry", nil}, Field{"autoDeploy", false}), {{"version", int64(1)}, {"kind", "web"}, {"uid", int64(-2)}, {"port", int64(-1)}, {"healthPath", "relative"}}} {
		s, err := Parse(o)
		if err != nil {
			t.Fatal(err)
		}
		if s.MemoryMiB != "128" {
			t.Fatal(s)
		}
		if o.Get("kind") == "worker" && s.Runtime.Get("uid") != int64(65532) {
			t.Fatal("zero/absent uid defaults")
		}
	}
	for _, q := range []struct {
		q    string
		want int64
	}{{"192Mi", 192}, {"1536Ki", 2}, {"512Ki", 1}, {"2Gi", 2048}} {
		n, err := mebibytes(q.q)
		if err != nil || string(n) != strconv.FormatInt(q.want, 10) {
			t.Fatalf("%s = %s, %v", q.q, n, err)
		}
	}
	s, err := Parse(worker(Field{"resources", Object{{"memoryLimit", "384Mi"}}}))
	if err != nil || s.MemoryMiB != "128" {
		t.Fatal(s, err)
	}
}
func TestSourceRefusals(t *testing.T) {
	for _, f := range []Field{{"colour", "blue"}, {"kind", "daemon"}, {"uid", true}, {"port", int64(80)}, {"healthPath", "/health"}, {"stack", false}, {"description", "two  spaces"}, {"description", strings.Repeat("x", 201)}, {"autoDeploy", "true"}, {"telemetry", "zipkin"}, {"databaseSize", "1Gi"}, {"database", "sqlite"}, {"secrets", []any{"SWHURL_APP"}}, {"secrets", []any{"HOST_IP"}}, {"secrets", []any{"NODE_USE_ENV_PROXY"}}, {"secrets", []any{"A", "A"}}, {"secrets", []any{int64(1)}}, {"secrets", []any{"bad-key"}}, {"team", "demo"}, {"exposure", "private"}, {"calls", []any{}}, {"alerts", false}, {"resources", Object{{"extra", "1Mi"}}}, {"resources", Object{{"memory", int64(128)}}}, {"resources", Object{{"cpu", "-1"}}}, {"provides", Object{{"eventsFile", "../escape"}}}, {"provides", Object{{"eventsFile", "a/../../b"}}}, {"provides", Object{{"api", "/api"}}}, {"events", Object{{"publish", []any{}}}}, {"events", Object{{"subscribe", []any{}}}}, {"events", Object{{"other", nil}}}} {
		t.Run(scalarText(f.Name)+"="+encode(f.Value, false, 0), func(t *testing.T) {
			o := worker()
			if f.Name == "kind" {
				o[1] = f
			} else {
				o = append(o, f)
			}
			_, err := Parse(o)
			if err == nil || !strings.Contains(err.Error(), "swhurl.yaml") {
				t.Fatalf("must refuse %v: %v", f, err)
			}
		})
	}
	for _, o := range []Object{nil, {}, {{"version", int64(2)}, {"kind", "worker"}}, {{"version", int64(1)}, {"kind", "web"}}} {
		if _, err := Parse(o); err == nil {
			t.Fatalf("accepted %#v", o)
		}
	}
}
func TestFlagsRules(t *testing.T) {
	cases := []Object{
		{{"type", "boolean"}, {"default", false}}, {{"type", "integer"}, {"default", int64(-2147483648)}, {"max", int64(2147483647)}}, {{"type", "number"}, {"default", 1.5}, {"min", 1.0}, {"max", 2.0}, {"staging", int64(2)}}, {{"type", "string"}, {"default", "tea"}, {"enum", []any{"tea", "coffee"}}, {"prod", "coffee"}},
	}
	for _, flag := range cases {
		if _, err := Parse(worker(Field{"flags", Object{{"sample", flag}}})); err != nil {
			t.Fatal(flag, err)
		}
	}
	for _, flag := range []Object{{{"type", "unknown"}, {"default", false}}, {{"type", "boolean"}}, {{"type", "boolean"}, {"default", int64(0)}}, {{"type", "integer"}, {"default", true}}, {{"type", "integer"}, {"default", int64(2147483648)}}, {{"type", "number"}, {"default", math.Inf(1)}}, {{"type", "integer"}, {"default", int64(2)}, {"min", 1.5}}, {{"type", "integer"}, {"default", int64(2)}, {"min", int64(3)}}, {{"type", "number"}, {"default", 2.0}, {"min", 3.0}, {"max", 1.0}}, {{"type", "number"}, {"default", 1.0}, {"prod", false}}, {{"type", "string"}, {"default", "tea"}, {"enum", []any{"tea", "tea"}}}, {{"type", "string"}, {"default", "tea"}, {"enum", []any{"coffee"}}}, {{"type", "string"}, {"default", "tea"}, {"description", nil}}, {{"type", "boolean"}, {"default", true}, {"other", false}}} {
		if _, err := Parse(worker(Field{"flags", Object{{"sample", flag}}})); err == nil {
			t.Errorf("accepted %#v", flag)
		}
	}
	for _, name := range []string{"BadName", "too--many", "a-1", "", "a" + strings.Repeat("b", 40)} {
		if err := (flagRules{"swhurl.yaml"}).flags(Object{{name, cases[0]}}); err == nil {
			t.Error(name)
		}
	}
}
func TestLabelCapCountsUTF8Bytes(t *testing.T) {
	s, _ := Parse(worker())
	base, _ := Label(s, nil, nil)
	overhead := len(strings.TrimPrefix(base, LabelKey+"=")) + len(`,"description":""`)
	s.Description = strings.Repeat("x", MaxLabelBytes-overhead)
	label, err := Label(s, nil, nil)
	if err != nil || len(strings.TrimPrefix(label, LabelKey+"=")) != MaxLabelBytes {
		t.Fatal(len(label), err)
	}
	s.Description += "x"
	if label, err := Label(s, nil, nil); err == nil || label != "" {
		t.Fatal("over cap accepted")
	}
	s.Description = strings.Repeat("é", (MaxLabelBytes-overhead)/2+1)
	if label, err := Label(s, nil, nil); err == nil || label != "" {
		t.Fatal("counted characters instead of UTF-8 bytes")
	}
}
func TestCompactLineAndFlagsEscaping(t *testing.T) {
	s, _ := Parse(worker())
	s.Description = "é 😀 <>& \u2028"
	a, err := Label(s, nil, nil)
	b, _ := Label(s, nil, nil)
	if err != nil || a != b || strings.Contains(a, "\n") || strings.Contains(a, `\u2028`) || !strings.Contains(a, "é 😀 <>&") {
		t.Fatal(a, err)
	}
	got := string(FlagsDocument(Object{{"flag", Object{{"description", "\a\v\x7f\u0080😀\"\\\n"}, {"default", math.Copysign(0, -1)}}}}))
	want := "{\n  \"flag\": {\n    \"description\": \"\\u0007\\u000b\\u007f\\u0080\\ud83d\\ude00\\\"\\\\\\n\",\n    \"default\": -0.0\n  }\n}\n"
	if got != want {
		t.Fatalf("%q != %q", got, want)
	}
	for _, c := range []struct {
		n float64
		s string
	}{{1e-4, "0.0001"}, {1e-5, "1e-05"}, {1e15, "1000000000000000.0"}, {1e16, "1e+16"}, {1.0, "1.0"}, {math.Inf(1), "Infinity"}} {
		if pythonFloat(c.n) != c.s {
			t.Errorf("%g => %s", c.n, pythonFloat(c.n))
		}
	}
}

func TestMixedBoundsUseBinaryFloatNotDisplayDecimal(t *testing.T) {
	a := json.Number("100000000000000000000000")
	b := json.Number("99999999999999999999999")
	if atMost(a, 1e23) || atMost(b, 1e23) {
		t.Fatal("rounded display decimal was used instead of the binary float")
	}
	if !atMost(1e23, a) || !greater(b, 1e23) {
		t.Fatal("exact int/float ordering lost")
	}
}
