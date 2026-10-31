package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// editDocument is the versioned report `edit` writes: the named change, the instance's revision before and after it,
// the files it wrote, and whether the committed resolution is now older than the record.
type editDocument struct {
	Kind            string          `json:"kind"`
	Version         int             `json:"version"`
	Change          string          `json:"change"`
	Application     string          `json:"application"`
	Environment     string          `json:"environment"`
	Before          string          `json:"before"`
	After           string          `json:"after"`
	ResolutionStale bool            `json:"resolutionStale"`
	Instances       []string        `json:"instances,omitempty"`
	Files           []files.Outcome `json:"files"`
	Note            string          `json:"note,omitempty"`
}

// editChange is one named change as the command line states it: the setting's name, and the kernel rule bound to the
// value the operator gave. Team is the one app-level setting, so it is applied to every environment of the app.
type editChange struct {
	name     string
	appLevel bool
	team     string
	apply    func(kernel.InstanceSpec, kernel.ReleaseBundle) (kernel.InstanceSpec, error)
}

// ResolutionStaleNote is what a relationship change leaves to the operator: the resolution is decided from a pinned
// revision of every instance, so it is regenerated from the edited tree rather than inside one record's change.
const resolutionStaleNote = "the resolution is older than this change: regenerate v2-records/calls.json from the " +
	"edited tree and compile again before committing"

// editSettings is every setting an edit names, with the parser that reads its value. A setting v2 does not model is
// named too, so it is refused by name instead of being mistaken for another fact: an egress grant is a record field
// v2 does not have, and an alert is a platform document it does not render yet.
var editSettings = map[string]func(string) (editChange, error){
	"replicas":      instanceSetting("replicas", kernel.WithReplicas),
	"memoryMiB":     instanceSetting("memoryMiB", kernel.WithMemory),
	"exposure":      releaseSetting("exposure", kernel.WithExposure),
	"calls":         listSetting("calls", kernel.WithCalls),
	"allows":        listSetting("allows", kernel.WithAllows),
	"subscriptions": listSetting("subscriptions", kernel.WithSubscriptions),
	"team":          teamSetting,
	"egress":        notModelled("egress"),
	"alerts":        notModelled("alerts"),
}

// editChangeOf parses `name=value` into the kernel rule it names.
func editChangeOf(setting string) (editChange, error) {
	name, value, found := strings.Cut(setting, "=")
	if !found {
		return editChange{}, fmt.Errorf("invalid: set: expected name=value")
	}
	build, known := editSettings[name]
	if !known {
		return editChange{}, fmt.Errorf("invalid: set: unknown setting %q", name)
	}
	return build(value)
}

// instanceSetting is a change whose rule reads only the record.
func instanceSetting(name string, rule func(kernel.InstanceSpec, int32) (kernel.InstanceSpec, error)) func(string) (editChange, error) {
	return func(value string) (editChange, error) {
		count, err := editInteger(name, value)
		if err != nil {
			return editChange{}, err
		}
		return editChange{name: name,
			apply: func(spec kernel.InstanceSpec, _ kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
				return rule(spec, count)
			}}, nil
	}
}

// releaseSetting is a change the release has a say in: the kind decides whether the setting is meaningful at all.
func releaseSetting(name string, rule func(kernel.InstanceSpec, kernel.ReleaseBundle, string) (kernel.InstanceSpec, error)) func(string) (editChange, error) {
	return func(value string) (editChange, error) {
		return editChange{name: name, apply: func(spec kernel.InstanceSpec, release kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
			return rule(spec, release, value)
		}}, nil
	}
}

// listSetting is a change whose value is a declaration list: names separated by commas, and an empty value clears it.
func listSetting(name string, rule func(kernel.InstanceSpec, []string) (kernel.InstanceSpec, error)) func(string) (editChange, error) {
	return func(value string) (editChange, error) {
		names := editList(value)
		return editChange{name: name,
			apply: func(spec kernel.InstanceSpec, _ kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
				return rule(spec, names)
			}}, nil
	}
}

// teamSetting is the one app-level change: it names no environment and reaches every one of the app's.
func teamSetting(value string) (editChange, error) {
	if value == "" {
		return editChange{}, fmt.Errorf("invalid: team: expected a team name")
	}
	return editChange{name: "team", appLevel: true, team: value}, nil
}

