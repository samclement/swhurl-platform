package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy/ntfytest"
	"github.com/samclement/swhurl-platform/v2/notify"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// notifyAPI returns Kubernetes wire documents, including the whole patched ConfigMap, not just a status.
// The unrelated key must survive every merge patch.
type notifyAPI struct {
	lock             sync.Mutex
	state            string
	maxPatchResponse int
	patches          int
}

func (a *notifyAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.lock.Lock()
	defer a.lock.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/apis/authentication.k8s.io/v1/tokenreviews":
		_, _ = io.WriteString(w, `{"status":{"authenticated":true,"audiences":["notify"],"user":{"username":"system:serviceaccount:console:notification-check"}}}`)
	case "/api/v1/namespaces/orders-staging":
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"orders-staging","labels":{"platform.swhurl.com/team":"payments"}}}`)
	case "/api/v1/namespaces/notify/configmaps":
		name := strings.TrimPrefix(r.URL.Query().Get("fieldSelector"), "metadata.name=")
		data := map[string]string{notify.OutboxKey: a.state, "unrelated": "keep"}
		if name == notify.RoutesName {
			data = map[string]string{notify.RoutesKey: `{"channels":{},"teams":{"payments":["payments-phone"]}}`}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{notifyConfigMap(name, data)}})
	case "/api/v1/namespaces/notify/configmaps/notify-outbox":
		var patch struct{ Data map[string]string }
		if r.Method != http.MethodPatch || r.Header.Get("Content-Type") != "application/merge-patch+json" ||
			r.URL.Query().Get("fieldManager") != notify.FieldManager || json.NewDecoder(r.Body).Decode(&patch) != nil {
			http.Error(w, "invalid patch", 400)
			return
		}
		a.state = patch.Data[notify.OutboxKey]
		body, _ := json.Marshal(notifyConfigMap(notify.OutboxName, map[string]string{notify.OutboxKey: a.state, "unrelated": "keep"}))
		a.patches++
		a.maxPatchResponse = max(a.maxPatchResponse, len(body))
		_, _ = w.Write(body)
	case "/api/v1/namespaces/notify/secrets":
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{
			"metadata": map[string]any{"name": "channel-payments-phone", "namespace": notify.Namespace},
			"data":     map[string][]byte{notify.AddressKey: []byte(notifyAddress)},
		}}})
	default:
		http.NotFound(w, r)
	}
}

func notifyConfigMap(name string, data map[string]string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]string{"name": name, "namespace": notify.Namespace}, "data": data}
}

func notifyConnection(t *testing.T, server *httptest.Server) kube.Connection {
	t.Helper()
	mount, _ := serviceAccountMount(t, server)
	c := cli{readFile: readInput, inCluster: mount, diagnostic: io.Discard}
	connection, _, status := c.serviceAccount()
	if status != 0 {
		t.Fatal("service account", status)
	}
	return connection
}

func TestDeliveryOfALegalHeavilyEscapedOutbox(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	state := notify.State{}
	for i := range 85 {
		state.Pending = append(state.Pending, notify.Entry{Title: strings.Repeat(`\`, 250), Message: strings.Repeat(`\`, 3500),
			Priority: 3, Key: fmt.Sprintf("event:%d@payments-phone", i), Channel: "payments-phone", Queued: float64(now.Unix()), App: "orders", Env: "staging"})
	}
	raw, err := state.Encode()
	if err != nil || len(raw) >= 700_000 {
		t.Fatalf("legal outbox: bytes %d, error %v", len(raw), err)
	}
	api := &notifyAPI{state: raw}
	server := httptest.NewTLSServer(api)
	defer server.Close()
	connection := notifyConnection(t, server)
	connect := func() (orchestration.NotifyCluster, error) { return notifyClusterOf(connection) }
	published := ntfytest.New()
	done, err := orchestration.NewNotifier(published).Deliver(context.Background(), connect, now)
	if err != nil || len(done) != 85 || len(published.Sent) != 85 {
		t.Fatalf("delivery must reach the publisher: done %d, sent %d, error %v", len(done), len(published.Sent), err)
	}
	api.lock.Lock()
	defer api.lock.Unlock()
	if api.maxPatchResponse <= 1<<20 || api.patches != 86 {
		t.Fatalf("whole ConfigMap responses: max %d bytes, %d patches", api.maxPatchResponse, api.patches)
	}
	saved, err := notify.DecodeState(api.state)
	if err != nil || len(saved.Pending) != 0 || len(saved.Seen) != 85 {
		t.Fatalf("saved state: %+v, %v", saved, err)
	}
}

func TestFreshNotifyConnectionsDoNotLeaveIdleTransports(t *testing.T) {
	var lock sync.Mutex
	connections := map[net.Conn]http.ConnState{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":{"authenticated":false}}`)
	}))
	server.Config.ConnState = func(c net.Conn, state http.ConnState) {
		lock.Lock()
		defer lock.Unlock()
		if state == http.StateClosed {
			delete(connections, c)
		} else {
			connections[c] = state
		}
	}
	server.StartTLS()
	defer server.Close()
	connection := notifyConnection(t, server)
	for range 12 {
		cluster, err := notifyClusterOf(connection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cluster.Reviewer.Review(context.Background(), "caller", notify.Audience); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		lock.Lock()
		count := len(connections)
		lock.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	lock.Lock()
	defer lock.Unlock()
	t.Fatalf("fresh requests left %d open connections: %v", len(connections), connections)
}

func TestStalledNotifyAPICallsReleaseDeliveryAndAllowHealthyRequests(t *testing.T) {
	for _, mode := range []string{"client timeout", "caller cancellation"} {
		t.Run(mode, func(t *testing.T) {
			api := &notifyAPI{}
			entered, cancelled, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var stall sync.Once
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				blocked := false
				if r.URL.Path == "/api/v1/namespaces/notify/configmaps" &&
					r.URL.Query().Get("fieldSelector") == "metadata.name=notify-outbox" {
					stall.Do(func() { blocked = true })
				}
				if !blocked {
					api.ServeHTTP(w, r)
					return
				}
				close(entered)
				select {
				case <-r.Context().Done():
					close(cancelled)
				case <-abort:
				}
			}))
			defer server.Close()
			defer close(abort)
			connection := notifyConnection(t, server)
			timeout := 100 * time.Millisecond
			if mode == "caller cancellation" {
				timeout = time.Hour
			}
			connect := func() (orchestration.NotifyCluster, error) { return notifyClusterWithTimeout(connection, timeout) }
			published := ntfytest.New()
			now := time.Unix(1_800_000_000, 0)
			service := newNotifyService(connect, published, io.Discard, func() time.Time { return now }, func() string { return notifyRandom })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := service.notifier.Deliver(ctx, connect, now); done <- err }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("delivery did not reach the stalled API")
			}
			if mode == "caller cancellation" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "timed_out") {
					t.Fatalf("stalled API: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stalled API held the notifier lock")
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("API request was not cancelled")
			}
			assertHealthyNotifyAfterStall(t, service, published)
		})
	}
}

func assertHealthyNotifyAfterStall(t *testing.T, service *notifyService, published *ntfytest.Fake) {
	t.Helper()
	body, _ := json.Marshal(notifyBody)
	request := httptest.NewRequest(http.MethodPost, "/v1/notify", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer checker-token")
	response := httptest.NewRecorder()
	service.handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Body.String() != `{"channels":["payments-phone"]}` {
		t.Fatalf("healthy acceptance after stalled delivery: %d %s", response.Code, response.Body)
	}
	service.pass(context.Background())
	if len(published.Sent) != 1 {
		t.Fatalf("healthy delivery after stall: sent %d", len(published.Sent))
	}
}
