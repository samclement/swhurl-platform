// Package nats is the event service's key-value store as a reader and a writer: it signs in with a user's seed and
// answers one request at a time.
//
// It is the protocol, and only the protocol. The subjects it is given arrive as strings — the bucket, the stream and
// each key are the kernel's names — so it knows no naming rule, no environment and no application. The value it
// stores is the caller's bytes: an override is JSON because the caller encodes it that way, not because this package
// parses it. The seed is a secret: it signs the server's nonce, derives the public key the server matches, and is
// never returned, printed or put into an error.
package nats

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Timeout bounds one connection's reads when the caller's context names no deadline.
const Timeout = 5 * time.Second

// maxFrame bounds one protocol line and one message: an answer is JSON of a few kilobytes, and a longer one is not
// an answer this client reads.
const maxFrame = 64 << 10

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
// subject a flag's override lives at; a prefix selects one instance's keys.
type KV interface {
	// Keys is the subjects stored under a prefix, in the server's answer.
	Keys(ctx context.Context, stream, prefix string) ([]string, error)
	// Get is the newest message at a key: ErrAbsent when there is none.
	Get(ctx context.Context, stream, key string) (Message, error)
	// Put publishes one value at a subject and waits for JetStream's acknowledgement.
	Put(ctx context.Context, subject string, value []byte, headers map[string]string) error
	// Close ends the connection.
	Close() error
}

// Open connects to the event service at url and signs in with seed, subscribing to inbox for the answers to its
// requests. The inbox is the caller's, because it is what the user's permissions allow; a store that cannot be
// reached or refuses the sign-in is an error, as reading nothing is not absence.
func Open(ctx context.Context, url, seed, inbox string) (KV, error) {
	key, err := kernel.DecodeUserSeed(seed)
	if err != nil {
		return nil, err
	}
	address := strings.TrimPrefix(url, "nats://")
	if !strings.Contains(address, ":") {
		address += ":4222"
	}
	dialer := net.Dialer{Timeout: Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("cannot reach NATS at %s: %s", url, err)
	}
	connection := &connection{conn: conn, reader: bufio.NewReaderSize(conn, maxFrame), inbox: inbox}
	if err := connection.signIn(ctx, key); err != nil {
		conn.Close()
		return nil, err
	}
	return connection, nil
}

// connection is one signed-in session: one reader, one inbox, one request at a time.
type connection struct {
	conn   net.Conn
	reader *bufio.Reader
	inbox  string
	next   int
}

// signIn answers the server's INFO with a signed CONNECT, subscribes to the inbox and waits for the server's PONG.
func (c *connection) signIn(ctx context.Context, seed [32]byte) error {
	nonce, err := c.hello(ctx)
	if err != nil {
		return err
	}
	signature := base64.RawURLEncoding.EncodeToString(kernel.Sign(seed, []byte(nonce)))
	connect, err := json.Marshal(map[string]any{
		"verbose": false, "pedantic": false, "headers": true, "no_responders": true, "protocol": 1,
		"name": "swhurl-v2", "nkey": kernel.NewUserKey(seed).Public, "sig": signature,
	})
	if err != nil {
		return err
	}
	if err := c.send(ctx, "CONNECT "+string(connect)+"\r\nSUB "+c.inbox+".* 1\r\nPING\r\n"); err != nil {
		return err
	}
	return c.await(ctx)
}

// hello reads the server's INFO and is the nonce it wants signed.
func (c *connection) hello(ctx context.Context) (string, error) {
	line, err := c.line(ctx)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, "INFO ") {
		return "", fmt.Errorf("NATS did not state its INFO: %.40s", line)
	}
	var info struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "INFO ")), &info); err != nil {
		return "", fmt.Errorf("NATS INFO was not JSON: %s", err)
	}
	return info.Nonce, nil
}

// await waits for the server's PONG, answering its pings, and is where a refused sign-in arrives.
func (c *connection) await(ctx context.Context) error {
	for {
		line, err := c.line(ctx)
		if err != nil {
			return err
		}
		switch {
		case line == "PONG":
			return nil
		case strings.HasPrefix(line, "-ERR"):
			return refused("NATS refused the sign-in", line)
		case line == "PING":
			if err := c.send(ctx, "PONG\r\n"); err != nil {
				return err
			}
		}
	}
}

