package producer

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Emitted is one event an AsyncAPI 3 document says its app sends: the channel's address, what the channel or its first
// message says about it, and the payload schema and first example its subscribers read.
type Emitted struct {
	Name, Description string
	Payload, Example  any // the schema and the example payload as the document writes them: an Object, []any, a scalar or nil
}

// EmittedEvents checks AsyncAPI 3 send operations and returns what they send, in channel order, not operation order.
// References have the source producer's one-hop semantics; receives are checked but never published.
func EmittedEvents(doc Object, name string) ([]Emitted, error) {
	fail := func(detail string) error { return fmt.Errorf("%s: %s", name, detail) }
	if !strings.HasPrefix(scalarText(doc.Get("asyncapi")), "3.") {
		return nil, fail("must be an AsyncAPI 3 document (asyncapi: 3.x)")
	}
	channels, err := mappingField(doc, "channels", name)
	if err != nil {
		return nil, err
	}
	operations, err := mappingField(doc, "operations", name)
	if err != nil {
		return nil, err
	}
	sends, err := sentChannels(doc, channels, operations, name)
	if err != nil {
		return nil, err
	}
	return emittedEvents(doc, channels, sends, name)
}

// Publications is the events an AsyncAPI 3 document says its app sends, by name: EmittedEvents' names, so what a
// document publishes and what a catalogue shows about it cannot disagree.
func Publications(doc Object, name string) ([]string, error) {
	events, err := EmittedEvents(doc, name)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return names, nil
}
func sentChannels(doc, channels, operations Object, name string) (map[int]bool, error) {
	sends := map[int]bool{}
	for _, f := range operations {
		key, send, err := operation(doc, channels, f, name)
		if err != nil {
			return nil, err
		}
		if send {
			sends[key] = true
		}
	}
	return sends, nil
}
func emittedEvents(doc, channels Object, sends map[int]bool, name string) ([]Emitted, error) {
	fail := func(detail string) error { return fmt.Errorf("%s: %s", name, detail) }
	found := []Emitted{}
	seen := map[string]bool{}
	for i, f := range channels {
		if !sends[i] {
			continue
		}
		channel, err := settled(doc, f.Value, name)
		if err != nil {
			return nil, err
		}
		event := scalarText(f.Name)
		if truth(channel.Get("address")) {
			event = scalarText(channel.Get("address"))
		}
		if !matches(`[a-z]([a-z0-9-]{0,38}[a-z0-9])?`, event) {
			return nil, fail("channel " + scalarText(f.Name) + " has invalid event address " + event)
		}
		if seen[event] {
			return nil, fail(event + " is described twice")
		}
		seen[event] = true
		described, err := emittedMessage(doc, channel, name)
		if err != nil {
			return nil, err
		}
		described.Name = event
		found = append(found, described)
	}
	return found, nil
}

// emittedMessage is what a channel and its first message say about an event: the channel's description or the
// message's summary, and the payload schema and first example a subscriber reads. A channel with no messages is
// described by itself alone, so a document may state a publication without a payload.
func emittedMessage(doc, channel Object, name string) (Emitted, error) {
	messages, err := mappingField(channel, "messages", name)
	if err != nil {
		return Emitted{}, err
	}
	found := Emitted{Description: spoken(channel, "description")}
	if len(messages) == 0 {
		return found, nil
	}
	message, err := settled(doc, messages[0].Value, name)
	if err != nil {
		return Emitted{}, err
	}
	if found.Payload, err = messagePayload(doc, message, name); err != nil {
		return Emitted{}, err
	}
	if found.Description == "" {
		found.Description = spoken(message, "summary")
	}
	found.Example = firstExample(message)
	return found, nil
}

// messagePayload is a message's payload schema, followed through its reference: nil without one.
func messagePayload(doc, message Object, name string) (any, error) {
	if !truth(message.Get("payload")) {
		return nil, nil
	}
	return settled(doc, message.Get("payload"), name)
}

// firstExample is the payload of a message's first example: nil without one.
func firstExample(message Object) any {
	examples, ok := message.Get("examples").([]any)
	if !ok || len(examples) == 0 {
		return nil
	}
	return object(examples[0]).Get("payload")
}

// spoken is what an object says about itself under one key, in Python's text: ” when it says nothing.
func spoken(o Object, key string) string {
	if !truth(o.Get(key)) {
		return ""
	}
	return scalarText(o.Get(key))
}
func scalarText(v any) string {
	switch v := v.(type) {
	case nil:
		return "None"
	case string:
		return v
	case json.Number:
		return string(v)
	case float64:
		return pythonFloat(v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(v)
	}
}
func mappingField(o Object, key, name string) (Object, error) {
	if !o.has(key) {
		return Object{}, nil
	}
	v, ok := o.Get(key).(Object)
	if !ok {
		return nil, fmt.Errorf("%s: %s must be a mapping", name, key)
	}
	return v, nil
}
func settled(doc Object, v any, name string) (Object, error) {
	o := object(v)
	ref, ok := o.Get("$ref").(string)
	if !ok {
		return o, nil
	}
	var found any = doc
	for _, key := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		found = object(found).Get(key)
	}
	target, ok := found.(Object)
	if !ok {
		return nil, fmt.Errorf("%s: %s points at nothing", name, ref)
	}
	return target, nil
}
func operation(doc, channels Object, f Field, name string) (int, bool, error) {
	fail := func(detail string) (int, bool, error) {
		return -1, false, fmt.Errorf("%s: operation %s %s", name, scalarText(f.Name), detail)
	}
	if _, ok := f.Value.(Object); !ok {
		return fail("must be a mapping")
	}
	o, err := settled(doc, f.Value, name)
	if err != nil {
		return -1, false, err
	}
	action := text(o.Get("action"))
	if action != "send" && action != "receive" {
		return fail("action must be send or receive")
	}
	ref := text(object(o.Get("channel")).Get("$ref"))
	// Reference strings can collide (for example 123 and "123"); the last channel wins,
	// just as Python's reference dictionary does, without coalescing the channel keys.
	for i := len(channels) - 1; i >= 0; i-- {
		c := channels[i]
		escaped := strings.ReplaceAll(strings.ReplaceAll(scalarText(c.Name), "~", "~0"), "/", "~1")
		if ref != "#/channels/"+escaped {
			continue
		}
		if _, ok := c.Value.(Object); !ok {
			return fail("channel must be a mapping")
		}
		_, err := settled(doc, c.Value, name)
		return i, action == "send", err
	}
	return fail("channel must reference an existing #/channels/ entry")
}
