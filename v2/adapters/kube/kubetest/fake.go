// Package kubetest is the fake for kube.Reader and the contract suite both implementations run.
package kubetest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// Answers is the scenario the contract runs against both implementations: one object that is there,
// one that is absent, one that cannot be read, one whose answer cannot be used, and a pod list.
type Answers struct {
	Object     observe.Ref
	Document   []byte
	Missing    observe.Ref
	Failed     observe.Ref
	Malformed  observe.Ref
	Mismatched observe.Ref // a 200 whose document is not the object the ref names
	Pods       observe.Ref
	Selector   string
	Items      [][]byte
}

// Fake answers scripted reads and records what it was asked. It copies the bytes it returns, as the
// real adapter does, so a caller cannot change a later answer.
type Fake struct {
	Answers   Answers
	Reads     []observe.Ref
	Selectors []string // the selectors it was asked for
	Refuse    error    // returned instead of an answer, after the call is recorded
}

var _ kube.Reader = (*Fake)(nil)

// New returns a fake that answers from the scenario.
func New(answers Answers) *Fake { return &Fake{Answers: answers} }

// Read answers from the scenario, and refuses anything the scenario does not name.
func (f *Fake) Read(_ context.Context, ref observe.Ref) (observe.Read, error) {
	f.Reads = append(f.Reads, ref)
	if f.Refuse != nil {
		return observe.Read{}, f.Refuse
	}
	if ref.Name == "" {
		return observe.Read{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "name_required"}
	}
	switch ref {
	case f.Answers.Object:
		return observe.Read{Ref: ref, Status: observe.Present, Object: bytes.Clone(f.Answers.Document)}, nil
	case f.Answers.Missing:
		return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
	case f.Answers.Failed:
		return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: observe.ReasonForbidden}, nil
	case f.Answers.Malformed, f.Answers.Mismatched:
		return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: observe.ReasonMalformed}, nil
	}
	return observe.Read{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "unsupported_kind"}
}

// List answers the pod list, and refuses a request the real adapter would also refuse.
func (f *Fake) List(_ context.Context, ref observe.Ref, selector string) (observe.List, error) {
	f.Selectors = append(f.Selectors, selector)
	if f.Refuse != nil {
		return observe.List{}, f.Refuse
	}
	if selector == "" {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "selector", Reason: "required"}
	}
	if ref != f.Answers.Pods {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "unsupported_kind"}
	}
	items := make([][]byte, 0, len(f.Answers.Items))
	for _, item := range f.Answers.Items {
		items = append(items, bytes.Clone(item))
	}
	return observe.List{Ref: ref, Selector: selector, Status: observe.Present, Items: items}, nil
}

// Contract is the behaviour every kube.Reader must have: presence, absence, a failed read and an
// unusable answer are four different reports, and a request it cannot make is an error rather than
// evidence. The real adapter runs it on the raw API documents in kube/testdata and the fake runs it
// here, so the fake cannot drift from the adapter.
func Contract(t *testing.T, answers Answers, open func(*testing.T, Answers) kube.Reader) {
	t.Helper()
	reader := open(t, answers)
	objectIsPresent(t, reader, answers)
	mismatchedAnswersAreUnavailable(t, reader, answers)
	absenceIsNotAFailure(t, reader, answers)
	failuresAreUnavailable(t, reader, answers)
	unusableAnswersAreUnavailable(t, reader, answers)
	refusesWhatItCannotAsk(t, reader, answers)
	collectionIsPresent(t, reader, answers)
}

// objectIsPresent: the object's own bytes come back, as owned copies rather than a borrowed buffer. The
// expected bytes are copied first, so a reader that hands back the buffer it was given fails here.
func objectIsPresent(t *testing.T, reader kube.Reader, answers Answers) {
	t.Helper()
	expected := bytes.Clone(answers.Document)
	read, err := reader.Read(context.Background(), answers.Object)
	if err != nil {
		t.Fatalf("reading a present object: %v", err)
	}
	if read.Status != observe.Present || read.Reason != "" || !bytes.Equal(read.Object, expected) {
		t.Fatalf("present object read: %+v", read)
	}
	if len(read.Object) > 0 {
		read.Object[0] ^= 0xff
	}
	again, err := reader.Read(context.Background(), answers.Object)
	if err != nil || !bytes.Equal(again.Object, expected) {
		t.Fatalf("a caller changed a later answer: %v", err)
	}
}

