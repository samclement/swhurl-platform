package nats_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/samclement/swhurl-platform/v2/adapters/nats"
	"github.com/samclement/swhurl-platform/v2/adapters/nats/natstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// The console signs in with one key and the test sets the bucket up with another, as the stream controller does.
var (
	console = kernel.NewUserKey([32]byte{1})
	admin   = kernel.NewUserKey([32]byte{2})
)

// broker is one embedded NATS server with JetStream: its address, and what it needs to start again where it stopped.
type broker struct {
	server *server.Server
	opts   *server.Options
}

// start runs a server whose users are the console, with the permissions the platform renders for it, and an
// administrator; a port of -1 is any free one.
func start(t *testing.T, port int, storeDir string) *broker {
	t.Helper()
	opts := &server.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: storeDir, NoLog: true,
		NoSigs: true, Nkeys: []*server.NkeyUser{consoleUser(t), {Nkey: admin.Public}}}
	running, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	running.Start()
	if !running.ReadyForConnections(5 * time.Second) {
		t.Fatal("the embedded NATS server did not start")
	}
	t.Cleanup(running.Shutdown)
	return &broker{server: running, opts: opts}
}

// url is where the broker listens.
func (b *broker) url() string { return b.server.ClientURL() }

// consoleUser is the console's entry in the user list the platform renders: the test signs in under the
// permissions production grants, so a subject the adapter needs and the list lacks fails here.
func consoleUser(t *testing.T) *server.NkeyUser {
	t.Helper()
	rendered, err := kernel.RenderUsers(kernel.NATSUsers{ControllerKey: admin.Public, ConsoleKey: console.Public})
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(rendered.Files[0].Document, &object); err != nil {
		t.Fatal(err)
	}
	var values struct {
		Config struct {
			Merge struct {
				Authorization struct {
					Users []struct {
						NKey        string `json:"nkey"`
						Permissions struct {
							Publish   struct{ Allow []string } `json:"publish"`
							Subscribe struct{ Allow []string } `json:"subscribe"`
						} `json:"permissions"`
					} `json:"users"`
				} `json:"authorization"`
			} `json:"merge"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(object.Data["values.yaml"]), &values); err != nil {
		t.Fatal(err)
	}
	for _, user := range values.Config.Merge.Authorization.Users {
		if user.NKey == console.Public {
			return &server.NkeyUser{Nkey: user.NKey, Permissions: &server.Permissions{
				Publish:   &server.SubjectPermission{Allow: user.Permissions.Publish.Allow},
				Subscribe: &server.SubjectPermission{Allow: user.Permissions.Subscribe.Allow}}}
		}
	}
	t.Fatal("the rendered user list has no console")
	return nil
}

// bucket creates staging's flag bucket as the administrator, as the stream controller does in the cluster, from
// testdata/bucket.json: the staging KeyValue's spec in platform/flags/buckets.yaml, in the API's own words.
func bucket(t *testing.T, url string) {
	t.Helper()
	pair, err := natsgoPair(admin)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := natsgo.Connect(url, pair)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(filepath.Join("testdata", "bucket.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config jetstream.KeyValueConfig
	if err := json.Unmarshal(document, &config); err != nil || config.Bucket != kernel.FlagsBucket("staging") {
		t.Fatalf("the bucket fixture: %+v, %v", config, err)
	}
	if _, err := stream.CreateKeyValue(context.Background(), config); err != nil {
		t.Fatal(err)
	}
}

// natsgoPair signs in as one kernel key: the seed the kernel mints is one nkeys reads.
func natsgoPair(key kernel.UserKey) (natsgo.Option, error) {
	pair, err := nkeys.FromSeed([]byte(key.Seed))
	if err != nil {
		return nil, err
	}
	return natsgo.Nkey(key.Public, pair.Sign), nil
}

// open is the contract's store: the console signed in to a fresh broker that holds staging's bucket.
func open(t *testing.T) nats.KV {
	t.Helper()
	running := start(t, -1, t.TempDir())
	bucket(t, running.url())
	return signIn(t, running.url())
}

// signIn is the console's store at url.
func signIn(t *testing.T, url string) nats.KV {
	t.Helper()
	store, err := nats.Open(context.Background(), url, console.Seed, kernel.ConsoleInbox)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestContract is the real adapter against a real server, under the console's rendered permissions: a stored value,
// a clear, absence, the keys under a prefix and a closed connection.
func TestContract(t *testing.T) { natstest.Contract(t, open) }

// A bucket the server does not hold is named as missing, never read as empty.
func TestAMissingBucketIsNotAnEmptyOne(t *testing.T) {
	store := open(t)
	if _, err := store.Keys(context.Background(), kernel.FlagsStream("prod"), kernel.FlagsPrefix("prod", "demo-shop")); err == nil ||
		!strings.Contains(err.Error(), "is the bucket there?") {
		t.Fatalf("a missing bucket: %v", err)
	}
}

// The store survives the server going away: while it is down a call fails at once and says so, and once it is back
// the same store reads what was stored before. A long-lived caller, the console, never holds a dead connection.
func TestTheStoreReconnectsAfterTheServerRestarts(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	running := start(t, port, dir)
	bucket(t, running.url())
	store := signIn(t, running.url())
	if err := store.Put(context.Background(), natstest.StoredKey, []byte("true"), nil); err != nil {
		t.Fatal(err)
	}
	running.server.Shutdown()
	running.server.WaitForShutdown()
	time.Sleep(200 * time.Millisecond) // the client reads the closed socket on its own goroutine
	began := time.Now()
	if _, err := store.Get(context.Background(), natstest.Stream, natstest.StoredKey); err == nil ||
		!strings.Contains(err.Error(), "NATS is not connected") || time.Since(began) > time.Second {
		t.Fatalf("a call while the server is down: %v after %s", err, time.Since(began))
	}
	start(t, port, dir)
	deadline := time.Now().Add(15 * time.Second)
	for {
		message, err := store.Get(context.Background(), natstest.Stream, natstest.StoredKey)
		if err == nil {
			if string(message.Value) != "true" {
				t.Fatalf("after the restart: %+v", message)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the store did not reconnect: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Requests from many handlers at once share one store, and each gets its own answer.
func TestConcurrentCallsGetTheirOwnAnswers(t *testing.T) {
	store := open(t)
	ctx := context.Background()
	for i := range 8 {
		if err := store.Put(ctx, natstest.Prefix+"flag-"+strconv.Itoa(i), []byte(strconv.Itoa(i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	var wait sync.WaitGroup
	failures := make(chan string, 64)
	for round := range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			i := strconv.Itoa(round % 8)
			message, err := store.Get(ctx, natstest.Stream, natstest.Prefix+"flag-"+i)
			if err != nil || string(message.Value) != i {
				failures <- "flag-" + i + ": " + string(message.Value)
			}
		}()
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

// A seed the server does not know is refused, and the refusal says so: a failed sign-in is never an empty store.
// A value that is not a seed is refused without repeating it.
func TestOpenRefusesWhatCannotSignIn(t *testing.T) {
	running := start(t, -1, t.TempDir())
	stranger := kernel.NewUserKey([32]byte{9})
	if _, err := nats.Open(context.Background(), running.url(), stranger.Seed, kernel.ConsoleInbox); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "authorization violation") {
		t.Fatalf("a refused sign-in: %v", err)
	}
	if _, err := nats.Open(context.Background(), running.url(), "SUnot-a-seed", kernel.ConsoleInbox); err == nil ||
		strings.Contains(err.Error(), "not-a-seed") {
		t.Fatalf("a value that is not a seed: %v", err)
	}
	if _, err := nats.Open(context.Background(), "nats://127.0.0.1:"+strconv.Itoa(freePort(t)), console.Seed,
		kernel.ConsoleInbox); err == nil || !strings.Contains(err.Error(), "cannot reach NATS at") {
		t.Fatalf("a store that is not there: %v", err)
	}
}

// A server that accepts the connection and stays silent times out within the caller's deadline.
func TestASilentServerTimesOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			time.Sleep(time.Second) // longer than the deadline the caller states
			conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err = nats.Open(ctx, "nats://"+listener.Addr().String(), console.Seed, kernel.ConsoleInbox)
	if err == nil || errors.Is(err, nats.ErrAbsent) || time.Since(began) > 900*time.Millisecond {
		t.Fatalf("a silent server: %v after %s", err, time.Since(began))
	}
}

// freePort is a loopback port nothing listens on now, so a restarted server can take the same one.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
