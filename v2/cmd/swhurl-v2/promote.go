package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
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
	inputs, status := c.promoteInputs(r)
	if status != 0 {
		return status
	}
	result, err := orchestration.Promote(inputs, r.out, c.writer)
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
	fmt.Fprintln(c.diagnostic, promoteSummary(document))
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

// promoteSummary is the one line the run's log carries: what production selects now, and what changed.
func promoteSummary(document promoteDocument) string {
	if !document.Changed {
		return fmt.Sprintf("%s/%s: production already selects %s at %s", document.Application,
			document.Environment, document.Staging.Digest, document.Staging.Commit[:12])
	}
	return fmt.Sprintf("%s/%s: %s -> %s at %s", document.Application, document.Environment,
		document.Before.Digest, document.Staging.Digest, document.Staging.Commit[:12])
}

// promoteInputs reads the two instances, the app record, the profile and the two optional documents. A tree that
// does not hold them refuses before anything runs.
func (c cli) promoteInputs(r request) (orchestration.PromoteInputs, int) {
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return orchestration.PromoteInputs{}, status
	}
	calls, status := c.optional(r.callFile, "calls")
	if status != 0 {
		return orchestration.PromoteInputs{}, status
	}
	egress, status := c.optional(r.egressFile, "egress")
	if status != 0 {
		return orchestration.PromoteInputs{}, status
	}
	apps := map[string][]byte{}
	staging, status := c.readInstance(r.records, r.app, "staging", apps)
	if status != 0 {
		return orchestration.PromoteInputs{}, status
	}
	target, status := c.readInstance(r.records, r.app, r.env, apps)
	if status != 0 {
		return orchestration.PromoteInputs{}, status
	}
	app, seen := apps[r.app]
	if !seen {
		if app, status = c.read(filepath.Join(r.records, r.app, appFile), "app"); status != 0 {
			return orchestration.PromoteInputs{}, status
		}
	}
	offers, status := c.stagingOffers(r.records, r.app)
	if status != 0 {
		return orchestration.PromoteInputs{}, status
	}
	return orchestration.PromoteInputs{StagingInstance: staging.instance, StagingRelease: staging.release,
		Instance: target.instance, Release: target.release, App: app, Profile: profile, Calls: calls,
		Egress: egress, Offers: offers, Expected: kernel.InstanceRevision(r.expected)}, 0
}

// optional reads a document a command may be given, if it names one: an absent name is no document rather than a
// failure, and the use case's own decoder refuses what it needs.
func (c cli) optional(path, what string) ([]byte, int) {
	if path == "" {
		return nil, 0
	}
	return c.read(path, what)
}

// stagingOffers reads the documents staging's release carries, if it carries any: production inherits them with the
// release, written beside its records and compiled files as the delivery that produced staging wrote them. A
// staging instance that publishes nothing has no folder to carry.
func (c cli) stagingOffers(records, app string) ([]kernel.File, int) {
	folder := filepath.Join(records, app, "staging", offersFolder)
	if _, err := os.Stat(folder); err != nil {
		return nil, 0
	}
	return c.offersAt(folder)
}