// notModelled refuses a named setting v2 has no record field or document for.
func notModelled(name string) func(string) (editChange, error) {
	return func(string) (editChange, error) {
		return editChange{}, &orchestration.Refusal{Subject: name, Reason: "not_modelled"}
	}
}

// editInteger is one integer setting: its range is the record's own rule, so this only refuses what is not a number.
func editInteger(name, value string) (int32, error) {
	count, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid: %s: expected an integer", name)
	}
	return int32(count), nil
}

// editList is a declaration list: names separated by commas, and an empty value clears the list.
func editList(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ",")
}

// recordsEdit is one instance's whole folder as an edit reads it: its record pair, the app record they share, its seed
// when it has one, and the documents it publishes.
type recordsEdit struct {
	application, environment       string
	instancePath                   string
	instance, release, app, secret []byte
	offers                         []kernel.File
}

// edit applies one named change to one v2-owned instance and writes the record it changed with the files it renders.
func (c cli) edit(r request) int {
	change, err := editChangeOf(r.change)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if c.appTreeRefused(r.out) {
		return 2
	}
	if change.appLevel {
		return c.setTeam(r, change)
	}
	one, status := c.recordsEdit(r.out, r.records, r.app, r.env)
	if status != 0 {
		return status
	}
	inputs, status := c.editInputs(r, one)
	if status != 0 {
		return status
	}
	result, err := orchestration.Edit(orchestration.Editable{
		InstanceInputs: orchestration.InstanceInputs{Inputs: inputs, InstancePath: one.instancePath},
		Change:         change.name,
		Apply:          change.apply,
	}, r.out, c.writer)
	if err != nil {
		return c.wrote(result.Written, err)
	}
	document, err := encodeEdit(result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintf(c.diagnostic, "%s/%s %s: %d files\n", result.Application, result.Environment, change.name,
		len(result.Written))
	if result.ResolutionStale {
		fmt.Fprintln(c.diagnostic, resolutionStaleNote)
	}
	return emit(c.out, c.diagnostic, document)
}

// setTeam applies the app-level change: the app record's team, and every environment's files with the label it renders.
func (c cli) setTeam(r request, change editChange) int {
	environments, err := appEnvironments(r.records, r.app)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: records: cannot read %s\n", filepath.Join(r.records, r.app))
		return 1
	}
	appPath, err := underOut(r.out, filepath.Join(r.records, r.app, appFile))
	if err != nil {
		return fail(c.diagnostic, err)
	}
	app, status := c.read(filepath.Join(r.records, r.app, appFile), "app")
	if status != 0 {
		return status
	}
	inputs := orchestration.TeamInputs{App: app, AppPath: appPath, Team: change.team}
	for _, environment := range environments {
		one, status := c.recordsEdit(r.out, r.records, r.app, environment)
		if status != 0 {
			return status
		}
		instanceInputs, status := c.editInputs(r, one)
		if status != 0 {
			return status
		}
		inputs.Instances = append(inputs.Instances,
			orchestration.InstanceInputs{Inputs: instanceInputs, InstancePath: one.instancePath})
	}
	result, err := orchestration.SetTeam(inputs, r.out, c.writer)
	if err != nil {
		return c.wrote(result.Written, err)
	}
	document, err := encodeTeam(result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintf(c.diagnostic, "%s team %s: %d instances, %d files\n", result.Application, result.After.Team,
		len(result.Instances), len(result.Written))
	return emit(c.out, c.diagnostic, document)
}

// recordsEdit reads one instance's own folder, with everything a write of it needs. A folder that holds a seed or
// offered documents keeps them: a write either carries them or is refused by the compiler, never drops them.
func (c cli) recordsEdit(out, root, application, environment string) (recordsEdit, int) {
	folder := filepath.Join(root, application, environment)
	instancePath, err := underOut(out, filepath.Join(folder, instanceFile))
	if err != nil {
		return recordsEdit{}, fail(c.diagnostic, err)
	}
	one := recordsEdit{application: application, environment: environment, instancePath: instancePath}
	what := application + "/" + environment
	var status int
	if one.instance, status = c.read(filepath.Join(folder, instanceFile), what+" instance"); status != 0 {
		return recordsEdit{}, status
	}
	if one.release, status = c.read(filepath.Join(folder, releaseFile), what+" release"); status != 0 {
		return recordsEdit{}, status
	}
	if one.app, status = c.read(filepath.Join(root, application, appFile), "app"); status != 0 {
		return recordsEdit{}, status
	}
	if one.secret, status = c.readPresent(filepath.Join(folder, kernel.EventsSecretFile), what+" events secret"); status != 0 {
		return recordsEdit{}, status
	}
	offers := filepath.Join(folder, "offers")
	if _, err := os.Stat(offers); err == nil {
		if one.offers, status = c.offersAt(offers); status != 0 {
			return recordsEdit{}, status
		}
	}
	return one, 0
}

// editInputs reads the documents a compilation of the edited record takes.
func (c cli) editInputs(r request, one recordsEdit) (orchestration.Inputs, int) {
	inputs := orchestration.Inputs{Instance: one.instance, Release: one.release, App: one.app,
		EventsSecret: one.secret, Offers: one.offers}
	for _, read := range []struct {
		path   string
		what   string
		target *[]byte
	}{
		{r.profile, "profile", &inputs.Profile},
		{r.callFile, "", &inputs.Calls},
		{r.egressFile, "", &inputs.Egress},
	} {
		if read.path == "" {
			continue
		}
		data, status := c.read(read.path, read.what)
		if status != 0 {
			return orchestration.Inputs{}, status
		}
		*read.target = data
	}
	return inputs, 0
}

// underOut is a path as the destination addresses it. An edit writes what it read, and a writer resolves only the paths
// inside the destination it was given, so the records tree is named relative to --out — "v2-records" for a checkout —
// and a tree outside it is refused before anything is read.
func underOut(out, path string) (string, error) {
	relative, err := filepath.Rel(filepath.Clean(out), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid: records: the records tree must be under the destination --out")
	}
	return filepath.ToSlash(relative), nil
}

// readPresent reads a file an instance may not have, and answers nothing when it is absent.
func (c cli) readPresent(path, what string) ([]byte, int) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, 0
	}
	return c.read(path, what)
}

