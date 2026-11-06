package kube

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

// execChannels owns remotecommand's multiplexed streams and terminal status. Terminal status alone
// does not end reception: trailing stdout and orderly channel closures still belong to this run.
type execChannels struct {
	result ExecResult
	status bool
	closed [4]bool
	size   int
}

func (s *execSocket) receive() (ExecResult, error) {
	channels := execChannels{}
	var message []byte
	fragmented := false
	for {
		f, err := s.frame()
		if err != nil {
			return channels.finish(err, fragmented)
		}
		if f.opcode >= 8 {
			closed, err := s.control(f)
			if err != nil {
				return ExecResult{}, err
			}
			if closed {
				return channels.finish(io.EOF, fragmented)
			}
			continue
		}

		message, fragmented, err = appendFragment(message, fragmented, f)
		if err != nil {
			return ExecResult{}, err
		}
		if fragmented {
			continue
		}
		if err := channels.accept(message); err != nil {
			return ExecResult{}, err
		}
		message = nil
	}
}

func appendFragment(message []byte, fragmented bool, f execFrame) ([]byte, bool, error) {
	if f.opcode != 2 && f.opcode != 0 || (f.opcode == 0) != fragmented {
		return nil, false, execError("malformed_fragment")
	}
	if len(message)+len(f.body) > execLimit+1 {
		return nil, false, execError("too_large")
	}
	return append(message, f.body...), !f.final, nil
}

func (c *execChannels) finish(err error, fragmented bool) (ExecResult, error) {
	if errors.Is(err, io.EOF) && c.status && !fragmented {
		return c.result, nil
	}
	return ExecResult{}, execError("incomplete_stream")
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
