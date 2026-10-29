package legacy_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/legacy"
)

// commit is a full commit whose prefix the fixture's records can abbreviate.
const commit = "9f3ab21c0d4e5f60718293a4b5c6d7e8f9012345"

// migrate runs the migration over the fixture, which states a web workload and no database.
func migrate(t *testing.T, supplied legacy.Supplied) legacy.Migration {
	t.Helper()
	migration, err := legacy.Migrate(records(t), supplied)
	if err != nil {
		t.Fatal(err)
	}
	return migration
}

// supplied agrees with the fixture's image name; its commit cannot be checked, because the fixture's tag is
// not an abbreviated commit.
func supplied() legacy.Supplied {
	return legacy.Supplied{Repository: "nginxinc/nginx-unprivileged", Commit: commit}
}

// withRecords changes the three legacy records through mutate, one visible change each.
func withRecords(t *testing.T, changeRelease, changeInstance func(map[string]any)) legacy.Records {
	t.Helper()
	documents := records(t)
	if changeRelease != nil {
		documents.Release = mutate(t, documents.Release, changeRelease)
	}
	if changeInstance != nil {
		documents.Instance = mutate(t, documents.Instance, changeInstance)
	}
	return documents
}

// gapFacts is the gaps' facts, for asserting what the record pair cannot carry.
func gapFacts(migration legacy.Migration) []string {
	facts := make([]string, 0, len(migration.Gaps))
	for _, gap := range migration.Gaps {
		facts = append(facts, gap.Fact)
	}
	return facts
}

// The migration records exactly what the fixture states, with a source for every fact and the tag, the team
// and the provenance named as facts the pair cannot carry.
func TestMigrateRecordsWhatTheRecordsState(t *testing.T) {
	migration := migrate(t, supplied())
	if migration.Application != "hello" || migration.Environment != "staging" {
		t.Fatalf("identity %s/%s", migration.Application, migration.Environment)
	}
	if migration.Source != (legacy.Source{Repository: "nginxinc/nginx-unprivileged", Commit: commit}) {
		t.Fatalf("source %+v", migration.Source)
	}
	if len(migration.Corroborated) != 0 {
		t.Fatalf("nothing in the fixture abbreviates the commit: %v", migration.Corroborated)
	}
	want := legacy.Workload{Image: "docker.io/nginxinc/nginx-unprivileged", Tag: "1.27-alpine",
		Digest: "sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0",
		Kind:   "web", Port: 8080, HealthPath: "/", UID: 101}
	if migration.Workload != want {
		t.Fatalf("workload %+v", migration.Workload)
	}
	if migration.Instance.Replicas != 1 || migration.Instance.MemoryMiB != 128 || migration.Instance.Exposure != "authenticated-web" {
		t.Fatalf("instance %+v", migration.Instance)
	}
	if migration.Database != "" || migration.Claim != "" {
		t.Fatalf("a stateless instance asks for neither: %q %q", migration.Database, migration.Claim)
	}
	if migration.Sources["source.repository"] != "supplied" || migration.Sources["instance.memory"] != "committed HelmRelease" {
		t.Fatalf("sources %v", migration.Sources)
	}
	facts := gapFacts(migration)
	for _, want := range []string{"release.yaml.image.tag", "image.provenance"} {
		if !slices.Contains(facts, want) {
			t.Errorf("the gap list does not name %q: %v", want, facts)
		}
	}
	for _, gap := range migration.Gaps {
		if gap.Owner == "" || gap.Where == "" {
			t.Errorf("a gap without an owner or a place: %+v", gap)
		}
	}
}

// The supplied commit is checked against what the records abbreviate — the tag's hex suffix and the commit
// the offered documents were read at — and never expanded from it.
func TestMigrateCorroboratesTheSuppliedSource(t *testing.T) {
	documents := withRecords(t, func(release map[string]any) {
		release["image"].(map[string]any)["tag"] = "12-" + commit[:7]
		release["offers"] = commit[:7]
	}, nil)
	migration, err := legacy.Migrate(documents, supplied())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"release.yaml: image.tag", "release.yaml: offers"}; !slices.Equal(migration.Corroborated, want) {
		t.Fatalf("corroborated by %v", migration.Corroborated)
	}
	// A tag that is the whole commit corroborates it on its own.
	documents = withRecords(t, func(release map[string]any) {
		release["image"].(map[string]any)["tag"] = commit
	}, nil)
	if migration, err = legacy.Migrate(documents, supplied()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"release.yaml: image.tag"}; !slices.Equal(migration.Corroborated, want) {
		t.Fatalf("corroborated by %v", migration.Corroborated)
	}
}

