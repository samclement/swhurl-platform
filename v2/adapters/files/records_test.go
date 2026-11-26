package files_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// recordTree is a records tree as a repository holds it: two apps, one in two environments, a folder that holds no
// instance, and files beside the records that are not records.
var recordTree = map[string]string{
	"web/app.json":                 "web app",
	"web/staging/instance.json":    "web staging",
	"web/staging/release.json":     "web staging release",
	"web/staging/offers/api.json":  "an offer",
	"web/prod/instance.json":       "web prod",
	"web/prod/release.json":        "web prod release",
	"web/draft/notes.json":         "not an instance",
	"api-v2/app.json":              "api app",
	"api-v2/staging/instance.json": "api staging",
	"api-v2/staging/release.json":  "api staging release",
	"calls.json":                   "the resolution",
}

var wantRecords = []files.InstanceRecords{
	{Application: "api-v2", Environment: "staging", App: []byte("api app"), Instance: []byte("api staging"),
		Release: []byte("api staging release")},
	{Application: "web", Environment: "prod", App: []byte("web app"), Instance: []byte("web prod"),
		Release: []byte("web prod release")},
	{Application: "web", Environment: "staging", App: []byte("web app"), Instance: []byte("web staging"),
		Release: []byte("web staging release")},
}

// under is recordTree keyed under root, as a repository archive reads it.
func under(root string, without ...string) map[string][]byte {
	tree := map[string][]byte{"v2/testdata/profile.json": []byte("outside the records")}
	for file, body := range recordTree {
		tree[root+"/"+file] = []byte(body)
	}
	for _, file := range without {
		delete(tree, root+"/"+file)
	}
	return tree
}

// onDisk writes recordTree into a fresh records directory, less the files named.
func onDisk(t *testing.T, without ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "v2-records")
	for file, body := range recordTree {
		if contains(without, file) {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func contains(list []string, file string) bool {
	for _, one := range list {
		if one == file {
			return true
		}
	}
	return false
}

// The same records read the same from a tree an archive gave and from a directory: every folder holding an instance
// record, in app and environment order, and nothing else.
func TestRecords(t *testing.T) {
	fromTree, err := files.Records(under("v2-records"), "v2-records")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromTree, wantRecords) {
		t.Fatalf("tree records = %+v, want %+v", fromTree, wantRecords)
	}
	fromDisk, err := files.ReadRecords(onDisk(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromDisk, wantRecords) {
		t.Fatalf("directory records = %+v, want %+v", fromDisk, wantRecords)
	}
}

// An instance without its release or its app record refuses the whole tree, read either way; an empty tree holds no
// instance.
func TestRecordsRefuseAnIncompleteInstance(t *testing.T) {
	for missing, want := range map[string]string{
		"web/prod/release.json": "invalid: records: web/prod has no release.json",
		"api-v2/app.json":       "invalid: records: api-v2 has no app.json",
	} {
		if found, err := files.Records(under("v2-records", missing), "v2-records"); err == nil || err.Error() != want {
			t.Errorf("tree without %s: %+v, %v; want %s", missing, found, err, want)
		}
		if found, err := files.ReadRecords(onDisk(t, missing)); err == nil || err.Error() != want {
			t.Errorf("directory without %s: %+v, %v; want %s", missing, found, err, want)
		}
	}
	if found, err := files.Records(map[string][]byte{}, "v2-records"); err != nil || len(found) != 0 {
		t.Errorf("empty tree = %+v, %v", found, err)
	}
}

// A records directory that cannot be read, or a record in it, is an I/O failure rather than an absent instance.
func TestReadRecordsFailure(t *testing.T) {
	var failure *files.Error
	if _, err := files.ReadRecords(filepath.Join(t.TempDir(), "absent")); !errors.As(err, &failure) ||
		failure.Code != files.IO {
		t.Fatalf("absent directory: %v", err)
	}
	dir := onDisk(t)
	release := filepath.Join(dir, "web", "prod", "release.json")
	if err := os.Remove(release); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(release, 0o755); err != nil { // a folder where the record belongs cannot be read as one
		t.Fatal(err)
	}
	_, err := files.ReadRecords(dir)
	if !errors.As(err, &failure) || failure.Code != files.IO || !strings.HasSuffix(failure.Path, "web/prod/release.json") {
		t.Fatalf("unreadable record: %v", err)
	}
}
