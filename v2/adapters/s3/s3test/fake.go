// Package s3test is the fake for s3.Reader and the contract suite both implementations run.
package s3test

import (
	"context"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// Scenario is what the contract runs against both implementations: a prefix with objects, a prefix the
// server refuses, and a request the reader itself refuses.
type Scenario struct {
	Query     observe.ArchiveQuery
	Objects   []observe.Archive
	Forbidden observe.ArchiveQuery
	Empty     observe.ArchiveQuery
}

// Fake answers a scripted listing and records what it was asked. It copies the objects it returns, as the
// real adapter does, so a caller cannot change a later answer.
type Fake struct {
	Scenario Scenario
	Lists    []observe.ArchiveQuery
	Refuse   error // returned instead of a listing, after the call is recorded
}

var _ s3.Reader = (*Fake)(nil)

// New returns a fake that answers from the scenario.
func New(scenario Scenario) *Fake { return &Fake{Scenario: scenario} }

// List answers from the scenario, and refuses what the scenario does not name.
func (f *Fake) List(_ context.Context, query observe.ArchiveQuery) (observe.ArchiveList, error) {
	f.Lists = append(f.Lists, query)
	if f.Refuse != nil {
		return observe.ArchiveList{}, f.Refuse
	}
	switch {
	case query == f.Scenario.Query:
		objects := make([]observe.Archive, 0, len(f.Scenario.Objects))
		for _, object := range f.Scenario.Objects {
			object.Key = strings.Clone(object.Key)
			objects = append(objects, object)
		}
		return observe.ArchiveList{Query: query, Status: observe.Present, Objects: objects}, nil
	case query == f.Scenario.Forbidden:
		return observe.ArchiveList{Query: query, Status: observe.Unavailable, Reason: observe.ReasonForbidden}, nil
	}
	return observe.ArchiveList{}, &s3.Error{Code: s3.RefusedRequest, Subject: "bucket", Reason: "required"}
}

// Contract is the behaviour every s3.Reader must have: a listing is present with its objects, a subject the
// server refuses is unavailable with a fixed reason, and a request the reader cannot make is an error rather
// than evidence. The real adapter runs it over a recorded answer, and the fake runs it here.
func Contract(t *testing.T, scenario Scenario, open func(*testing.T, Scenario) s3.Reader) {
	t.Helper()
	reader := open(t, scenario)
	listsObjects(t, reader, scenario)
	refusedIsUnavailable(t, reader, scenario)
	refusesWhatItCannotAsk(t, reader, scenario)
}

// listsObjects: a listing is present with its objects, copies and all.
func listsObjects(t *testing.T, reader s3.Reader, scenario Scenario) {
	t.Helper()
	listed, err := reader.List(context.Background(), scenario.Query)
	if err != nil {
		t.Fatalf("listing a present prefix: %v", err)
	}
	if listed.Status != observe.Present || listed.Reason != "" || listed.Query != scenario.Query {
		t.Fatalf("present listing: %+v", listed)
	}
	if len(listed.Objects) != len(scenario.Objects) {
		t.Fatalf("%d objects for %d", len(listed.Objects), len(scenario.Objects))
	}
	for i, object := range listed.Objects {
		if object.Key != scenario.Objects[i].Key || !object.LastModified.Equal(scenario.Objects[i].LastModified) {
			t.Fatalf("object %d: %+v", i, object)
		}
	}
}

// refusedIsUnavailable: a subject the server refuses is a failed read with a fixed reason, never an empty
// prefix.
func refusedIsUnavailable(t *testing.T, reader s3.Reader, scenario Scenario) {
	t.Helper()
	refused, err := reader.List(context.Background(), scenario.Forbidden)
	if err != nil {
		t.Fatalf("listing a refused prefix: %v", err)
	}
	if refused.Status != observe.Unavailable || refused.Reason != observe.ReasonForbidden || len(refused.Objects) != 0 {
		t.Fatalf("refused listing: %+v", refused)
	}
}

// refusesWhatItCannotAsk: a request the reader cannot make is an error rather than evidence.
func refusesWhatItCannotAsk(t *testing.T, reader s3.Reader, scenario Scenario) {
	t.Helper()
	_, err := reader.List(context.Background(), scenario.Empty)
	if err == nil {
		t.Fatal("a request the reader cannot make was answered as evidence")
	}
	if refusal, ok := err.(*s3.Error); !ok || refusal.Code != s3.RefusedRequest {
		t.Fatalf("refusal: %v", err)
	}
}
