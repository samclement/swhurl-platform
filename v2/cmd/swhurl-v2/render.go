package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// render: every file v2 derives from its records, from the tree they live in.

const (
	callsFile      = "calls.json"
	allocationPath = "platform/egress/allocation.json"
	platformFolder = "platform"
	// provisionalRevision stands in for the commit a check was not given: the documents a compilation takes name one,
	// and a check compares neither document's revision.
	provisionalRevision = "0000000000000000000000000000000000000000"
	renderNote          = "every file v2 derives from its records, rendered offline; nothing is applied to the cluster"
	renderCheckNote     = "a check: the rendered files were compared with the ones under --out and nothing was written"
)

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

// rendering is one whole-tree render, decided before anything is written: the two documents that state the revision
// they were read at, the platform's other files, and each instance's compilation inputs with the files they render.
type rendering struct {
	callsPath  string
	resolution resolutionDocument
	allocation kernel.EgressDocument
	platform   []kernel.File
	instances  []orchestration.Inputs
	compiled   []kernel.File
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
	callsPath, err := underOut(r.out, filepath.Join(r.records, callsFile))
	if err != nil {
		return fail(c.diagnostic, err)
	}
	rendered, status := c.renderTree(r, revision)
	if status != 0 {
		return status
	}
	rendered.callsPath = callsPath
	if r.check {
		return c.checkRendered(r, rendered)
	}
	return c.writeRendered(r, rendered)
}

// renderRevision is the commit the documents record: required for a write, which states it, and stood in for when a
// check, which compares no revision, is given none.
func (c cli) renderRevision(r request) (string, int) {
	switch {
	case r.revision != "" && !commitRevision.MatchString(r.revision):
		return "", fail(c.diagnostic, errors.New("invalid: revision: expected_a_full_commit"))
	case r.revision != "":
		return r.revision, 0
	case r.check:
		return provisionalRevision, 0
	}
	return "", fail(c.diagnostic, errors.New("invalid: revision: required"))
}

// renderTree reads every instance folder and decides every file: a record anything refuses stops the run here.
func (c cli) renderTree(r request, revision string) (rendering, int) {
	profileData, status := c.read(r.profile, "profile")
	if status != 0 {
		return rendering{}, status
	}
	profile, err := kernel.DecodeProfile(profileData)
	if err != nil {
		return rendering{}, fail(c.diagnostic, err)
	}
	folders, status := c.renderFolders(r)
	if status != 0 {
		return rendering{}, status
	}
	previous, status := c.renderPrevious(r)
	if status != 0 {
		return rendering{}, status
	}
	pinned := snapshotOf(revision, folders)
	rendered, err := renderPlatform(pinned, previous, profile)
	if err != nil {
		return rendering{}, fail(c.diagnostic, err)
	}
	calls, err := encodeResolution(rendered.resolution)
	if err != nil {
		return rendering{}, fail(c.diagnostic, err)
	}
	egress, err := kernel.EncodeEgressDocument(rendered.allocation)
	if err != nil {
		return rendering{}, fail(c.diagnostic, err)
	}
	for _, one := range folders {
		inputs := orchestration.Inputs{Instance: one.instance, Release: one.release, App: one.app, Profile: profileData,
			Calls: calls, Egress: allocatedFor(&rendered.allocation, egress, recordInstance{application: one.application,
				environment: one.environment}), EventsSecret: one.secret, Offers: one.offers}
		written, err := instanceRender(inputs)
		if err != nil {
			return rendering{}, fail(c.diagnostic, fmt.Errorf("%s/%s: %w", one.application, one.environment, err))
		}
		rendered.instances = append(rendered.instances, inputs)
		rendered.compiled = append(rendered.compiled, written...)
	}
	return rendered, 0
}

