// Package git reads what Git knows about one checkout: its tracked files, the files its working tree holds, the paths
// with uncommitted changes and the commits its refs reach. It is the third approved subprocess adapter (see the
// `no-subprocess` rule and docs/iteration-2-plan.md#guardrails), and it only reads: four fixed commands, each with
// `--no-optional-locks` so not even `git status` refreshes the index, and no argument a caller can choose except the
// checkout's root. No GIT_* variable from the environment reaches them, so the root alone names the repository. Each
// command is bounded in time and output. There is no commit, fetch, push or general command runner here.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const (
	// RefusedRead is the code every failure here carries: a checkout this adapter could not read.
	RefusedRead = "refused_read"
	// NotARepository and NotTheTopLevel are the two refusals of a root: Git cannot read it as a working tree, or it
	// is a folder inside one.
	NotARepository = "not_a_repository"
	NotTheTopLevel = "not_the_top_level"
	// MaxOutput bounds what one command may return: a listing of every file or commit in a repository.
	MaxOutput = 64 << 20
	// ReadTimeout bounds one command.
	ReadTimeout = time.Minute
	// maxDetail bounds how much of Git's own message a refusal carries.
	maxDetail = 200
)

// Checkout is one working tree as Git sees it. Paths are repository-relative with '/' separators, sorted, and
// never a directory: an untracked folder is listed file by file.
type Checkout struct {
	// Tracked is every file in the index, including one deleted from the working tree but not from the index.
	Tracked []string
	// Present is every file the working tree holds that Git does not ignore: tracked and not deleted, or untracked.
	Present []string
	// Uncommitted is every path with a change Git would commit or could add: modified, staged, deleted, untracked,
	// and both sides of a rename.
	Uncommitted []string
	// Shallow says the history is truncated, so Commits is not every commit the repository has.
	Shallow bool
	// Commits is the full hash of every commit a ref reaches.
	Commits []string
}

// Reader reads one checkout, named by its top-level directory.
type Reader interface {
	Read(root string) (Checkout, error)
}

// Error names a fixed reason and, as Detail, at most one line of what Git said: the commands read file names, never
// file contents, so the message holds nothing a listing would not.
type Error struct{ Reason, Detail string }

func (e *Error) Error() string {
	if e.Detail == "" {
		return RefusedRead + ": " + e.Reason
	}
	return RefusedRead + ": " + e.Reason + ": " + e.Detail
}

// Command runs the git binary named by Binary, which is looked up in PATH.
type Command struct{ Binary string }

// New is the adapter that runs `git` from PATH.
func New() *Command { return &Command{Binary: "git"} }

var _ Reader = (*Command)(nil)

// Read refuses a root that is not the top of a working tree: Git reports index paths from the top and status paths
// from the top too, but only the top makes both mean the same files the caller names.
func (c *Command) Read(root string) (Checkout, error) {
	head, err := c.output(root, "rev-parse", "--show-prefix", "--is-shallow-repository")
	if err != nil {
		var refusal *Error
		if errors.As(err, &refusal) && strings.HasPrefix(refusal.Reason, "git_exit_") {
			return Checkout{}, &Error{Reason: NotARepository, Detail: refusal.Detail}
		}
		return Checkout{}, err
	}
	prefix, shallow, _ := strings.Cut(string(head), "\n")
	if prefix != "" {
		return Checkout{}, &Error{Reason: NotTheTopLevel, Detail: prefix}
	}
	read := Checkout{Shallow: strings.TrimSpace(shallow) == "true"}
	if read.Tracked, err = c.fields(root, '\x00', "ls-files", "-z"); err != nil {
		return Checkout{}, err
	}
	status, err := c.output(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return Checkout{}, err
	}
	entries, err := parseStatus(status)
	if err != nil {
		return Checkout{}, err
	}
	read.Present, read.Uncommitted = present(read.Tracked, entries), uncommitted(entries)
	if read.Commits, err = c.fields(root, '\n', "rev-list", "--all"); err != nil {
		return Checkout{}, err
	}
	return read, nil
}

