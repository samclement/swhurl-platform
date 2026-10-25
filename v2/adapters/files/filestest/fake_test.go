package filestest_test

import (
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

func documents() []kernel.File {
	return []kernel.File{
		{Path: "instances/demo/one.json", Document: []byte(`{"kind":"Namespace","version":1}`)},
		{Path: "instances/demo/two.json", Document: []byte(`{"kind":"ResourceQuota","version":1}`)},
	}
}

func TestContract(t *testing.T) {
	filestest.Contract(t, func(t *testing.T) (files.Writer, string) {
		return filestest.New(), t.TempDir()
	}, documents())
}

func TestFakeRecordsTheCallAndCanFail(t *testing.T) {
	f := filestest.New()
	f.Fail = &files.Error{Code: files.IO, Path: "instances/demo/one.json", Reason: "cannot write the file"}
	if _, err := f.Write("/destination", documents()); err == nil {
		t.Fatal("a configured failure was not returned")
	}
	if f.Writes != 1 || len(f.Files) != 2 {
		t.Fatalf("the call was not recorded: %d writes, %d files", f.Writes, len(f.Files))
	}
	if len(f.Contents) != 0 {
		t.Fatal("a failed write kept content")
	}
}
