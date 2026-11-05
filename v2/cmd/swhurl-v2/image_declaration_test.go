package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource/appsourcetest"
	"github.com/samclement/swhurl-platform/v2/producer"
)

func TestImageDeclarationCLI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "swhurl.yaml"), []byte("version: 1\nkind: worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, args := range [][]string{{"image-declaration"}, {"image-declaration", "--app-dir", root}} {
		var out, err bytes.Buffer
		code := runWith(args, &out, &err, cli{})
		want := producer.LabelKey + `={"schemaVersion":2,"runtime":{"kind":"worker","uid":65532},"memoryMiB":128}` + "\n"
		if code != 0 || out.String() != want || err.Len() != 0 {
			t.Fatal(code, out.String(), err.String())
		}
	}
}
func TestImageDeclarationUsageAndAtomicRefusals(t *testing.T) {
	for _, args := range [][]string{{"image-declaration", "extra"}, {"image-declaration", "--app-dir", ""}, {"image-declaration", "--app-dir"}, {"image-declaration", "--app-dir", "a", "--app-dir", "b"}, {"image-declaration", "--profile", "hidden"}, {"image-declaration", "--unknown"}} {
		var out, err bytes.Buffer
		code := runWith(args, &out, &err, cli{})
		if code != 2 || out.Len() != 0 || !strings.Contains(err.String(), "usage:") {
			t.Fatal(args, code, out.String(), err.String())
		}
	}
	var out, err bytes.Buffer
	code := runWith([]string{"image-declaration", "--help"}, &out, &err, cli{})
	if code != 0 || err.Len() != 0 || !strings.Contains(out.String(), "image-declaration") {
		t.Fatal(code, out.String(), err.String())
	}
	out.Reset()
	fake := &appsourcetest.Fake{Err: errors.New("swhurl.yaml: missing")}
	code = runWith([]string{"image-declaration"}, &out, &err, cli{appSource: fake})
	if code != 2 || out.Len() != 0 || !strings.Contains(err.String(), "swhurl.yaml") {
		t.Fatal(code, out.String(), err.String())
	}
}
func TestImageDeclarationOutputFailure(t *testing.T) {
	fake := &appsourcetest.Fake{Root: ".", Source: producer.Object{{Name: "version", Value: int64(1)}, {Name: "kind", Value: "worker"}}}
	var diagnostic bytes.Buffer
	code := runWith([]string{"image-declaration"}, failingOutput{}, &diagnostic, cli{appSource: fake})
	if code != 1 || !strings.Contains(diagnostic.String(), "cannot write result") {
		t.Fatal(code, diagnostic.String())
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestImageDeclarationCapRefusalHasNoStdout(t *testing.T) {
	fake := &appsourcetest.Fake{Root: ".", Source: producer.Object{{Name: "version", Value: int64(1)}, {Name: "kind", Value: "web"}, {Name: "healthPath", Value: "/h"}, {Name: "provides", Value: producer.Object{{Name: "api", Value: "/api"}, {Name: "apiFile", Value: "api"}}}}, Files: map[string][]byte{"api": []byte(strings.Repeat("x", 1024*1024))}}
	var out, diagnostic bytes.Buffer
	code := runWith([]string{"image-declaration"}, &out, &diagnostic, cli{appSource: fake})
	if code != 2 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "api (1048576 bytes)") {
		t.Fatal(code, out.String(), diagnostic.String())
	}
}
