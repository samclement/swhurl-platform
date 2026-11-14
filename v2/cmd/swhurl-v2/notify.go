package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/notify"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// defaultListen is where notify-serve listens unless --listen says otherwise: the port its Service targets.
const defaultListen = ":8080"

// deliveryInterval is how long the delivery loop waits for an accepted message before it tries again what could not
// be sent.
const deliveryInterval = 30 * time.Second

// notifyServe runs the notify service until it fails to serve: the HTTP endpoints, and the delivery loop, which
// starts with a pass for what a previous pod left in the outbox. It refuses to start outside a pod.
func (c cli) notifyServe(r request) int {
	if _, err := c.notifyCluster(); err != nil {
		fmt.Fprintln(c.diagnostic, err)
		return 2
	}
	listener, err := net.Listen("tcp", r.listen)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: listen: %v\n", err)
		return 1
	}
	service := newNotifyService(c.notifyCluster, ntfy.New(nil), c.out, time.Now, randomKey)
	go service.loop(context.Background(), deliveryInterval)
	service.wakeUp()
	service.log.write("INFO", "Listening on "+listener.Addr().String())
	server := &http.Server{Handler: service.handler(), ReadHeaderTimeout: 10 * time.Second}
	err = server.Serve(listener)
	fmt.Fprintf(c.diagnostic, "io: serve: %v\n", err)
	return 1
}

// notifyCluster is a new connection from the pod's ServiceAccount, its token read again, so each request and pass
// follows the kubelet's rotation of it. The error is the reason the mount could not be used.
func (c cli) notifyCluster() (orchestration.NotifyCluster, error) {
	var why strings.Builder
	quiet := c
	quiet.diagnostic = &why
	connection, _, status := quiet.serviceAccount()
	if status != 0 {
		return orchestration.NotifyCluster{}, errors.New(strings.TrimSpace(why.String()))
	}
	return notifyClusterOf(connection)
}

// notifyClusterOf is the three ports over one pass/request-owned client. API round trips are bounded even when
// delivery uses a background context, and idle connections cannot outlive this short-lived client.
func notifyClusterOf(connection kube.Connection) (orchestration.NotifyCluster, error) {
	return notifyClusterWithTimeout(connection, 10*time.Second)
}

func notifyClusterWithTimeout(connection kube.Connection, timeout time.Duration) (orchestration.NotifyCluster, error) {
	client, err := kube.Client(connection.Credentials)
	if err != nil {
		return orchestration.NotifyCluster{}, err
	}
	client.Timeout = timeout
	// kube.Client constructs this transport; only notify opts out of connection reuse.
	client.Transport.(*http.Transport).DisableKeepAlives = true
	api, err := kube.NewAPI(connection.Endpoint, client, connection.Token)
	if err != nil {
		return orchestration.NotifyCluster{}, err
	}
	return orchestration.NotifyCluster{Reviewer: api, Data: api, Reader: api}, nil
}

// randomKey is the key of an app's message that gives none: 32 lowercase hex characters.
func randomKey() string {
	var key [16]byte
	_, _ = rand.Read(key[:]) // crypto/rand.Read never fails
	return hex.EncodeToString(key[:])
}

// notifyService is the notify service's server: the use cases' notifier, how it connects, the clock and the random
// key it gives them, its log, and the signal that wakes the delivery loop.
type notifyService struct {
	notifier  *orchestration.Notifier
	connect   orchestration.NotifyConnect
	now       func() time.Time
	randomKey func() string
	log       *jsonLog
	wake      chan struct{}
}

func newNotifyService(connect orchestration.NotifyConnect, publisher ntfy.Publisher, log io.Writer,
	now func() time.Time, random func() string) *notifyService {
	return &notifyService{notifier: orchestration.NewNotifier(publisher), connect: connect, now: now,
		randomKey: random, log: &jsonLog{out: log, now: now, name: "notify"}, wake: make(chan struct{}, 1)}
}