// mismatchedAnswersAreUnavailable: a 200 whose document is not the object asked for is a failed read.
func mismatchedAnswersAreUnavailable(t *testing.T, reader kube.Reader, answers Answers) {
	t.Helper()
	read, err := reader.Read(context.Background(), answers.Mismatched)
	if err != nil {
		t.Fatalf("reading a mismatched answer: %v", err)
	}
	if read.Status != observe.Unavailable || read.Reason != observe.ReasonMalformed || len(read.Object) != 0 {
		t.Fatalf("mismatched answer read: %+v", read)
	}
}

// absenceIsNotAFailure: a successful answer that the object is not there is absence, with no content.
func absenceIsNotAFailure(t *testing.T, reader kube.Reader, answers Answers) {
	t.Helper()
	read, err := reader.Read(context.Background(), answers.Missing)
	if err != nil {
		t.Fatalf("reading an absent object: %v", err)
	}
	if read.Status != observe.Absent || read.Reason != observe.ReasonNotFound || len(read.Object) != 0 {
		t.Fatalf("absent read: %+v", read)
	}
}

// failuresAreUnavailable: a forbidden read reports unavailable, never absence.
func failuresAreUnavailable(t *testing.T, reader kube.Reader, answers Answers) {
	t.Helper()
	read, err := reader.Read(context.Background(), answers.Failed)
	if err != nil {
		t.Fatalf("reading a forbidden object: %v", err)
	}
	if read.Status != observe.Unavailable || read.Reason != observe.ReasonForbidden || len(read.Object) != 0 {
		t.Fatalf("failed read: %+v", read)
	}
}

// unusableAnswersAreUnavailable: an answer whose bytes are not a JSON object is a failed read.
func unusableAnswersAreUnavailable(t *testing.T, reader kube.Reader, answers Answers) {
	t.Helper()
	read, err := reader.Read(context.Background(), answers.Malformed)
	if err != nil {
		t.Fatalf("reading an unusable answer: %v", err)
	}
	if read.Status != observe.Unavailable || read.Reason != observe.ReasonMalformed || len(read.Object) != 0 {
		t.Fatalf("unusable answer read: %+v", read)
	}
}

// refusesWhatItCannotAsk: an unsupported kind, a missing name and an empty selector are refusals, so a
// caller never mistakes "I did not read" for evidence about an object.
func refusesWhatItCannotAsk(t *testing.T, reader kube.Reader, answers Answers) {
	t.Helper()
	unsupported := observe.Ref{APIVersion: "batch/v1", Kind: "CronJob", Namespace: answers.Object.Namespace, Name: "nightly"}
	if _, err := reader.Read(context.Background(), unsupported); !refused(err, "unsupported_kind") {
		t.Fatalf("an unsupported kind: %v", err)
	}
	unnamed := answers.Object
	unnamed.Name = ""
	if _, err := reader.Read(context.Background(), unnamed); !refused(err, "name_required") {
		t.Fatalf("a read without a name: %v", err)
	}
	if _, err := reader.List(context.Background(), answers.Pods, ""); !refused(err, "required") {
		t.Fatalf("a list without a selector: %v", err)
	}
}

// collectionIsPresent: a list is present with its items, even when the list is empty, and the caller
// owns what it is given.
func collectionIsPresent(t *testing.T, reader kube.Reader, answers Answers) {
	t.Helper()
	list, err := reader.List(context.Background(), answers.Pods, answers.Selector)
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if list.Status != observe.Present || list.Selector != answers.Selector || list.Reason != "" {
		t.Fatalf("pod list: %+v", list)
	}
	expected := cloneItems(answers.Items)
	if !sameItems(list.Items, expected) {
		t.Fatalf("pod list items differ from the scenario")
	}
	if len(list.Items) > 0 && len(list.Items[0]) > 0 {
		list.Items[0][0] ^= 0xff
	}
	again, err := reader.List(context.Background(), answers.Pods, answers.Selector)
	if err != nil || !sameItems(again.Items, expected) {
		t.Fatalf("a caller changed a later list: %v", err)
	}
}

// cloneItems copies documents, so a mutated result cannot hide a borrowed buffer.
func cloneItems(items [][]byte) [][]byte {
	copied := make([][]byte, 0, len(items))
	for _, item := range items {
		copied = append(copied, bytes.Clone(item))
	}
	return copied
}

// refused reports whether an error is the adapter's refusal with this reason.
func refused(err error, reason string) bool {
	var refusal *kube.Error
	return errors.As(err, &refusal) && refusal.Code == kube.RefusedRequest && refusal.Reason == reason
}

// sameItems compares two lists of documents.
func sameItems(items, want [][]byte) bool {
	return slices.EqualFunc(items, want, bytes.Equal)
}
