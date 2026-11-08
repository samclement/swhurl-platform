package nats_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/adapters/nats/natstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// testSeed is the seed the scripted server knows: the pair the server matches against, and the nonce it signs.
var testSeed [32]byte

func init() { copy(testSeed[:], "a seed that is never a credential") }

// greeting is the INFO frame every client is greeted with, and its nonce is what a sign-in signs.
func greeting(t *testing.T) (frame []byte, nonce string) {
	t.Helper()
	frame = fixture(t, "info.json")
	var said struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(frame, &said); err != nil {
		t.Fatal(err)
	}
	return frame, said.Nonce
}

// fixture reads one raw frame the scripted server answers with.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// serve starts the scripted server on the loopback interface and is its address.
func serve(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go converse(t, conn)
		}
	}()
	return listener.Addr().String()
}

// open is the contract's store: one connection to the scripted server, signed in with the known seed.
func open(t *testing.T) nats.KV {
	t.Helper()
	store, err := nats.Open(context.Background(), "nats://"+serve(t), kernel.NewUserKey(testSeed).Seed,
		kernel.ConsoleInbox)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestContract is the real adapter against the raw frames: sign-in, a listing, a stored value, a clear, absence, the
// keys under a prefix and a closed connection.
func TestContract(t *testing.T) { natstest.Contract(t, open) }

// converse is one client's session: the INFO it is greeted with, the CONNECT it must prove, and one answer per
// request. Its bucket is its own, so the contract's sequence holds: nothing stored, then a value, then a clear.
func converse(t *testing.T, conn net.Conn) {
	defer conn.Close()
	hello, nonce := greeting(t)
	if _, err := conn.Write([]byte("INFO " + string(hello) + "\r\n")); err != nil {
		return
	}
	reader := bufio.NewReaderSize(conn, 1<<16)
	bucket := &bucket{t: t, cleared: map[string]bool{}}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		verb, rest, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch verb {
		case "CONNECT":
			if !signedIn(t, rest, nonce) {
				conn.Write([]byte(strings.TrimSpace(string(fixture(t, "sign-in-refused.txt"))) + "\r\n"))
				return
			}
		case "PING":
			conn.Write([]byte("PONG\r\n"))
		case "PUB", "HPUB":
			found, err := read(reader, verb, rest)
			if err != nil {
				return
			}
			if !strings.HasPrefix(found.reply, kernel.ConsoleInbox+".") {
				t.Errorf("a request without the console's inbox: %s", found.reply)
			}
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(bucket.reply(found)); err != nil {
				return
			}
		}
	}
}

// bucket is what the scripted server holds in one session: the subjects it stored, and which of them were cleared.
type bucket struct {
	t       *testing.T
	cleared map[string]bool
}

// reply is the one answer a request gets, as a raw MSG or HMSG frame.
func (b *bucket) reply(found request) []byte {
	switch {
	case strings.HasPrefix(found.subject, "$JS.API.STREAM.INFO."):
		prefix := strings.TrimSuffix(requested(b.t, found.body, "subjects_filter"), "*")
		return msgFrame(found.reply, b.listing(prefix))
	case strings.HasPrefix(found.subject, "$JS.API.STREAM.MSG.GET."):
		return msgFrame(found.reply, b.read(requested(b.t, found.body, "last_by_subj")))
	case strings.HasPrefix(found.subject, "$KV."):
		b.write(found)
		return msgFrame(found.reply, fixture(b.t, "ack.json"))
	}
	return hmsgFrame(found.reply, []byte("NATS/1.0 503\r\n\r\n"))
}

// write stores one value, remembering a clear by the operation header the frame carries.
func (b *bucket) write(found request) {
	b.cleared[found.subject] = strings.Contains(string(found.head), "KV-Operation: DEL")
}

// read is the newest message at a key: the stored value's own frame, a clear's, or the 404 a missing key gets.
func (b *bucket) read(key string) []byte {
	switch operation, stored := b.cleared[key]; {
	case !stored:
		return fixture(b.t, "absent.json")
	case operation:
		return fixture(b.t, "cleared.json")
	}
	return fixture(b.t, "msg.json")
}

