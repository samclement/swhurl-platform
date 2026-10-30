package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/legacy"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// migrationNote is what the document says about itself: it wrote v2's records and files, and nothing else.
const migrationNote = "v2's records and files only; v1's records, manifests and unit are untouched, and the unit's registration stays a reviewed edit"

// migrate translates one predecessor instance into v2's record pair, compiles the pair with the platform's
// documents, and writes both. The precedent's four records come from a pinned snapshot, and the source no
// record in this repository states comes from the operator, checked against what the records abbreviate.
func (c cli) migrate(r request) int {
	pinned, inputs, status := c.migrateInputs(r)
	if status != 0 {
		return status
	}
	if c.appTreeRefused(r.out) {
		return 2
	}
	result, err := orchestration.Migrate(inputs, r.out, c.writer)
	if err != nil {
		// A migration that stopped part-way says which files it did write before the failure.
		for _, outcome := range result.Written {
			fmt.Fprintf(c.out, "%s %s\n", outcome.State, outcome.Path)
		}
		return c.failure(err)
	}
	document, err := encodeMigration(pinned.Revision, result)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintln(c.diagnostic, migrationSummary(result))
	return emit(c.out, c.diagnostic, document)
}

// migrateInputs reads what a migration takes: one instance's four records out of the pinned snapshot, the
// source only the operator knows, the platform profile and the two optional documents.
func (c cli) migrateInputs(r request) (snapshot, orchestration.MigrationInputs, int) {
	data, status := c.read(r.snapshot, "snapshot")
	if status != 0 {
		return snapshot{}, orchestration.MigrationInputs{}, status
	}
	pinned, err := decodeSnapshot(data)
	if err != nil {
		return snapshot{}, orchestration.MigrationInputs{}, fail(c.diagnostic, err)
	}
	instance, err := pinnedInstance(pinned, r.app, r.env)
	if err != nil {
		return snapshot{}, orchestration.MigrationInputs{}, fail(c.diagnostic, err)
	}
	if len(instance.Missing) > 0 {
		fmt.Fprintf(c.diagnostic, "missing: snapshot: %s\n", instance.Missing[0])
		return snapshot{}, orchestration.MigrationInputs{}, 2
	}
	inputs := orchestration.MigrationInputs{
		Records: legacy.Records{App: instance.Records["app"], Instance: instance.Records["instance"],
			Release: instance.Records["release"], Committed: instance.Records["helmrelease"]},
		Supplied: legacy.Supplied{Repository: r.sourceRepository, Commit: r.sourceCommit},
	}
	for _, read := range []struct {
		path     string
		into     *[]byte
		what     string
		optional bool
	}{
		{r.profile, &inputs.Profile, "profile", false},
		{r.callFile, &inputs.Calls, "", true},
		{r.egressFile, &inputs.Egress, "", true},
		{r.eventsSecret, &inputs.EventsSecret, "events secret", true},
	} {
		if read.optional && read.path == "" {
			continue
		}
		data, status := c.read(read.path, read.what)
		if status != 0 {
			return snapshot{}, orchestration.MigrationInputs{}, status
		}
		*read.into = data
	}
	documents, status := c.offersAt(r.offers)
	if status != 0 {
		return snapshot{}, orchestration.MigrationInputs{}, status
	}
	inputs.Offers = documents
	return pinned, inputs, 0
}

// pinnedInstance is the one instance a migration is about, or a refusal naming what the snapshot lacks.
func pinnedInstance(pinned snapshot, application, environment string) (snapshotInstance, error) {
	for _, instance := range pinned.Instances {
		if instance.Application == application && instance.Environment == environment {
			return instance, nil
		}
	}
	return snapshotInstance{}, errors.New("missing: snapshot: instance_not_found")
}

// migrationDocument is the report's shape: field order is the document order, and a change raises Version.
// A fact's source is in Sources, never inferred from a value; a legacy fact the record pair cannot carry is
// a gap with the owner who must resolve it.
type migrationDocument struct {
	Kind          string                 `json:"kind"`
	Version       int                    `json:"version"`
	Application   string                 `json:"application"`
	Environment   string                 `json:"environment"`
	Snapshot      string                 `json:"snapshot"`
	Source        migrationSource        `json:"source"`
	Identities    migrationIdentities    `json:"identities"`
	Egress        *migrationEgress       `json:"egress"`
	Relationships migrationRelationships `json:"relationships"`
	Files         []files.Outcome        `json:"files"`
	Registrations []string               `json:"registrations"`
	Sources       map[string]string      `json:"sources"`
	Gaps          []gapDocument          `json:"gaps"`
	Note          string                 `json:"note"`
}

