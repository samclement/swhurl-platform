// Package filestest is the fake for files.Writer and the contract suite both implementations run.
package filestest

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Fake records what it was asked to write and keeps the bytes in memory. It applies the real
// adapter's destination rules, so it cannot accept something the real one refuses, and it writes
// nothing until every path has been checked; a test also uses it to fail a write on purpose.
type Fake struct {
	Contents map[string][]byte
	Writes   int
	Files    []kernel.File
	// Prunes and Removals count the calls that delete, so a test can say a use case pruned once.
	Prunes   int
	Removals int
	// PruneResult and RemoveResult are what those calls report, when a test needs a removal it did not plan.
	PruneResult  []files.Outcome
	RemoveResult []files.Outcome
	Fail         error // returned instead of writing, after the call is recorded
}

// New returns an empty fake.
func New() *Fake { return &Fake{Contents: map[string][]byte{}} }

var _ files.Writer = (*Fake)(nil)

// Write records the call and applies the contract in memory.
func (f *Fake) Write(dir string, docs []kernel.File) ([]files.Outcome, error) {
	f.Writes++
	f.Files = slices.Clone(docs)
	if f.Fail != nil {
		return nil, f.Fail
	}
	for _, doc := range docs {
		if _, err := files.Target(dir, doc.Path); err != nil {
			return nil, err
		}
	}
	outcomes := make([]files.Outcome, 0, len(docs))
	for _, doc := range docs {
		state := files.Written
		if existing, found := f.Contents[doc.Path]; found && bytes.Equal(existing, doc.Document) {
			state = files.Unchanged
		}
		if state == files.Written {
			f.Contents[doc.Path] = bytes.Clone(doc.Document)
		}
		outcomes = append(outcomes, files.Outcome{Path: doc.Path, State: state})
	}
	return outcomes, nil
}

// Prune removes the rendered names the compilation no longer renders from the folder, as the real adapter does.
func (f *Fake) Prune(dir, folder string, rendered []string) ([]files.Outcome, error) {
	f.Prunes++
	if f.Fail != nil {
		return nil, f.Fail
	}
	if f.PruneResult != nil {
		return f.PruneResult, nil
	}
	if _, err := files.Target(dir, folder); err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, name := range rendered {
		kept[name] = true
	}
	names := make([]string, 0, len(f.Contents))
	for path := range f.Contents {
		if prunable(path, folder, kept) {
			names = append(names, path)
		}
	}
	return f.forget(names), nil
}

// prunable is one in-memory path: directly in the folder, a rendered name, and not one this compilation names.
func prunable(path, folder string, kept map[string]bool) bool {
	rest, inFolder := strings.CutPrefix(path, folder+"/")
	return inFolder && !strings.Contains(rest, "/") && kernel.Rendered(rest) && !kept[rest]
}

// Remove deletes one path and everything below it, as the real adapter does.
func (f *Fake) Remove(dir, path string) ([]files.Outcome, error) {
	f.Removals++
	if f.Fail != nil {
		return nil, f.Fail
	}
	if f.RemoveResult != nil {
		return f.RemoveResult, nil
	}
	if path == "" || path == "." {
		return nil, &files.Error{Code: files.RefusedDestination, Path: path, Reason: "cannot_remove_the_destination"}
	}
	if _, err := files.Target(dir, path); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(f.Contents))
	for candidate := range f.Contents {
		if candidate == path || strings.HasPrefix(candidate, path+"/") {
			names = append(names, candidate)
		}
	}
	return f.forget(names), nil
}

// forget deletes the named paths in order and reports them as removed.
func (f *Fake) forget(names []string) []files.Outcome {
	slices.Sort(names)
	outcomes := make([]files.Outcome, 0, len(names))
	for _, name := range names {
		delete(f.Contents, name)
		outcomes = append(outcomes, files.Outcome{Path: name, State: files.Removed})
	}
	return outcomes
}

