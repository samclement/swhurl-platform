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
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Written, Unchanged and Removed are the outcomes a path can have.
const (
	Written   = "written"
	Unchanged = "unchanged"
	Removed   = "removed"
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

// Writer writes every file it is given, or reports why it cannot. It writes only the paths it was given, and it
// deletes only what a pruned folder or a removal names: a write never removes anything by itself.
type Writer interface {
	Write(dir string, files []kernel.File) ([]Outcome, error)
	Prune(dir, folder string, rendered []string) ([]Outcome, error)
	Remove(dir, path string) ([]Outcome, error)
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

// Prune removes the files in one compiled folder that the compilation no longer renders: the names the renderer
// produces (kernel.Rendered) minus the names this compilation produced. A declared input beside them — the
// instance's seed, the documents it publishes — is not a rendered name, so it is never removed, and neither is
// anything below a folder, which is a copy rather than a render. A folder that does not exist has nothing to
// prune, so pruning an unmade destination is not a failure.
func (Disk) Prune(dir, folder string, rendered []string) ([]Outcome, error) {
	target, entries, err := folderEntries(dir, folder)
	if err != nil || entries == nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, name := range rendered {
		kept[name] = true
	}
	outcomes := make([]Outcome, 0, len(entries))
	for _, entry := range entries {
		if !prunable(entry, kept) {
			continue
		}
		relative := folder + "/" + entry.Name()
		if err := removeFile(filepath.Join(target, entry.Name()), relative); err != nil {
			return outcomes, err
		}
		outcomes = append(outcomes, Outcome{relative, Removed})
	}
	return outcomes, nil
}

// folderEntries is a folder's direct entries under the destination, or none when it does not exist.
func folderEntries(dir, folder string) (string, []os.DirEntry, error) {
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, &Error{IO, folder, "cannot resolve the destination"}
	}
	target, err := Target(base, folder)
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return target, nil, nil
	case err != nil:
		return "", nil, &Error{IO, folder, "cannot read the folder"}
	}
	return target, entries, nil
}

// prunable is a file the renderer produced that this compilation no longer names. A folder and a symlink are never
// pruned: one is a copy, and the other does not belong to the compilation.
func prunable(entry os.DirEntry, kept map[string]bool) bool {
	return kernel.Rendered(entry.Name()) && !kept[entry.Name()] &&
		!entry.IsDir() && entry.Type()&os.ModeSymlink == 0
}

// Remove deletes one file or one folder tree beneath dir, reporting every file it removed. It is what a removal
// of an instance takes: the files are listed before anything is deleted, so the report names what was there even
// when a deletion fails part-way. A path that is not there is not a failure — a removal is idempotent — and the
// destination itself is refused, because removing it would take everything the caller did not name.
func (Disk) Remove(dir, path string) ([]Outcome, error) {
	base, target, info, err := removedTarget(dir, path)
	if err != nil || info == nil {
		return nil, err
	}
	if !info.IsDir() {
		if err := removeFile(target, path); err != nil {
			return nil, err
		}
		return []Outcome{{path, Removed}}, nil
	}
	outcomes, err := listTree(base, path, target)
	if err != nil {
		return nil, err
	}
	if err := removeListed(base, outcomes); err != nil {
		return outcomes, err
	}
	if err := os.RemoveAll(target); err != nil {
		return outcomes, &Error{IO, path, "cannot remove the folder"}
	}
	return outcomes, nil
}

// removedTarget resolves what a removal names. An absent path is a nil info and no error: there is nothing to remove.
func removedTarget(dir, path string) (string, string, os.FileInfo, error) {
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", "", nil, &Error{IO, path, "cannot resolve the destination"}
	}
	if path == "" || path == "." {
		return "", "", nil, refuse(path, "cannot_remove_the_destination")
	}
	target, err := Target(base, path)
	if err != nil {
		return "", "", nil, err
	}
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return base, target, nil, nil
	case err != nil:
		return "", "", nil, &Error{IO, path, "cannot inspect the path"}
	}
	return base, target, info, nil
}

// removeListed deletes every file a tree listing named, in the listing's order, which is deepest first.
func removeListed(base string, outcomes []Outcome) error {
	for _, outcome := range outcomes {
		current, err := Target(base, outcome.Path)
		if err != nil {
			return err
		}
		if err := removeFile(current, outcome.Path); err != nil {
			return err
		}
	}
	return nil
}

// removeFile deletes one file, and a file that is already gone is not a failure.
func removeFile(target, relative string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &Error{IO, relative, "cannot remove the file"}
	}
	return nil
}

// listTree is every regular file in a folder, deepest first, as the relative paths a report states.
func listTree(base, folder, target string) ([]Outcome, error) {
	outcomes := make([]Outcome, 0, 8)
	err := filepath.WalkDir(target, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return &Error{IO, folder, "cannot read the folder"}
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, err := filepath.Rel(base, current)
		if err != nil {
			return &Error{IO, folder, "cannot name a file in the folder"}
		}
		outcomes = append(outcomes, Outcome{filepath.ToSlash(relative), Removed})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(outcomes, func(a, b Outcome) int { return strings.Compare(b.Path, a.Path) })
	return outcomes, nil
}
