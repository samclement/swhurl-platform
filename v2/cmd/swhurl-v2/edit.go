package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/apps"
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
	AllocationStale bool            `json:"allocationStale"`
	Instances       []string        `json:"instances,omitempty"`
	Files           []files.Outcome `json:"files"`
	Note            string          `json:"note,omitempty"`
}

// ResolutionStaleNote is what a relationship change leaves to the operator: the resolution is decided from a pinned
// revision of every instance, so it is regenerated from the edited tree rather than inside one record's change.
const resolutionStaleNote = "the resolution is older than this change: regenerate v2-records/calls.json from the " +
	"edited tree and compile again before committing"

// allocationStaleNote is what an egress change leaves to the operator: the allocation gives the instance its proxy port
// and copies its grants into the proxy's configuration, and it is the whole tree's render that decides both.
const allocationStaleNote = "the egress allocation is older than this change: run `make v2-render` to give the instance " +
	"its port and render the proxy's configuration and the instance's annotation before committing"

// edit applies one named change to one v2-owned instance and writes the record it changed with the files it renders.
func (c cli) edit(r request) int {
	setting, err := apps.ParseSetting(r.change)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if c.appTreeRefused(r.out) {
		return 2
	}
	if setting.AppLevel() {
		return c.setTeam(r, setting)
	}
	result, err := apps.EditIn(r.checkout(), r.app, r.env, setting, c.writer)
	if err != nil {
		return c.wrote(result.Written, err)
	}
	document, err := encodeEdit(result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, result.Summary())
	if result.ResolutionStale {
		fmt.Fprintln(c.diagnostic, resolutionStaleNote)
	}
	if result.AllocationStale {
		fmt.Fprintln(c.diagnostic, allocationStaleNote)
	}
	return emit(c.out, c.diagnostic, document)
}

// setTeam applies the app-level change: the app record's team, and every environment's files with the label it renders.
func (c cli) setTeam(r request, setting apps.Setting) int {
	result, err := apps.SetTeamIn(r.checkout(), r.app, setting.Team, c.writer)
	if err != nil {
		return c.wrote(result.Written, err)
	}
	document, err := encodeTeam(result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, result.Summary())
	return emit(c.out, c.diagnostic, document)
}

// checkout is where a command over the records tree finds its files: --out, --records and the documents it names.
func (r request) checkout() files.Checkout {
	return files.Checkout{Root: r.out, Records: r.records, Profile: r.profile, Calls: r.callFile, Egress: r.egressFile,
		Previous: r.previous}
}

// underOut is a path as the destination addresses it: a records tree outside --out is refused before anything is read.
func underOut(out, path string) (string, error) {
	return files.Checkout{Root: out}.Relative(path)
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
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return names, nil
}

// encodeEdit is the report of one applied change.
func encodeEdit(result apps.Edition) ([]byte, error) {
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
		AllocationStale: result.AllocationStale, Files: nonNilOutcomes(result.Written)}
	if result.ResolutionStale {
		document.Note = resolutionStaleNote
	}
	if result.AllocationStale {
		document.Note = allocationStaleNote
	}
	return encodeDocument(document)
}

// encodeTeam is the report of one applied app-level change.
func encodeTeam(result apps.TeamChange) ([]byte, error) {
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
