package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/workstreams"
)

// maxDocument bounds one Markdown file the workstream check reads: the dated record is the largest, under 1 MiB.
const maxDocument = 16 << 20

var errDocumentTooLarge = errors.New("a Markdown file is larger than 16 MiB")

// workstreams compares the workstream record with the checkout at --repo-root and prints one line per check:
// [OK], [BAD] per problem, or [WARN], then the count. Findings exit 1. A root Git refuses (not a checkout, or not its
// top) is a refused input, exit 2; a Git command or a file that fails is I/O, exit 1. Either says why on stderr.
func (c cli) workstreams(r request) int {
	reader := c.git
	if reader == nil {
		reader = git.New()
	}
	root, err := os.OpenRoot(r.repoRoot)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: workstreams: cannot open %s\n", r.repoRoot)
		return 1
	}
	defer root.Close()
	results, err := orchestration.CheckWorkstreams(r.repoRoot, reader, func(path string) ([]byte, error) {
		return readDocument(root, path)
	})
	var refusal *git.Error
	switch {
	case errors.As(err, &refusal) && (refusal.Reason == git.NotARepository || refusal.Reason == git.NotTheTopLevel):
		fmt.Fprintf(c.diagnostic, "invalid: workstreams: %v\n", err)
		return 2
	case err != nil:
		fmt.Fprintf(c.diagnostic, "io: workstreams: %v\n", err)
		return 1
	}
	text, failures := workstreamReport(results)
	if status := emit(c.out, c.diagnostic, []byte(text)); status != 0 {
		return status
	}
	if failures > 0 {
		return 1
	}
	return 0
}

// readDocument reads one repository path inside root, which refuses a path or symlink that leaves it.
func readDocument(root *os.Root, path string) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDocument+1))
	if err == nil && len(data) > maxDocument {
		return nil, fmt.Errorf("%s: %w", path, errDocumentTooLarge)
	}
	return data, err
}

// workstreamReport is the operator's text: each check's okay line or its problems, a warning when one could not
// run, and the totals.
func workstreamReport(results []workstreams.Result) (string, int) {
	var out strings.Builder
	failures, warnings := 0, 0
	for _, result := range results {
		switch {
		case result.Warning != "":
			warnings++
			fmt.Fprintf(&out, "[WARN] %s\n", result.Warning)
		case len(result.Problems) > 0:
			failures += len(result.Problems)
			for _, problem := range result.Problems {
				fmt.Fprintf(&out, "[BAD] %s\n", problem)
			}
		default:
			fmt.Fprintf(&out, "[OK] %s\n", result.Okay)
		}
	}
	fmt.Fprintf(&out, "\n%d error(s), %d warning(s).\n", failures, warnings)
	return out.String(), failures
}
