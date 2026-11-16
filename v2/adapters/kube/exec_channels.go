package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/coder/websocket"
)

// execChannels owns remotecommand's multiplexed streams and terminal status. Terminal status alone
// does not end reception: trailing stdout and orderly channel closures still belong to this run.
type execChannels struct {
	result ExecResult
	status bool
	closed [4]bool
	size   int
}

// receive reads whole messages until the peer ends the stream. The library answers pings and closes;
// a message cut off mid-way is never an ending, so a truncated fragment fails even after status.
func receive(ctx context.Context, conn *websocket.Conn) (ExecResult, error) {
	channels := execChannels{}
	for {
		kind, reader, err := conn.Reader(ctx)
		if err != nil {
			return channels.finish(err)
		}
		message, err := io.ReadAll(reader)
		if err != nil || kind != websocket.MessageBinary {
			return ExecResult{}, execError("incomplete_stream")
		}
		if err := channels.accept(message); err != nil {
			return ExecResult{}, err
		}
	}
}

// stdin sends the caller's bytes on channel 0 and then v5's close signal, which closes only stdin,
// not stdout/status or the WebSocket.
func stdin(ctx context.Context, conn *websocket.Conn, data []byte) error {
	if data == nil {
		return nil
	}
	for len(data) > 0 {
		n := min(len(data), 16<<10)
		if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{0}, data[:n]...)); err != nil {
			return err
		}
		data = data[n:]
	}
	return conn.Write(ctx, websocket.MessageBinary, []byte{255, 0})
}

// finish is how the stream ended between messages: an orderly close (1000, 1001 or no code) or the
// connection ending after terminal status is the run's result; an error close fails it even after
// status arrived.
func (c *execChannels) finish(err error) (ExecResult, error) {
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway, websocket.StatusNoStatusRcvd:
	case -1:
		if !errors.Is(err, io.EOF) {
			return ExecResult{}, execError("incomplete_stream")
		}
	default:
		return ExecResult{}, execError("closed_with_error")
	}
	if !c.status {
		return ExecResult{}, execError("incomplete_stream")
	}
	return c.result, nil
}

func (c *execChannels) accept(message []byte) error {
	if len(message) == 0 {
		return execError("malformed_channel")
	}
	channel, body := message[0], message[1:]
	c.size += len(body)
	if c.size > execLimit {
		return execError("too_large")
	}
	if channel == 255 {
		return c.closeChannel(body)
	}
	if channel < 1 || channel > 3 || c.closed[channel] {
		return execError("malformed_channel")
	}
	switch channel {
	case 1:
		c.result.Stdout = append(c.result.Stdout, body...)
	case 2: // stderr is deliberately discarded, but charged against the aggregate bound.
	case 3:
		return c.terminalStatus(body)
	}
	return nil
}

func (c *execChannels) terminalStatus(body []byte) error {
	if c.status {
		return execError("duplicate_status")
	}
	code, err := execStatus(body)
	if err != nil {
		return err
	}
	c.status, c.result.ExitCode = true, code
	return nil
}

func (c *execChannels) closeChannel(body []byte) error {
	if len(body) != 1 || body[0] > 3 || c.closed[body[0]] {
		return execError("malformed_channel_close")
	}
	c.closed[body[0]] = true
	return nil
}

type exitCause struct{ Reason, Message string }

func exitCode(causes []exitCause) (int, error) {
	if len(causes) != 1 || causes[0].Reason != "ExitCode" {
		return 0, execError("malformed_status")
	}
	text := causes[0].Message
	n, err := strconv.Atoi(text)
	if err != nil || n <= 0 || n > 255 || strconv.Itoa(n) != text {
		return 0, execError("malformed_status")
	}
	return n, nil
}

func execStatus(body []byte) (int, error) {
	var status struct {
		Status  string `json:"status"`
		Reason  string `json:"reason"`
		Details *struct {
			Causes []exitCause `json:"causes"`
		} `json:"details"`
	}
	if json.Unmarshal(body, &status) != nil {
		return 0, execError("malformed_status")
	}
	if status.Status == "Success" && status.Reason == "" && status.Details == nil {
		return 0, nil
	}
	if status.Status != "Failure" || status.Reason != "NonZeroExitCode" || status.Details == nil {
		return 0, execError("malformed_status")
	}
	return exitCode(status.Details.Causes)
}