// Keys is the JetStream stream's own answer for the subjects under a prefix.
func (c *connection) Keys(ctx context.Context, stream, prefix string) ([]string, error) {
	answer, err := c.request(ctx, "$JS.API.STREAM.INFO."+stream, mustJSON(map[string]string{"subjects_filter": prefix + "*"}))
	if err != nil {
		return nil, err
	}
	var reply struct {
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
		State struct {
			Subjects map[string]int `json:"subjects"`
		} `json:"state"`
	}
	if err := json.Unmarshal(answer, &reply); err != nil {
		return nil, fmt.Errorf("NATS answered the listing with something else: %s", err)
	}
	if reply.Error != nil {
		return nil, errors.New(reply.Error.Description)
	}
	keys := make([]string, 0, len(reply.State.Subjects))
	for key := range reply.State.Subjects {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

// Get is the newest message at a key, as JetStream's message GET returns it: base64 for the body and the headers,
// and the server's time for when it was stored.
func (c *connection) Get(ctx context.Context, stream, key string) (Message, error) {
	answer, err := c.request(ctx, "$JS.API.STREAM.MSG.GET."+stream, mustJSON(map[string]string{"last_by_subj": key}))
	if err != nil {
		return Message{}, err
	}
	var reply struct {
		Error   *wireError  `json:"error"`
		Message wireMessage `json:"message"`
	}
	if err := json.Unmarshal(answer, &reply); err != nil {
		return Message{}, fmt.Errorf("NATS answered the read with something else: %s", err)
	}
	if reply.Error != nil {
		if reply.Error.Code == 404 {
			return Message{}, ErrAbsent
		}
		return Message{}, errors.New(reply.Error.Description)
	}
	return reply.Message.decode()
}

// Put publishes one value at a subject and reads JetStream's acknowledgement: a store that did not take it says so.
func (c *connection) Put(ctx context.Context, subject string, value []byte, headers map[string]string) error {
	answer, err := c.request(ctx, subject, value, headers)
	if err != nil || len(answer) == 0 {
		return err
	}
	var reply struct {
		Error *wireError `json:"error"`
	}
	if err := json.Unmarshal(answer, &reply); err != nil {
		return nil // an empty acknowledgement is how a key-value write is accepted
	}
	if reply.Error != nil {
		return fmt.Errorf("NATS did not store it: %s", reply.Error.Description)
	}
	return nil
}

// Close ends the connection.
func (c *connection) Close() error { return c.conn.Close() }

// wireError is a JetStream API failure, as the server states it.
type wireError struct {
	Code        int    `json:"code"`
	Description string `json:"description"`
}

// wireMessage is one stored message in the API's own form: base64 for what is stored.
type wireMessage struct {
	Data  string `json:"data"`
	Hdrs  string `json:"hdrs"`
	Time  string `json:"time"`
	Topic string `json:"subject"`
}

// decode is a message's bytes, its headers and the server's time.
func (m wireMessage) decode() (Message, error) {
	value, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil {
		return Message{}, fmt.Errorf("NATS sent a value that is not base64: %s", err)
	}
	headers, err := decodeHeaders(m.Hdrs)
	if err != nil {
		return Message{}, err
	}
	return Message{Value: value, Headers: headers, StoredAt: m.Time}, nil
}

// decodeHeaders is a header block as the API returns it: base64 of a NATS/1.0 block, whose first line is the
// status and whose other lines are name: value.
func decodeHeaders(encoded string) (map[string]string, error) {
	headers := map[string]string{}
	if encoded == "" {
		return headers, nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("NATS sent headers that are not base64: %s", err)
	}
	lines := strings.Split(string(raw), "\r\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "NATS/1.0") {
		return nil, errors.New("NATS sent a header block this client cannot read")
	}
	for _, line := range lines[1:] {
		if name, value, found := strings.Cut(line, ":"); found && strings.TrimSpace(name) != "" {
			headers[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return headers, nil
}

// request publishes with a reply subject and waits for the one answer that is this request's.
func (c *connection) request(ctx context.Context, subject string, payload []byte, headers ...map[string]string) ([]byte, error) {
	c.next++
	reply := c.inbox + "." + strconv.Itoa(c.next)
	var sent map[string]string
	if len(headers) > 0 {
		sent = headers[0]
	}
	if err := c.send(ctx, frame(subject, reply, payload, sent)); err != nil {
		return nil, err
	}
	for {
		line, err := c.line(ctx)
		if err != nil {
			return nil, err
		}
		body, arrived, err := c.handle(ctx, line, reply, subject)
		if err != nil {
			return nil, err
		}
		if arrived {
			return body, nil
		}
	}
}

// handle answers one line read while waiting for a reply: whether it is this request's answer, and what it says.
func (c *connection) handle(ctx context.Context, line, reply, subject string) ([]byte, bool, error) {
	switch {
	case line == "PING":
		return nil, false, c.send(ctx, "PONG\r\n")
	case strings.HasPrefix(line, "-ERR"):
		return nil, false, refused("NATS refused it", line)
	case strings.HasPrefix(line, "MSG "), strings.HasPrefix(line, "HMSG "):
		message, err := c.message(ctx, line)
		if err != nil {
			return nil, false, err
		}
		if message.subject != reply {
			return nil, false, nil // this connection asks one thing at a time; another answer is not this one
		}
		switch {
		case strings.HasPrefix(message.status, "503"):
			return nil, false, fmt.Errorf("nothing answers %s (is the bucket there?)", subject)
		case message.status != "" && !strings.HasPrefix(message.status, "2"):
			return nil, false, fmt.Errorf("NATS answered %s with status %s", subject, message.status)
		}
		return message.payload, true, nil
	}
	return nil, false, nil
}

// answer is one MSG or HMSG frame: the subject it was delivered to, its status line and its payload.
type answer struct {
	subject, status string
	payload         []byte
}

// message reads the frame whose first line was just read: the header block of an HMSG, then the body.
func (c *connection) message(ctx context.Context, line string) (answer, error) {
	subject, headSize, size, err := frameSizes(strings.Fields(line))
	if err != nil {
		return answer{}, err
	}
	body, err := c.exactly(ctx, size)
	if err != nil {
		return answer{}, err
	}
	found := answer{subject: subject, payload: body[headSize:]}
	if headSize > 0 {
		found.status = statusOf(body[:headSize])
	}
	return found, nil
}

// frameSizes is a frame's own subject and its two sizes. A frame is MSG subject sid size, or HMSG subject sid
// headerSize size: the reply-to a publisher may have set is not read, because this client asks one thing at a time
// and recognises its answer by the subject the server delivered it to.
func frameSizes(parts []string) (string, int, int, error) {
	headered := len(parts) > 0 && parts[0] == "HMSG"
	wanted := 4
	if headered {
		wanted = 5
	}
	if len(parts) != wanted {
		return "", 0, 0, fmt.Errorf("NATS sent a frame this client cannot read: %.80s", strings.Join(parts, " "))
	}
	head := 0
	if headered {
		size, err := strconv.Atoi(parts[3])
		if err != nil {
			return "", 0, 0, fmt.Errorf("NATS sent a header size that is not a number: %s", parts[3])
		}
		head = size
	}
	size, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || size < head || size > maxFrame {
		return "", 0, 0, fmt.Errorf("NATS sent a message size this client will not read: %s", parts[len(parts)-1])
	}
	return parts[1], head, size, nil
}

// statusOf is an HMSG head's status line, without the protocol's own prefix.
func statusOf(head []byte) string {
	first := strings.SplitN(string(head), "\r\n", 2)[0]
	return strings.TrimSpace(strings.TrimPrefix(first, "NATS/1.0"))
}

// send writes one frame, with the deadline its context names.
func (c *connection) send(ctx context.Context, data string) error {
	deadline, err := c.deadline(ctx)
	if err != nil {
		return err
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if _, err := io.WriteString(c.conn, data); err != nil {
		return fmt.Errorf("NATS closed the connection: %s", err)
	}
	return nil
}

// line is one protocol line, without its CRLF.
func (c *connection) line(ctx context.Context) (string, error) {
	deadline, err := c.deadline(ctx)
	if err != nil {
		return "", err
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return "", err
	}
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return "", readError(err)
	}
	if len(line) > maxFrame {
		return "", errors.New("NATS sent a line this client will not read")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// exactly reads a frame's body and the CRLF after it.
func (c *connection) exactly(ctx context.Context, size int) ([]byte, error) {
	deadline, err := c.deadline(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	body := make([]byte, size+2)
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return nil, readError(err)
	}
	if string(body[size:]) != "\r\n" {
		return nil, errors.New("NATS sent a message without its terminator")
	}
	return body[:size], nil
}

// deadline is the deadline one read or write has: the caller's, or the default when it named none.
func (c *connection) deadline(ctx context.Context) (time.Time, error) {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline, nil
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Now().Add(Timeout), nil
}

// refused is the server's own refusal, with the line's quotes taken off.
func refused(prefix, line string) error {
	return fmt.Errorf("%s: %s", prefix, strings.Trim(strings.TrimSpace(line[4:]), "'"))
}

// readError says what a failed read means: a server that stayed silent is not a server that answered nothing.
func readError(err error) error {
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return errors.New("NATS did not answer in time")
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("NATS closed the connection")
	}
	return err
}

// frame is one PUB or HPUB, as the protocol writes it.
func frame(subject, reply string, payload []byte, headers map[string]string) string {
	if len(headers) == 0 {
		return fmt.Sprintf("PUB %s %s %d\r\n%s\r\n", subject, reply, len(payload), payload)
	}
	head := "NATS/1.0\r\n"
	for _, name := range headerNames(headers) {
		head += name + ": " + headers[name] + "\r\n"
	}
	head += "\r\n"
	return fmt.Sprintf("HPUB %s %s %d %d\r\n%s%s\r\n", subject, reply, len(head), len(head)+len(payload), head, payload)
}

// headerNames is the headers in a fixed order, so one write gives one frame.
func headerNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// mustJSON marshals a request's tiny body; the values are plain strings, which cannot fail.
func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}
