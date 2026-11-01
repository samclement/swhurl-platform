package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// create states a new app's first instance from the image its repository publishes: the records it is, and nothing
// else. The compiled files, the unit and the instance's place on the proxy are the whole tree's, so the next render
// decides them.

// createNote is what the document says the operator still has to do. A create that wrote the compiled files would
// compile against an allocation the instance is not in yet, which is why the render that allocates comes after it.
const createNote = "the records only: the next make v2-render writes the compiled files, the unit and the instance's " +
	"proxy port, and the unit's one line in clusters/home/kustomization.yaml is a reviewed edit"

// createDocument is the report: the records' identities, where the image came from, and every file written.
type createDocument struct {
	Kind        string           `json:"kind"`
	Version     int              `json:"version"`
	Application string           `json:"application"`
	Environment string           `json:"environment"`
	Source      createSource     `json:"source"`
	Image       createImage      `json:"image"`
	Identities  createIdentities `json:"identities"`
	Public      string           `json:"public"`
	Files       []files.Outcome  `json:"files"`
	Note        string           `json:"note"`
}

type createSource struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}

type createImage struct {
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
}

type createIdentities struct {
	App      string `json:"app"`
	Release  string `json:"release"`
	Instance string `json:"instance"`
}

// create writes a new app's staging records from the image it publishes, with a freshly minted broker identity.
func (c cli) create(r request) int {
	if c.appTreeRefused(r.out) {
		return 2
	}
	if status := c.appAbsent(r.records, r.app); status != 0 {
		return status
	}
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return status
	}
	root, err := underOut(r.out, r.records)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	seed, err := c.seed()
	if err != nil {
		fmt.Fprintln(c.diagnostic, "io: randomness: cannot read a seed")
		return 1
	}
	result, err := orchestration.Create(context.Background(), orchestration.CreateInputs{
		Application: r.app, Team: r.team, Image: r.image, Profile: profile, RecordsRoot: root,
		Exposure: r.exposure, Seed: seed}, c.registries, r.out, c.writer, c.encrypter)
	if err != nil {
		return c.wrote(result.Written, err)
	}
	document, err := encodeCreate(result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, createSummary(result))
	return emit(c.out, c.diagnostic, document)
}

// appAbsent refuses an app that already has records: a create states what an app starts with, so a second one would
// overwrite a release an instance may be selecting.
func (c cli) appAbsent(records, application string) int {
	folder := filepath.Join(records, application)
	entries, err := os.ReadDir(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: records: cannot read %s\n", folder)
		return 1
	}
	if len(entries) == 0 {
		return 0
	}
	return c.failure(&orchestration.Refusal{Subject: "app", Reason: "the_app_already_has_records"})
}

// encodeCreate is the report of one creation.
func encodeCreate(result orchestration.Created) ([]byte, error) {
	revision, err := result.Instance.Revision()
	if err != nil {
		return nil, err
	}
	app, err := result.App.ID()
	if err != nil {
		return nil, err
	}
	document := createDocument{Kind: "v2-create", Version: 1, Application: result.Instance.Application,
		Environment: result.Instance.Environment,
		Source:      createSource{Repository: result.Release.Source.Repository, Commit: result.Release.Source.Commit},
		Image:       createImage{Tag: result.Tag, Digest: result.Release.Artifact.Digest},
		Identities: createIdentities{App: string(app), Release: string(result.Release.ReleaseID),
			Instance: string(revision)},
		Public: result.Public, Files: nonNilOutcomes(result.Written), Note: createNote}
	return encodeDocument(document)
}

// createSummary is the one line the run's log carries: what was written, and what the operator runs next.
func createSummary(result orchestration.Created) string {
	return fmt.Sprintf("create: %s/%s wrote %d file(s), release %s, instance %s; run make v2-render next",
		result.Instance.Application, result.Instance.Environment, len(result.Written),
		short(string(result.Release.ReleaseID)), short(string(result.Instance.Release)))
}
