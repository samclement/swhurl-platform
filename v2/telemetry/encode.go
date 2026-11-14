package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// OTLP/JSON differs from protobuf's usual JSON mapping in two ways this file follows: identifiers are hex, not
// base64, and enums are numbers. 64-bit integers are strings, as the mapping has them.
const (
	statusError = 2
	cumulative  = 2
)

// EncodeTraces is one OTLP/HTTP JSON trace export (`/v1/traces`) of the source's spans.
func EncodeTraces(source Source, spans []Span) ([]byte, error) {
	encoded := make([]jsonSpan, 0, len(spans))
	for _, span := range spans {
		if span.Trace.IsZero() || span.ID.IsZero() {
			return nil, fmt.Errorf("span %q has no identifier", span.Name)
		}
		s := jsonSpan{TraceID: span.Trace.String(), SpanID: span.ID.String(), Name: span.Name, Kind: int(span.Kind),
			Start: nanos(span.Start), End: nanos(span.End), Attributes: attributesOf(span.Attributes)}
		if !span.Parent.IsZero() {
			s.Parent = span.Parent.String()
		}
		if span.Failed {
			s.Status = &jsonStatus{Code: statusError, Message: span.Message}
		}
		encoded = append(encoded, s)
	}
	return encode(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": attributesOf(source.Resource)},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": source.Scope}, "spans": encoded}},
	}}})
}

// EncodeMetrics is one OTLP/HTTP JSON metric export (`/v1/metrics`): every sum monotonic and every histogram
// explicit-bucket, both cumulative from metrics.Start.
func EncodeMetrics(source Source, metrics Metrics) ([]byte, error) {
	start, at := nanos(metrics.Start), nanos(metrics.At)
	encoded := []any{}
	for _, sum := range metrics.Sums {
		encoded = append(encoded, sumOf(sum, start, at))
	}
	for _, histogram := range metrics.Histograms {
		h, err := histogramOf(histogram, start, at)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, h)
	}
	return encode(map[string]any{"resourceMetrics": []any{map[string]any{
		"resource":     map[string]any{"attributes": attributesOf(source.Resource)},
		"scopeMetrics": []any{map[string]any{"scope": map[string]any{"name": source.Scope}, "metrics": encoded}},
	}}})
}

func sumOf(sum Sum, start, at string) map[string]any {
	points := []any{}
	for _, p := range sum.Points {
		points = append(points, map[string]any{"attributes": attributesOf(p.Attributes),
			"startTimeUnixNano": start, "timeUnixNano": at, "asInt": strconv.FormatUint(p.Value, 10)})
	}
	return map[string]any{"name": sum.Name, "unit": sum.Unit, "description": sum.Description,
		"sum": map[string]any{"dataPoints": points, "aggregationTemporality": cumulative, "isMonotonic": true}}
}

func histogramOf(histogram Distribution, start, at string) (map[string]any, error) {
	points, bounds := []any{}, histogram.Bounds
	if bounds == nil {
		bounds = []float64{}
	}
	for _, p := range histogram.Points {
		if math.IsNaN(p.Sum) || math.IsInf(p.Sum, 0) {
			return nil, fmt.Errorf("histogram %q has a sum JSON cannot carry", histogram.Name)
		}
		points = append(points, map[string]any{"attributes": attributesOf(p.Attributes),
			"startTimeUnixNano": start, "timeUnixNano": at, "count": strconv.FormatUint(p.Count, 10),
			"sum": p.Sum, "bucketCounts": uintTexts(p.Buckets), "explicitBounds": bounds})
	}
	return map[string]any{"name": histogram.Name, "unit": histogram.Unit, "description": histogram.Description,
		"histogram": map[string]any{"dataPoints": points, "aggregationTemporality": cumulative}}, nil
}

type jsonSpan struct {
	TraceID    string          `json:"traceId"`
	SpanID     string          `json:"spanId"`
	Parent     string          `json:"parentSpanId,omitempty"`
	Name       string          `json:"name"`
	Kind       int             `json:"kind"`
	Start      string          `json:"startTimeUnixNano"`
	End        string          `json:"endTimeUnixNano"`
	Attributes []jsonAttribute `json:"attributes"`
	Status     *jsonStatus     `json:"status,omitempty"`
}

type jsonStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type jsonAttribute struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

// attributesOf is the attributes as OTLP's typed values; never null, so an empty list is `[]`.
func attributesOf(attributes []Attribute) []jsonAttribute {
	encoded := make([]jsonAttribute, 0, len(attributes))
	for _, attribute := range attributes {
		encoded = append(encoded, jsonAttribute{attribute.Key, typedValue(attribute.Value)})
	}
	return encoded
}

func typedValue(value any) map[string]any {
	switch v := value.(type) {
	case bool:
		return map[string]any{"boolValue": v}
	case int:
		return map[string]any{"intValue": strconv.Itoa(v)}
	case int64:
		return map[string]any{"intValue": strconv.FormatInt(v, 10)}
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return map[string]any{"doubleValue": v}
		}
	}
	return map[string]any{"stringValue": valueText(value)}
}

// valueText is a value as the text a string attribute carries.
func valueText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func uintTexts(values []uint64) []string {
	texts := make([]string, 0, len(values))
	for _, value := range values {
		texts = append(texts, strconv.FormatUint(value, 10))
	}
	return texts
}

// nanos is a time as OTLP's fixed64 nanoseconds since the Unix epoch, as text; the zero time is 0.
func nanos(at time.Time) string {
	if at.IsZero() {
		return "0"
	}
	return strconv.FormatInt(at.UnixNano(), 10)
}

func encode(document any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