// appEnvironments is every environment folder under one app's records that holds an instance.
func appEnvironments(root, application string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, application))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, application, entry.Name(), instanceFile)); err == nil {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// encodeEdit is the report of one applied change.
func encodeEdit(result orchestration.Edition) ([]byte, error) {
	before, err := result.Before.Revision()
	if err != nil {
		return nil, err
	}
	after, err := result.After.Revision()
	if err != nil {
		return nil, err
	}
	document := editDocument{Kind: "v2-edit", Version: 1, Change: result.Change,
		Application: result.Application, Environment: result.Environment,
		Before: string(before), After: string(after), ResolutionStale: result.ResolutionStale,
		Files: nonNilOutcomes(result.Written)}
	if result.ResolutionStale {
		document.Note = resolutionStaleNote
	}
	return encodeDocument(document)
}

// encodeTeam is the report of one applied app-level change.
func encodeTeam(result orchestration.TeamChange) ([]byte, error) {
	previous, err := result.Before.ID()
	if err != nil {
		return nil, err
	}
	after, err := result.After.ID()
	if err != nil {
		return nil, err
	}
	instances := make([]string, 0, len(result.Instances))
	for _, spec := range result.Instances {
		revision, err := spec.Revision()
		if err != nil {
			return nil, err
		}
		instances = append(instances, string(revision))
	}
	document := editDocument{Kind: "v2-edit", Version: 1, Change: "team", Application: result.Application,
		Before: string(previous), After: string(after), Instances: instances, Files: nonNilOutcomes(result.Written)}
	return encodeDocument(document)
}

// encodeDocument is a report's bytes: compact keys in field order, no HTML escaping, and a trailing newline.
func encodeDocument(document any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// nonNilOutcomes is a report's file list: empty rather than absent, so a reader sees "nothing" instead of "unknown".
func nonNilOutcomes(outcomes []files.Outcome) []files.Outcome {
	if outcomes == nil {
		return []files.Outcome{}
	}
	return outcomes
}
