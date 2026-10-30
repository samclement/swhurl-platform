package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// deploy records: every v2-owned staging instance, from the tree its records live in.

const (
	recordsKind  = "v2-deploy-records"
	instanceFile = "instance.json"
	releaseFile  = "release.json"
	appFile      = "app.json"
	stagingDir   = "staging"
)

// Every delivery's outcome: what the report states and how the exit status reads it.
const (
	outcomeChanged   = "changed"
	outcomeUnchanged = "unchanged"
	outcomeRefused   = "refused"
	outcomeFailed    = "failed"
)

// recordInstance is one delivery's documents, read before anything runs: a broken tree refuses the whole run
// rather than being discovered halfway through it.
type recordInstance struct {
	application, environment string
	instance, release, app   []byte
	revision                 kernel.InstanceRevision
	image                    string
}

// deployRecordsDocument is the report: one entry per instance, in name order.
type deployRecordsDocument struct {
	Kind      string               `json:"kind"`
	Version   int                  `json:"version"`
	Records   string               `json:"records"`
	Instances []deployRecordsEntry `json:"instances"`
	Note      string               `json:"note"`
}

// deployRecordsEntry is what one delivery did, or why it did nothing.
type deployRecordsEntry struct {
	Application string          `json:"application"`
	Environment string          `json:"environment"`
	Outcome     string          `json:"outcome"`
	Tag         string          `json:"tag,omitempty"`
	ReleaseID   string          `json:"releaseId,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	Files       []files.Outcome `json:"files,omitempty"`
}

const recordsNote = "the image's own declaration; v2's records and files only, one instance at a time"

// deployRecords brings every v2-owned staging instance to the newest image its own repository publishes.
// An instance that refuses or fails does not stop the others: each one keeps the image it runs, and the exit
// status is 1 if any failed, else 2 if any refused, else 0.
func (c cli) deployRecords(r request) int {
	if c.appTreeRefused(r.out) {
		return 2
	}
	instances, profile, calls, status := c.recordsInputs(r)
	if status != 0 {
		return status
	}
	egress, allocation, status := c.recordsEgress(r)
	if status != 0 {
		return status
	}
	document := deployRecordsDocument{Kind: recordsKind, Version: 1, Records: r.records,
		Instances: make([]deployRecordsEntry, 0, len(instances)), Note: recordsNote}
	for _, one := range instances {
		entry := c.deployOne(r, one, profile, calls, egress, allocation)
		document.Instances = append(document.Instances, entry)
		fmt.Fprintf(c.diagnostic, "%s/%s: %s %s\n", entry.Application, entry.Environment, entry.Outcome, detail(entry))
	}
	fmt.Fprintln(c.diagnostic, recordsSummary(document.Instances))
	data, err := json.Marshal(document)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if status := emit(c.out, c.diagnostic, data); status != 0 {
		return status
	}
	return recordsStatus(document.Instances)
}

// deployOne runs one delivery: the registry its image is published to, its allocation when the allocation records
// a grant for it, and the decisions the single-instance command makes.
func (c cli) deployOne(r request, one recordInstance, profile, calls, egress []byte,
	allocation *kernel.EgressDocument) deployRecordsEntry {
	entry := deployRecordsEntry{Application: one.application, Environment: one.environment}
	images, err := c.registries(one.image)
	if err != nil {
		entry.Outcome, entry.Reason = outcomeRefused, err.Error()
		return entry
	}
	inputs := orchestration.DeployInputs{Instance: one.instance, Release: one.release, App: one.app,
		Profile: profile, Calls: calls, Egress: allocatedFor(allocation, egress, one), Expected: one.revision}
	result, err := orchestration.DeployImage(context.Background(), inputs, images, r.out, c.writer)
	entry.Files = result.Written
	switch {
	case err != nil:
		entry.Outcome, entry.Reason = outcomeRefused, err.Error()
		if exitFor(err) == 1 {
			entry.Outcome = outcomeFailed
		}
	case result.Changed:
		entry.Outcome, entry.Tag, entry.ReleaseID = outcomeChanged, result.Tag, string(result.Release.ReleaseID)
	default:
		entry.Outcome, entry.Tag, entry.ReleaseID = outcomeUnchanged, result.Tag, string(result.Release.ReleaseID)
	}
	return entry
}

// allocatedFor is whether an instance is compiled behind the proxy. The allocation document is given whole to
// every instance, and an instance takes it only when the document records something that instance may reach: a
// port kept for an instance with nothing to reach is not a reason to put it behind the proxy, and a database's
// backup grant is what makes it use one. An instance whose release keeps a database and whose allocation records
// no grant is refused by the compiler rather than passed silently.
func allocatedFor(allocation *kernel.EgressDocument, egress []byte, one recordInstance) []byte {
	if allocation == nil {
		return nil
	}
	own, err := allocation.EgressOf(one.application, one.environment)
	if err != nil || len(own.Grants) == 0 {
		return nil
	}
	return egress
}

// recordsEgress reads the allocation once: the decoded document decides each instance, and the bytes are what a
// delivery compiles with.
func (c cli) recordsEgress(r request) ([]byte, *kernel.EgressDocument, int) {
	if r.egressFile == "" {
		return nil, nil, 0
	}
	egress, status := c.read(r.egressFile, "egress")
	if status != 0 {
		return nil, nil, status
	}
	decoded, err := kernel.DecodeEgressDocument(egress)
	if err != nil {
		return nil, nil, fail(c.diagnostic, err)
	}
	return egress, &decoded, 0
}

// recordsStatus is the worst outcome: an I/O or registry failure, else a refusal, else success.
func recordsStatus(entries []deployRecordsEntry) int {
	status := 0
	for _, entry := range entries {
		switch entry.Outcome {
		case outcomeFailed:
			return 1
		case outcomeRefused:
			status = 2
		}
	}
	return status
}

// detail is the rest of an instance's line: the tag it selected, or why it did nothing. A refusal states its own
// code (`refused: declaration: absent`), so the outcome is not repeated before it.
func detail(entry deployRecordsEntry) string {
	if entry.Reason == "" {
		return entry.Tag
	}
	return strings.TrimPrefix(entry.Reason, entry.Outcome+": ")
}

func recordsSummary(entries []deployRecordsEntry) string {
	counts := map[string]int{}
	for _, entry := range entries {
		counts[entry.Outcome]++
	}
	return fmt.Sprintf("%d changed, %d unchanged, %d refused, %d failed", counts[outcomeChanged],
		counts[outcomeUnchanged], counts[outcomeRefused], counts[outcomeFailed])
}

// recordsInputs reads the platform profile, the resolution and every instance's own records. A tree that does not
// hold what a delivery needs refuses the whole run: nothing is attempted from a half-read tree.
func (c cli) recordsInputs(r request) ([]recordInstance, []byte, []byte, int) {
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return nil, nil, nil, status
	}
	calls, status := c.recordsCalls(r)
	if status != 0 {
		return nil, nil, nil, status
	}
	names, err := instanceFolders(r.records)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: records: %v\n", err)
		return nil, nil, nil, 1
	}
	apps := map[string][]byte{}
	instances := make([]recordInstance, 0, len(names))
	for _, name := range names {
		one, status := c.readInstance(r.records, name, stagingDir, apps)
		if status != 0 {
			return nil, nil, nil, status
		}
		instances = append(instances, one)
	}
	return instances, profile, calls, 0
}

func (c cli) recordsCalls(r request) ([]byte, int) {
	if r.callFile == "" {
		return nil, 0
	}
	return c.read(r.callFile, "calls")
}

// readInstance reads one app's records in one environment, and keeps the app record it shares with the app's other
// instances.
func (c cli) readInstance(root, name, environment string, apps map[string][]byte) (recordInstance, int) {
	one := recordInstance{application: name, environment: environment}
	folder := filepath.Join(root, name, environment)
	what := name + "/" + environment
	var status int
	if one.instance, status = c.read(filepath.Join(folder, instanceFile), what+" instance"); status != 0 {
		return recordInstance{}, status
	}
	if one.release, status = c.read(filepath.Join(folder, releaseFile), what+" release"); status != 0 {
		return recordInstance{}, status
	}
	app, seen := apps[name]
	if !seen {
		if app, status = c.read(filepath.Join(root, name, appFile), "app"); status != 0 {
			return recordInstance{}, status
		}
		apps[name] = app
	}
	one.app = app
	if status := c.describeRecords(&one); status != 0 {
		return recordInstance{}, status
	}
	return one, 0
}

// describeRecords reads what a delivery needs before it runs: the instance's own name and revision, and the image
// its registry is addressed by.
func (c cli) describeRecords(one *recordInstance) int {
	spec, err := kernel.DecodeInstance(one.instance)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if spec.Application != one.application || spec.Environment != one.environment {
		return fail(c.diagnostic, fmt.Errorf("invalid: records: %s/%s holds %s/%s", one.application,
			one.environment, spec.Application, spec.Environment))
	}
	if one.revision, err = spec.Revision(); err != nil {
		return fail(c.diagnostic, err)
	}
	release, err := kernel.DecodeRelease(one.release)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if release.Application != one.application {
		return fail(c.diagnostic, fmt.Errorf("invalid: records: %s/%s holds a release for %s", one.application,
			one.environment, release.Application))
	}
	one.image = release.Artifact.Image
	return 0
}

// instanceFolders is every app folder under root that holds a staging instance record, in name order.
func instanceFolders(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(root, entry.Name(), stagingDir, instanceFile))
		if err == nil && !info.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
