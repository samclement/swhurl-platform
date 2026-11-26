package files_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// entry is one archive member: a regular file with its mode, a folder when its name ends in "/", or a symlink when
// link is set.
type entry struct {
	name, body, link string
	mode             int64
}

// tarOf builds a tar archive of the entries in their order, gzipped when asked.
func tarOf(t *testing.T, gzipped bool, entries ...entry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var sink interface {
		Write([]byte) (int, error)
		Close() error
	} = nopCloser{&buffer}
	if gzipped {
		sink = gzip.NewWriter(&buffer)
	}
	writer := tar.NewWriter(sink)
	for _, one := range entries {
		header := &tar.Header{Name: one.name, Mode: one.mode, Size: int64(len(one.body)), Typeflag: tar.TypeReg}
		switch {
		case one.link != "":
			header = &tar.Header{Name: one.name, Linkname: one.link, Typeflag: tar.TypeSymlink}
		case strings.HasSuffix(one.name, "/"):
			header = &tar.Header{Name: one.name, Mode: one.mode, Typeflag: tar.TypeDir}
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(one.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type nopCloser struct{ *bytes.Buffer }

func (nopCloser) Close() error { return nil }

func every(string) bool { return true }

// GitHub's tarball loses its leading folder and a checkout's archive keeps every path; either may be gzipped. Only
// selected regular files are read, in path order, with the executable bit the archive states.
func TestReadArchive(t *testing.T) {
	members := []entry{
		{name: "owner-repo-abc/", mode: 0o755},
		{name: "owner-repo-abc/v2-records/web/app.json", body: "app", mode: 0o644},
		{name: "owner-repo-abc/host/run.sh", body: "#!/bin/sh", mode: 0o755},
		{name: "owner-repo-abc/docs/link.md", link: "/etc/passwd"},
		{name: "top.json", body: "outside the leading folder", mode: 0o644},
	}
	for _, gzipped := range []bool{false, true} {
		tree, err := files.ReadArchive(tarOf(t, gzipped, members...), true, every)
		if err != nil {
			t.Fatal(err)
		}
		want := []files.TreeFile{{Path: "host/run.sh", Executable: true, Body: []byte("#!/bin/sh")},
			{Path: "v2-records/web/app.json", Body: []byte("app")}}
		if !sameTree(tree, want) {
			t.Fatalf("gzipped %v: tree = %+v, want %+v", gzipped, tree, want)
		}
	}
	tree, err := files.ReadArchive(tarOf(t, false, members...), false,
		func(file string) bool { return strings.HasSuffix(file, ".json") })
	if err != nil {
		t.Fatal(err)
	}
	if !sameTree(tree, []files.TreeFile{{Path: "owner-repo-abc/v2-records/web/app.json", Body: []byte("app")},
		{Path: "top.json", Body: []byte("outside the leading folder")}}) {
		t.Fatalf("unprefixed, selected tree = %+v", tree)
	}
}

// A path leaving the repository refuses the whole archive, selected or not, and so does a file over the bound.
func TestReadArchiveRefuses(t *testing.T) {
	for name, archive := range map[string][]byte{
		"parent":   tarOf(t, false, entry{name: "repo/ok.json", body: "{}"}, entry{name: "repo/../escape", body: "x"}),
		"absolute": tarOf(t, false, entry{name: "/etc/passwd", body: "x"}),
		"large":    tarOf(t, false, entry{name: "repo/big.json", body: strings.Repeat("x", 8<<20+1)}),
	} {
		if tree, err := files.ReadArchive(archive, true, every); err == nil {
			t.Errorf("%s: read %+v", name, tree)
		}
	}
	if tree, err := files.ReadArchive(tarOf(t, false, entry{name: "repo/../escape", body: "x"}), true,
		func(string) bool { return false }); err == nil {
		t.Errorf("an unselected path leaving the repository was accepted: %+v", tree)
	}
}

func sameTree(got, want []files.TreeFile) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Path != want[i].Path || got[i].Executable != want[i].Executable ||
			!bytes.Equal(got[i].Body, want[i].Body) {
			return false
		}
	}
	return true
}