// renderFolders is every instance folder under the records tree, app by app and environment by environment, each
// read whole and checked to hold the instance and release its path names.
func (c cli) renderFolders(r request) ([]recordsEdit, int) {
	entries, err := os.ReadDir(r.records)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: records: cannot read %s\n", r.records)
		return nil, 1
	}
	folders := []recordsEdit{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		environments, err := appEnvironments(r.records, entry.Name())
		if err != nil {
			fmt.Fprintf(c.diagnostic, "io: records: cannot read %s\n", filepath.Join(r.records, entry.Name()))
			return nil, 1
		}
		for _, environment := range environments {
			one, status := c.recordsEdit(r.out, r.records, entry.Name(), environment)
			if status != 0 {
				return nil, status
			}
			described := recordInstance{application: one.application, environment: one.environment,
				instance: one.instance, release: one.release}
			if status := c.describeRecords(&described); status != 0 {
				return nil, status
			}
			folders = append(folders, one)
		}
	}
	return folders, 0
}

// renderPrevious is the allocation whose ports are kept: the one --previous names, else the one under --out, else none.
func (c cli) renderPrevious(r request) (kernel.EgressDocument, int) {
	path := r.previous
	if path == "" {
		path = filepath.Join(r.out, allocationPath)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return kernel.EgressDocument{}, 0
		}
	}
	return c.allocation(path, "previous allocation")
}

// snapshotOf is the records tree as the snapshot readers take it: every instance in v2's own record shape.
func snapshotOf(revision string, folders []recordsEdit) snapshot {
	pinned := snapshot{Version: 1, Revision: revision, Instances: make([]snapshotInstance, 0, len(folders))}
	for _, one := range folders {
		pinned.Instances = append(pinned.Instances, snapshotInstance{Application: one.application,
			Environment: one.environment, Format: "v2", Records: map[string]json.RawMessage{
				"instance": one.instance, "release": one.release, "app": one.app}})
	}
	return pinned
}

// renderPlatform decides the platform's documents from one snapshot: the resolution, the allocation, and the files
// rendered from them and from the records — the proxy, the auth folder, NATS's users and Reloader's values.
func renderPlatform(pinned snapshot, previous kernel.EgressDocument, profile kernel.PlatformProfile) (rendering, error) {
	resolution, err := resolutionOf(pinned)
	if err != nil {
		return rendering{}, err
	}
	allocation, err := allocationAt(pinned, previous, profile)
	if err != nil {
		return rendering{}, err
	}
	proxy, err := kernel.RenderProxy(allocation.Instances, profile)
	if err != nil {
		return rendering{}, err
	}
	_, auth, err := renderAuth(pinned, profile)
	if err != nil {
		return rendering{}, err
	}
	_, users, err := renderUsers(pinned, profile)
	if err != nil {
		return rendering{}, err
	}
	_, reloader, err := renderReloader(pinned, profile)
	if err != nil {
		return rendering{}, err
	}
	rendered := rendering{resolution: resolution, allocation: allocation}
	for _, set := range []kernel.ResourceSet{proxy, auth, users, reloader} {
		for _, file := range set.Files {
			rendered.platform = append(rendered.platform, kernel.File{Path: platformFolder + "/" + file.Path,
				Document: file.Document})
		}
	}
	return rendered, nil
}

// instanceRender is the files one instance's folder holds once compiled: the compilation with its seed and the
// documents it publishes, as CompileInto writes them.
func instanceRender(inputs orchestration.Inputs) ([]kernel.File, error) {
	compiled, err := orchestration.Compile(inputs)
	if err != nil {
		return nil, err
	}
	written, err := orchestration.WithEventsSecret(compiled, inputs.EventsSecret)
	if err != nil {
		return nil, err
	}
	return orchestration.WithOffers(written, inputs.Offers)
}