// listing is the stream's answer: the fixture's own fields, with the subjects this bucket holds under a prefix.
func (b *bucket) listing(prefix string) []byte {
	var answer map[string]any
	if err := json.Unmarshal(fixture(b.t, "stream-info.json"), &answer); err != nil {
		b.t.Fatal(err)
	}
	subjects := map[string]int{}
	for key := range b.cleared {
		if strings.HasPrefix(key, prefix) {
			subjects[key] = 1
		}
	}
	answer["state"].(map[string]any)["subjects"] = subjects
	encoded, err := json.Marshal(answer)
	if err != nil {
		b.t.Fatal(err)
	}
	return encoded
}

// signedIn is the CONNECT the server accepts: the known public key, and a signature over the nonce it sent.
func signedIn(t *testing.T, line, nonce string) bool {
	var connect struct{ NKey, Sig string }
	if err := json.Unmarshal([]byte(line), &connect); err != nil {
		t.Errorf("CONNECT: %v", err)
		return false
	}
	if connect.NKey != kernel.NewUserKey(testSeed).Public {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(connect.Sig)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.NewKeyFromSeed(testSeed[:]).Public().(ed25519.PublicKey), []byte(nonce), signature)
}

// request is one PUB or HPUB as the server reads it: its subject, its reply subject, its head and its body.
type request struct {
	verb, subject, reply string
	head, body           []byte
}

// read is the request whose first line was just read: the frame's size words name its body.
func read(reader *bufio.Reader, verb, rest string) (request, error) {
	words := strings.Fields(rest)
	need := 3
	if verb == "HPUB" {
		need = 4
	}
	if len(words) < need {
		return request{}, fmt.Errorf("a frame this server cannot read: %s %s", verb, rest)
	}
	headSize, size := 0, 0
	if verb == "HPUB" {
		headSize, _ = strconv.Atoi(words[2])
		size, _ = strconv.Atoi(words[3])
	} else {
		size, _ = strconv.Atoi(words[2])
	}
	framed := make([]byte, size+2)
	if _, err := io.ReadFull(reader, framed); err != nil {
		return request{}, err
	}
	return request{verb: verb, subject: words[0], reply: words[1], head: framed[:headSize], body: framed[headSize:size]}, nil
}

// requested is one field of a JetStream request body.
func requested(t *testing.T, payload []byte, field string) string {
	t.Helper()
	var body text
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Errorf("a request body: %v", err)
	}
	return body[field]
}

// text is a JetStream request's flat body.
type text map[string]string

// msgFrame is one answer without a head, delivered to the inbox: the server sends MSG subject sid size, never the
// reply-to a publisher set.
func msgFrame(reply string, body []byte) []byte {
	return []byte(fmt.Sprintf("MSG %s 1 %d\r\n%s\r\n", reply, len(body), body))
}

// hmsgFrame is one answer whose status line is its whole head.
func hmsgFrame(reply string, head []byte) []byte {
	return []byte(fmt.Sprintf("HMSG %s 1 %d %d\r\n%s\r\n", reply, len(head), len(head), head))
}

// A seed the server does not know is refused, and the refusal says so: a failed sign-in is never an empty store.
func TestOpenRefusesAStoreThatRefusesTheSignIn(t *testing.T) {
	server := serve(t)
	other := [32]byte{}
	other[0] = 9
	if _, err := nats.Open(context.Background(), "nats://"+server, kernel.NewUserKey(other).Seed,
		kernel.ConsoleInbox); err == nil || !strings.Contains(err.Error(), "refused the sign-in") {
		t.Fatalf("a refused sign-in: %v", err)
	}
	if _, err := nats.Open(context.Background(), "nats://"+server, "not-a-seed", kernel.ConsoleInbox); err == nil {
		t.Fatal("a value that is not a seed was accepted")
	}
	if _, err := nats.Open(context.Background(), "nats://127.0.0.1:1", kernel.NewUserKey(testSeed).Seed,
		kernel.ConsoleInbox); err == nil || !strings.Contains(err.Error(), "cannot reach NATS at") {
		t.Fatalf("a store that is not there: %v", err)
	}
}

// A server that stays silent times out, and does not answer with absence.
func TestASilentServerTimesOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var held sync.WaitGroup
	held.Add(1)
	go func() {
		defer held.Done()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		time.Sleep(time.Second) // longer than the deadline the caller states
		conn.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = nats.Open(ctx, "nats://"+listener.Addr().String(), kernel.NewUserKey(testSeed).Seed, kernel.ConsoleInbox)
	if err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Fatalf("a silent server: %v", err)
	}
	held.Wait()
}