// Contract is the behaviour every files.Writer must have. The real adapter runs it on the raw
// fixtures in files/testdata and the fake runs it here, so the fake cannot drift from the adapter.
func Contract(t *testing.T, open func(*testing.T) (files.Writer, string), docs []kernel.File) {
	t.Helper()
	w, dir := open(t)
	writtenFirst(t, w, dir, docs)
	unchangedAfterwards(t, w, dir, docs)
	writtenAgainWhenChanged(t, w, dir, docs)
	refusedOutsideTheDestination(t, open, docs)
	prunesWhatTheCompilationStoppedRendering(t, w, dir, docs)
	removesATree(t, open, dir, docs)
}

// prunesWhatTheCompilationStoppedRendering: a rendered name the folder holds and this compilation does not is
// removed and reported; a declared input beside it — the seed — is kept, because it is not a rendered name.
func prunesWhatTheCompilationStoppedRendering(t *testing.T, w files.Writer, dir string, docs []kernel.File) {
	t.Helper()
	folder, rendered := folderAndNames(docs)
	if folder == "" {
		return
	}
	stale := staleRenderedName(rendered)
	extras := []kernel.File{
		{Path: folder + "/" + stale, Document: []byte(`{"kind":"ConfigMap"}`)},
		{Path: folder + "/events.sops.yaml", Document: []byte("sops:\n")},
	}
	if _, err := w.Write(dir, extras); err != nil {
		t.Fatalf("writing the folder's inputs: %v", err)
	}
	prunedOnce(t, w, dir, folder, stale, rendered)
	// The seed is a declared input, so it is still there: a second write of it changes nothing.
	after, err := w.Write(dir, extras[1:])
	if err != nil {
		t.Fatalf("rewriting the seed: %v", err)
	}
	if after[0].State != files.Unchanged {
		t.Errorf("the prune removed the folder's declared input: %s is %s", extras[1].Path, after[0].State)
	}
}

// prunedOnce: the stale rendered name went, it was reported, and a second prune finds nothing.
func prunedOnce(t *testing.T, w files.Writer, dir, folder, stale string, rendered []string) {
	t.Helper()
	outcomes, err := w.Prune(dir, folder, rendered)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Path != folder+"/"+stale || outcomes[0].State != files.Removed {
		t.Fatalf("prune reported %+v, want the stale %s removed", outcomes, stale)
	}
	again, err := w.Prune(dir, folder, rendered)
	if err != nil || len(again) != 0 {
		t.Fatalf("a repeat prune reported %+v (%v), want nothing", again, err)
	}
}

// removesATree: one path takes the files below it with it, and a path that is gone is not a failure.
func removesATree(t *testing.T, open func(*testing.T) (files.Writer, string), dir string, docs []kernel.File) {
	t.Helper()
	wrapped, dir := open(t)
	folder, _ := folderAndNames(docs)
	if folder == "" {
		return
	}
	if _, err := wrapped.Write(dir, docs); err != nil {
		t.Fatalf("writing before the removal: %v", err)
	}
	outcomes, err := wrapped.Remove(dir, folder)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	everyReported(t, outcomes, docs)
	again, err := wrapped.Remove(dir, folder)
	if err != nil || len(again) != 0 {
		t.Fatalf("a repeat removal reported %+v (%v), want nothing", again, err)
	}
	if _, err := wrapped.Remove(dir, "../escape"); err == nil {
		t.Fatal("a removal outside the destination was accepted")
	}
}

// everyReported: the removal named each file it took, as a removal under the folder it was given.
func everyReported(t *testing.T, outcomes []files.Outcome, docs []kernel.File) {
	t.Helper()
	removed := map[string]bool{}
	for _, outcome := range outcomes {
		if outcome.State != files.Removed {
			t.Fatalf("removal reported %+v", outcome)
		}
		removed[outcome.Path] = true
	}
	for _, doc := range docs {
		if !removed[doc.Path] {
			t.Errorf("removal did not report %s", doc.Path)
		}
	}
}

