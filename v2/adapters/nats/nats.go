// Package nats is the event service's key-value store as a reader and a writer: it signs in with a user's seed over
// the official NATS client, which keeps the connection alive and reconnects after the server drops it.
//
// It is the protocol, and only the protocol. The subjects it is given arrive as strings — the bucket, the stream and
// each key are the kernel's names — so it knows no naming rule, no environment and no application. The value it
// stores is the caller's bytes: an override is JSON because the caller encodes it that way, not because this package
// parses it. The seed is a secret: it signs the server's nonce, derives the public key the server matches, and is
// never returned, printed or put into an error.
package nats

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
)

// Timeout bounds the first connection and each call when the caller's context names no deadline.
const Timeout = 5 * time.Second

// ErrAbsent is a key the store holds no live message for: never written, or cleared.
var ErrAbsent = errors.New("the store holds no such key")

// Message is one stored entry: the bytes, the headers it was written with (a clear is the DEL operation, which is
// why the headers matter) and the server's own time for it, as an RFC 3339 stamp with nanoseconds.
type Message struct {
	Value    []byte
	Headers  map[string]string
	StoredAt string
}

// KV is the key-value store's two reads and its one write. A stream is the bucket's stream name and a key is the
// subject a flag's override lives at; a prefix selects one instance's keys. It is safe for concurrent use.
type KV interface {
	// Keys is the subjects stored under a prefix, sorted.
	Keys(ctx context.Context, stream, prefix string) ([]string, error)
	// Get is the newest message at a key: ErrAbsent when there is none.
	Get(ctx context.Context, stream, key string) (Message, error)
	// Put publishes one value at a subject and waits for JetStream's acknowledgement.
	Put(ctx context.Context, subject string, value []byte, headers map[string]string) error
	// Close ends the connection.
	Close() error
}

// Open connects to the event service at url and signs in with seed, receiving its answers under inbox. The inbox is
// the caller's, because it is what the user's permissions allow. A store that cannot be reached or refuses the
// sign-in is an error, as reading nothing is not absence. Once open, the connection reconnects by itself after the
// server drops it; a call made while it is down fails at once and says why.
func Open(ctx context.Context, url, seed, inbox string) (KV, error) {
	pair, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		return nil, errors.New("the NATS seed is not a user seed")
	}
	public, err := pair.PublicKey()
	if err != nil {
		return nil, errors.New("the NATS seed is not a user seed")
	}
	timeout := Timeout
	if deadline, stated := ctx.Deadline(); stated {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return nil, errors.New("NATS did not answer in time")
	}
	conn, err := natsgo.Connect(url, natsgo.Name("swhurl-v2"), natsgo.Nkey(public, pair.Sign),
		natsgo.CustomInboxPrefix(inbox), natsgo.Timeout(timeout), natsgo.MaxReconnects(-1),
		natsgo.ReconnectBufSize(-1))
	if err != nil {
		return nil, fmt.Errorf("cannot reach NATS at %s: %s", url, err)
	}
	stream, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &connection{conn: conn, js: stream}, nil
}

// connection is one signed-in client; the library serialises what it sends, so concurrent calls are safe.
type connection struct {
	conn *natsgo.Conn
	js   jetstream.JetStream
}

// Keys is the JetStream stream's own answer for the subjects under a prefix.
func (c *connection) Keys(ctx context.Context, stream, prefix string) ([]string, error) {
	ctx, cancel, err := c.call(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	found, err := c.js.Stream(ctx, stream)
	if err != nil {
		return nil, answered(err, stream)
	}
	info, err := found.Info(ctx, jetstream.WithSubjectFilter(prefix+"*"))
	if err != nil {
		return nil, answered(err, stream)
	}
	keys := make([]string, 0, len(info.State.Subjects))
	for key := range info.State.Subjects {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

// Get is the newest message at a key, with its headers and the time the server stored it.
func (c *connection) Get(ctx context.Context, stream, key string) (Message, error) {
	ctx, cancel, err := c.call(ctx)
	if err != nil {
		return Message{}, err
	}
	defer cancel()
	found, err := c.js.Stream(ctx, stream)
	if err != nil {
		return Message{}, answered(err, stream)
	}
	stored, err := found.GetLastMsgForSubject(ctx, key)
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return Message{}, ErrAbsent
	}
	if err != nil {
		return Message{}, answered(err, stream)
	}
	headers := make(map[string]string, len(stored.Header))
	for name := range stored.Header {
		headers[name] = stored.Header.Get(name)
	}
	return Message{Value: stored.Data, Headers: headers, StoredAt: stored.Time.UTC().Format(time.RFC3339Nano)}, nil
}

// Put publishes one value at a subject and waits for the stream's acknowledgement: a store that did not take it says
// so.
func (c *connection) Put(ctx context.Context, subject string, value []byte, headers map[string]string) error {
	ctx, cancel, err := c.call(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	message := natsgo.NewMsg(subject)
	message.Data = value
	for name, header := range headers {
		message.Header.Set(name, header)
	}
	if _, err := c.js.PublishMsg(ctx, message); err != nil {
		if errors.Is(err, jetstream.ErrNoStreamResponse) {
			return fmt.Errorf("nothing answers %s (is the bucket there?)", subject)
		}
		return fmt.Errorf("NATS did not store it: %s", err)
	}
	return nil
}

// Close ends the connection.
func (c *connection) Close() error {
	c.conn.Close()
	return nil
}

// call is what every call shares: a connection that is up now, and the caller's deadline or Timeout.
func (c *connection) call(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if !c.conn.IsConnected() {
		reason := c.conn.Status().String()
		if last := c.conn.LastError(); last != nil {
			reason += ": " + last.Error()
		}
		return nil, nil, fmt.Errorf("NATS is not connected (%s)", reason)
	}
	if _, stated := ctx.Deadline(); stated {
		ctx, cancel := context.WithCancel(ctx)
		return ctx, cancel, nil
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	return ctx, cancel, nil
}

// answered is a JetStream API failure in the card's words: a missing stream is a missing bucket, and a server
// that let the deadline pass did not answer.
func answered(err error, stream string) error {
	switch {
	case errors.Is(err, jetstream.ErrStreamNotFound), errors.Is(err, natsgo.ErrNoResponders):
		return fmt.Errorf("nothing answers %s (is the bucket there?)", stream)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, natsgo.ErrTimeout):
		return errors.New("NATS did not answer in time")
	}
	return err
}
