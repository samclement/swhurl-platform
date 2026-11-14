package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// jsonLog writes one JSON line per event, in the console's shape: time, level, logger and message, then whichever
// fields the event adds. The collector's JSON parser reads each field as a log attribute of the same name, so a line
// is searchable and countable without a second parser.
type jsonLog struct {
	name string
	lock sync.Mutex
	out  io.Writer
	now  func() time.Time
}

// logField is one field of a line, after the message.
type logField struct {
	name  string
	value any
}

// write is one line with nothing but its message.
func (l *jsonLog) write(level, message string) {
	l.writeFields(level, message)
}

// writeFields is one line with the fields the event adds, in the order given.
func (l *jsonLog) writeFields(level, message string, fields ...logField) {
	var line bytes.Buffer
	line.WriteByte('{')
	writeField(&line, "time", l.now().UTC().Format("2006-01-02T15:04:05.000000-07:00"))
	writeField(&line, "level", level)
	writeField(&line, "logger", l.name)
	writeField(&line, "message", message)
	for _, field := range fields {
		writeField(&line, field.name, field.value)
	}
	line.WriteString("}\n")
	l.lock.Lock()
	defer l.lock.Unlock()
	_, _ = l.out.Write(line.Bytes())
}

// writeField writes one name and value, separated from what came before it.
func writeField(out *bytes.Buffer, name string, value any) {
	if out.Len() > 1 {
		out.WriteByte(',')
	}
	out.Write(compactJSON(name))
	out.WriteByte(':')
	out.Write(compactJSON(value))
}

// compactJSON is a document as compact JSON without HTML escaping.
func compactJSON(document any) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(document) // maps of strings and string fields always encode
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}
