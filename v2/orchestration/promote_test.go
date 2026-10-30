package orchestration

import (
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// promoteInputs is the fixture pair: staging selects r42, production r41, both of demo-orders.
func promoteInputs(t *testing.T) PromoteInputs {
	t.Helper()
	prod, err := kernel.DecodeInstance(fixture(t, "prod.json"))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := prod.Revision()
	if err != nil {
		t.Fatal(err)
	}
	return PromoteInputs{StagingInstance: fixture(t, "staging.json"), StagingRelease: fixture(t, "r42.json"),
		Instance: fixture(t, "prod.json"), Release: fixture(t, "r41.json"), App: fixture(t, "app.json"),
		Profile: fixture(t, "profile.json"), Expected: revision}
}

func TestPromoteSelectsStagingsReleaseForProduction(t *testing.T) {
	inputs := promoteInputs(t)
	writer := filestest.New()
	result, err := Promote(inputs, t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := kernel.DecodeRelease(inputs.StagingRelease)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Release.ReleaseID != candidate.ReleaseID {
		t.Fatalf("changed %v, release %s, want %s", result.Changed, result.Release.ReleaseID, candidate.ReleaseID)
	}
	before, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if result.Instance.Configuration != before.Configuration || result.Instance.Storage != before.Storage ||
		result.Instance.Grants != before.Grants || len(result.Instance.Calls) != len(before.Calls) {
		t.Fatalf("production changed beyond its release: %+v", result.Instance)
	}
	release, err := kernel.DecodeRelease(writer.Contents["v2-records/demo-orders/prod/release.json"])
	if err != nil {
		t.Fatal(err)
	}
	if release.ReleaseID != candidate.ReleaseID || release.Artifact.Digest != candidate.Artifact.Digest {
		t.Fatalf("production's record states %s", release.ReleaseID)
	}
	instance, err := kernel.DecodeInstance(writer.Contents["v2-records/demo-orders/prod/instance.json"])
	if err != nil {
		t.Fatal(err)
	}
	if instance.Release != candidate.ReleaseID || instance.Configuration != before.Configuration {
		t.Fatalf("production's instance: %+v", instance)
	}
	helms := string(writer.Contents["v2-apps/instances/demo-orders/prod/helmrelease.json"])
	if !strings.Contains(helms, candidate.Artifact.Digest) {
		t.Error("the compiled production HelmRelease does not name the promoted digest")
	}
}

func TestPromoteIsANoOpForTheReleaseProductionAlreadySelects(t *testing.T) {
	inputs := promoteInputs(t)
	first := filestest.New()
	if _, err := Promote(inputs, t.TempDir(), first); err != nil {
		t.Fatal(err)
	}
	again := inputs
	again.Instance = first.Contents["v2-records/demo-orders/prod/instance.json"]
	again.Release = first.Contents["v2-records/demo-orders/prod/release.json"]
	prod, err := kernel.DecodeInstance(again.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if again.Expected, err = prod.Revision(); err != nil {
		t.Fatal(err)
	}
	writer := filestest.New()
	result, err := Promote(again, t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || writer.Writes != 0 {
		t.Fatalf("a second promotion changed %v and wrote %d times", result.Changed, writer.Writes)
	}
}

func TestPromoteRefusesAReleaseTheVolumeCannotCarry(t *testing.T) {
	inputs := promoteInputs(t)
	staging, err := kernel.DecodeInstance(inputs.StagingInstance)
	if err != nil {
		t.Fatal(err)
	}
	database, err := kernel.DecodeRelease(fixture(t, "r43.json"))
	if err != nil {
		t.Fatal(err)
	}
	staging.Release = database.ReleaseID
	if inputs.StagingInstance, err = kernel.EncodeInstance(staging); err != nil {
		t.Fatal(err)
	}
	inputs.StagingRelease = fixture(t, "r43.json")
	writer := filestest.New()
	_, err = Promote(inputs, t.TempDir(), writer)
	if err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("a database without production's volume was not refused: %v", err)
	}
	if writer.Writes != 0 {
		t.Fatalf("a refusal wrote %d times", writer.Writes)
	}
}

func TestPromoteRefusesACandidateStagingDoesNotSelect(t *testing.T) {
	inputs := promoteInputs(t)
	inputs.StagingRelease = fixture(t, "r41.json") // production's own release, not staging's
	_, err := Promote(inputs, t.TempDir(), filestest.New())
	if err == nil || !strings.Contains(err.Error(), "not the release staging selects") {
		t.Fatalf("a candidate staging does not select was not refused: %v", err)
	}
}

func TestPromoteRefusesAStaleRevision(t *testing.T) {
	inputs := promoteInputs(t)
	inputs.Expected = kernel.InstanceRevision("inst-sha256:" + strings.Repeat("0", 64))
	_, err := Promote(inputs, t.TempDir(), filestest.New())
	if err == nil || !strings.Contains(err.Error(), "instance changed") {
		t.Fatalf("a stale revision was not refused: %v", err)
	}
}

var _ files.Writer = filestest.New()
