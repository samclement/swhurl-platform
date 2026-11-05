package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testNames = `{"kind": "platform-names", "version": 1, "names": [
  {"name": "SWHURL_APP", "surface": "env", "meaning": "App name", "availability": "Every instance"},
  {"name": "SWHURL_SERVICE_<APP>_URL", "surface": "env", "meaning": "A callee's address", "availability": "Callers"},
  {"name": "platform.swhurl.com/team", "surface": "label", "meaning": "Owning team", "availability": "Every instance"}
]}`

// namesApp writes a catalogue and a rendered app holding files, and returns their paths.
func namesApp(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	catalogue := filepath.Join(root, "names.json")
	if err := os.WriteFile(catalogue, []byte(testNames), 0o600); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(root, "app")
	for name, text := range files {
		path := filepath.Join(app, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return catalogue, app
}

func TestNamesListedPrintNothing(t *testing.T) {
	catalogue, app := namesApp(t, map[string]string{
		"swhurl.yaml": "kind: web\n",
		"src/a.ts":    "process.env.SWHURL_APP; `SWHURL_SERVICE_${name}` SWHURL_SERVICE_ORDERS_URL platform.swhurl.com/team",
	})
	var out, diagnostic bytes.Buffer
	code := run([]string{"names", "--catalogue", catalogue, "--app-dir", app}, &out, &diagnostic, readInput, nil)
	if code != 0 || out.Len() != 0 || diagnostic.Len() != 0 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
}

// Each unlisted name is printed once, sorted, with the first file using it in lexical order: a folder's files before
// a later sibling's.
func TestNamesUnlistedArePrintedWithTheirFirstFile(t *testing.T) {
	catalogue, app := namesApp(t, map[string]string{
		"b.txt":       "SWHURL_ZED platform.swhurl.com/other",
		"a/deep.txt":  "SWHURL_ZED",
		"c/later.txt": "SWHURL_ALPHA SWHURL_APP",
	})
	var out, diagnostic bytes.Buffer
	code := run([]string{"names", "--catalogue", catalogue, "--app-dir", app}, &out, &diagnostic, readInput, nil)
	want := "SWHURL_ALPHA (c/later.txt)\nSWHURL_ZED (a/deep.txt)\nplatform.swhurl.com/other (b.txt)\n"
	if code != 1 || out.String() != want || diagnostic.Len() != 0 {
		t.Fatalf("code %d\nout %q\nwant %q\n%s", code, out.String(), want, diagnostic.String())
	}
}

func TestNamesRefusesUsageAndABadCatalogue(t *testing.T) {
	catalogue, app := namesApp(t, map[string]string{"a": "SWHURL_APP"})
	for _, args := range [][]string{{"names"}, {"names", "--catalogue", catalogue}, {"names", "--app-dir", app},
		{"names", "--catalogue", catalogue, "--app-dir", app, "extra"}} {
		var out, diagnostic bytes.Buffer
		if code := run(args, &out, &diagnostic, readInput, nil); code != 2 || out.Len() != 0 ||
			!strings.Contains(diagnostic.String(), "usage:") {
			t.Fatal(args, code, out.String(), diagnostic.String())
		}
	}
	if err := os.WriteFile(catalogue, []byte(`{"kind": "platform-names", "version": 2, "names": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if code := run([]string{"names", "--catalogue", catalogue, "--app-dir", app}, &out, &diagnostic, readInput, nil); code != 2 ||
		out.Len() != 0 || !strings.Contains(diagnostic.String(), "unsupported: kind") {
		t.Fatal(code, out.String(), diagnostic.String())
	}
}

// A folder that cannot be read, or a symlink leaving it, is an I/O failure, never a clean report.
func TestNamesFailsOnAnUnreadableTree(t *testing.T) {
	catalogue, app := namesApp(t, map[string]string{"a": "SWHURL_APP"})
	outside := filepath.Join(filepath.Dir(app), "outside")
	if err := os.WriteFile(outside, []byte("SWHURL_UNLISTED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(app, "link")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{app, filepath.Join(app, "missing")} {
		var out, diagnostic bytes.Buffer
		code := run([]string{"names", "--catalogue", catalogue, "--app-dir", dir}, &out, &diagnostic, readInput, nil)
		if code != 1 || out.Len() != 0 || !strings.HasPrefix(diagnostic.String(), "io: names:") {
			t.Fatal(dir, code, out.String(), diagnostic.String())
		}
	}
}
