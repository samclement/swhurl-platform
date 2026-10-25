// Package files writes a compilation's files to one destination directory, and nothing else.
//
// It is the only effect a v2 slice has so far. The compiler decides which paths exist and what they
// contain; this adapter decides only whether a destination is safe to write and what each path needs:
// nothing outside the destination, no symlink that leaves it, an existing file with identical bytes
// reported rather than rewritten, and a detected problem reported before anything is written.
package files

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Written and Unchanged are the two outcomes a path can have.
const (
	Written   = "written"
	Unchanged = "unchanged"
)

// Outcome says what happened to one path the compilation named, in the compilation's order.
type Outcome struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

// The two error codes: a destination or path this adapter refuses (nothing was written), and a
// failure of the filesystem itself (something may have been).
const (
	RefusedDestination = "refused_destination"
	IO                 = "io"
)

// Error names a code, the compilation's path and a fixed reason. It never carries file content.
type Error struct {
	Code   string
	Path   string
	Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Code, e.Path, e.Reason) }

// Writer writes every file it is given, or reports why it cannot. It never deletes, and it writes
// only the paths it was given.
type Writer interface {
	Write(dir string, files []kernel.File) ([]Outcome, error)
}

// Target checks one compilation path against a destination and reports where it would be written.
// It reads and writes nothing: a path that would leave the destination is refused, including through
// a symlink that already exists at or below it. The destination itself may be a symlink, because the
// operator chose it; nothing inside it may be.
func Target(base, relative string) (string, error) {
	target, err := targetPath(base, relative)
	if err != nil {
		return "", err
	}
	if err := noSymlink(base, relative); err != nil {
		return "", err
	}
	return target, nil
}

// targetPath joins a compilation path to the destination, refusing one that leaves or reshapes it.
func targetPath(base, relative string) (string, error) {
	cleaned := filepath.FromSlash(relative)
	if relative == "" || filepath.IsAbs(cleaned) || cleaned != filepath.Clean(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", refuse(relative, "not_a_relative_path")
	}
	target := filepath.Join(base, cleaned)
	// A destination may be "/" or end in a separator, so compare the relation, not the prefix.
	within, err := filepath.Rel(base, target)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", refuse(relative, "escapes_destination")
	}
	return target, nil
}

// noSymlink refuses a symlink at or below the destination: the file would land somewhere else.
func noSymlink(base, relative string) error {
	current := base
	for _, segment := range strings.Split(relative, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil // nothing further exists, so nothing further can be followed
		case err != nil:
			return &Error{IO, relative, "cannot inspect the destination"}
		case info.Mode()&os.ModeSymlink != 0:
			return refuse(relative, "symlink_in_path")
		}
	}
	return nil
}

// Disk writes to the filesystem.
type Disk struct{}

// New returns the adapter that writes to the filesystem.
func New() Writer { return Disk{} }

// Write prepares every file first and writes only once every one of them is sound, so a refused
// destination or path writes nothing at all. A filesystem failure during the writes themselves is
// reported with the outcomes already completed.
func (Disk) Write(dir string, docs []kernel.File) ([]Outcome, error) {
	base, err := filepath.Abs(dir)
	if err != nil {
		return nil, &Error{IO, "destination", "cannot resolve the destination"}
	}
	type planned struct {
		outcome Outcome
		target  string
		content []byte
	}
	plans := make([]planned, 0, len(docs))
	for _, doc := range docs {
		target, err := Target(base, doc.Path)
		if err != nil {
			return nil, err
		}
		outcome, content, err := decide(doc.Path, target, doc.Document)
		if err != nil {
			return nil, err
		}
		plans = append(plans, planned{outcome, target, content})
	}
	outcomes := make([]Outcome, 0, len(plans))
	for _, plan := range plans {
		if plan.outcome.State == Written {
			// A filesystem failure part-way is not something preparation can detect, so the outcomes
			// already completed are returned with it and the caller reports them.
			if err := writeAtomically(plan.outcome.Path, plan.target, plan.content); err != nil {
				return outcomes, err
			}
		}
		outcomes = append(outcomes, plan.outcome)
	}
	return outcomes, nil
}

// writeAtomically writes to a temporary name beside the target and renames it, so an interrupted run
// cannot leave a truncated file where a compilation should be.
func writeAtomically(relative, target string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return &Error{IO, relative, "cannot create the directory"}
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".swhurl-v2-*")
	if err != nil {
		return &Error{IO, relative, "cannot create the file"}
	}
	name := temp.Name()
	defer os.Remove(name) // a no-op once the rename has succeeded
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return &Error{IO, relative, "cannot write the file"}
	}
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return &Error{IO, relative, "cannot write the file"}
	}
	if err := temp.Close(); err != nil {
		return &Error{IO, relative, "cannot write the file"}
	}
	if err := os.Rename(name, target); err != nil {
		return &Error{IO, relative, "cannot write the file"}
	}
	return nil
}

// decide compares a document with what is already at the target: identical bytes are not rewritten.
func decide(relative, target string, content []byte) (Outcome, []byte, error) {
	existing, err := os.ReadFile(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Outcome{relative, Written}, content, nil
	case err != nil:
		return Outcome{}, nil, &Error{IO, relative, "cannot read the existing file"}
	case bytes.Equal(existing, content):
		return Outcome{relative, Unchanged}, nil, nil
	default:
		return Outcome{relative, Written}, content, nil
	}
}

func refuse(relative, reason string) *Error {
	return &Error{RefusedDestination, relative, reason}
}
