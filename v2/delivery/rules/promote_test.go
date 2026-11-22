package rules

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// promoteFixture is the fixture staging instance and the release it selects, and production's own pair.
func promoteFixture(t *testing.T) (staging, prod kernel.InstanceSpec, current, candidate kernel.ReleaseBundle) {
	t.Helper()
	staging, prod = instance(t, "staging.json"), instance(t, "prod.json")
	candidate, current = mustRelease(t, "r42.json"), mustRelease(t, "r41.json")
	return staging, prod, current, candidate
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func instance(t *testing.T, name string) kernel.InstanceSpec {
	t.Helper()
	s, err := kernel.DecodeInstance(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustRelease(t *testing.T, name string) kernel.ReleaseBundle {
	t.Helper()
	r, err := kernel.DecodeRelease(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSelectPromotionReplacesOnlyTheRelease(t *testing.T) {
	staging, prod, current, candidate := promoteFixture(t)
	if staging.Release != candidate.ReleaseID || prod.Release != current.ReleaseID {
		t.Fatalf("the fixtures do not select what they state: %s/%s", staging.Release, prod.Release)
	}
	revision, err := prod.Revision()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectPromotion(staging, prod, current, candidate, revision)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Release != candidate.ReleaseID {
		t.Fatalf("production selects %s, want %s", selected.Release, candidate.ReleaseID)
	}
	if selected.Configuration != prod.Configuration || !reflect.DeepEqual(selected.Grants, prod.Grants) ||
		selected.Storage != prod.Storage || len(selected.Calls) != len(prod.Calls) ||
		len(selected.Allows) != len(prod.Allows) || selected.Events.Key != prod.Events.Key ||
		len(selected.Events.Subscriptions) != len(prod.Events.Subscriptions) {
		t.Fatalf("production changed beyond its release: %+v", selected)
	}
}

func TestSelectPromotionRefusals(t *testing.T) {
	staging, prod, current, candidate := promoteFixture(t)
	revision, err := prod.Revision()
	if err != nil {
		t.Fatal(err)
	}
	refuse := func(name string, staging, target kernel.InstanceSpec, current, candidate kernel.ReleaseBundle, want Code, field string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			_, err := SelectPromotion(staging, target, current, candidate, revision)
			var refused *Error
			if !errors.As(err, &refused) || refused.Code != want || refused.Field != field {
				t.Fatalf("err %v, want %s %s", err, want, field)
			}
		})
	}
	notStaging := staging
	notStaging.Environment = "prod"
	refuse("the source is not staging", notStaging, prod, current, candidate, Invalid, "environment")
	refuse("the target is not production", staging, staging, current, candidate, Invalid, "environment")

	otherInstance, otherRelease := prod, current
	otherInstance.Application, otherRelease.Application = "other-app", "other-app"
	otherRelease.ReleaseID = "" // the assertion it carried belongs to the unchanged release
	refuse("another application", staging, otherInstance, otherRelease, candidate, WrongApplication, "application")
	foreign, foreignRelease := staging, current
	foreignRelease.Application, foreignRelease.ReleaseID = "other-app", ""
	if foreignRelease.ReleaseID, err = foreignRelease.ID(); err != nil {
		t.Fatal(err)
	}
	foreign.Release = foreignRelease.ReleaseID // a staging record that selects another app's release
	refuse("a release for another application", foreign, prod, current, foreignRelease, WrongApplication, "application")

	withVolume, reuser := prod, candidate
	withVolume.Storage = kernel.Storage{SizeMiB: 1024}
	reuser.Runtime.UID, reuser.ReleaseID = 1000, ""
	if reuser.ReleaseID, err = reuser.ID(); err != nil {
		t.Fatal(err)
	}
	staged := staging
	staged.Release = reuser.ReleaseID
	refuse("the user changes on a volume", staged, withVolume, current, reuser, Unsupported, "runtime.uid")
}

func TestSelectPromotionRefusesACandidateStagingDoesNotSelect(t *testing.T) {
	staging, prod, current, candidate := promoteFixture(t)
	revision, err := prod.Revision()
	if err != nil {
		t.Fatal(err)
	}
	staging.Release = current.ReleaseID // staging records something else than the candidate
	_, err = SelectPromotion(staging, prod, current, candidate, revision)
	var refused *Error
	if !errors.As(err, &refused) || refused.Code != WrongRelease || refused.Field != "release" {
		t.Fatalf("err %v, want a refusal that the candidate is not staging's", err)
	}
	if !strings.Contains(err.Error(), "not the release staging selects") {
		t.Fatalf("the refusal does not say what is wrong: %v", err)
	}
}

func TestSelectPromotionKeepsSelectionAndStaleness(t *testing.T) {
	staging, prod, current, candidate := promoteFixture(t)
	revision, err := prod.Revision()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectPromotion(staging, prod, current, candidate, revision)
	if err != nil {
		t.Fatal(err)
	}
	keptRevision, err := selected.Revision()
	if err != nil {
		t.Fatal(err)
	}
	kept, err := SelectPromotion(staging, selected, candidate, candidate, keptRevision)
	if err != nil {
		t.Fatalf("a promotion of the release production already selects was refused: %v", err)
	}
	if kept.Release != candidate.ReleaseID {
		t.Fatalf("the second promotion changed the release to %s", kept.Release)
	}
	_, err = SelectPromotion(staging, prod, current, candidate,
		kernel.InstanceRevision("inst-sha256:"+strings.Repeat("0", 64)))
	var refused *kernel.Error // staleness is selection's refusal, in its own vocabulary
	if !errors.As(err, &refused) || refused.Code != kernel.StaleInstance {
		t.Fatalf("err %v, want a stale refusal", err)
	}
}
