// Package kubetest is the fake for kube.Reader and the contract suite both implementations run.
package kubetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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
	Collection []byte // optional raw successful list response for shared completeness tests
	ListReason string // optional unavailable response
	Items      [][]byte
}

// Fake answers scripted reads and records what it was asked. It copies the bytes it returns, as the
// real adapter does, so a caller cannot change a later answer.
type Fake struct {
	Answers   Answers
	Reads     []observe.Ref
	Writes    []observe.Ref
	ReadLogs  []observe.Ref
	Log       string
	Selectors []string // the selectors it was asked for
	Refuse    error    // returned instead of an answer, after the call is recorded
	// Objects are further objects Read answers as present. With Objects set, any other object of a kind the adapter
	// reads, and the scenario does not name, is absent, as the API server's 404 says.
	Objects map[observe.Ref][]byte
	// Collections are further collections answered as present, keyed by a ref without a name: ListNamespace's by
	// its namespace, List's by its namespace (none for a cluster-scoped kind) and ListAllNamespaces' by none. A list
	// with a selector answers every item, whatever the selector says.
	Collections map[observe.Ref][][]byte
	// Signing is what SigningKeys answers.
	Signing kube.SigningKeys
	// Tokens is what Review answers for each token; the API server vouches for no other.
	Tokens map[string]kube.TokenReview
	// ConfigMapData and SecretData are each namespace's ConfigMaps and Secrets; SetConfigMapKey changes the first.
	ConfigMapData map[string][]kube.ConfigMap
	SecretData    map[string][]kube.Secret
	// Failing makes one operation fail, after it is recorded: "review", "configmaps <selector>",
	// "secrets <selector>" or "set <name>", where a selector is its Name or its Labels.
	Failing map[string]error
	// Calls is every Review, list and key set, as its operation.
	Calls []string
}

var _ kube.Reader = (*Fake)(nil)
var _ kube.Writer = (*Fake)(nil)
var _ kube.Reviewer = (*Fake)(nil)
var _ kube.Data = (*Fake)(nil)
var _ kube.SecretNamer = (*Fake)(nil)
var _ kube.Issuer = (*Fake)(nil)

// New returns a fake that answers from the scenario.
func New(answers Answers) *Fake { return &Fake{Answers: answers} }

// Patch, Create and Delete record the change and refuse it when the fake was told to, as the adapter's own
// refusal is: a fake that silently succeeded would let a caller believe an effect happened.
func (f *Fake) Patch(_ context.Context, ref observe.Ref, _ []byte) error {
	f.Writes = append(f.Writes, ref)
	return f.Refuse
}

func (f *Fake) Create(_ context.Context, ref observe.Ref, _ []byte) error {
	f.Writes = append(f.Writes, ref)
	return f.Refuse
}

func (f *Fake) Delete(_ context.Context, ref observe.Ref) error {
	f.Writes = append(f.Writes, ref)
	return f.Refuse
}

// Logs answers the scripted log tail.
func (f *Fake) Logs(_ context.Context, ref observe.Ref, _ int64) (string, error) {
	f.ReadLogs = append(f.ReadLogs, ref)
	if f.Refuse != nil {
		return "", f.Refuse
	}
	return f.Log, nil
}

// WriteContract is the behaviour every kube.Writer must have: the three changes reach the object they name,
// and a pod's log comes back as it was answered. Refusals are each adapter's own, tested beside it.
func WriteContract(t *testing.T, log string, open func(*testing.T, string) kube.Writer) {
	t.Helper()
	writer := open(t, log)
	run, err := kube.RefOf("batch", "Job", "hello-staging", "restore")
	if err != nil {
		t.Fatal(err)
	}
	pod, err := kube.RefOf("", "Pod", "hello-staging", "restore-pod")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name string
		call func() error
	}{
		{"patch", func() error { return writer.Patch(context.Background(), run, []byte(`{"spec":{"suspend":true}}`)) }},
		{"create", func() error {
			return writer.Create(context.Background(), run, []byte(`{"apiVersion":"batch/v1","kind":"Job"}`))
		}},
		{"delete", func() error { return writer.Delete(context.Background(), run) }},
	} {
		if err := change.call(); err != nil {
			t.Fatalf("%s: %v", change.name, err)
		}
	}
	body, err := writer.Logs(context.Background(), pod, 20)
	if err != nil || body != log {
		t.Fatalf("logs %q, %v", body, err)
	}
}

