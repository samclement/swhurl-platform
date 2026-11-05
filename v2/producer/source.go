// Package producer owns the app-source contract, not deployment acceptance. In particular a job or an image
// needing secrets must describe itself truthfully even when the platform cannot run it.
package producer

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Field retains a typed YAML key and insertion order. Only source domain fields require string names;
// an events document may contain scalar keys of other types.
type Field struct {
	Name  any
	Value any
}

// Object is a source mapping. Values are Object, []any, string, bool, int64, json.Number, float64 or nil.
// json.Number retains integers outside int64 without rounding; mapping order is never reconstructed from a map.
// Standard YAML timestamp/binary scalars may also appear in unused document content; source fields are type checked.
type Object []Field

// Get returns a field's value, or nil for an absent field (source nulls have the same default semantics).
func (o Object) Get(name string) any {
	for _, f := range o {
		if f.Name == name {
			return f.Value
		}
	}
	return nil
}
func (o Object) has(name string) bool {
	for _, f := range o {
		if f.Name == name {
			return true
		}
	}
	return false
}
func object(v any) Object { o, _ := v.(Object); return o }
func text(v any) string   { s, _ := v.(string); return s }
func truth(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case int64:
		return v != 0
	case json.Number:
		return number(v) != 0
	case []byte:
		return len(v) > 0
	case float64:
		return v != 0
	case Object:
		return len(v) > 0
	case []any:
		return len(v) > 0
	}
	return true
}
func issue(field any, detail string) error { return fmt.Errorf("swhurl.yaml: %v: %s", field, detail) }
func matches(pattern, value string) bool {
	return regexp.MustCompile("^(?:" + pattern + ")$").MatchString(value)
}

// Source is a checked app declaration and the exact document paths it names. It is not a creation request.
type Source struct {
	Runtime                                   Object
	Telemetry                                 bool
	Database                                  string
	DatabaseSizeMiB, MemoryMiB                json.Number
	Secrets                                   []any
	Description, APIPath, APIFile, EventsFile string
	Flags                                     Object
}

// Parse checks the source contract without applying deployment/creation validation.
func Parse(o Object) (Source, error) {
	if err := shape(o); err != nil {
		return Source{}, err
	}
	kind := text(o.Get("kind"))
	if !slices.Contains([]string{"web", "worker", "job"}, kind) {
		return Source{}, issue("kind", "required: web, worker or job")
	}
	s := Source{Runtime: Object{{"kind", kind}, {"uid", integerDefault(o.Get("uid"), 65532)}}, Telemetry: o.Get("telemetry") == "otlp", MemoryMiB: "128", Description: text(o.Get("description")), Database: text(o.Get("database"))}
	if utf8.RuneCountInString(s.Description) > 200 || strings.Join(strings.FieldsFunc(s.Description, sourceSpace), " ") != s.Description {
		return Source{}, issue("description", "one line of at most 200 characters, without leading, trailing or repeated spaces")
	}
	if err := runtime(o, kind, &s); err != nil {
		return Source{}, err
	}
	if err := resources(o, &s); err != nil {
		return Source{}, err
	}
	if err := provides(object(o.Get("provides")), kind, &s); err != nil {
		return Source{}, err
	}
	s.Flags = object(o.Get("flags"))
	s.Secrets, _ = o.Get("secrets").([]any)
	if err := secrets(s.Secrets); err != nil {
		return Source{}, err
	}
	if err := sourceOffers(o, s); err != nil {
		return Source{}, err
	}
	return s, nil
}
func sourceOffers(o Object, s Source) error {
	if err := flags(s.Flags); err != nil {
		return err
	}
	return sourceEvents(object(o.Get("events")))
}

func integerDefault(v any, fallback int64) any {
	if n, ok := v.(int64); ok && n != 0 {
		return n
	}
	if n, ok := v.(json.Number); ok {
		return n
	}
	return fallback
}

