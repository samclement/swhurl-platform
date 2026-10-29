package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
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
	prod, r42, r41, profile := fixture(t, "prod.json"), fixture(t, "r42.json"), fixture(t, "r41.json"), fixture(t, "profile.json")
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
		{[]string{"select-release", "--instance", "prod", "--instance", "prod", "--release", "release", "--expect", string(revision)}, 2, "", "usage"},
		{[]string{"select-release", "--instance", "prod", "--release", "huge", "--expect", string(revision)}, 2, "", "invalid: release: input file is larger than 1 MiB"},
		{[]string{"select-release", "--instance", "", "--release", "release", "--expect", string(revision)}, 2, "", "usage"},
		{[]string{"instance-revision", ""}, 1, "", "io: instance: cannot read input file"},
		{[]string{"instance-revision", "huge"}, 2, "", "invalid: instance: input file is larger than 1 MiB"},
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
		{[]string{"compile", "--instance", "prod", "--release", "r41", "--app", "app", "--profile", "profile"}, 0, `"kind": "v2-compilation"`, ""},
		{[]string{"compile", "--instance", "prod", "--release", "release", "--app", "app", "--profile", "profile"}, 2, "", "wrong_release: release:"},
		// A refusal is a refusal with --out too: the destination is only an effect, not a different answer.
		{[]string{"compile", "--instance", "prod", "--release", "release", "--app", "app", "--profile", "profile", "--out", "destination"}, 2, "", "wrong_release: release:"},
		{[]string{"compile", "--instance", "prod", "--release", "r41", "--app", "app", "--profile", "bad"}, 2, "", "invalid: document:"},
		{[]string{"compile", "--instance", "prod", "--release", "r41", "--app", "app", "--profile", "huge"}, 2, "", "invalid: profile: input file is larger than 1 MiB"},
		{[]string{"compile", "--instance", "prod", "--release", "r41", "--app", "app", "--profile", "unreadable"}, 1, "", "io: profile: cannot read input file"},
		{[]string{"compile", "--instance", "prod", "--release", "r41", "--app", "app"}, 2, "", "usage"},
		{[]string{"compile", "--instance", "prod", "--release", "r41", "--app", "app", "--profile", "profile", "--profile", "profile"}, 2, "", "usage"},
		{[]string{"compile", "--instance", "prod", "--release", "r41", "--app", "app", "--profile", "profile", "--expect", "x"}, 2, "", "usage"},
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
				case "r41":
					return r41, nil
				case "app":
					return fixture(t, "app.json"), nil
				case "profile":
					return profile, nil
				case "bad":
					return []byte(`{"secret":"do-not-echo"`), nil
				case "unsupported":
					return bytes.ReplaceAll(r42, []byte(`"web"`), []byte(`"do-not-echo"`)), nil
				case "unreadable", "":
					return nil, errors.New("do-not-echo")
				case "huge":
					return nil, errTooLarge
				default:
					t.Fatalf("unexpected file read: %s", name)
					return nil, nil
				}
			}
			status := run(tt.args, &out, &diagnostic, read, filestest.New())
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
		status := run([]string{"release-id", "r42"}, writer, &diagnostic, func(string) ([]byte, error) { return fixture(t, "r42.json"), nil }, filestest.New())
		if status != 1 || !strings.Contains(diagnostic.String(), "io") {
			t.Fatalf("output failure: %d %s", status, &diagnostic)
		}
	}
}

