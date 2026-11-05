package producer

import (
	"encoding/json"
	"math"
	"math/big"
	"slices"
	"strconv"
)

func flags(o Object) error {
	for _, f := range o {
		name, ok := f.Name.(string)
		if !ok || len(name) > 40 || !matches(`[a-z][a-z0-9]*(-[a-z][a-z0-9]*)*`, name) {
			return issue("flags."+scalarText(f.Name), "name must be kebab case, at most 40 characters")
		}
		flag, ok := f.Value.(Object)
		if !ok {
			return issue("flags."+scalarText(f.Name), "must be a mapping with type and default")
		}
		if err := flagShape(name, flag); err != nil {
			return err
		}
		if err := flagValues(name, flag); err != nil {
			return err
		}
	}
	return nil
}
func flagShape(name string, o Object) error {
	kind := text(o.Get("type"))
	if !slices.Contains([]string{"boolean", "integer", "number", "string"}, kind) {
		return issue("flags."+name+".type", "must be boolean, integer, number or string")
	}
	allowed := []string{"type", "description", "default", "staging", "prod"}
	if kind == "number" || kind == "integer" {
		allowed = append(allowed, "min", "max")
	}
	if kind == "string" {
		allowed = append(allowed, "enum")
	}
	for _, f := range o {
		if !slices.Contains(allowed, text(f.Name)) {
			return issue("flags."+name+"."+scalarText(f.Name), "unknown field")
		}
	}
	if o.has("description") {
		if _, ok := o.Get("description").(string); !ok {
			return issue("flags."+name+".description", "must be text")
		}
	}
	if !o.has("default") {
		return issue("flags."+name+".default", "required")
	}
	return flagBounds(name, o, kind)
}
func flagBounds(name string, o Object, kind string) error {
	for _, key := range []string{"min", "max"} {
		if o.has(key) && !bound(o.Get(key), kind) {
			return issue("flags."+name+"."+key, "wrong numeric type")
		}
	}
	if o.has("min") && o.has("max") && greater(o.Get("min"), o.Get("max")) {
		return issue("flags."+name+".min", "above its max")
	}
	return flagEnum(name, o.Get("enum"))
}
func flagEnum(name string, v any) error {
	if v == nil {
		return nil
	}
	values, ok := v.([]any)
	if !ok || len(values) == 0 {
		return issue("flags."+name+".enum", "must list distinct strings")
	}
	seen := map[string]bool{}
	for _, v := range values {
		s, ok := v.(string)
		if !ok || seen[s] {
			return issue("flags."+name+".enum", "must list distinct strings")
		}
		seen[s] = true
	}
	return nil
}
func bound(v any, kind string) bool {
	switch v.(type) {
	case int64, json.Number:
		return true
	case float64:
		return kind == "number"
	}
	return false
}
func number(v any) float64 {
	switch v := v.(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	case json.Number:
		n, _ := strconv.ParseFloat(string(v), 64)
		return n
	}
	return math.NaN()
}
func typed(v any, kind string) bool {
	switch kind {
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer":
		n, ok := v.(int64)
		return ok && n >= -2147483648 && n <= 2147483647
	case "number":
		return bound(v, kind) && !math.IsNaN(number(v)) && !math.IsInf(number(v), 0)
	case "string":
		_, ok := v.(string)
		return ok
	}
	return false
}
func flagValues(name string, o Object) error {
	kind := text(o.Get("type"))
	for _, key := range []string{"default", "staging", "prod"} {
		if !o.has(key) {
			continue
		}
		v := o.Get(key)
		if !typed(v, kind) {
			return issue("flags."+name+"."+key, "must be a "+kind)
		}
		if outsideBounds(o, v, kind) {
			return issue("flags."+name+"."+key, "outside min and max")
		}

		if enum, ok := o.Get("enum").([]any); ok && !slices.Contains(enum, v) {
			return issue("flags."+name+"."+key, "must be one of enum")
		}
	}
	return nil
}

func outsideBounds(o Object, v any, kind string) bool {
	if kind != "number" && kind != "integer" {
		return false
	}
	return (o.has("min") && !atMost(o.Get("min"), v)) || (o.has("max") && !atMost(v, o.Get("max")))
}

func greater(a, b any) bool { return !atMost(a, b) && !math.IsNaN(number(a)) && !math.IsNaN(number(b)) }

// Comparisons use the actual binary float, never its rounded display spelling.
func atMost(a, b any) bool {
	x, y := exactNumber(a), exactNumber(b)
	if x != nil && y != nil {
		return x.Cmp(y) <= 0
	}
	if math.IsNaN(number(a)) || math.IsNaN(number(b)) {
		return false
	}
	if n, ok := a.(float64); ok && math.IsInf(n, 0) {
		return n < 0 || positiveInfinity(b)
	}
	return positiveInfinity(b)
}
func exactNumber(v any) *big.Rat {
	switch v := v.(type) {
	case int64:
		return new(big.Rat).SetInt64(v)
	case json.Number:
		n, ok := new(big.Int).SetString(string(v), 10)
		if ok {
			return new(big.Rat).SetInt(n)
		}
	case float64:
		return new(big.Rat).SetFloat64(v)
	}
	return nil
}
func positiveInfinity(v any) bool { n, ok := v.(float64); return ok && math.IsInf(n, 1) }