// fields runs one command and splits its output, dropping the empty field a trailing separator leaves.
func (c *Command) fields(root string, separator byte, args ...string) ([]string, error) {
	out, err := c.output(root, args...)
	if err != nil {
		return nil, err
	}
	var found []string
	for field := range bytes.SplitSeq(out, []byte{separator}) {
		if len(field) > 0 {
			found = append(found, string(field))
		}
	}
	slices.Sort(found)
	// An unmerged path is in the index once per stage.
	return slices.Compact(found), nil
}

// output runs one fixed read: no optional locks, so `git status` writes no refreshed index, no fsmonitor hook, and an
// environment without GIT_* variables, so GIT_DIR or GIT_INDEX_FILE cannot point it at another repository.
func (c *Command) output(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ReadTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.Binary, append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", root}, args...)...)
	command.Env = slices.DeleteFunc(os.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "GIT_") })
	// WaitDelay bounds inherited pipes even if a failing executable leaves children behind.
	command.WaitDelay = time.Second
	stdout, stderr := &bounded{limit: MaxOutput}, &bounded{limit: maxDetail, truncate: true}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	var exit *exec.ExitError
	switch {
	case stdout.exceeded:
		return nil, refuse("output_too_large")
	case ctx.Err() != nil:
		return nil, refuse("timeout")
	case errors.As(err, &exit):
		return nil, &Error{Reason: fmt.Sprintf("git_exit_%d", exit.ExitCode()), Detail: firstLine(stderr.buffer.String())}
	case err != nil:
		return nil, refuse("git_not_available")
	}
	return stdout.buffer.Bytes(), nil
}

// bounded keeps at most limit bytes: past it, a truncating writer drops the rest and any other fails the write.
type bounded struct {
	buffer             bytes.Buffer
	limit              int
	truncate, exceeded bool
}

func (b *bounded) Write(p []byte) (int, error) {
	room := b.limit - b.buffer.Len()
	if len(p) <= room {
		return b.buffer.Write(p)
	}
	if b.truncate {
		b.buffer.Write(p[:max(room, 0)])
		return len(p), nil
	}
	b.exceeded = true
	return 0, io.ErrShortWrite
}

// entry is one path `git status --porcelain=v1 -z` reports: its two status letters, and the path a rename came from.
type entry struct {
	index, tree byte
	path, from  string
}

// parseStatus reads porcelain v1 with -z: "XY path" per field, and for a rename or copy, in the index or the working
// tree, the original path as the next field.
func parseStatus(out []byte) ([]entry, error) {
	fields := strings.Split(string(out), "\x00")
	var entries []entry
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if field == "" {
			continue
		}
		if len(field) < 4 || field[2] != ' ' {
			return nil, refuse("unreadable_status")
		}
		e := entry{index: field[0], tree: field[1], path: field[3:]}
		if strings.ContainsAny(string([]byte{e.index, e.tree}), "RC") {
			if i+1 >= len(fields) || fields[i+1] == "" {
				return nil, refuse("unreadable_status")
			}
			i++
			e.from = fields[i]
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// present is the index without the files gone from the working tree, plus the untracked files. A file is gone when
// the working tree deleted it or renamed it away; in an unmerged entry (U in the index column) the file is still on
// disk, whichever side deleted it.
func present(tracked []string, entries []entry) []string {
	gone, found := map[string]bool{}, slices.Clone(tracked)
	for _, e := range entries {
		switch {
		case e.index == '?':
			found = append(found, e.path)
		case e.tree == 'D' && e.index != 'U':
			gone[e.path] = true
		case e.tree == 'R':
			gone[e.from] = true
		}
	}
	found = slices.DeleteFunc(found, func(path string) bool { return gone[path] })
	slices.Sort(found)
	return slices.Compact(found)
}

func uncommitted(entries []entry) []string {
	var found []string
	for _, e := range entries {
		found = append(found, e.path)
		if e.from != "" {
			found = append(found, e.from)
		}
	}
	slices.Sort(found)
	return slices.Compact(found)
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}

func refuse(reason string) error { return &Error{Reason: reason} }
