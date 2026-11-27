package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/delivery"
	"github.com/samclement/swhurl-platform/v2/kernel"
	provisioningrules "github.com/samclement/swhurl-platform/v2/provisioning/rules"
)

// deploy records: every v2-owned staging instance, from the tree its records live in.

const (
	recordsKind  = "v2-deploy-records"
	instanceFile = kernel.InstanceRecordName
	releaseFile  = kernel.ReleaseRecordName
	appFile      = kernel.AppRecordName
	stagingDir   = "staging"
	offersFolder = files.OffersFolder
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
	inputs, status := c.recordsInputs(r)
	if status != 0 {
		return status
	}
	deploys, err := delivery.DeployAll(context.Background(), inputs, c.registries, r.out, c.writer)
	if err != nil {
		return c.failure(err)
	}
	document := deployRecordsDocument{Kind: recordsKind, Version: 1, Records: r.records,
		Instances: make([]deployRecordsEntry, 0, len(deploys)), Note: recordsNote}
	for _, one := range deploys {
		entry := recordsEntry(one)
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

// recordsEntry is how the report states one instance's delivery: an error is a failure when it would exit 1 and a
// refusal otherwise.
func recordsEntry(one delivery.InstanceDeploy) deployRecordsEntry {
	entry := deployRecordsEntry{Application: one.Application, Environment: one.Environment, Files: one.Deploy.Written}
	switch {
	case one.Err != nil:
		entry.Outcome, entry.Reason = outcomeRefused, one.Err.Error()
		if exitFor(one.Err) == 1 {
			entry.Outcome = outcomeFailed
		}
	case one.Deploy.Changed:
		entry.Outcome, entry.Tag, entry.ReleaseID = outcomeChanged, one.Deploy.Tag, string(one.Deploy.Release.ReleaseID)
	default:
		entry.Outcome, entry.Tag, entry.ReleaseID = outcomeUnchanged, one.Deploy.Tag, string(one.Deploy.Release.ReleaseID)
	}
	return entry
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

// recordsInputs reads the platform profile, the two optional documents and every instance's records. A tree that
// does not hold every record it names refuses the whole run: nothing is attempted from a half-read tree.
func (c cli) recordsInputs(r request) (delivery.DeployAllInputs, int) {
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return delivery.DeployAllInputs{}, status
	}
	calls, status := c.optional(r.callFile, "calls")
	if status != 0 {
		return delivery.DeployAllInputs{}, status
	}
	egress, status := c.optional(r.egressFile, "egress")
	if status != 0 {
		return delivery.DeployAllInputs{}, status
	}
	records, status := c.records(r.records)
	if status != 0 {
		return delivery.DeployAllInputs{}, status
	}
	return delivery.DeployAllInputs{Records: records, Profile: profile, Calls: calls, Egress: egress}, 0
}

// records is every instance the records tree holds, as the files adapter reads it. A tree that names an instance
// without its release or app record is refused whole (2); a record that cannot be read is an I/O failure (1).
func (c cli) records(root string) ([]files.InstanceRecords, int) {
	records, err := files.ReadRecords(root)
	if err != nil {
		return nil, c.failure(err)
	}
	return records, 0
}

// recordOf is one instance's records from the tree's, or a refusal when the tree holds no such instance.
func (c cli) recordOf(records []files.InstanceRecords, application, environment string) (files.InstanceRecords, int) {
	for _, one := range records {
		if one.Application == application && one.Environment == environment {
			return one, 0
		}
	}
	return files.InstanceRecords{}, fail(c.diagnostic, fmt.Errorf("invalid: records: %s/%s has no %s", application,
		environment, instanceFile))
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

// optional reads a document a command may be given, if it names one: an absent name is no document rather than a
// failure, and the use case's own decoder refuses what it needs.
func (c cli) optional(path, what string) ([]byte, int) {
	if path == "" {
		return nil, 0
	}
	return c.read(path, what)
}

// describeRecords reads what a delivery needs before it runs: the instance's own name and revision, and the image
// its registry is addressed by.
func (c cli) describeRecords(one *recordInstance) int {
	spec, release, err := provisioningrules.FolderHolds(one.application, one.environment, one.instance, one.release)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if one.revision, err = spec.Revision(); err != nil {
		return fail(c.diagnostic, err)
	}
	one.image = release.Artifact.Image
	return 0
}
