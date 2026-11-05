package producer

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// MaxLabelBytes caps the encoded JSON in UTF-8 bytes, not characters; the label is a build argument.
const MaxLabelBytes = 120 * 1024

// LabelKey is the config-label key the deployment decoder reads.
const LabelKey = "platform.swhurl.com/declaration"

// Document is a named offered document's unchanged bytes, in api/events/flags order.
type Document struct {
	Role  string
	Bytes []byte
}

// FlagsDocument returns Python json.dumps(indent=2)'s bytes plus newline. They participate in release identity.
func FlagsDocument(flags Object) []byte { return []byte(encode(flags, true, 0) + "\n") }

// Label builds the compact deterministic label from a checked Source. Documents are encoded by the CLI-independent JSON encoder.
func Label(s Source, publications []string, documents []Document) (string, error) {
	o := Object{{"schemaVersion", int64(2)}, {"runtime", s.Runtime}}
	if s.Telemetry {
		o = append(o, Field{"telemetry", true})
	}
	if s.Database != "" {
		o = append(o, Field{"database", s.Database}, Field{"databaseSizeMiB", s.DatabaseSizeMiB})
	}
	if len(publications) > 0 {
		values := []any{}
		for _, p := range publications {
			values = append(values, p)
		}
		o = append(o, Field{"publishes", values})
	}
	if len(s.Secrets) > 0 {
		o = append(o, Field{"secrets", s.Secrets})
	}
	o = append(o, Field{"memoryMiB", s.MemoryMiB})
	if s.Description != "" {
		o = append(o, Field{"description", s.Description})
	}
	if len(documents) > 0 {
		if s.APIPath != "" {
			o = append(o, Field{"apiPath", s.APIPath})
		}
		o = append(o, Field{"documents", encodedDocuments(documents)})
	}
	encoded := encode(o, false, 0)
	if err := labelSize(encoded, documents); err != nil {
		return "", err
	}
	return LabelKey + "=" + encoded, nil
}

func labelSize(encoded string, documents []Document) error {
	if len(encoded) > MaxLabelBytes {
		sizes := []string{}
		for _, d := range documents {
			sizes = append(sizes, fmt.Sprintf("%s (%d bytes)", d.Role, len(d.Bytes)))
		}
		if len(sizes) == 0 {
			sizes = append(sizes, "no documents")
		}
		return fmt.Errorf("the declaration is %d bytes encoded, over the %d-byte cap a build argument allows: %s", len(encoded), MaxLabelBytes, strings.Join(sizes, ", "))
	}
	return nil
}

// encode implements the source's two JSON formats, retaining mappings' insertion order. Flags use ASCII
// escaping, spaces and Python's float representation; label JSON uses literal Unicode and no extra spaces.
func encode(v any, pretty bool, depth int) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		return quote(v, pretty)
	case bool:
		return strconv.FormatBool(v)
	case json.Number:
		return string(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return pythonFloat(v)
	case Object:
		return encodeObject(v, pretty, depth)
	case []any:
		return encodeArray(v, pretty, depth)
	}
	panic("unsupported producer value")
}
func encodeObject(o Object, pretty bool, depth int) string {
	if len(o) == 0 {
		return "{}"
	}
	parts := []string{}
	colon := ":"
	if pretty {
		colon = ": "
	}
	for _, f := range o {
		parts = append(parts, quote(f.Name.(string), pretty)+colon+encode(f.Value, pretty, depth+1))
	}
	return join(parts, "{", "}", pretty, depth)
}
func encodeArray(a []any, pretty bool, depth int) string {
	if len(a) == 0 {
		return "[]"
	}
	parts := []string{}
	for _, v := range a {
		parts = append(parts, encode(v, pretty, depth+1))
	}
	return join(parts, "[", "]", pretty, depth)
}
func join(parts []string, left, right string, pretty bool, depth int) string {
	if !pretty {
		return left + strings.Join(parts, ",") + right
	}
	indent := strings.Repeat("  ", depth+1)
	return left + "\n" + indent + strings.Join(parts, ",\n"+indent) + "\n" + strings.Repeat("  ", depth) + right
}
func quote(s string, ascii bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			quoteRune(&b, r, ascii)
		}
	}
	b.WriteByte('"')
	return b.String()
}
func quoteRune(b *strings.Builder, r rune, ascii bool) {
	if r < 32 || (ascii && r >= 127) {
		if r > 0xffff {
			a, z := 0xd800+((r-0x10000)>>10), 0xdc00+((r-0x10000)&0x3ff)
			b.WriteString(fmt.Sprintf(`\u%04x\u%04x`, a, z))
		} else {
			b.WriteString(fmt.Sprintf(`\u%04x`, r))
		}
	} else {
		b.WriteRune(r)
	}
}

func pythonFloat(n float64) string {
	s := strconv.FormatFloat(n, 'g', -1, 64)
	switch s {
	case "+Inf":
		return "Infinity"
	case "-Inf":
		return "-Infinity"
	case "NaN":
		return "NaN"
	}
	if i := strings.IndexByte(s, 'e'); i >= 0 {
		exponent, _ := strconv.Atoi(s[i+1:])
		if exponent >= -4 && exponent < 16 {
			s = strconv.FormatFloat(n, 'f', -1, 64)
		}
	}
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func encodedDocuments(documents []Document) Object {
	o := Object{}
	for _, d := range documents {
		o = append(o, Field{d.Role, base64.StdEncoding.EncodeToString(d.Bytes)})
	}
	return o
}