// A source the records contradict is a refusal naming the field, never a migration that records a commit
// nothing in the records supports.
func TestMigrateRefusesASourceTheRecordsContradict(t *testing.T) {
	abbreviated := withRecords(t, func(release map[string]any) {
		release["image"].(map[string]any)["tag"] = "12-" + commit[:7]
		release["offers"] = commit[:7]
	}, nil)
	for _, tt := range []struct {
		name      string
		documents legacy.Records
		supplied  legacy.Supplied
		want      string
	}{
		{"repository", records(t), legacy.Supplied{Repository: "nginxinc/other", Commit: commit},
			"invalid: supplied: source.repository: does_not_match_the_image_name"},
		{"tag", abbreviated, legacy.Supplied{Repository: "nginxinc/nginx-unprivileged", Commit: strings.Repeat("a", 40)},
			"invalid: supplied: source.commit: does_not_match_the_tag"},
		{"missing", records(t), legacy.Supplied{Repository: "nginxinc/nginx-unprivileged"},
			"missing: supplied: source: the_records_keep_no_full_commit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := legacy.Migrate(tt.documents, tt.supplied)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	// The offered documents' own abbreviation is checked too: a tag that agrees does not excuse a mismatch.
	offersOnly := withRecords(t, func(release map[string]any) { release["offers"] = "0123456" }, nil)
	if _, err := legacy.Migrate(offersOnly, supplied()); err == nil ||
		!strings.Contains(err.Error(), "does_not_match_offers") {
		t.Fatalf("error = %v", err)
	}
}

// A database instance adopts the claim it already has: named by the release, or generated and named after the
// release by the pinned chart. A release that states no volume has no claim to adopt, which is a refusal.
func TestMigrateKeepsTheDatabaseOnItsClaim(t *testing.T) {
	documents := withRecords(t, func(release map[string]any) { release["database"] = "sqlite" }, nil)
	generated := mutate(t, documents.Committed, func(committed map[string]any) {
		values(committed)["persistence"] = map[string]any{"data": map[string]any{"type": "persistentVolumeClaim"}}
	})
	documents.Committed = generated
	migration, err := legacy.Migrate(documents, supplied())
	if err != nil {
		t.Fatal(err)
	}
	if migration.Database != "sqlite" || migration.Claim != "hello" {
		t.Fatalf("database %q claim %q", migration.Database, migration.Claim)
	}
	if !strings.Contains(migration.Sources["storage.claimName"], "metadata.name") {
		t.Fatalf("claim source %q", migration.Sources["storage.claimName"])
	}
	if slices.Contains(gapFacts(migration), "release.yaml.database") {
		t.Fatal("the database is carried, so it is not a gap")
	}

	documents.Committed = mutate(t, documents.Committed, func(committed map[string]any) {
		values(committed)["persistence"].(map[string]any)["data"].(map[string]any)["existingClaim"] = "hello-data"
	})
	if migration, err = legacy.Migrate(documents, supplied()); err != nil {
		t.Fatal(err)
	}
	if migration.Claim != "hello-data" || !strings.Contains(migration.Sources["storage.claimName"], "existingClaim") {
		t.Fatalf("claim %q source %q", migration.Claim, migration.Sources["storage.claimName"])
	}

	documents.Committed = mutate(t, records(t).Committed, func(committed map[string]any) {
		delete(values(committed), "persistence")
	})
	if _, err := legacy.Migrate(documents, supplied()); err == nil ||
		err.Error() != "missing: committed HelmRelease: values.persistence.data: the_database_claim_is_not_stated" {
		t.Fatalf("error = %v", err)
	}
}

// A legacy secret has no v2 binding, so migrating the instance would change what the app can do: a refusal,
// not a gap.
func TestMigrateRefusesSecrets(t *testing.T) {
	documents := withRecords(t, func(release map[string]any) { release["secrets"] = []any{"api-key"} }, nil)
	_, err := legacy.Migrate(documents, supplied())
	if err == nil || err.Error() != "unsupported: release.yaml: secrets: bindings_are_not_modelled" {
		t.Fatalf("error = %v", err)
	}
	// An empty list is the record saying nothing, not a fact to carry.
	documents = withRecords(t, func(release map[string]any) { release["secrets"] = []any{} }, nil)
	if _, err := legacy.Migrate(documents, supplied()); err != nil {
		t.Fatal(err)
	}
}

// The predecessor's declarations are reported for comparison with the resolution and are not record facts,
// so they are neither carried nor gaps.
func TestMigrateReportsTheDeclarations(t *testing.T) {
	documents := withRecords(t, nil, func(instance map[string]any) {
		instance["calls"] = []any{"swhurl-try-10"}
		instance["allows"] = []any{"team:demo", "swhurl-try-11"}
	})
	migration, err := legacy.Migrate(documents, supplied())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(migration.Calls, []string{"swhurl-try-10"}) {
		t.Fatalf("calls %v", migration.Calls)
	}
	if !slices.Equal(migration.Allows, []string{"swhurl-try-11", "team:demo"}) {
		t.Fatalf("callers %v", migration.Allows)
	}
	for _, fact := range gapFacts(migration) {
		if strings.HasPrefix(fact, "instance.yaml.calls") || strings.HasPrefix(fact, "instance.yaml.allows") {
			t.Fatalf("a declaration is reported, not a gap: %s", fact)
		}
	}
}

// The refusals keep their order: the workload is checked before the supplied source, so a kind this v2 cannot
// render is reported as such even when the source is unusable too.
func TestMigrateChecksTheWorkloadBeforeTheSource(t *testing.T) {
	documents := withRecords(t, func(release map[string]any) {
		release["kind"] = "job"
		release["secrets"] = []any{"api-key"}
	}, nil)
	_, err := legacy.Migrate(documents, legacy.Supplied{})
	if err == nil || err.Error() != "unsupported: release.yaml: kind: only_web_and_worker" {
		t.Fatalf("error = %v", err)
	}
}