// documents is the two documents that record a revision, at the revisions given, and the platform's other files. The
// two are the files `resolve` and `egress` print, so each ends in the newline their output does.
func (r rendering) documents(callsRevision, allocationRevision string) ([]kernel.File, error) {
	resolution, allocation := r.resolution, r.allocation
	resolution.Revision, allocation.Revision = callsRevision, allocationRevision
	calls, err := encodeResolution(resolution)
	if err != nil {
		return nil, err
	}
	egress, err := kernel.EncodeEgressDocument(allocation)
	if err != nil {
		return nil, err
	}
	return append([]kernel.File{{Path: r.callsPath, Document: append(calls, '\n')},
		{Path: allocationPath, Document: append(egress, '\n')}}, r.platform...), nil
}

// writeRendered writes the platform's files in one write and then every instance's folder, which CompileInto writes
// and prunes. Nothing is written until every file has been decided.
func (c cli) writeRendered(r request, rendered rendering) int {
	platform, err := rendered.documents(rendered.resolution.Revision, rendered.allocation.Revision)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	outcomes, err := c.writer.Write(r.out, platform)
	for _, inputs := range rendered.instances {
		if err != nil {
			break
		}
		var result orchestration.Result
		result, err = orchestration.CompileInto(inputs, r.out, c.writer)
		outcomes = append(outcomes, result.Written...)
	}
	fmt.Fprintln(c.diagnostic, writtenSummary(outcomes))
	if status := c.emitRender(r, nonNilOutcomes(outcomes), []string{}); status != 0 {
		return status
	}
	if err != nil {
		return c.failure(err)
	}
	return 0
}

// checkRendered compares every rendered file with the one under --out. The resolution and the allocation are
// compared at the revision the file on disk records, so only their content can differ.
func (c cli) checkRendered(r request, rendered rendering) int {
	platform, err := rendered.documents(recordedRevision(r.out, rendered.callsPath),
		recordedRevision(r.out, allocationPath))
	if err != nil {
		return fail(c.diagnostic, err)
	}
	checked := append(platform, rendered.compiled...)
	differences := []string{}
	for _, file := range checked {
		same, err := sameOnDisk(r.out, file)
		if err != nil {
			return c.failure(err)
		}
		if !same {
			differences = append(differences, file.Path)
		}
	}
	summary := fmt.Sprintf("render: %d file(s) checked, %d differ", len(checked), len(differences))
	if len(differences) > 0 {
		summary += ": " + strings.Join(differences, ", ")
	}
	fmt.Fprintln(c.diagnostic, summary)
	if status := c.emitRender(r, []files.Outcome{}, differences); status != 0 {
		return status
	}
	if len(differences) > 0 {
		return 1
	}
	return 0
}

// recordedRevision is the revision a document on disk records, or the provisional one when there is none to read:
// a missing or unreadable file differs whatever revision it is compared at.
func recordedRevision(out, path string) string {
	data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
	if err != nil {
		return provisionalRevision
	}
	var recorded struct{ Revision string }
	if json.Unmarshal(data, &recorded) != nil || !commitRevision.MatchString(recorded.Revision) {
		return provisionalRevision
	}
	return recorded.Revision
}

// sameOnDisk is whether the file under out holds exactly the rendered bytes. A missing file differs; a path the
// writer would refuse, or a file that cannot be read, is an error rather than a difference.
func sameOnDisk(out string, file kernel.File) (bool, error) {
	base, err := filepath.Abs(out)
	if err != nil {
		return false, &files.Error{Code: files.IO, Path: "destination", Reason: "cannot resolve the destination"}
	}
	target, err := files.Target(base, file.Path)
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, &files.Error{Code: files.IO, Path: file.Path, Reason: "cannot read the existing file"}
	}
	return bytes.Equal(existing, file.Document), nil
}

// writtenSummary is the one stderr line of a write: how many files each outcome has.
func writtenSummary(outcomes []files.Outcome) string {
	counts := map[string]int{}
	for _, outcome := range outcomes {
		counts[outcome.State]++
	}
	return fmt.Sprintf("render: %d written, %d unchanged, %d removed", counts[files.Written], counts[files.Unchanged],
		counts[files.Removed])
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