func TestReadInputLimit(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		size     int
		tooLarge bool
	}{{maxInput, false}, {maxInput + 1, true}} {
		path := filepath.Join(dir, "input.json")
		if err := os.WriteFile(path, bytes.Repeat([]byte(" "), tt.size), 0600); err != nil {
			t.Fatal(err)
		}
		data, err := readInput(path)
		if errors.Is(err, errTooLarge) != tt.tooLarge || !tt.tooLarge && (err != nil || len(data) != tt.size) {
			t.Fatalf("size %d: %d bytes, %v", tt.size, len(data), err)
		}
	}
	if _, err := readInput(filepath.Join(dir, "missing.json")); err == nil || errors.Is(err, errTooLarge) {
		t.Fatalf("a missing file is an I/O error: %v", err)
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
	for _, file := range []string{"r41.json", "r42.json", "staging.json", "prod.json", "app.json", "profile.json"} {
		b := fixture(t, file)
		originals[file] = b
		if err := os.WriteFile(filepath.Join(dir, file), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// invoke runs the binary; a refusal must print its code (or "io: ...") on stderr.
	invoke := func(want int, diagnosticWant string, args ...string) []byte {
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
		if !strings.HasPrefix(diagnostic.String(), diagnosticWant) {
			t.Fatalf("%v: stderr %q, want it to start with %q", args, &diagnostic, diagnosticWant)
		}
		return out.Bytes()
	}
	id := strings.TrimSpace(string(invoke(0, "", "release-id", "r42.json")))
	revision := strings.TrimSpace(string(invoke(0, "", "instance-revision", "prod.json")))
	out := invoke(0, "", "select-release", "--instance", "prod.json", "--release", "r42.json", "--expect", revision)
	after, err := kernel.DecodeInstance(out)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := kernel.DecodeInstance(originals["prod.json"])
	want := before
	want.Release = kernel.ReleaseID(id)
	// A record carrying declaration lists is not comparable: "more than the release changed" is a deep
	// comparison.
	if !reflect.DeepEqual(after, want) {
		t.Fatal("binary changed more than release")
	}
	if err := os.WriteFile(filepath.Join(dir, "selected.json"), out, 0600); err != nil {
		t.Fatal(err)
	}
	newRevision := strings.TrimSpace(string(invoke(0, "", "instance-revision", "selected.json")))
	again := invoke(0, "", "select-release", "--instance", "selected.json", "--release", "r42.json", "--expect", newRevision)
	if !bytes.Equal(out, again) {
		t.Fatal("binary no-op changed output")
	}
	invoke(2, "stale_instance:", "select-release", "--instance", "selected.json", "--release", "r42.json", "--expect", revision)
	for _, bad := range []struct {
		data []byte
		want string
	}{
		{bytes.ReplaceAll(originals["r42.json"], []byte("demo-orders"), []byte("other")), "wrong_application:"},
		{bytes.ReplaceAll(originals["r42.json"], []byte(`"web"`), []byte(`"worker"`)), "unsupported: runtime.kind:"},
		{bytes.ReplaceAll(originals["r42.json"], []byte(`"runtime":`), []byte(`"grants":{"exposure":"public"},"runtime":`)), "unsupported: release:"},
	} {
		if err := os.WriteFile(filepath.Join(dir, "bad.json"), bad.data, 0600); err != nil {
			t.Fatal(err)
		}
		invoke(2, bad.want, "select-release", "--instance", "prod.json", "--release", "bad.json", "--expect", revision)
		actual, _ := os.ReadFile(filepath.Join(dir, "bad.json"))
		if !bytes.Equal(actual, bad.data) {
			t.Fatal("refusal changed candidate file")
		}
	}
	compiled := invoke(0, "", "compile", "--instance", "prod.json", "--release", "r41.json", "--app", "app.json", "--profile", "profile.json")
	if !bytes.Equal(compiled, fixture(t, "compiled/prod.json")) {
		t.Fatal("the binary's compilation differs from the golden document")
	}
	invoke(2, "wrong_release:", "compile", "--instance", "staging.json", "--release", "r41.json", "--app", "app.json", "--profile", "profile.json")
	invoke(1, "io: release:", "release-id", "missing.json")
	if err := os.WriteFile(filepath.Join(dir, "huge.json"), bytes.Repeat([]byte(" "), maxInput+1), 0600); err != nil {
		t.Fatal(err)
	}
	invoke(2, "invalid: release: input file is larger than 1 MiB", "release-id", "huge.json")
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

// inputs are the fixture files compile reads, by the name the command line uses.
func inputs(t *testing.T) func(string) ([]byte, error) {
	t.Helper()
	docs := map[string][]byte{"prod.json": fixture(t, "prod.json"), "r41.json": fixture(t, "r41.json"),
		"app.json": fixture(t, "app.json"), "profile.json": fixture(t, "profile.json")}
	return func(name string) ([]byte, error) { return docs[name], nil }
}

func compileArgs(dir string) []string {
	return []string{"compile", "--instance", "prod.json", "--release", "r41.json", "--app", "app.json", "--profile", "profile.json", "--out", dir}
}

// --out writes the compiled files and prints one line per path, not the document.
func TestCompileOut(t *testing.T) {
	dir := t.TempDir()
	var out, diagnostic bytes.Buffer
	if status := run(compileArgs(dir), &out, &diagnostic, inputs(t), files.New()); status != 0 {
		t.Fatalf("status %d: %s", status, &diagnostic)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("%d lines for 6 files: %q", len(lines), out.String())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "written ") {
			t.Errorf("line %q, want a written outcome", line)
		}
	}
	if strings.Contains(out.String(), "v2-compilation") {
		t.Error("--out printed the document")
	}
	// The unit lands where kustomize can list it; the instance files stay under the profile's root.
	for _, path := range []string{"clusters/home/app-demo-orders-prod.json", "v2-apps/instances/demo-orders/prod/networkpolicy.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path))); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	// A repeat changes nothing.
	out.Reset()
	if status := run(compileArgs(dir), &out, &diagnostic, inputs(t), files.New()); status != 0 {
		t.Fatalf("repeat status %d: %s", status, &diagnostic)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, "unchanged ") {
			t.Errorf("repeat line %q, want unchanged", line)
		}
	}
}

// A destination may not resolve into a checkout's apps/, where v1 records its instances: not when it
// is named as apps/, not through a symlink, and not when it only looks like one.
func TestCompileOutRefusesTheAppTree(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"clusters", "apps"} {
		if err := os.MkdirAll(filepath.Join(repo, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(repo, "out-link")
	if err := os.Symlink(filepath.Join(repo, "apps"), link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	for _, dir := range []string{filepath.Join(repo, "apps"), filepath.Join(repo, "apps", "demo"), link} {
		t.Run(dir, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			if status := run(compileArgs(dir), &out, &diagnostic, inputs(t), files.New()); status != 2 {
				t.Fatalf("status %d, want 2: %s", status, &diagnostic)
			}
			if !strings.Contains(diagnostic.String(), files.RefusedDestination) || out.Len() != 0 {
				t.Fatalf("stdout %q, stderr %q", out.String(), diagnostic.String())
			}
			if strings.Contains(diagnostic.String(), dir) {
				t.Error("the refusal echoed the destination")
			}
		})
	}
	entries, err := os.ReadDir(filepath.Join(repo, "apps"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused destination wrote %d entries into apps/", len(entries))
	}
	// A folder that merely has "apps" in its path, outside any checkout, is an ordinary destination.
	elsewhere := filepath.Join(t.TempDir(), "home", "apps", "project", "out")
	var out, diagnostic bytes.Buffer
	if status := run(compileArgs(elsewhere), &out, &diagnostic, inputs(t), files.New()); status != 0 {
		t.Fatalf("a non-checkout apps/ path: status %d: %s", status, &diagnostic)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "clusters", "home", "app-demo-orders-prod.json")); err != nil {
		t.Errorf("the unit was not written: %v", err)
	}
}

// An explicitly empty destination is a usage error, not a request to print the document.
func TestCompileOutRejectsAnEmptyDestination(t *testing.T) {
	var out, diagnostic bytes.Buffer
	args := []string{"compile", "--instance", "prod.json", "--release", "r41.json", "--app", "app.json", "--profile", "profile.json", "--out", ""}
	if status := run(args, &out, &diagnostic, inputs(t), files.New()); status != 2 {
		t.Fatalf("status %d, want 2", status)
	}
	if !strings.Contains(diagnostic.String(), "usage") || strings.Contains(out.String(), "v2-compilation") {
		t.Fatalf("stdout %q, stderr %q", out.String(), diagnostic.String())
	}
}

// The command hands the compiled files to the adapter and maps its failures to exit codes.
func TestCompileOutAdapterOutcomes(t *testing.T) {
	var out, diagnostic bytes.Buffer
	fake := filestest.New()
	if status := run(compileArgs("/destination"), &out, &diagnostic, inputs(t), fake); status != 0 {
		t.Fatalf("status %d: %s", status, &diagnostic)
	}
	if fake.Writes != 1 || len(fake.Files) != 6 {
		t.Fatalf("%d writes of %d files", fake.Writes, len(fake.Files))
	}
	for _, tt := range []struct {
		fail   error
		status int
	}{{&files.Error{Code: files.RefusedDestination, Path: "x", Reason: "symlink_in_path"}, 2},
		{&files.Error{Code: files.IO, Path: "x", Reason: "cannot write the file"}, 1}} {
		diagnostic.Reset()
		fake.Fail = tt.fail
		if status := run(compileArgs("/destination"), &out, &diagnostic, inputs(t), fake); status != tt.status {
			t.Errorf("status %d, want %d: %s", status, tt.status, &diagnostic)
		}
	}
}
