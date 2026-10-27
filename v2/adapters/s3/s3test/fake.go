// Package s3test is the fake for s3.Reader and the contract suite both implementations run.
package s3test

import (
	"context"
	"strings"
	"testing"
	"time"

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
	// Key is one object under Query, with its bytes; Missing is a key the bucket does not hold.
	Key, Body, Missing string
}

// Fake answers a scripted listing and records what it was asked. It copies the objects it returns, as the
// real adapter does, so a caller cannot change a later answer.
type Fake struct {
	Scenario Scenario
	Lists    []observe.ArchiveQuery
	Gets     []string
	Presigns []string
	Refuse   error // returned instead of a listing, after the call is recorded
}

var _ s3.Reader = (*Fake)(nil)

// New returns a fake that answers from the scenario.
func New(scenario Scenario) *Fake { return &Fake{Scenario: scenario} }

// Get answers the scenario's object, its absence, and refuses a key it does not name.
func (f *Fake) Get(_ context.Context, query observe.ArchiveQuery, key string) ([]byte, error) {
	f.Gets = append(f.Gets, key)
	if f.Refuse != nil {
		return nil, f.Refuse
	}
	switch {
	case query == f.Scenario.Query && key == f.Scenario.Key:
		return []byte(f.Scenario.Body), nil
	case query == f.Scenario.Query && key == f.Scenario.Missing:
		return nil, &s3.Error{Code: "not_found", Subject: "object", Reason: observe.ReasonNotFound}
	}
	return nil, &s3.Error{Code: s3.RefusedRequest, Subject: "key", Reason: "not_under_the_prefix"}
}

// PresignURL answers a read URL for the scenario's own key, and refuses exactly what the adapter refuses: a
// key outside the prefix and an expiry outside SigV4's range.
func (f *Fake) PresignURL(_ context.Context, query observe.ArchiveQuery, key string, ttl time.Duration) (string, error) {
	f.Presigns = append(f.Presigns, key)
	if f.Refuse != nil {
		return "", f.Refuse
	}
	if query.Bucket == "" || key == "" || !strings.HasPrefix(key, query.Prefix) {
		return "", &s3.Error{Code: s3.RefusedRequest, Subject: "key", Reason: "not_under_the_prefix"}
	}
	if ttl <= 0 || ttl > s3.MaxPresignTTL {
		return "", &s3.Error{Code: s3.RefusedRequest, Subject: "ttl", Reason: "out_of_range"}
	}
	return "https://" + query.Bucket + ".s3.us-east-1.amazonaws.com/" + key + "?X-Amz-Signature=fake", nil
}

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
	readsOneObject(t, reader, scenario)
	presignsOneObject(t, reader, scenario)
}

// readsOneObject: one object comes back with its bytes, its absence is the not_found code rather than an
// empty body, and a key outside the prefix is refused.
func readsOneObject(t *testing.T, reader s3.Reader, scenario Scenario) {
	t.Helper()
	if scenario.Key == "" {
		return
	}
	body, err := reader.Get(context.Background(), scenario.Query, scenario.Key)
	if err != nil {
		t.Fatalf("reading a present object: %v", err)
	}
	if string(body) != scenario.Body {
		t.Fatalf("object body %q", body)
	}
	if _, err := reader.Get(context.Background(), scenario.Query, scenario.Missing); err == nil {
		t.Fatal("a missing object was answered")
	} else if refusal, ok := err.(*s3.Error); !ok || refusal.Code != "not_found" {
		t.Fatalf("missing object: %v", err)
	}
	if _, err := reader.Get(context.Background(), scenario.Query, "elsewhere/key"); err == nil {
		t.Fatal("a key outside the prefix was read")
	}
}

// presignsOneObject: a presigned read is an https URL for that one key, and one for a key outside the
// instance's prefix, or an expiry outside SigV4's range, is refused rather than shortened or clamped.
func presignsOneObject(t *testing.T, reader s3.Reader, scenario Scenario) {
	t.Helper()
	if scenario.Key == "" {
		return
	}
	address, err := reader.PresignURL(context.Background(), scenario.Query, scenario.Key, time.Hour)
	if err != nil || !strings.HasPrefix(address, "https://") || !strings.Contains(address, scenario.Key) {
		t.Fatalf("presigned read %q, %v", address, err)
	}
	for _, call := range []struct {
		name string
		key  string
		ttl  time.Duration
	}{
		{"another prefix", "elsewhere/key", time.Hour},
		{"no expiry", scenario.Key, 0},
		{"too long", scenario.Key, s3.MaxPresignTTL + time.Hour},
	} {
		if _, err := reader.PresignURL(context.Background(), scenario.Query, call.key, call.ttl); err == nil {
			t.Fatalf("%s was presigned", call.name)
		}
	}
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
