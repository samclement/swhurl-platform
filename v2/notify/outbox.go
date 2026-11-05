package notify

import (
	"bytes"
	"encoding/json"
	"errors"
)

// The outbox's bounds: how many copies wait at once, how large its document is, and how long a delivered or
// abandoned key is remembered and a failing copy retried.
const (
	MaxPending     = 500
	MaxOutboxBytes = 700_000
	History        = 7200 // seconds a delivered entry's key is remembered, so a repeated request is not sent twice
	ChannelRetry   = 7200 // seconds a copy that cannot be sent stays in the outbox
)

// ErrInvalidOutbox is an outbox document of another version or shape: it is refused, never reset, because resetting
// it would forget which keys were already delivered.
var ErrInvalidOutbox = errors.New("the outbox is invalid; refusing to reset delivery history")

// ErrOutboxTooLarge is an outbox that would exceed MaxOutboxBytes once encoded.
var ErrOutboxTooLarge = errors.New("the outbox exceeds its bounded size")

// State is the outbox: the keys already delivered or abandoned, each with when it may be forgotten, and the copies
// waiting to be sent, in order. Times are seconds since the epoch.
type State struct {
	Seen    map[string]float64
	Pending []Entry
}

// Entry is one copy of a notice for one channel, as the outbox document keeps it; its fields are in the order the
// Python service wrote them. History, when set, is how long its key is remembered instead of History.
type Entry struct {
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
	Key      string   `json:"key"`
	Channel  string   `json:"channel"`
	Queued   float64  `json:"queued"`
	App      string   `json:"app"`
	Env      string   `json:"env"`
	History  *float64 `json:"history,omitempty"`
}

// stateDocument is the outbox's document, version 1.
type stateDocument struct {
	Version json.RawMessage `json:"version"`
	Seen    json.RawMessage `json:"seen"`
	Pending json.RawMessage `json:"pending"`
}

// DecodeState reads the outbox document; an empty one is a new outbox. It refuses another version or a document
// whose seen keys or pending copies have another shape. A copy missing a field is read with that field empty.
func DecodeState(raw string) (State, error) {
	state := State{Seen: map[string]float64{}, Pending: []Entry{}}
	if raw == "" {
		return state, nil
	}
	var doc stateDocument
	var version float64
	if json.Unmarshal([]byte(raw), &doc) != nil || json.Unmarshal(doc.Version, &version) != nil || version != 1 {
		return State{}, ErrInvalidOutbox
	}
	var pending []json.RawMessage
	if !shaped(doc.Seen, '{', &state.Seen) || !shaped(doc.Pending, '[', &pending) {
		return State{}, ErrInvalidOutbox
	}
	for _, item := range pending {
		var entry Entry
		if !shaped(item, '{', &entry) {
			return State{}, ErrInvalidOutbox
		}
		state.Pending = append(state.Pending, entry)
	}
	return state, nil
}

// shaped decodes raw into target when it is a JSON value that opens with the given delimiter: an object or an array,
// never null.
func shaped(raw json.RawMessage, opens byte, target any) bool {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	return len(raw) > 0 && raw[0] == opens && json.Unmarshal(raw, target) == nil
}

// Encode is the outbox document, compact and with every non-ASCII character escaped, as the Python service wrote
// it; it refuses one over MaxOutboxBytes.
func (s State) Encode() (string, error) {
	doc := struct {
		Version int                `json:"version"`
		Seen    map[string]float64 `json:"seen"`
		Pending []Entry            `json:"pending"`
	}{1, s.Seen, s.Pending}
	if doc.Seen == nil {
		doc.Seen = map[string]float64{}
	}
	if doc.Pending == nil {
		doc.Pending = []Entry{}
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		return "", err
	}
	encoded := asciiJSON(bytes.TrimSuffix(out.Bytes(), []byte("\n")))
	if len(encoded) > MaxOutboxBytes {
		return "", ErrOutboxTooLarge
	}
	return string(encoded), nil
}

// Accept queues one copy of the request for each channel, keyed <request key>@<channel>, and is the channels it
// queued. A copy already pending or remembered under its key is not queued again; a request that would take the
// outbox over MaxPending is refused and changes nothing.
func (s *State) Accept(request Request, channels []string, now float64) ([]string, error) {
	queued := []string{}
	for _, channel := range channels {
		key := request.Key + "@" + channel
		if _, seen := s.Seen[key]; !seen && !s.pending(key) {
			queued = append(queued, channel)
		}
	}
	if len(s.Pending)+len(queued) > MaxPending {
		return nil, &Refusal{503, "the outbox is full; try again later"}
	}
	for _, channel := range queued {
		s.Pending = append(s.Pending, entryOf(request, channel, now))
	}
	return queued, nil
}

func (s *State) pending(key string) bool {
	for _, entry := range s.Pending {
		if entry.Key == key {
			return true
		}
	}
	return false
}

// entryOf is the copy of a request for one channel, queued at now.
func entryOf(request Request, channel string, now float64) Entry {
	priority, tags := 3, []string{"white_check_mark"}
	if request.Notice.High {
		priority, tags = 4, []string{"warning"}
	}
	return Entry{Title: request.Notice.Title, Message: request.Notice.Message, Priority: priority, Tags: tags,
		Click: request.Notice.Link, Key: request.Key + "@" + channel, Channel: channel, Queued: now,
		App: request.App, Env: request.Env}
}

// Expire forgets the keys whose time is up, before a delivery pass.
func (s *State) Expire(now float64) {
	for key, until := range s.Seen {
		if until <= now {
			delete(s.Seen, key)
		}
	}
}

// Take is the next copy to send, off the front of the queue.
func (s *State) Take() (Entry, bool) {
	if len(s.Pending) == 0 {
		return Entry{}, false
	}
	entry := s.Pending[0]
	s.Pending = s.Pending[1:]
	return entry, true
}

// Settle records what became of a copy Take gave: one that was sent, or has failed for ChannelRetry since it was
// queued, has its key remembered and is done; one that failed sooner is to be retried, and the caller puts it back
// with Requeue once the pass has offered every other copy.
func (s *State) Settle(entry Entry, sent bool, now float64) (retry bool) {
	if !sent && now-entry.Queued < ChannelRetry {
		return true
	}
	history := float64(History)
	if entry.History != nil {
		history = *entry.History
	}
	if s.Seen == nil {
		s.Seen = map[string]float64{}
	}
	s.Seen[entry.Key] = now + history
	return false
}

// Requeue puts copies that will be retried back at the end of the queue.
func (s *State) Requeue(entries []Entry) { s.Pending = append(s.Pending, entries...) }

// Publication is what ntfy is sent for one copy: the fields of its JSON publish API but the topic.
type Publication struct {
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click,omitempty"`
}

// Publication is the copy as ntfy is sent it: a copy of priority 4 or more is high, anything else normal.
func (e Entry) Publication() Publication {
	if e.Priority >= 4 {
		return Publication{e.Title, e.Message, 4, []string{"warning"}, e.Click}
	}
	return Publication{e.Title, e.Message, 3, []string{"white_check_mark"}, e.Click}
}

// Document is the body of ntfy's JSON publish API for one topic: the publication's fields, then the topic, as the
// Python service sent them.
func (p Publication) Document(topic string) ([]byte, error) {
	return json.Marshal(struct {
		Publication
		Topic string `json:"topic"`
	}{p, topic})
}