// Read answers from the scenario, and refuses anything the scenario does not name.
func (f *Fake) Read(_ context.Context, ref observe.Ref) (observe.Read, error) {
	f.Reads = append(f.Reads, ref)
	if f.Refuse != nil {
		return observe.Read{}, f.Refuse
	}
	if ref.Name == "" {
		return observe.Read{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "name_required"}
	}
	if document, ok := f.Objects[ref]; ok {
		return observe.Read{Ref: ref, Status: observe.Present, Object: bytes.Clone(document)}, nil
	}
	if read, ok := f.scripted(ref); ok {
		return read, nil
	}
	if f.Objects != nil && readable(ref) {
		return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
	}
	return observe.Read{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "unsupported_kind"}
}

// readable is whether the adapter reads an object of this kind.
func readable(ref observe.Ref) bool {
	group, _, grouped := strings.Cut(ref.APIVersion, "/")
	if !grouped {
		group = ""
	}
	stated, err := kube.RefOf(group, ref.Kind, ref.Namespace, ref.Name)
	return err == nil && stated.APIVersion == ref.APIVersion
}

// scripted is the scenario's answer about one object, if it names it.
func (f *Fake) scripted(ref observe.Ref) (observe.Read, bool) {
	switch ref {
	case f.Answers.Object:
		return observe.Read{Ref: ref, Status: observe.Present, Object: bytes.Clone(f.Answers.Document)}, true
	case f.Answers.Missing:
		return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, true
	case f.Answers.Failed:
		return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: observe.ReasonForbidden}, true
	case f.Answers.Malformed, f.Answers.Mismatched:
		return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: observe.ReasonMalformed}, true
	}
	return observe.Read{}, false
}

// List answers the pod list, and refuses a request the real adapter would also refuse.
func (f *Fake) List(_ context.Context, ref observe.Ref, selector string) (observe.List, error) {
	return f.list(ref, selector, false)
}

func (f *Fake) list(ref observe.Ref, selector string, all bool) (observe.List, error) {
	f.Selectors = append(f.Selectors, selector)
	if f.Refuse != nil {
		return observe.List{}, f.Refuse
	}
	if selector == "" {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "selector", Reason: "required"}
	}
	if items, ok := f.Collections[ref]; ok {
		return kube.DecodeCollection(ref, selector, collection(items)), nil
	}
	expected := f.Answers.Pods
	if all {
		expected.Namespace = ""
	}
	if ref != expected {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "unsupported_kind"}
	}
	if f.Answers.ListReason != "" {
		return observe.List{Ref: ref, Selector: selector, Status: observe.Unavailable, Reason: f.Answers.ListReason}, nil
	}
	body := f.Answers.Collection
	if body == nil {
		body = collection(f.Answers.Items)
	}
	return kube.DecodeCollection(ref, selector, body), nil
}

// collection is the list document the API server would answer with these items.
func collection(found [][]byte) []byte {
	items := make([]json.RawMessage, 0, len(found))
	for _, item := range found {
		items = append(items, json.RawMessage(item))
	}
	body, _ := json.Marshal(struct {
		Items []json.RawMessage `json:"items"`
	}{items})
	return body
}

// ListNamespace answers a collection Collections names, else the scenario's pod list, and refuses what the real
// adapter refuses: a named ref, or no namespace.
func (f *Fake) ListNamespace(_ context.Context, ref observe.Ref) (observe.List, error) {
	f.Selectors = append(f.Selectors, "")
	if f.Refuse != nil {
		return observe.List{}, f.Refuse
	}
	if ref.Name != "" {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "collection_required"}
	}
	if ref.Namespace == "" || !readable(ref) {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "invalid_namespace"}
	}
	if items, ok := f.Collections[ref]; ok {
		return kube.DecodeCollection(ref, "", collection(items)), nil
	}
	if ref != f.Answers.Pods {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "unsupported_kind"}
	}
	if f.Answers.ListReason != "" {
		return observe.List{Ref: ref, Status: observe.Unavailable, Reason: f.Answers.ListReason}, nil
	}
	body := f.Answers.Collection
	if body == nil {
		body = collection(f.Answers.Items)
	}
	return kube.DecodeCollection(ref, "", body), nil
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
	unsupported := observe.Ref{APIVersion: "v1", Kind: "Secret", Namespace: answers.Object.Namespace, Name: "nightly"}
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

// ListAllNamespaces answers the scenario's collection without reinterpreting List's namespace.
func (f *Fake) ListAllNamespaces(_ context.Context, ref observe.Ref, selector string) (observe.List, error) {
	if ref.Namespace != "" || ref.Name != "" {
		return observe.List{}, &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "collection_required"}
	}
	return f.list(ref, selector, true)
}
