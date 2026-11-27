package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	connectivityrules "github.com/samclement/swhurl-platform/v2/connectivity/rules"
	"github.com/samclement/swhurl-platform/v2/provisioning"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// render: every file v2 derives from its records, from the tree they live in.

const (
	callsFile      = connectivityrules.ResolutionRecordName
	allocationPath = provisioningrules.AllocationPath
	// provisionalRevision stands in for the commit a check was not given.
	provisionalRevision = provisioningrules.ProvisionalRevision
	renderNote          = "every file v2 derives from its records, rendered offline; nothing is applied to the cluster"
	renderCheckNote     = "a check: the rendered files were compared with the ones under --out and nothing was written"
)

// commitRevision is a full commit, as a command that records one is given it.
var commitRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

// renderDocument is the report: what a write did to each file, or which files a check found different.
type renderDocument struct {
	Kind        string          `json:"kind"`
	Version     int             `json:"version"`
	Records     string          `json:"records"`
	Revision    string          `json:"revision"`
	Check       bool            `json:"check"`
	Files       []files.Outcome `json:"files"`
	Differences []string        `json:"differences"`
	Note        string          `json:"note"`
}

// render renders every file v2 derives from the records under --records and writes them under --out, or with --check
// compares them with what --out holds and writes nothing.
func (c cli) render(r request) int {
	revision, status := c.renderRevision(r)
	if status != 0 {
		return status
	}
	if c.appTreeRefused(r.out) {
		return 2
	}
	if r.check {
		return c.checkRendered(r)
	}
	result, err := provisioning.Render(r.checkout(), revision, c.writer)
	if err != nil && result.Written == nil {
		return c.failure(err)
	}
	fmt.Fprintln(c.diagnostic, result.Summary())
	if status := c.emitRender(r, nonNilOutcomes(result.Written), []string{}); status != 0 {
		return status
	}
	if err != nil {
		return c.failure(err)
	}
	return 0
}

// renderRevision is the commit the documents record: required for a write, which states it, and stood in for when a
// check, which compares no revision, is given none.
func (c cli) renderRevision(r request) (string, int) {
	switch {
	case r.revision != "" && !provisioningrules.CommitRevision(r.revision):
		return "", fail(c.diagnostic, errors.New("invalid: revision: expected_a_full_commit"))
	case r.revision != "":
		return r.revision, 0
	case r.check:
		return provisionalRevision, 0
	}
	return "", fail(c.diagnostic, errors.New("invalid: revision: required"))
}

// checkRendered compares every rendered file with the one under --out, and fails when any differs.
func (c cli) checkRendered(r request) int {
	checked, err := provisioning.CheckRender(r.checkout())
	if err != nil {
		return c.failure(err)
	}
	fmt.Fprintln(c.diagnostic, checked.Summary())
	if status := c.emitRender(r, []files.Outcome{}, checked.Differences); status != 0 {
		return status
	}
	if len(checked.Differences) > 0 {
		return 1
	}
	return 0
}

// emitRender writes the report to stdout.
func (c cli) emitRender(r request, outcomes []files.Outcome, differences []string) int {
	document := renderDocument{Kind: "v2-render", Version: 1, Records: r.records, Revision: r.revision, Check: r.check,
		Files: outcomes, Differences: differences, Note: renderNote}
	if r.check {
		document.Note = renderCheckNote
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, bytes.TrimSuffix(out.Bytes(), []byte{'\n'}))
}