// migrationSource is the provenance the records only abbreviate: what was supplied, and which record fields
// corroborated it.
type migrationSource struct {
	Repository   string   `json:"repository"`
	Commit       string   `json:"commit"`
	Corroborated []string `json:"corroborated"`
}

type migrationIdentities struct {
	App         string `json:"app"`
	Release     string `json:"release"`
	Instance    string `json:"instance"`
	Compilation string `json:"compilation"`
}

// migrationEgress is the instance's place on the proxy, as the allocation gives it. It is null when the
// allocation names the instance nowhere, which is not a v2 record fact and is reported rather than assumed.
type migrationEgress struct {
	Port   int32    `json:"port"`
	Grants []string `json:"grants"`
}

// migrationRelationships is the resolution's edges for the instance beside the predecessor's own
// declarations, and the declarations the resolution does not connect.
type migrationRelationships struct {
	Calls           []migrationCall `json:"calls"`
	Callers         []string        `json:"callers"`
	DeclaredCalls   []string        `json:"declaredCalls"`
	DeclaredCallers []string        `json:"declaredCallers"`
	UnmetCalls      []string        `json:"unmetCalls"`
	UnmetCallers    []string        `json:"unmetCallers"`
}

type migrationCall struct {
	Callee string `json:"callee"`
	Port   int32  `json:"port"`
}

func encodeMigration(revision string, result orchestration.Migration) ([]byte, error) {
	instanceRevision, err := result.Instance.Revision()
	if err != nil {
		return nil, err
	}
	appIdentity, err := result.App.ID()
	if err != nil {
		return nil, err
	}
	document := migrationDocument{
		Kind: "v2-migration", Version: 2,
		Application: result.Facts.Application, Environment: result.Facts.Environment, Snapshot: revision,
		Source: migrationSource{Repository: result.Facts.Source.Repository, Commit: result.Facts.Source.Commit,
			Corroborated: nonNil(result.Facts.Corroborated)},
		Identities: migrationIdentities{App: string(appIdentity), Release: string(result.Release.ReleaseID),
			Instance: string(instanceRevision), Compilation: string(result.Compilation.ID)},
		Egress:        migrationPlace(result.Egress),
		Relationships: migrationEdges(result),
		Files:         result.Written,
		Registrations: nonNil(result.Compilation.Registrations),
		Sources:       result.Facts.Sources,
		Gaps:          make([]gapDocument, 0, len(result.Facts.Gaps)),
		Note:          migrationNote,
	}
	for _, gap := range result.Facts.Gaps {
		document.Gaps = append(document.Gaps, gapDocument{gap.Where, gap.Fact, gap.Owner})
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// migrationPlace is the instance's allocation, or nil when it has none.
func migrationPlace(egress *kernel.Egress) *migrationEgress {
	if egress == nil {
		return nil
	}
	return &migrationEgress{Port: egress.Port, Grants: nonNil(egress.Grants)}
}

// migrationEdges is the rendered pairs beside the predecessor's declarations and what it left unconnected.
func migrationEdges(result orchestration.Migration) migrationRelationships {
	edges := migrationRelationships{
		Calls:           make([]migrationCall, 0, len(result.Rendered.Calls)),
		Callers:         nonNil(result.Rendered.Callers),
		DeclaredCalls:   nonNil(result.Facts.Calls),
		DeclaredCallers: nonNil(result.Facts.Allows),
		UnmetCalls:      nonNil(result.UnmetCalls),
		UnmetCallers:    nonNil(result.UnmetCallers),
	}
	for _, call := range result.Rendered.Calls {
		edges.Calls = append(edges.Calls, migrationCall{Callee: call.Callee, Port: call.Port})
	}
	return edges
}

// migrationSummary is the one human line for stderr: what was written and what the operator must read.
func migrationSummary(result orchestration.Migration) string {
	summary := fmt.Sprintf("migrate: %s/%s wrote %d file(s), release %s, instance %s",
		result.Facts.Application, result.Facts.Environment, len(result.Written),
		short(string(result.Release.ReleaseID)), short(string(result.Compilation.Inputs.Instance)))
	if len(result.Facts.Gaps) > 0 {
		summary += fmt.Sprintf(", %d gap(s)", len(result.Facts.Gaps))
	}
	if len(result.UnmetCalls)+len(result.UnmetCallers) > 0 {
		summary += ", unresolved declarations reported"
	}
	return summary
}

// short is an identity for a one-line summary — its prefix and enough hex to recognise it — while the
// document has the whole value.
func short(identity string) string {
	prefix, digest, found := strings.Cut(identity, ":")
	if !found {
		prefix, digest = "", identity
	}
	if len(digest) > 12 {
		digest = digest[:12]
	}
	if prefix == "" {
		return digest
	}
	return prefix + ":" + digest
}
