// Package telemetry is a service's own traces and metrics as OpenTelemetry describes them, and their OTLP/HTTP JSON
// encoding. It decides nothing about when a span starts or what it is called: the caller supplies identifiers and
// times, so every document here is a function of its arguments.
package telemetry

import (
	"encoding/hex"
	"strings"
	"time"
)

// TraceID names one trace; SpanID one span in it. All-zero values are invalid in OTLP and in a traceparent header.
type (
	TraceID [16]byte
	SpanID  [8]byte
)

func (t TraceID) String() string { return hex.EncodeToString(t[:]) }
func (s SpanID) String() string  { return hex.EncodeToString(s[:]) }

// IsZero is whether the identifier is the invalid all-zero one.
func (t TraceID) IsZero() bool { return t == TraceID{} }
func (s SpanID) IsZero() bool  { return s == SpanID{} }

// Parent is the span a new one continues: the trace it belongs to and the span it is a child of.
type Parent struct {
	Trace TraceID
	Span  SpanID
}

// ParseTraceparent reads a W3C traceparent header (version 00: `00-<trace>-<span>-<flags>`). It answers false for
// anything else, an all-zero identifier included, and the caller then starts a trace of its own.
func ParseTraceparent(header string) (Parent, bool) {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) != 4 || parts[0] != "00" || !decodeLower(make([]byte, 1), parts[3]) {
		return Parent{}, false
	}
	var parent Parent
	if !decodeLower(parent.Trace[:], parts[1]) || !decodeLower(parent.Span[:], parts[2]) {
		return Parent{}, false
	}
	return parent, !parent.Trace.IsZero() && !parent.Span.IsZero()
}

// decodeLower fills out from lowercase hex text of exactly its size, as the header requires.
func decodeLower(out []byte, text string) bool {
	if len(text) != 2*len(out) || strings.ToLower(text) != text {
		return false
	}
	n, err := hex.Decode(out, []byte(text))
	return err == nil && n == len(out)
}

// Attribute is one key and value. A value is a string, a bool, an int, an int64 or a float64; anything else is
// written as its text.
type Attribute struct {
	Key   string
	Value any
}

// Kind is a span's role, with OTLP's numbers.
type Kind int

const (
	Internal Kind = 1
	Server   Kind = 2
	Client   Kind = 3
)

// Span is one finished span. Parent is zero for a trace's root. Failed marks an error status, and Message says
// why in words that are safe to keep: never a token, a request's body or a response's.
type Span struct {
	Trace      TraceID
	ID         SpanID
	Parent     SpanID
	Name       string
	Kind       Kind
	Start, End time.Time
	Attributes []Attribute
	Failed     bool
	Message    string
}

// Source is who sends a document: the resource's attributes (service.name at least) and the instrumentation
// scope's name.
type Source struct {
	Resource []Attribute
	Scope    string
}
