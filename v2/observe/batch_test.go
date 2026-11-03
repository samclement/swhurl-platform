package observe_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

func TestCandidateIndexDoesNotTurnMalformedOrMissingListsIntoAbsence(t *testing.T) {
	base := target()
	for _, body := range []string{
		`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"other","namespace":"elsewhere"}}`,
		`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"hello"}}`,
		`null`,
	} {
		request := observe.ListRequest{}
		for _, query := range observe.BulkPlan([]observe.Target{base}) {
			if query.Ref.Kind == "Deployment" {
				request = query
			}
		}
		lists := []observe.List{{Ref: request.Ref, Selector: request.Selector, Status: observe.Present, Items: [][]byte{[]byte(body)}}}
		candidates, err := observe.IndexCandidates(lists)
		if err != nil {
			t.Fatal(err)
		}
		evidence := candidates.EvidenceFor(base)
		for _, read := range evidence.Objects {
			if read.Status != observe.Unavailable {
				t.Fatalf("malformed/missing list became %+v", read)
			}
		}
	}
}

// A list's items inherit the collection's type, because the API server omits theirs. An item that states
// the collection's own kind is read as present evidence, not as an invalid collection.
func TestCandidatesReadItemsWithoutTheirOwnType(t *testing.T) {
	base := target()
	for _, query := range observe.BulkPlan([]observe.Target{base}) {
		if query.Ref.Kind != "Pod" {
			continue
		}
		item := func(candidate string) observe.List {
			return observe.List{Ref: query.Ref, Selector: query.Selector, Status: observe.Present,
				Items: [][]byte{[]byte(candidate)}}
		}
		pod := fmt.Sprintf(`{"metadata":{"name":"hello-main-1","namespace":%q,"labels":{%q:%q,%q:%q}}}`, base.Pods.Namespace,
			"app.kubernetes.io/instance", "hello", "app.kubernetes.io/controller", "main")
		candidates, err := observe.IndexCandidates([]observe.List{item(pod)})
		if err != nil {
			t.Fatal(err)
		}
		evidence := candidates.EvidenceFor(base)
		if len(evidence.Pods) != 1 || evidence.Pods[0].Status != observe.Present || len(evidence.Pods[0].Items) != 1 {
			t.Fatalf("an item with no type of its own became %+v", evidence.Pods)
		}
		wrong := fmt.Sprintf(`{"apiVersion":"v1","kind":"Service","metadata":{"name":"hello-main-1","namespace":%q}}`, base.Pods.Namespace)
		candidates, err = observe.IndexCandidates([]observe.List{item(wrong)})
		if err != nil {
			t.Fatal(err)
		}
		if found := candidates.EvidenceFor(base).Pods; len(found) != 1 || found[0].Status != observe.Unavailable || found[0].Reason != observe.ReasonMalformed {
			t.Fatalf("an item that states another kind became %+v", found)
		}
	}
}

func TestBulkCollectionEmptyEnvelopeAndIndividualReportVersion(t *testing.T) {
	empty, err := observe.EncodeCollection(observe.Collection{Revision: commit})
	if err != nil || !strings.Contains(string(empty), `"instances":[]`) {
		t.Fatalf("%s %v", empty, err)
	}
	report := explain(t, healthy(t))
	data, err := observe.EncodeCollection(observe.Collection{Instances: []observe.InstanceReport{{Application: "hello", Environment: "staging", Report: report}}})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Kind      string
		Version   int
		Instances []struct {
			Application, Environment string
			Report                   struct {
				Kind        string
				Version     int
				Currentness observe.Currentness
			}
		}
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Kind != "v2-observations" || document.Version != 1 || document.Instances[0].Report.Kind != "v2-observation" || document.Instances[0].Report.Version != 5 || document.Instances[0].Application != "hello" || document.Instances[0].Report.Currentness.AppliedRevision != "main@sha1:"+commit {
		t.Fatalf("%s", data)
	}
}
