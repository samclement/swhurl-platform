package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

func (c cli) statusAll(r request) int {
	instances, shared, status := c.allStatusInputs(r)
	if status != 0 {
		return status
	}
	maxAge, status := c.maxAge(r)
	if status != 0 {
		return status
	}
	inputs, status := c.liveStatusInputs(r, shared.Profile, maxAge)
	if status != 0 {
		return status
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	result, err := orchestration.StatusAll(ctx, inputs, instances)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if !r.json {
		text := []string{}
		for _, instance := range result.Instances {
			text = append(text, statusText(instance.Application, instance.Environment, instance.Report))
		}
		if len(text) == 0 {
			text = append(text, "No recorded instances.")
		}
		return emit(c.out, c.diagnostic, []byte(strings.Join(text, "\n\n")))
	}
	data, err := observe.EncodeCollection(result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, data)
}

// allStatusInputs uses the existing record-folder enumeration and shared app reader, never live
// candidates as an inventory. Shared bytes are read once for this pinned bundle.
func (c cli) allStatusInputs(r request) ([]orchestration.Inputs, orchestration.Inputs, int) {
	shared, status := c.statusShared(r)
	if status != 0 {
		return nil, shared, status
	}
	entries, err := os.ReadDir(r.records)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: records: cannot read %s\n", r.records)
		return nil, shared, 1
	}
	apps := map[string][]byte{}
	instances := []orchestration.Inputs{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		environments, err := appEnvironments(r.records, entry.Name())
		if err != nil {
			fmt.Fprintf(c.diagnostic, "io: records: cannot read %s\n", filepath.Join(r.records, entry.Name()))
			return nil, shared, 1
		}
		for _, environment := range environments {
			one, status := c.readInstance(r.records, entry.Name(), environment, apps)
			if status != 0 {
				return nil, shared, status
			}
			inputs := shared
			inputs.Instance, inputs.Release, inputs.App = one.instance, one.release, one.app
			instances = append(instances, inputs)
		}
	}
	return instances, shared, 0
}

func (c cli) statusShared(r request) (orchestration.Inputs, int) {
	inputs := orchestration.Inputs{}
	for _, read := range []struct {
		path, what string
		target     *[]byte
	}{
		{r.profile, "profile", &inputs.Profile}, {r.callFile, "calls", &inputs.Calls}, {r.egressFile, "egress", &inputs.Egress},
	} {
		if read.path == "" {
			continue
		}
		data, status := c.read(read.path, read.what)
		if status != 0 {
			return inputs, status
		}
		*read.target = data
	}
	if _, err := kernel.DecodeProfile(inputs.Profile); err != nil {
		return inputs, fail(c.diagnostic, err)
	}
	return inputs, c.statusSharedDocuments(inputs)
}

func (c cli) statusSharedDocuments(inputs orchestration.Inputs) int {
	if inputs.Calls != nil {
		if _, err := kernel.ConnectionsOf(inputs.Calls, "", ""); err != nil {
			return fail(c.diagnostic, err)
		}
	}
	if inputs.Egress != nil {
		if _, err := kernel.DecodeEgressDocument(inputs.Egress); err != nil {
			return fail(c.diagnostic, err)
		}
	}
	return 0
}

func (c cli) liveStatusInputs(r request, profile []byte, maxAge time.Duration) (orchestration.StatusInputs, int) {
	inputs := orchestration.StatusInputs{Revision: r.revision, Now: time.Now, MaxAge: maxAge}
	if !commitRevision.MatchString(r.revision) {
		return inputs, fail(c.diagnostic, fmt.Errorf("invalid: revision: expected_a_commit"))
	}
	status := c.statusChanges(r, &inputs)
	if status != 0 {
		return inputs, status
	}
	connection, source, status := c.connection(r)
	if status != 0 {
		return inputs, status
	}
	reader, err := connection.Reader()
	if err != nil {
		return inputs, fail(c.diagnostic, err)
	}
	if r.source != "" {
		source = r.source
	}
	inputs.Reader, inputs.Source = reader, source
	if r.archive {
		inputs.Archive = c.archiveLister(r, profile)
	}
	return inputs, 0
}

// statusChanges maps extraction paths back to the repository namespace explicitly, never guessing a
// basename or a v1 path. Outside-root inputs cannot prove relevance and are refused with --changes.
func (c cli) statusChanges(r request, inputs *orchestration.StatusInputs) int {
	if r.changes == "" {
		return 0
	}
	data, status := c.read(r.changes, "changes")
	if status != 0 {
		return status
	}
	changes, err := observe.DecodeChanges(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	root := r.repoRoot
	if root == "" {
		root = "."
	}
	records, err := repositoryRelative(root, r.records)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	inputs.Changes, inputs.RecordsRoot = &changes, records
	for _, path := range []string{r.profile, r.callFile, r.egressFile} {
		if path == "" {
			continue
		}
		relative, err := repositoryRelative(root, path)
		if err != nil {
			return fail(c.diagnostic, err)
		}
		inputs.SharedPaths = append(inputs.SharedPaths, relative)
	}
	return 0
}

func repositoryRelative(root, path string) (string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return underOut(absoluteRoot, absolutePath)
}