func shape(o Object) error {
	if o == nil {
		return issue("document", "must be a mapping")
	}
	if !numericOne(o.Get("version")) {
		return issue("version", "must be 1")
	}
	allowed := strings.Fields("version stack kind description port healthPath uid telemetry resources database databaseSize secrets provides flags team exposure alerts calls events autoDeploy")
	for _, f := range o {
		if !slices.Contains(allowed, text(f.Name)) {
			return issue(f.Name, "unknown field")
		}
		if err := fieldType(f); err != nil {
			return err
		}
	}
	for _, name := range []string{"team", "exposure", "alerts", "calls"} {
		if o.Get(name) != nil {
			return issue(name, "a grant, set on the platform, not in this file; remove it")
		}
	}
	return nil
}
func numericOne(v any) bool {
	switch v := v.(type) {
	case int64:
		return v == 1
	case float64:
		return v == 1
	case bool:
		return v
	}
	return false
}
func fieldType(f Field) error {
	if f.Value == nil {
		return nil
	}
	valid := true
	switch f.Name {
	case "stack", "kind", "description", "healthPath", "telemetry", "database", "databaseSize":
		_, valid = f.Value.(string)
	case "port", "uid":
		valid = sourceInteger(f.Value)
	case "resources", "provides", "flags", "events":
		_, valid = f.Value.(Object)
	case "autoDeploy":
		_, valid = f.Value.(bool)
	case "secrets":
		_, valid = f.Value.([]any)
	}
	if !valid {
		return issue(f.Name, "wrong type")
	}
	return nil
}
func sourceInteger(v any) bool {
	switch v.(type) {
	case int64, json.Number:
		return true
	}
	return false
}

func runtime(o Object, kind string, s *Source) error {
	if t := o.Get("telemetry"); t != nil && t != "otlp" && t != "none" {
		return issue("telemetry", "must be otlp or none")
	}
	if kind == "web" {
		if text(o.Get("healthPath")) == "" {
			return issue("healthPath", "required for kind web")
		}
		s.Runtime = append(s.Runtime, Field{"port", integerDefault(o.Get("port"), 8080)}, Field{"healthPath", o.Get("healthPath")})
	} else if o.Get("port") != nil || o.Get("healthPath") != nil {
		return issue("port and healthPath", "apply to kind web only")
	}
	return database(o, kind, s)
}
func database(o Object, kind string, s *Source) error {
	if o.Get("database") != nil && s.Database == "" {
		return issue("database", "must be sqlite")
	}
	if s.Database != "" {
		if s.Database != "sqlite" {
			return issue("database", "must be sqlite")
		}
		if kind != "web" {
			return issue("database", "applies to kind web only")
		}
		size := text(o.Get("databaseSize"))
		if size == "" {
			size = "1Gi"
		}
		n, err := mebibytes(size)
		if err != nil {
			return err
		}
		s.DatabaseSizeMiB = n
	} else if o.Get("databaseSize") != nil {
		return issue("databaseSize", "needs database")
	}
	return nil
}

// Conversion is separate from resources' quantity grammar: databaseSize historically uses Python int
// on the prefix, admitting signs, whitespace and digit separators, even if deployment later refuses it.
func mebibytes(q string) (json.Number, error) {
	if len(q) < 2 {
		return "", issue("quantity", "expected Ki, Mi or Gi unit")
	}
	prefix := strings.Map(decimalRune, strings.TrimSpace(q[:len(q)-2]))
	if !matches(`[+-]?[0-9]+(_[0-9]+)*`, prefix) {
		return "", issue("quantity", "expected an integer prefix")
	}
	n, _ := new(big.Int).SetString(strings.ReplaceAll(prefix, "_", ""), 10)
	switch q[len(q)-2:] {
	case "Ki":
		n.Add(n, big.NewInt(1023))
		n.Div(n, big.NewInt(1024))
	case "Mi":
	case "Gi":
		n.Mul(n, big.NewInt(1024))
	default:
		return "", issue("quantity", "expected Ki, Mi or Gi unit")
	}
	return json.Number(n.String()), nil
}
func decimalRune(r rune) rune {
	for _, p := range unicode.Digit.R16 {
		if r >= rune(p.Lo) && r <= rune(p.Hi) && (r-rune(p.Lo))%rune(p.Stride) == 0 {
			return '0' + (r-rune(p.Lo))/rune(p.Stride)%10
		}
	}
	for _, p := range unicode.Digit.R32 {
		if r >= rune(p.Lo) && r <= rune(p.Hi) && (r-rune(p.Lo))%rune(p.Stride) == 0 {
			return '0' + (r-rune(p.Lo))/rune(p.Stride)%10
		}
	}
	return r
}