// folderAndNames is the folder the documents share and the file names they are written as.
func folderAndNames(docs []kernel.File) (string, []string) {
	if len(docs) == 0 {
		return "", nil
	}
	path := docs[0].Path
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return "", nil
	}
	names := make([]string, 0, len(docs))
	for _, doc := range docs {
		names = append(names, doc.Path[strings.LastIndex(doc.Path, "/")+1:])
	}
	return path[:index], names
}

// staleRenderedName is a name the renderer produces that this compilation did not.
func staleRenderedName(rendered []string) string {
	for _, candidate := range []string{"callers.json", "stream.json", "ingress.json"} {
		if !slices.Contains(rendered, candidate) {
			return candidate
		}
	}
	return "consumer-stale.json"
}

// writtenFirst: one outcome per document, in the compilation's order, each written.
func writtenFirst(t *testing.T, w files.Writer, dir string, docs []kernel.File) {
	t.Helper()
	outcomes, err := w.Write(dir, docs)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if len(outcomes) != len(docs) {
		t.Fatalf("%d outcomes for %d documents", len(outcomes), len(docs))
	}
	for i, outcome := range outcomes {
		if outcome.Path != docs[i].Path || outcome.State != files.Written {
			t.Errorf("outcome %d is %+v, want %s/%s", i, outcome, docs[i].Path, files.Written)
		}
	}
}

// unchangedAfterwards: the same documents again change nothing.
func unchangedAfterwards(t *testing.T, w files.Writer, dir string, docs []kernel.File) {
	t.Helper()
	outcomes, err := w.Write(dir, docs)
	if err != nil {
		t.Fatalf("repeat write: %v", err)
	}
	for i, outcome := range outcomes {
		if i >= len(docs) || outcome.Path != docs[i].Path || outcome.State != files.Unchanged {
			t.Errorf("repeat %d is %+v, want %s/%s", i, outcome, docs[i].Path, files.Unchanged)
		}
	}
}

// writtenAgainWhenChanged: a changed document is written again, and only that one is.
func writtenAgainWhenChanged(t *testing.T, w files.Writer, dir string, docs []kernel.File) {
	t.Helper()
	if len(docs) == 0 {
		return
	}
	changed := slices.Clone(docs)
	changed[0].Document = append(bytes.Clone(changed[0].Document), ' ')
	outcomes, err := w.Write(dir, changed)
	if err != nil {
		t.Fatalf("changed write: %v", err)
	}
	for i, outcome := range outcomes {
		want := files.Unchanged
		if i == 0 {
			want = files.Written
		}
		if outcome.State != want {
			t.Errorf("changed %d is %s, want %s", i, outcome.State, want)
		}
	}
}

// refusedOutsideTheDestination: a path that leaves the destination is refused, and nothing was
// written before the refusal, which a clean write afterwards proves: every path is still new.
func refusedOutsideTheDestination(t *testing.T, open func(*testing.T) (files.Writer, string), docs []kernel.File) {
	t.Helper()
	w, dir := open(t)
	escape := kernel.File{Path: "../escape.json", Document: []byte("{}")}
	_, err := w.Write(dir, append(slices.Clone(docs), escape))
	var refusal *files.Error
	if !errors.As(err, &refusal) || refusal.Code != files.RefusedDestination {
		t.Fatalf("an escaping path: %v, want a %s refusal", err, files.RefusedDestination)
	}
	outcomes, err := w.Write(dir, docs)
	if err != nil {
		t.Fatalf("write after a refusal: %v", err)
	}
	for i, outcome := range outcomes {
		if i >= len(docs) || outcome.State != files.Written {
			t.Errorf("a refused write left %s behind", docs[min(i, len(docs)-1)].Path)
		}
	}
}
