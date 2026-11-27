package main

import (
	"encoding/json"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/delivery"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// promote moves one app's production to the release its staging selects, from the tree both instances keep their
// records in: --records DIR, --app, and the production environment --env.

const promoteKind = "v2-promote"

// promoteNote is what the document says about itself. A promotion's direction is not decided from the records: a
// release states no build order, so the two releases are stated and a person reads a rollback from them.
const promoteNote = "the release staging selects; production's own configuration, grants, calls, storage and broker identity are unchanged, and a promotion's direction is not decided from the records"

type promoteRelease struct {
	ReleaseID string `json:"releaseId"`
	Digest    string `json:"digest"`
	Commit    string `json:"commit"`
}

// promoteDocument is the report: the release staging selects, production's before it, and what was written.
type promoteDocument struct {
	Kind        string          `json:"kind"`
	Version     int             `json:"version"`
	Application string          `json:"application"`
	Environment string          `json:"environment"`
	Staging     promoteRelease  `json:"staging"`
	Before      promoteRelease  `json:"before"`
	Changed     bool            `json:"changed"`
	Identities  promoteIdentity `json:"identities"`
	Files       []files.Outcome `json:"files"`
	Note        string          `json:"note"`
}

type promoteIdentity struct {
	ReleaseID string `json:"releaseId"`
	Revision  string `json:"instanceRevision"`
}

func (c cli) promote(r request) int {
	if c.appTreeRefused(r.out) {
		return 2
	}
	result, err := delivery.PromoteIn(r.checkout(), r.app, r.env, kernel.InstanceRevision(r.expected), c.writer)
	if err != nil {
		for _, outcome := range result.Written {
			fmt.Fprintf(c.out, "%s %s\n", outcome.State, outcome.Path)
		}
		return c.failure(err)
	}
	revision, err := result.Instance.Revision()
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document := promoteDocument{Kind: promoteKind, Version: 1, Application: result.Instance.Application,
		Environment: result.Instance.Environment, Staging: describeRelease(result.Staging),
		Before: describeRelease(result.Before), Changed: result.Changed,
		Identities: promoteIdentity{ReleaseID: string(result.Release.ReleaseID), Revision: string(revision)},
		Files:      result.Written, Note: promoteNote}
	fmt.Fprintln(c.diagnostic, result.Summary())
	data, err := json.Marshal(document)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, data)
}

func describeRelease(release kernel.ReleaseBundle) promoteRelease {
	return promoteRelease{ReleaseID: string(release.ReleaseID), Digest: release.Artifact.Digest,
		Commit: release.Source.Commit}
}
