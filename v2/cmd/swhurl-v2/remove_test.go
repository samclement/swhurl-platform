package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
)

// removeReport is the document `remove` writes.
type removeReport struct {
	Kind        string `json:"kind"`
	Application string `json:"application"`
	Environment string `json:"environment"`
	Files       []struct {
		Path  string `json:"path"`
		State string `json:"state"`
	} `json:"files"`
	Removed []struct {
		Path  string `json:"path"`
		State string `json:"state"`
	} `json:"removed"`
	Notes []string `json:"notes"`
}

// removalTree is a checkout: the instance's records under v2-records, its unit and its auth object registered, and the
// resolution beside them. It returns the destination and the writer primed with the files the instance already has.
func removalTree(t *testing.T, calls string) (string, *filestest.Fake) {
	t.Helper()
	root := recordsTree(t)
	out := filepath.Dir(root)
	registrations := map[string]string{
		"clusters/home/kustomization.yaml": "resources:\n  - infra.yaml\n  - app-demo-orders-staging.json\n",
		// v2 renders the auth folder's kustomization as JSON, so its list is a JSON array.
		"platform/auth/kustomization.yaml": `{"apiVersion":"kustomize.config.k8s.io/v1beta1","kind":"Kustomization",` +
			`"resources":["swhurl-auth-demo-orders-prod.json","swhurl-auth-demo-orders-staging.json"]}`,
	}
	for name, document := range registrations {
		path := filepath.Join(out, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "calls.json"), []byte(calls), 0o644); err != nil {
		t.Fatal(err)
	}
	writer := filestest.New()
	for _, path := range []string{"v2-apps/instances/demo-orders/staging/namespace.json",
		"clusters/home/app-demo-orders-staging.json", "platform/auth/swhurl-auth-demo-orders-staging.json"} {
		writer.Contents[path] = []byte("{}")
	}
	return out, writer
}

// emptyResolution is a resolution with no relationship in it.
const emptyResolution = `{"kind":"v2-calls","version":1,"revision":"8ae21b491dcbbe7306bc56073460fff24d61a7f2",` +
	`"pairs":[],"waiting":[],"refused":[],"subscriptions":[]}`

// removalRun runs one removal at a destination and reports what the command made of it.
func removalRun(t *testing.T, out string, writer *filestest.Fake) (int, removeReport, string) {
	t.Helper()
	var stdout, diagnostic bytes.Buffer
	args := []string{"remove", "--records", filepath.Join(out, "v2-records"), "--app", recordsFixture,
		"--env", "staging", "--profile", "../../testdata/profile.json", "--calls",
		filepath.Join(out, "v2-records", "calls.json"), "--out", out}
	status := run(args, &stdout, &diagnostic, os.ReadFile, writer)
	var report removeReport
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("the report is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return status, report, diagnostic.String()
}

// A removal deletes the instance's files and the registrations that named them, and says what it left to the platform.
func TestRemoveCLI(t *testing.T) {
	out, writer := removalTree(t, emptyResolution)
	writer.Contents["v2-records/demo-orders/staging/instance.json"] = []byte("{}")
	writer.Contents["v2-records/demo-orders/app.json"] = []byte("{}")
	status, report, diagnostic := removalRun(t, out, writer)
	if status != 0 {
		t.Fatalf("remove exited %d: %s", status, diagnostic)
	}
	if report.Kind != "v2-remove" || report.Application != recordsFixture || report.Environment != "staging" {
		t.Fatalf("the report is %+v", report)
	}
	for _, path := range []string{"v2-records/demo-orders/staging/instance.json", "v2-records/demo-orders/app.json",
		"v2-apps/instances/demo-orders/staging/namespace.json", "clusters/home/app-demo-orders-staging.json",
		"platform/auth/swhurl-auth-demo-orders-staging.json"} {
		if _, kept := writer.Contents[path]; kept {
			t.Errorf("%s survived the removal", path)
		}
	}
	for _, path := range []string{"clusters/home/kustomization.yaml", "platform/auth/kustomization.yaml"} {
		document := string(writer.Contents[path])
		if strings.Contains(document, "demo-orders-staging") || !strings.Contains(document, "infra.yaml") &&
			path == "clusters/home/kustomization.yaml" {
			t.Errorf("%s is %s", path, document)
		}
		if path == "platform/auth/kustomization.yaml" && !strings.Contains(document, "swhurl-auth-demo-orders-prod.json") {
			t.Errorf("the auth list lost a resource it did not name: %s", document)
		}
	}
	if len(report.Notes) == 0 || !strings.Contains(strings.Join(report.Notes, " "), "v2-users") {
		t.Errorf("the report leaves nothing to the platform: %v", report.Notes)
	}
}

// An instance the resolution still connects is refused, and nothing is deleted.
func TestRemoveCLIRefusesAConnectedInstance(t *testing.T) {
	connected := strings.Replace(emptyResolution, `"pairs":[]`,
		`"pairs":[{"caller":"demo-shop","callee":"demo-orders","environment":"staging","port":8080}]`, 1)
	out, writer := removalTree(t, connected)
	status, _, diagnostic := removalRun(t, out, writer)
	if status != 2 || !strings.Contains(diagnostic, "called_by demo-shop") {
		t.Fatalf("remove exited %d: %s", status, diagnostic)
	}
	if writer.Removals != 0 || writer.Writes != 0 {
		t.Error("a refused removal changed something")
	}
	for path := range writer.Contents {
		if _, kept := writer.Contents[path]; !kept {
			t.Errorf("a refused removal deleted %s", path)
		}
	}
	if len(writer.Contents) != 3 {
		t.Errorf("a refused removal left %d files, want the three it was handed", len(writer.Contents))
	}
}
