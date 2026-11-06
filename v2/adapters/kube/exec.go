package kube

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Executor runs in one named pod/container, separately from Reader and Data. A transport/protocol
// failure returns no output and a fixed error; a completed command returns its decoded exit code.
// Stderr is bounded and discarded, never a diagnostic. Returned stdout is owned by the caller.
type Executor interface {
	Exec(context.Context, ExecRequest) (ExecResult, error)
}

// ExecRequest names a pod/container and argv. Stdin is optional (nil disables its channel); its
// bytes must never appear in argv, URLs or diagnostics. The caller keeps ownership of these slices.
type ExecRequest struct {
	Namespace, Pod, Container string
	Command                   []string
	Stdin                     []byte
}

// ExecResult is complete stdout and a decoded process exit status, not a transport failure.
type ExecResult struct {
	Stdout   []byte
	ExitCode int
}

const execLimit = 1 << 20
const execProtocol = "v5.channel.k8s.io"

var _ Executor = (*API)(nil)

// Executor reaches this connection's API server using the same TLS material and authorization.
func (c Connection) Executor() (Executor, error) {
	return c.api()
}

// Exec upgrades one HTTP/1.1 request, sends stdin concurrently with receiving output, and waits for
// the peer to finish after terminal status, so output sent around that status is not dropped.
func (a *API) Exec(ctx context.Context, request ExecRequest) (ExecResult, error) {
	if err := validateExec(request); err != nil {
		return ExecResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := a.upgradeExec(ctx, request)
	if err != nil {
		return ExecResult{}, err
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	defer stop()
	socket := &execSocket{stream: stream}
	sent := make(chan error, 1)
	go func() { sent <- socket.stdin(request.Stdin) }()
	result, err := socket.receive()
	// Closing also unblocks a writer when the peer stopped reading stdin.
	stream.Close()
	sendErr := <-sent
	if ctx.Err() != nil {
		return ExecResult{}, execError("timed_out")
	}
	if err != nil {
		return ExecResult{}, err
	}
	if sendErr != nil {
		return ExecResult{}, execError("transport")
	}
	return result, nil
}

func validateExec(r ExecRequest) error {
	for _, name := range []string{r.Namespace, r.Pod, r.Container} {
		if !namePattern.MatchString(name) {
			return &Error{RefusedRequest, "exec", "invalid_target"}
		}
	}
	if len(r.Command) == 0 || len(r.Stdin) > execLimit {
		return &Error{RefusedRequest, "exec", "invalid_input"}
	}
	size := 0
	for _, arg := range r.Command {
		size += len(arg)
		if strings.ContainsRune(arg, 0) {
			return &Error{RefusedRequest, "exec", "invalid_input"}
		}
	}
	if size > 64<<10 {
		return &Error{RefusedRequest, "exec", "invalid_input"}
	}
	return nil
}

func execError(reason string) error { return &Error{Failed, "exec", reason} }

func (a *API) upgradeExec(ctx context.Context, r ExecRequest) (io.ReadWriteCloser, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, execError("transport")
	}
	key := base64.StdEncoding.EncodeToString(nonce)
	address := *a.base
	address.Path = strings.TrimSuffix(address.Path, "/") + "/api/v1/namespaces/" + url.PathEscape(r.Namespace) + "/pods/" + url.PathEscape(r.Pod) + "/exec"
	query := url.Values{"container": {r.Container}, "command": r.Command, "stdout": {"true"}, "stderr": {"true"}, "stdin": {"false"}, "tty": {"false"}}
	if r.Stdin != nil {
		query.Set("stdin", "true")
	}
	address.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return nil, execError("transport")
	}
	req.Header = http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {key}, "Sec-Websocket-Protocol": {execProtocol}}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	response, err := a.client.Do(req)
	if err != nil {
		return nil, execError(failureReason(err))
	}
	return upgradedStream(response, key)
}

func upgradedStream(response *http.Response, key string) (io.ReadWriteCloser, error) {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	valid := response.StatusCode == http.StatusSwitchingProtocols &&
		headerToken(response.Header.Get("Connection"), "upgrade") && strings.EqualFold(response.Header.Get("Upgrade"), "websocket") &&
		response.Header.Get("Sec-WebSocket-Accept") == base64.StdEncoding.EncodeToString(sum[:]) &&
		response.Header.Get("Sec-WebSocket-Protocol") == execProtocol &&
		// This client offers no extension, so a server selecting one has answered another handshake.
		response.Header.Get("Sec-WebSocket-Extensions") == ""
	stream, writable := response.Body.(io.ReadWriteCloser)
	if !valid || !writable {
		response.Body.Close()
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
			return nil, execError("forbidden")
		}
		return nil, execError("upgrade_refused")
	}
	return stream, nil
}

func headerToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}
