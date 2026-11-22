package rules

import (
	"os"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

const revision = "0123456789abcdef0123456789abcdef01234567"

// input is a compiled instance's documents: the fixture records, with the optional documents when given.
func input(t *testing.T, calls, egress []byte) Inputs {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile("../../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return Inputs{Instance: read("prod.json"), Release: read("r41.json"), App: read("app.json"),
		Profile: read("profile.json"), Calls: calls, Egress: egress}
}

// Compile compiles what the kernel compiles from the same values: it adds assembly, not rules.
func TestCompileIsTheKernelsCompilation(t *testing.T) {
	inputs := input(t, nil, nil)
	compiled, err := Compile(inputs)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		t.Fatal(err)
	}
	release, err := kernel.DecodeRelease(inputs.Release)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := kernel.DecodeProfile(inputs.Profile)
	if err != nil {
		t.Fatal(err)
	}
	app, err := kernel.DecodeApp(inputs.App)
	if err != nil {
		t.Fatal(err)
	}
	want, err := kernel.CompileWith(spec, release, app, profile, kernel.Connections{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.ID != want.ID || len(compiled.Files) != len(want.Files) {
		t.Fatalf("Compile and the kernel disagree: %s %s", compiled.ID, want.ID)
	}
}

// The optional documents reach the kernel: a pair renders as a call, an allocation as an egress.
func TestCompileTakesTheDocuments(t *testing.T) {
	calls := []byte(`{"kind":"v2-calls","version":1,"revision":"` + revision + `","pairs":[
		{"caller":"demo-orders","callee":"billing","environment":"prod","port":8081}]}`)
	egress := []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[
		{"application":"demo-orders","environment":"prod","port":30111,"grants":["api.example.com:443"]}]}`)
	compiled, err := Compile(input(t, calls, egress))
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Inputs.Calls == "" {
		t.Fatal("the resolution did not reach the compilation")
	}
	found := false
	for _, file := range compiled.Files {
		if strings.HasSuffix(file.Path, "/helmrelease.json") {
			found = strings.Contains(string(file.Document), "SWHURL_SERVICE_BILLING_URL") &&
				strings.Contains(string(file.Document), "egress.egress.svc.cluster.local:30111")
		}
	}
	if !found {
		t.Fatal("the documents did not reach the rendered instance")
	}
}

// A document that was given is read even when the instance needs nothing from it: a malformed one is refused,
// never ignored.
func TestCompileRefusesAMalformedDocument(t *testing.T) {
	for _, tt := range []struct {
		name          string
		calls, egress []byte
		want          string
	}{
		{"calls", []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `"}`), nil, "unsupported: calls"},
		{"egress", nil, []byte(`{"kind":"v2-calls","version":1,"revision":"` + revision + `"}`), "unsupported: egress"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(input(t, tt.calls, tt.egress))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
