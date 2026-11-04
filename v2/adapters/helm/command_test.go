package helm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func request() Request {
	return Request{Chart: "testdata/chart", ChartName: "boundary", ChartVersion: "0.1.0", Release: "boundary", Namespace: "staging", Values: json.RawMessage(`{"retained":"unchanged"}`)}
}

func executable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func refusedResult(t *testing.T, command *Command, req Request, reason string) {
	t.Helper()
	result, err := command.Render(req)
	refusal, ok := err.(*Error)
	if result != nil || !ok || refusal.Reason != reason {
		t.Fatalf("got %d objects, %v; want %s", len(result), err, reason)
	}
}

func TestCommandFailuresAreAtomicAndRedacted(t *testing.T) {
	raw := string(rawFixture(t))
	cases := []struct{ name, body, reason string }{
		{"nonzero", "printf '%s' '" + raw + "'; printf 'sensitive-sentinel' >&2; exit 1", "helm_failed"},
		{"parser", "printf '%s' 'apiVersion: [sensitive-sentinel'", "invalid_yaml"},
		{"stdout", "head -c " + strconv.Itoa(MaxOutput+1) + " /dev/zero", "output_too_large"},
		{"stderr", "head -c " + strconv.Itoa(MaxStderr+1) + " /dev/zero >&2", "stderr_too_large"},
		{"empty", ":", "empty_output"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refusedResult(t, &Command{Binary: executable(t, tc.body)}, request(), tc.reason)
		})
	}
	refusedResult(t, &Command{Binary: filepath.Join(t.TempDir(), "missing")}, request(), "helm_failed")
	refusedResult(t, &Command{Binary: executable(t, "exec sleep 10"), timeout: 25 * time.Millisecond}, request(), "timeout")
}

func TestFixedTemplateArgumentsPrivateValuesAndNoChartChanges(t *testing.T) {
	req := request()
	req.Values = json.RawMessage(`{"secret":"sensitive-sentinel","exact":9007199254740993}`)
	trace := filepath.Join(t.TempDir(), "trace")
	// The script itself is the executable boundary: it accepts only the complete, fixed template argv.
	body := `test "$#" = 8 && test "$8" = --include-crds && test "$1" = template && test "$2" = boundary && test "$4" = --namespace && test "$5" = staging && test "$6" = --values || exit 1
    test "$3" = '` + mustAbs(t, req.Chart) + `' || exit 1
    test "$(stat -c %a "$7")" = 600 || exit 1
    test "$(stat -c %a "$(dirname "$7")")" = 700 || exit 1
    printf '%s' "$7" > '` + trace + `'
    cat "$7" | grep -q '"exact":9007199254740993' || exit 1
    printf '%s' '` + string(rawFixture(t)) + `'`
	chartBefore, _ := os.ReadFile(filepath.Join(req.Chart, "templates/objects.yaml"))
	result, err := (&Command{Binary: executable(t, body)}).Render(req)
	if err != nil || len(result) != 2 {
		t.Fatalf("%v, %d objects", err, len(result))
	}
	values, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(values)); !os.IsNotExist(err) {
		t.Fatal("temporary values retained")
	}
	chartAfter, _ := os.ReadFile(filepath.Join(req.Chart, "templates/objects.yaml"))
	if string(chartBefore) != string(chartAfter) {
		t.Fatal("chart patched")
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func TestInputsFailBeforeStartingHelm(t *testing.T) {
	for _, change := range []func(*Request){
		func(r *Request) { r.Chart = "https://example.test/chart" },
		func(r *Request) { r.Chart = "" },
		func(r *Request) { r.Release = "--flag" },
		func(r *Request) { r.Release = strings.Repeat("a", 54) },
		func(r *Request) { r.Namespace = "invalid/namespace" },
		func(r *Request) { r.Namespace = "" },
		func(r *Request) { r.Values = json.RawMessage(`null`) },
		func(r *Request) { r.Values = json.RawMessage(`[]`) },
		func(r *Request) { r.Values = json.RawMessage(`{"a":1,"a":2}`) },
		func(r *Request) { r.Values = json.RawMessage(`{"nested":{"a":1,"a":2}}`) },
		func(r *Request) { r.Values = json.RawMessage(`{} {}`) },
		func(r *Request) { r.Values = json.RawMessage(strings.Repeat(" ", MaxValues+1)) },
	} {
		req := request()
		change(&req)
		result, err := (&Command{Binary: "must-not-be-run"}).Render(req)
		refusal, ok := err.(*Error)
		if result != nil || !ok || refusal.Reason == "helm_failed" {
			t.Fatal("input was not rejected before Helm")
		}
	}
}

func TestFailedCommandAlsoRemovesPrivateValues(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	binary := executable(t, `printf '%s' "$7" > '`+trace+`'; exit 1`)
	refusedResult(t, &Command{Binary: binary}, request(), "helm_failed")
	values, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(values)); !os.IsNotExist(err) {
		t.Fatal("failed render retained private values")
	}
}

func TestChartIdentityIsReadNotInferredFromTheDirectory(t *testing.T) {
	for _, change := range []func(*Request){
		func(r *Request) { r.ChartName = "another-chart" },
		func(r *Request) { r.ChartVersion = "9.9.9" },
		func(r *Request) { r.ChartName = "" },
	} {
		req := request()
		change(&req)
		refusedResult(t, &Command{Binary: "must-not-be-run"}, req, "chart_mismatch")
	}
}

func TestChartMetadataMustBeBoundedAndOneValidDocument(t *testing.T) {
	for _, text := range []string{
		"apiVersion: v2\nname: boundary\nversion: 0.1.0\n---\nname: boundary\n",
		"apiVersion: v2\nname: [\n",
		"apiVersion: v2\nname: boundary\nversion: 0.1.0\ndescription: " + strings.Repeat("x", 64<<10),
	} {
		chart := t.TempDir()
		if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		req := request()
		req.Chart = chart
		objects, err := (&Command{Binary: "must-not-execute"}).Render(req)
		if objects != nil || err == nil || err.Error() != "refused_render: invalid_chart" {
			t.Fatalf("metadata was not rejected before execution: %v", err)
		}
	}
}
