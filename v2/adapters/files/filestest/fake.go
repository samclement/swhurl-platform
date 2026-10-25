// Package filestest is the fake for files.Writer and the contract suite both implementations run.
package filestest

import (
	"bytes"
	"errors"
	"slices"
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
	Fail     error // returned instead of writing, after the call is recorded
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

// Contract is the behaviour every files.Writer must have. The real adapter runs it on the raw
// fixtures in files/testdata and the fake runs it here, so the fake cannot drift from the adapter.
func Contract(t *testing.T, open func(*testing.T) (files.Writer, string), docs []kernel.File) {
	t.Helper()
	w, dir := open(t)
	writtenFirst(t, w, dir, docs)
	unchangedAfterwards(t, w, dir, docs)
	writtenAgainWhenChanged(t, w, dir, docs)
	refusedOutsideTheDestination(t, open, docs)
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
