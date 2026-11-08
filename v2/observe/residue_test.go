package observe_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// removedCandidates is two complete lists — every unit in flux-system and every release anywhere — with the items
// the test names.
func removedCandidates(t *testing.T, units, releases []string) observe.Candidates {
	t.Helper()
	lists := []observe.List{}
	items := map[string][][]byte{}
	for _, query := range observe.BulkPlan([]observe.Target{target()}) {
		var found []string
		switch query.Ref.Kind {
		case "Kustomization":
			found = units
		case "HelmRelease":
			found = releases
		default:
			continue
		}
		for _, item := range found {
			items[query.Ref.Kind] = append(items[query.Ref.Kind], []byte(item))
		}
		lists = append(lists, observe.List{Ref: query.Ref, Selector: query.Selector, Status: observe.Present,
			Items: items[query.Ref.Kind]})
	}
	candidates, err := observe.IndexCandidates(lists)
	if err != nil {
		t.Fatal(err)
	}
	return candidates
}

// TestRemovedListsInstancesTheRecordsNoLongerName: an instance whose record is gone keeps a row while its unit, its
// release or both are still there, and an object that is not an app instance's is never one.
func TestRemovedListsInstancesTheRecordsNoLongerName(t *testing.T) {
	candidates := removedCandidates(t,
		[]string{
			`{"metadata":{"name":"app-hello-staging","namespace":"flux-system"}}`,
			`{"metadata":{"name":"app-gone-prod","namespace":"flux-system"}}`,
			`{"metadata":{"name":"app-half-staging","namespace":"flux-system"}}`,
			`{"metadata":{"name":"app-lone-staging","namespace":"flux-system"}}`,
			`{"metadata":{"name":"platform-backup","namespace":"flux-system"}}`,
		},
		[]string{
			`{"metadata":{"name":"hello","namespace":"hello-staging"}}`,
			`{"metadata":{"name":"gone","namespace":"gone-prod"}}`,
			`{"metadata":{"name":"half","namespace":"half-staging"}}`,
			`{"metadata":{"name":"mongodb","namespace":"observability"}}`,
		})
	found := candidates.Removed([]observe.Target{target()})
	want := []observe.Removed{
		{Application: "gone", Environment: "prod", Reason: observe.RemovedUnitAndRelease},
		{Application: "half", Environment: "staging", Reason: observe.RemovedUnitAndRelease},
		{Application: "lone", Environment: "staging", Reason: observe.RemovedUnit},
	}
	if fmt.Sprint(found) != fmt.Sprint(want) {
		t.Fatalf("removed %+v, want %+v", found, want)
	}
}

// TestRemovedKeepsAReleaseWithoutItsUnit: a HelmRelease whose unit Flux has pruned is still being uninstalled, so it
// is a removal of its own.
func TestRemovedKeepsAReleaseWithoutItsUnit(t *testing.T) {
	candidates := removedCandidates(t, nil, []string{`{"metadata":{"name":"gone","namespace":"gone-staging"}}`})
	found := candidates.Removed([]observe.Target{target()})
	if len(found) != 1 || found[0] != (observe.Removed{Application: "gone", Environment: "staging", Reason: observe.RemovedRelease}) {
		t.Fatalf("removed %+v", found)
	}
}

// TestRemovedReadsNothingWhenAListFailed: a collection the cluster did not answer contributes no removal, because a
// failed read is never an absence.
func TestRemovedReadsNothingWhenAListFailed(t *testing.T) {
	lists := []observe.List{}
	for _, query := range observe.BulkPlan([]observe.Target{target()}) {
		if query.Ref.Kind != "Kustomization" && query.Ref.Kind != "HelmRelease" {
			continue
		}
		lists = append(lists, observe.List{Ref: query.Ref, Selector: query.Selector, Status: observe.Unavailable,
			Reason: observe.ReasonIncomplete})
	}
	candidates, err := observe.IndexCandidates(lists)
	if err != nil {
		t.Fatal(err)
	}
	if found := candidates.Removed([]observe.Target{target()}); len(found) != 0 {
		t.Fatalf("a failed list became removals: %+v", found)
	}
}

// TestRemovedNeedsAnEnvironmentItKnows: only the platform's two environments name an instance, so a namespace with
// another suffix is not read as one, and a unit needs the prefix that makes it an app's.
func TestRemovedNeedsAnEnvironmentItKnows(t *testing.T) {
	for name, want := range map[string]bool{"app-hello-staging": true, "app-hello-prod": true,
		"app-hello-review": false, "platform-backup": false, "app-": false} {
		function := observe.InstanceOfUnit
		if name != "app-" && !strings.HasPrefix(name, "app-") {
			function = observe.InstanceOfNamespace
		}
		if _, _, ok := function(name); ok != want {
			t.Fatalf("%q named an instance: %v", name, ok)
		}
	}
}