// handler is the service's endpoints: POST /v1/notify, POST /v1/alert and GET /healthz.
func (s *notifyService) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/notify", s.endpoint("a message", func(r *http.Request, body []byte) (orchestration.Accepted, error) {
		return s.notifier.AcceptMessage(r.Context(), s.connect, r.Header.Get("Authorization"), body, s.now(), s.randomKey())
	}))
	mux.HandleFunc("POST /v1/alert", s.endpoint("an alert", func(r *http.Request, body []byte) (orchestration.Accepted, error) {
		return s.notifier.AcceptAlert(r.Context(), s.connect, body, s.now())
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	return logRequests(s.log, mux)
}

// endpoint answers one POST: 202 with the channels a copy was queued for, waking the delivery loop when there are
// any, or the refusal's status and reason. Both are logged.
func (s *notifyService) endpoint(what string, accept func(*http.Request, []byte) (orchestration.Accepted, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, notify.MaxBody+1))
		var accepted orchestration.Accepted
		if err == nil {
			accepted, err = accept(r, body)
		}
		if err != nil {
			refusal := &notify.Refusal{Status: http.StatusBadRequest, Reason: "the body could not be read"}
			errors.As(err, &refusal)
			s.refused(what, accepted, refusal)
			answer(w, refusal.Status, map[string]any{"error": refusal.Reason})
			return
		}
		from := "ClickStack"
		if accepted.Caller != "" {
			from = accepted.Caller[strings.LastIndex(accepted.Caller, ":")+1:]
		}
		s.log.write("INFO", fmt.Sprintf(`Accepted "%s" about %s/%s from %s for %d channel(s)`, accepted.Request.Notice.Title,
			accepted.Request.App, accepted.Request.Env, from, len(accepted.Channels)))
		if len(accepted.Channels) > 0 {
			s.wakeUp()
		}
		answer(w, http.StatusAccepted, map[string]any{"channels": accepted.Channels})
	}
}

// refused logs a refusal: by the caller's name once Kubernetes has said it, and with the failure behind a 503.
func (s *notifyService) refused(what string, accepted orchestration.Accepted, refusal *notify.Refusal) {
	message := "Refused " + what
	if accepted.Caller != "" {
		message += " from " + accepted.Caller
	}
	message += fmt.Sprintf(": %d %s", refusal.Status, refusal.Reason)
	level := "WARNING"
	if accepted.Failure != nil {
		message += " (" + accepted.Failure.Error() + ")"
		level = "ERROR"
	}
	s.log.write(level, message)
}

// answer writes a JSON answer, compact and with its characters as they are, as the Python service wrote it.
func answer(w http.ResponseWriter, status int, document any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(compactJSON(document))
}

// wakeUp asks the delivery loop for a pass; a pass already asked for is not asked twice.
func (s *notifyService) wakeUp() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// loop runs a delivery pass whenever it is woken, and every interval for what could not be sent, until ctx ends.
func (s *notifyService) loop(ctx context.Context, interval time.Duration) {
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
		case <-timer.C:
		}
		timer.Stop()
		s.pass(ctx)
	}
}

// pass runs one delivery pass and logs what became of each copy, and why the pass stopped if it did.
func (s *notifyService) pass(ctx context.Context) {
	done, err := s.notifier.Deliver(ctx, s.connect, s.now())
	for _, offered := range done {
		switch {
		case offered.Err == nil:
			s.log.write("INFO", fmt.Sprintf("Notification delivered: %s (channel %s)", offered.Entry.Title, offered.Entry.Channel))
		case offered.Retry:
			s.log.write("ERROR", fmt.Sprintf("channel %s: %v (will retry)", offered.Entry.Channel, offered.Err))
		default:
			s.log.write("ERROR", fmt.Sprintf("channel %s: %v (gave up)", offered.Entry.Channel, offered.Err))
		}
	}
	if err != nil {
		s.log.write("ERROR", "Delivery pass failed: "+err.Error())
	}
}
