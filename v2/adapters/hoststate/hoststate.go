// Package hoststate keeps one small private file on the host: it reads it, telling a file that is absent from one
// that cannot be read, and replaces it atomically, so a reader sees the old bytes or the new and never a part. Its
// errors name a fixed reason, never the file's content.
package hoststate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// The two failures: a file that cannot be read, and one that cannot be replaced (the old bytes are still there).
const (
	Unreadable = "unreadable"
	Unwritable = "unwritable"
)

// Error names a code and a fixed reason.
type Error struct{ Code, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Reason) }

// Store reads and replaces one file. Read reports false, with no error, only for a file that does not exist; any
// other failure, a directory or a file over MaxBytes included, is an error. Replace leaves the file holding exactly
// data, readable by its owner only, in a directory only its owner can enter, which it creates if it must.
type Store interface {
	Read(path string) ([]byte, bool, error)
	Replace(path string, data []byte) error
}

// MaxBytes bounds what Read returns: the state it keeps is a few hundred bytes.
const MaxBytes = 1 << 20

// Disk keeps files on the local filesystem.
type Disk struct{}

var _ Store = Disk{}

// New returns the store over the local filesystem.
func New() Store { return Disk{} }

// Read reads the whole file, up to MaxBytes.
func (Disk) Read(path string) ([]byte, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, &Error{Unreadable, "cannot open"}
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, false, &Error{Unreadable, "cannot read"}
	}
	if len(data) > MaxBytes {
		return nil, false, &Error{Unreadable, "too_large"}
	}
	return data, true, nil
}

// Replace writes data to a temporary file beside path with mode 0600, flushes it to disk and renames it over path.
// The directory is created with mode 0700 and set to it. A failure removes the temporary file.
func (Disk) Replace(path string, data []byte) error {
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil || os.Chmod(dir, 0o700) != nil {
		return &Error{Unwritable, "cannot prepare the directory"}
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return &Error{Unwritable, "cannot create a temporary file"}
	}
	if reason := fill(temporary, data); reason != "" {
		_ = os.Remove(temporary.Name())
		return &Error{Unwritable, reason}
	}
	if os.Rename(temporary.Name(), path) != nil {
		_ = os.Remove(temporary.Name())
		return &Error{Unwritable, "cannot rename"}
	}
	return nil
}

// fill makes the temporary file private, writes data, flushes it to disk and closes it; it returns why it could not.
func fill(file *os.File, data []byte) string {
	reason := ""
	switch {
	case file.Chmod(0o600) != nil:
		reason = "cannot set the mode"
	case writeAll(file, data) != nil:
		reason = "cannot write"
	case file.Sync() != nil:
		reason = "cannot flush"
	}
	if file.Close() != nil && reason == "" {
		reason = "cannot close"
	}
	return reason
}

func writeAll(file *os.File, data []byte) error {
	_, err := file.Write(data)
	return err
}
