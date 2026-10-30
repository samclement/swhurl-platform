package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// deployNote is what the document says about itself: the image's own declaration reached v2's records and
// compiled files, and nothing else changed.
const deployNote = "the image's own declaration; v2's records and files only, and the unit's registration stays a reviewed edit"

// deploy brings one staging instance to the newest image its repository publishes, by the platform's own tag
// policy, and writes the record pair and the compiled files under --out.
func (c cli) deploy(r request) int {
	if r.records != "" {
		return c.deployRecords(r)
	}
	inputs, status := c.deployInputs(r)
	if status != 0 {
		return status
	}
	if c.appTreeRefused(r.out) {
		return 2
	}
	current, err := kernel.DecodeRelease(inputs.Release)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	images, err := c.registries(current.Artifact.Image)
	if err != nil {
		return c.failure(err)
	}
	result, err := orchestration.DeployImage(context.Background(), inputs, images, r.out, c.writer)
	if err != nil {
		// A write that stopped part-way says which files it completed; the document would not be an answer.
		for _, outcome := range result.Written {
			fmt.Fprintf(c.out, "%s %s\n", outcome.State, outcome.Path)
		}
		return c.failure(err)
	}
	document, err := encodeDeploy(result, current.Artifact.Digest)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, deploySummary(result))
	return emit(c.out, c.diagnostic, document)
}

// deployInputs reads what a delivery takes: the instance's records, the app record, the profile and the two
// optional documents, plus the revision the caller reviewed.
func (c cli) deployInputs(r request) (orchestration.DeployInputs, int) {
	inputs, status := c.inputs(r)
	if status != 0 {
		return orchestration.DeployInputs{}, status
	}
	return orchestration.DeployInputs{Instance: inputs.Instance, Release: inputs.Release, App: inputs.App,
		Profile: inputs.Profile, Calls: inputs.Calls, Egress: inputs.Egress,
		Expected: kernel.InstanceRevision(r.expected)}, 0
}

// deployDocument is the report's shape: the tag it read, the image it selected, the identities of what it
// wrote, and every file's outcome. A no-op says changed false and names no file.
type deployDocument struct {
	Kind        string           `json:"kind"`
	Version     int              `json:"version"`
	Application string           `json:"application"`
	Environment string           `json:"environment"`
	Tag         string           `json:"tag"`
	Image       deployImage      `json:"image"`
	Changed     bool             `json:"changed"`
	Identities  deployIdentities `json:"identities"`
	Files       []files.Outcome  `json:"files"`
	Note        string           `json:"note"`
}

// deployImage is the selected artifact and what the instance ran before it.
type deployImage struct {
	Digest   string `json:"digest"`
	Previous string `json:"previous"`
}

type deployIdentities struct {
	Release     string `json:"release"`
	Instance    string `json:"instance"`
	Compilation string `json:"compilation"`
}

func encodeDeploy(result orchestration.Deploy, previous string) ([]byte, error) {
	revision, err := result.Instance.Revision()
	if err != nil {
		return nil, err
	}
	document := deployDocument{
		Kind: "v2-deploy", Version: 1,
		Application: result.Instance.Application, Environment: result.Instance.Environment,
		Tag: result.Tag, Image: deployImage{Digest: result.Release.Artifact.Digest, Previous: previous},
		Changed: result.Changed,
		Identities: deployIdentities{Release: string(result.Release.ReleaseID), Instance: string(revision),
			Compilation: string(result.Compilation.ID)},
		Files: result.Written,
		Note:  deployNote,
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// deploySummary is the one line stderr carries; the document on stdout has the whole answer.
func deploySummary(result orchestration.Deploy) string {
	if !result.Changed {
		return fmt.Sprintf("deploy: %s/%s runs its newest image %s", result.Instance.Application,
			result.Instance.Environment, result.Tag)
	}
	return fmt.Sprintf("deploy: %s/%s: image %s (%s)",
		result.Instance.Application, result.Instance.Environment, result.Tag, short(result.Release.Artifact.Digest))
}