func resources(o Object, s *Source) error {
	for _, f := range object(o.Get("resources")) {
		if !slices.Contains([]string{"cpu", "memory", "memoryLimit"}, text(f.Name)) {
			return issue("resources", "may set cpu, memory, memoryLimit only")
		}
		if f.Value == nil {
			continue
		}
		pattern := `[\p{Nd}]+(Ki|Mi|Gi)`
		if f.Name == "cpu" {
			pattern = `[\p{Nd}]+(\.[\p{Nd}]+)?m?`
		}
		v, ok := f.Value.(string)
		if !ok || !matches(pattern, v) {
			return issue("resources."+scalarText(f.Name), "must be a quantity string")
		}
		if f.Name == "memory" {
			n, err := mebibytes(v)
			if err != nil {
				return err
			}
			s.MemoryMiB = n
		}
	}
	return nil
}
func provides(o Object, kind string, s *Source) error {
	if err := providedPaths(o, s); err != nil {
		return err
	}

	if (s.APIPath != "" || s.APIFile != "") && kind != "web" {
		return issue("provides.api", "applies to kind web only")
	}
	if (s.APIPath == "") != (s.APIFile == "") {
		return issue("provides.api and provides.apiFile", "stated together or not at all")
	}
	return nil
}
func providedPaths(o Object, s *Source) error {
	for _, f := range o {
		switch f.Name {
		case "api":
			v, ok := f.Value.(string)
			if !ok || !strings.HasPrefix(v, "/") {
				return issue("provides.api", "must be an absolute path")
			}
			s.APIPath = v
		case "apiFile", "eventsFile":
			if f.Value != nil {
				v, ok := f.Value.(string)
				if !ok || !matches(`[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*`, v) {
					return issue("provides."+scalarText(f.Name), "must name a file in the repository")
				}
				if f.Name == "apiFile" {
					s.APIFile = v
				} else {
					s.EventsFile = v
				}
			}
		default:
			return issue("provides."+scalarText(f.Name), "unknown field")
		}
	}
	return nil
}

func sourceEvents(o Object) error {
	for _, f := range o {
		if f.Name == "subscribe" && f.Value != nil {
			return issue("events.subscribe", "a grant, set on the platform; remove it")
		}
		if f.Name == "publish" {
			return issue("events.publish", "no longer supported; use provides.eventsFile")
		}
		if f.Name != "subscribe" {
			return issue("events", "use provides.eventsFile and asyncapi.yaml")
		}
	}
	return nil
}
func secrets(keys []any) error {
	seen := map[string]bool{}
	for _, v := range keys {
		k, ok := v.(string)
		if !ok || !matches(`[A-Z_][A-Z0-9_]*`, k) {
			return issue("secrets", "must be environment variable names (A-Z, 0-9, _)")
		}
		if seen[k] {
			return issue("secrets", "secret keys repeated: "+k)
		}
		seen[k] = true
		if slices.Contains([]string{"HOST_IP", "DATABASE_PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "NODE_USE_ENV_PROXY", "JAVA_TOOL_OPTIONS"}, k) || strings.HasPrefix(k, "OTEL_") || strings.HasPrefix(k, "SWHURL_") || strings.HasPrefix(k, "KUBERNETES_") {
			return issue("secrets", "reserved for the platform: "+k)
		}
	}
	return nil
}

func sourceSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }
