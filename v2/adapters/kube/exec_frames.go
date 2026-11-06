package kube

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"sync"
	"unicode/utf8"
)

// execSocket owns RFC6455 framing. Only writes need serialization: stdin and control replies may
// interleave, while one receiver owns fragmentation. Each frame and assembled message is bounded.
type execSocket struct {
	stream io.ReadWriteCloser
	writes sync.Mutex
}

type execFrame struct {
	final  bool
	opcode byte
	body   []byte
}

func (s *execSocket) frame() (execFrame, error) {
	var header [2]byte
	if _, err := io.ReadFull(s.stream, header[:]); err != nil {
		return execFrame{}, err
	}
	if header[0]&0x70 != 0 || header[1]&0x80 != 0 {
		return execFrame{}, execError("malformed_frame")
	}
	f := execFrame{final: header[0]&0x80 != 0, opcode: header[0] & 15}
	length, err := s.frameLength(header[1] & 127)
	if err != nil {
		return f, err
	}
	if f.opcode >= 8 && (!f.final || length > 125) {
		return f, execError("malformed_control")
	}
	f.body = make([]byte, int(length))
	_, err = io.ReadFull(s.stream, f.body)
	if err != nil {
		return f, io.ErrUnexpectedEOF
	}
	return f, nil
}

func (s *execSocket) frameLength(short byte) (uint64, error) {
	size := uint64(short)
	switch short {
	case 126:
		var data [2]byte
		if _, err := io.ReadFull(s.stream, data[:]); err != nil {
			return 0, io.ErrUnexpectedEOF
		}
		size = uint64(binary.BigEndian.Uint16(data[:]))
		if size < 126 {
			return 0, execError("malformed_frame")
		}
	case 127:
		var data [8]byte
		if _, err := io.ReadFull(s.stream, data[:]); err != nil {
			return 0, io.ErrUnexpectedEOF
		}
		size = binary.BigEndian.Uint64(data[:])
		if size < 65536 || size>>63 != 0 {
			return 0, execError("malformed_frame")
		}
	}
	if size > execLimit+1 {
		return 0, execError("too_large")
	}
	return size, nil
}

func (s *execSocket) write(opcode byte, body []byte) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	header := []byte{0x80 | opcode, 0x80}
	switch {
	case len(body) < 126:
		header[1] |= byte(len(body))
	case len(body) <= 65535:
		header[1] |= 126
		header = binary.BigEndian.AppendUint16(header, uint16(len(body)))
	default:
		header[1] |= 127
		header = binary.BigEndian.AppendUint64(header, uint64(len(body)))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return execError("transport")
	}
	frame := append(header, mask[:]...)
	for i, b := range body {
		frame = append(frame, b^mask[i%4])
	}
	for len(frame) > 0 {
		n, err := s.stream.Write(frame)
		if err != nil || n == 0 {
			return execError("transport")
		}
		frame = frame[n:]
	}
	return nil
}

func (s *execSocket) stdin(data []byte) error {
	if data == nil {
		return nil
	}
	for len(data) > 0 {
		n := min(len(data), 16<<10)
		if err := s.write(2, append([]byte{0}, data[:n]...)); err != nil {
			return err
		}
		data = data[n:]
	}
	// v5's close signal closes only stdin, not stdout/status or the WebSocket.
	return s.write(2, []byte{255, 0})
}

func (s *execSocket) control(f execFrame) (bool, error) {
	switch f.opcode {
	case 9:
		return false, s.write(10, f.body)
	case 10:
		return false, nil
	case 8:
		if !validClose(f.body) {
			return false, execError("malformed_close")
		}
		// A valid frame is not a successful ending: only 1000/1001, or no code at all, is orderly.
		// An error close (1002, 1008, 1011, ...) fails the run even after a status arrived.
		if len(f.body) >= 2 && !orderlyClose(binary.BigEndian.Uint16(f.body)) {
			return false, execError("closed_with_error")
		}
		return true, s.write(8, f.body)
	}
	return false, execError("malformed_control")
}

// orderlyClose is RFC6455's normal and going-away codes: the peer finished, it did not fail.
func orderlyClose(code uint16) bool { return code == 1000 || code == 1001 }

func validClose(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	if len(body) < 2 || !utf8.Valid(body[2:]) {
		return false
	}
	code := binary.BigEndian.Uint16(body)
	switch code {
	case 1004, 1005, 1006, 1015:
		return false
	}
	return code >= 1000 && code < 5000 && !(code >= 1016 && code < 3000)
}
