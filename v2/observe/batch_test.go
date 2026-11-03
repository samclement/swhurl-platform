package observe_test

import (
	"encoding/json"
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
