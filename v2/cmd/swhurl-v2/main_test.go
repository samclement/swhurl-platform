package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCLI(t *testing.T) {
	prod, r42 := fixture(t, "prod.json"), fixture(t, "r42.json")
	before := bytes.Clone(prod)
	s, err := kernel.DecodeInstance(prod)
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := s.Revision()
	for _, tt := range []struct {
		args            []string
		status          int
		out, diagnostic string
	}{
		{[]string{"release-id", "release"}, 0, "rel-sha256:", ""},
		{[]string{"instance-revision", "prod"}, 0, string(revision), ""},
		{[]string{"select-release", "--instance", "prod", "--release", "release", "--expect", string(revision)}, 0, `"release":`, ""},
		{[]string{"select-release", "--instance", "prod", "--release", "release", "--expect", "inst-sha256:" + strings.Repeat("0", 64)}, 2, "", "stale_instance"},
		{[]string{"select-release", "--instance", "prod", "--release", "release"}, 2, "", "usage"},
		{[]string{"select-release", "--instance", "prod", "--release", "release", "--expect", string(revision), "extra"}, 2, "", "usage"},
		{[]string{"release-id", "unreadable"}, 1, "", "io"},
		{[]string{"instance-revision", "unreadable"}, 1, "", "io"},
		{[]string{"release-id", "bad"}, 2, "", "invalid"},
		{[]string{"instance-revision", "bad"}, 2, "", "invalid"},
		{[]string{"release-id", "unsupported"}, 2, "", "unsupported"},
		{[]string{"release-id", "--unknown=do-not-echo"}, 2, "", "usage"},
		{[]string{"release-id"}, 2, "", "usage"},
		{[]string{"release-id", "release", "extra"}, 2, "", "usage"},
		{[]string{"promote"}, 2, "", "usage"},
		{nil, 2, "", "usage"},
		{[]string{"--help"}, 0, "usage", ""},
		{[]string{"select-release", "--help"}, 0, "usage", ""},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			read := func(name string) ([]byte, error) {
				switch name {
				case "prod":
					return prod, nil
				case "release":
					return r42, nil
				case "bad":
					return []byte(`{"secret":"do-not-echo"`), nil
				case "unsupported":
					return bytes.ReplaceAll(r42, []byte(`"web"`), []byte(`"do-not-echo"`)), nil
				case "unreadable":
					return nil, errors.New("do-not-echo")
				default:
					t.Fatalf("unexpected file read: %s", name)
					return nil, nil
				}
			}
			status := run(tt.args, &out, &diagnostic, read)
			if status != tt.status {
				t.Fatalf("status %d, want %d; %s", status, tt.status, &diagnostic)
			}
			if tt.out == "" && out.Len() != 0 || !strings.Contains(out.String(), tt.out) {
				t.Fatalf("stdout: %q", out.String())
			}
			if tt.diagnostic == "" && diagnostic.Len() != 0 || !strings.Contains(diagnostic.String(), tt.diagnostic) {
				t.Fatalf("stderr: %q", diagnostic.String())
			}
			if strings.Contains(diagnostic.String(), "do-not-echo") {
				t.Fatal("diagnostic leaked rejected values")
			}
			if !bytes.Equal(prod, before) {
				t.Fatal("input bytes mutated")
			}
		})
	}
}

type failedWriter struct{ short bool }

func (w failedWriter) Write(b []byte) (int, error) {
	if w.short {
		return len(b) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestOutputFailure(t *testing.T) {
	for _, writer := range []io.Writer{failedWriter{}, failedWriter{true}} {
		var diagnostic bytes.Buffer
		status := run([]string{"release-id", "r42"}, writer, &diagnostic, func(string) ([]byte, error) { return fixture(t, "r42.json"), nil })
		if status != 1 || !strings.Contains(diagnostic.String(), "io") {
			t.Fatalf("output failure: %d %s", status, &diagnostic)
		}
	}
}

func TestBinary(t *testing.T) {
	// Build once, then invoke the real binary with no PATH, service configuration or credentials.
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "swhurl-v2")
	build := exec.Command(goTool, "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=go1.26.0", "GOPROXY=off", "GOSUMDB=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	originals := map[string][]byte{}
	for _, file := range []string{"r41.json", "r42.json", "staging.json", "prod.json"} {
		b := fixture(t, file)
		originals[file] = b
		if err := os.WriteFile(filepath.Join(dir, file), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(want int, args ...string) []byte {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Dir = dir
		command.Env = []string{"PATH=", "HOME=" + dir}
		var out, diagnostic bytes.Buffer
		command.Stdout = &out
		command.Stderr = &diagnostic
		err := command.Run()
		status := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			status = exit.ExitCode()
		}
		if status != want {
			t.Fatalf("%v: exit %d, want %d: %s", args, status, want, &diagnostic)
		}
		if want != 0 && out.Len() != 0 {
			t.Fatal("refusal emitted a result")
		}
		if want == 0 && diagnostic.Len() != 0 {
			t.Fatalf("success diagnostic: %s", &diagnostic)
		}
		return out.Bytes()
	}
	id := strings.TrimSpace(string(invoke(0, "release-id", "r42.json")))
	revision := strings.TrimSpace(string(invoke(0, "instance-revision", "prod.json")))
	out := invoke(0, "select-release", "--instance", "prod.json", "--release", "r42.json", "--expect", revision)
	after, err := kernel.DecodeInstance(out)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := kernel.DecodeInstance(originals["prod.json"])
	want := before
	want.Release = kernel.ReleaseID(id)
	if after != want {
		t.Fatal("binary changed more than release")
	}
	if err := os.WriteFile(filepath.Join(dir, "selected.json"), out, 0600); err != nil {
		t.Fatal(err)
	}
	newRevision := strings.TrimSpace(string(invoke(0, "instance-revision", "selected.json")))
	again := invoke(0, "select-release", "--instance", "selected.json", "--release", "r42.json", "--expect", newRevision)
	if !bytes.Equal(out, again) {
		t.Fatal("binary no-op changed output")
	}
	invoke(2, "select-release", "--instance", "selected.json", "--release", "r42.json", "--expect", revision)
	for _, bad := range [][]byte{
		bytes.ReplaceAll(originals["r42.json"], []byte("demo-orders"), []byte("other")),
		bytes.ReplaceAll(originals["r42.json"], []byte(`"web"`), []byte(`"worker"`)),
		bytes.ReplaceAll(originals["r42.json"], []byte(`"runtime":`), []byte(`"grants":{"exposure":"public"},"runtime":`)),
	} {
		if err := os.WriteFile(filepath.Join(dir, "bad.json"), bad, 0600); err != nil {
			t.Fatal(err)
		}
		invoke(2, "select-release", "--instance", "prod.json", "--release", "bad.json", "--expect", revision)
		actual, _ := os.ReadFile(filepath.Join(dir, "bad.json"))
		if !bytes.Equal(actual, bad) {
			t.Fatal("refusal changed candidate file")
		}
	}
	invoke(1, "release-id", "missing.json")
	for file, before := range originals {
		after, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("input file changed: %s", file)
		}
	}
	selected, _ := os.ReadFile(filepath.Join(dir, "selected.json"))
	if !bytes.Equal(out, selected) {
		t.Fatal("stale refusal changed instance file")
	}
}
