package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/git"
	"github.com/samclement/swhurl-platform/v2/adapters/manifests"
	"github.com/samclement/swhurl-platform/v2/checkrepo"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// checkRepoKind names the --json document.
const checkRepoKind = "v2-check-repo"

// checkRepo runs check-repo's decisions over the checkout at --root and prints one line per check and the active
// render paths, or with --json the v2-check-repo document. A finding exits 1; a checkout that cannot be opened or
// read exits 2. With --rendered it reads what Kustomize rendered for that path on stdin and prints what Flux applies.
func (c cli) checkRepo(r request) int {
	reader := c.git
	if reader == nil {
		reader = git.New()
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "invalid: check-repo: cannot open %s\n", r.root)
		return 2
	}
	defer root.Close()
	read := func(path string) ([]byte, error) { return readDocument(root, path) }
	if r.rendered != "" {
		return c.renderPath(r, reader, read)
	}
	report, err := orchestration.CheckRepo(r.root, reader, manifests.YAML{}, read)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "invalid: check-repo: %v\n", err)
		return 2
	}
	output, failures := checkRepoText(report)
	if r.json {
		if output, err = checkRepoJSON(report, failures); err != nil {
			fmt.Fprintf(c.diagnostic, "io: check-repo: %v\n", err)
			return 1
		}
	}
	if status := emit(c.out, c.diagnostic, output); status != 0 {
		return status
	}
	if failures > 0 {
		return 1
	}
	return 0
}

// renderPath substitutes one rendered folder read from stdin and prints the result. An unresolved substitution or an
// incomplete document is a finding (exit 1, on stderr); a path Flux does not render, or a checkout or stdin that
// cannot be read, exits 2.
func (c cli) renderPath(r request, reader git.Reader, read func(string) ([]byte, error)) int {
	stdin := c.stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	rendered, err := io.ReadAll(io.LimitReader(stdin, maxDocument+1))
	if err == nil && len(rendered) > maxDocument {
		err = errDocumentTooLarge
	}
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: check-repo: reading the rendered manifests: %v\n", err)
		return 2
	}
	applied, err := orchestration.RenderPath(r.root, reader, manifests.YAML{}, read, r.rendered, rendered)
	var finding *checkrepo.Finding
	switch {
	case errors.As(err, &finding):
		fmt.Fprintf(c.diagnostic, "[BAD] %s\n", finding.Problem)
		return 1
	case err != nil:
		fmt.Fprintf(c.diagnostic, "invalid: check-repo: %v\n", err)
		return 2
	}
	return emit(c.out, c.diagnostic, []byte(applied))
}

// checkRepoText is the operator's text, and how many problems it holds.
func checkRepoText(report checkrepo.Report) ([]byte, int) {
	var out strings.Builder
	failures := 0
	for _, result := range report.Results {
		for _, problem := range result.Problems {
			fmt.Fprintf(&out, "[BAD] %s\n", problem)
		}
		if len(result.Problems) == 0 {
			fmt.Fprintf(&out, "[OK] %s\n", result.Okay)
		}
		failures += len(result.Problems)
	}
	fmt.Fprintf(&out, "\n%d active render path(s):\n", len(report.Paths))
	for _, path := range report.Paths {
		if path.Values == nil {
			fmt.Fprintf(&out, "  %s\n", path.Path)
			continue
		}
		fmt.Fprintf(&out, "  %s (substitutes %d value(s))\n", path.Path, len(path.Values))
	}
	fmt.Fprintf(&out, "\n%d error(s).\n", failures)
	return []byte(out.String()), failures
}

// checkRepoJSON is the v2-check-repo document: each check with its okay line or problems, then each active render
// path with the values it substitutes (null when it substitutes nothing).
func checkRepoJSON(report checkrepo.Report, failures int) ([]byte, error) {
	type check struct {
		Check    string   `json:"check"`
		Okay     string   `json:"okay,omitempty"`
		Problems []string `json:"problems"`
	}
	type path struct {
		Path   string            `json:"path"`
		Values map[string]string `json:"values"`
	}
	document := struct {
		Kind    string  `json:"kind"`
		Version int     `json:"version"`
		Passed  bool    `json:"passed"`
		Checks  []check `json:"checks"`
		Paths   []path  `json:"paths"`
	}{Kind: checkRepoKind, Version: 1, Passed: failures == 0, Checks: []check{}, Paths: []path{}}
	for _, result := range report.Results {
		document.Checks = append(document.Checks, check{result.Check, result.Okay, append([]string{}, result.Problems...)})
	}
	for _, p := range report.Paths {
		document.Paths = append(document.Paths, path{p.Path, p.Values})
	}
	return json.MarshalIndent(document, "", "  ")
}
