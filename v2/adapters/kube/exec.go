package kube

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
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
	conn, err := a.upgradeExec(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return ExecResult{}, execError("timed_out")
		}
		return ExecResult{}, err
	}
	defer conn.CloseNow()
	sent := make(chan error, 1)
	go func() { sent <- stdin(ctx, conn, request.Stdin) }()
	result, err := receive(ctx, conn)
	// Closing also unblocks a writer when the peer stopped reading stdin.
	conn.CloseNow()
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

// upgradeExec opens the exec WebSocket: the v5 channel protocol only, no compression, and the
// connection's own authorization. A refused upgrade is a fixed reason, never the server's words.
func (a *API) upgradeExec(ctx context.Context, r ExecRequest) (*websocket.Conn, error) {
	header := http.Header{}
	if a.token != "" {
		header.Set("Authorization", "Bearer "+a.token)
	}
	conn, response, err := websocket.Dial(ctx, a.execURL(r), &websocket.DialOptions{HTTPClient: a.client,
		HTTPHeader: header, Subprotocols: []string{execProtocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, dialRefusal(response, err)
	}
	// This client offers one subprotocol and no extension, so a server choosing otherwise has answered
	// another handshake.
	if conn.Subprotocol() != execProtocol || response.Header.Get("Sec-WebSocket-Extensions") != "" {
		conn.CloseNow()
		return nil, execError("upgrade_refused")
	}
	conn.SetReadLimit(execLimit + 1)
	return conn, nil
}

// execURL is the pod's exec endpoint with the container, the argv and the channels it opens.
func (a *API) execURL(r ExecRequest) string {
	address := *a.base
	address.Path = strings.TrimSuffix(address.Path, "/") + "/api/v1/namespaces/" + url.PathEscape(r.Namespace) + "/pods/" + url.PathEscape(r.Pod) + "/exec"
	query := url.Values{"container": {r.Container}, "command": r.Command, "stdout": {"true"}, "stderr": {"true"}, "stdin": {"false"}, "tty": {"false"}}
	if r.Stdin != nil {
		query.Set("stdin", "true")
	}
	address.RawQuery = query.Encode()
	return address.String()
}

// dialRefusal is why the upgrade did not happen: no answer at all, a refused authorization, or an
// answer that is not this handshake.
func dialRefusal(response *http.Response, err error) error {
	switch {
	case response == nil:
		return execError(failureReason(err))
	case response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized:
		return execError("forbidden")
	}
	return execError("upgrade_refused")
}
