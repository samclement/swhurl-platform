package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/adapters/helm/helmtest"
)

func policyRun(t *testing.T, charts helm.Renderer, args ...string) (int, policyDocument, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	writer := filestest.New()
	status := runWith(append([]string{"policy"}, args...), &out, &diagnostic, cli{readFile: os.ReadFile, writer: writer, charts: charts})
	if writer.Writes+writer.Prunes+writer.Removals > 0 {
		t.Fatal("read-only policy invoked the writer")
	}
	var document policyDocument
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
	}
	return status, document, out.String(), diagnostic.String()
}
func TestPolicyVisitsEveryInstanceEvenAfterARenderFailure(t *testing.T) {
	out, records := renderTree(t)
	before := treeContents(t, out)
	renderer := &helmtest.Fake{Err: errors.New("sensitive-sentinel")}
	status, report, stdout, diagnostic := policyRun(t, renderer, renderArgs(out, records, "--chart", "explicit-local-chart")...)
	if status != 1 || len(report.Instances) != 3 || len(renderer.Calls) != 3 {
		t.Fatalf("status %d, %d instances, %d calls: %s", status, len(report.Instances), len(renderer.Calls), diagnostic)
	}
	namespaces := []string{}
	for _, call := range renderer.Calls {
		if call.Chart != "explicit-local-chart" || call.ChartName != "app-template" || call.ChartVersion != "5.3.0" {
			t.Fatal("chart identity not supplied from the profile")
		}
		namespaces = append(namespaces, call.Namespace)
	}
	if strings.Join(namespaces, ",") != "demo-orders-prod,demo-orders-staging,demo-shop-staging" {
		t.Fatalf("not the complete stable tree: %v", namespaces)
	}
	for _, result := range report.Instances {
		if result.RenderFailure != helm.RefusedRender || len(result.Findings) != 0 {
			t.Fatal("render failure confused with findings")
		}
	}
	if strings.Contains(stdout+diagnostic, "sensitive-sentinel") {
		t.Fatal("renderer diagnostic escaped")
	}
	if !reflect.DeepEqual(before, treeContents(t, out)) {
		t.Fatal("policy changed the checkout")
	}
}
func TestPolicyInvalidWholeTreeHasNoPartialReportOrRender(t *testing.T) {
	for _, bad := range []string{"late-instance", "release", "profile", "teams"} {
		t.Run(bad, func(t *testing.T) {
			out, records := renderTree(t)
			args := renderArgs(out, records, "--chart", "explicit-local-chart")
			path := filepath.Join(records, "demo-shop", "staging", instanceFile)
			if bad == "release" {
				path = filepath.Join(records, "demo-shop", "staging", releaseFile)
			}
			if bad == "teams" {
				path = filepath.Join(records, "teams.json")
			}
			if bad == "profile" {
				path = filepath.Join(out, "bad-profile.json")
				args = []string{"--records", records, "--profile", path, "--out", out, "--chart", "explicit-local-chart"}
			}
			if err := os.WriteFile(path, []byte(`{"invalid":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			renderer := &helmtest.Fake{Err: errors.New("unreached")}
			status, _, stdout, _ := policyRun(t, renderer, args...)
			if status == 0 || stdout != "" || len(renderer.Calls) > 0 {
				t.Fatalf("invalid %s partially reported/rendered: %d %q", bad, status, stdout)
			}
		})
	}
}
func TestPolicyFlagsAndHelp(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--records", "r", "--profile", "p", "--out", "o"},
		{"--records", "r", "--profile", "p", "--out", "o", "--chart", "c", "--chart", "c"},
		{"--records", "r", "--profile", "p", "--out", "o", "--chart", "c", "--check"},
	} {
		var out, diagnostic bytes.Buffer
		status := runWith(append([]string{"policy"}, args...), &out, &diagnostic, cli{})
		if status != 2 || out.Len() != 0 {
			t.Fatalf("invalid flags passed: %v", args)
		}
	}
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"policy", "--help"}, &out, &diagnostic, cli{})
	if status != 0 || !strings.Contains(out.String(), "--chart LOCAL_DIR") {
		t.Fatalf("missing help: %s %s", out.String(), diagnostic.String())
	}
}

type oneFailure struct {
	delegate helm.Renderer
	calls    int
}

func (r *oneFailure) Render(request helm.Request) ([]json.RawMessage, error) {
	r.calls++
	if r.calls == 1 {
		return nil, errors.New("sensitive-sentinel")
	}
	return r.delegate.Render(request)
}
func realPolicyChart(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(cache, "swhurl-platform/charts/app-template-5.3.0")
	_, binary := exec.LookPath("helm")
	_, metadata := os.Stat(filepath.Join(chart, "Chart.yaml"))
	if binary != nil || metadata != nil {
		if os.Getenv("REQUIRE_HELM") != "" {
			t.Fatal("REQUIRE_HELM needs Helm and the cached pinned chart")
		}
		t.Skip("run make check-apps to cache the pinned chart; Helm is also required")
	}
	return chart
}
func TestPolicyRealChartWholeTreeAndOneFailure(t *testing.T) {
	chart := realPolicyChart(t)
	out, records := renderTree(t)
	before := treeContents(t, out)
	args := renderArgs(out, records, "--chart", chart)
	status, report, _, diagnostic := policyRun(t, helm.New(), args...)
	if status != 0 || len(report.Instances) != 3 {
		t.Fatalf("real chart: %d %v %s", status, report, diagnostic)
	}
	renderer := &oneFailure{delegate: helm.New()}
	status, report, _, diagnostic = policyRun(t, renderer, args...)
	if status != 1 || renderer.calls != 3 || len(report.Instances) != 3 {
		t.Fatalf("one failure lost the rest: %d %v %s", status, report, diagnostic)
	}
	if report.Instances[0].RenderFailure == "" || report.Instances[1].RenderFailure != "" || report.Instances[2].RenderFailure != "" {
		t.Fatal("one failure not reported distinctly")
	}
	if !reflect.DeepEqual(before, treeContents(t, out)) {
		t.Fatal("policy changed the checkout")
	}
}

func TestPolicyViolationIsNonzeroAndMalformedObjectsAreAtomic(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		out, records := renderTree(t)
		raw := json.RawMessage(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"harmless"}}`)
		if malformed {
			raw = json.RawMessage(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"bad"},"spec":[]}`)
		}
		renderer := &helmtest.Fake{Output: []json.RawMessage{raw}}
		status, report, stdout, _ := policyRun(t, renderer, renderArgs(out, records, "--chart", "explicit-local-chart")...)
		if status == 0 {
			t.Fatal("failed policy passed")
		}
		if malformed {
			if stdout != "" {
				t.Fatal("malformed object printed a partial report")
			}
			continue
		}
		if status != 1 || len(report.Instances) != 3 {
			t.Fatal("violations did not accumulate for every instance")
		}
		for _, result := range report.Instances {
			if result.RenderFailure != "" || len(result.Findings) == 0 {
				t.Fatal("policy findings confused with render failures")
			}
		}
	}
}
