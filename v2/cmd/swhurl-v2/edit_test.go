package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// editReport is the document `edit` writes.
type editReport struct {
	Kind            string `json:"kind"`
	Change          string `json:"change"`
	Application     string `json:"application"`
	Environment     string `json:"environment"`
	Before, After   string
	ResolutionStale bool     `json:"resolutionStale"`
	Instances       []string `json:"instances"`
	Files           []struct {
		Path  string `json:"path"`
		State string `json:"state"`
	} `json:"files"`
}

// editTree is a records tree for one instance, as a checkout keeps it: the destination that writes it is its parent.
func editTree(t *testing.T) string {
	t.Helper()
	return recordsTree(t)
}

// editRun runs one edit and reports what the command made of it. The destination is the tree's parent, as a checkout
// names it: the record is written where it was read.
func editRun(t *testing.T, root string, extra ...string) (int, editReport, string, *filestest.Fake) {
	t.Helper()
	writer := filestest.New()
	var stdout, diagnostic bytes.Buffer
	args := append([]string{"edit", "--records", root, "--app", recordsFixture, "--profile", "../../testdata/profile.json",
		"--out", filepath.Dir(root)}, extra...)
	status := run(args, &stdout, &diagnostic, os.ReadFile, writer)
	var report editReport
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("the report is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return status, report, diagnostic.String(), writer
}

// One change writes the instance's record and the files it renders, and says what it changed.
func TestEditCLI(t *testing.T) {
	root := editTree(t)
	status, report, diagnostic, writer := editRun(t, root, "--env", "staging", "--set", "replicas=3")
	if status != 0 {
		t.Fatalf("edit exited %d: %s", status, diagnostic)
	}
	if report.Kind != "v2-edit" || report.Change != "replicas" || report.Application != recordsFixture {
		t.Fatalf("the report is %+v", report)
	}
	if report.Before == report.After || report.ResolutionStale {
		t.Errorf("the report's identities are %s → %s (stale %v)", report.Before, report.After, report.ResolutionStale)
	}
	records, written := writer.Contents["v2-records/demo-orders/staging/instance.json"]
	if !written {
		t.Fatal("the record was not written")
	}
	instance, err := kernel.DecodeInstance(records)
	if err != nil || instance.Configuration.Replicas != 3 {
		t.Fatalf("the written record is %s (%v)", records, err)
	}
	if _, kept := writer.Contents["v2-records/demo-orders/staging/events.sops.yaml"]; kept {
		t.Error("the edit wrote the seed, which is a declared input it copies nowhere")
	}
	if _, written := writer.Contents["v2-apps/instances/demo-orders/staging/helmrelease.json"]; !written {
		t.Error("the compiled files were not written")
	}
}

// A setting v2 does not model is refused by name, and so is a change that changes nothing.
func TestEditCLIRefusals(t *testing.T) {
	root := editTree(t)
	for _, refused := range []struct{ setting, want string }{
		{"egress=example.com", "refused: egress: not_modelled"},
		{"alerts=none", "refused: alerts: not_modelled"},
		{"replicas=2", "refused: replicas: already_set"},
		{"replicas=x", "invalid: replicas: expected an integer"},
		{"hosts=a", `invalid: set: unknown setting "hosts"`},
	} {
		status, _, diagnostic, writer := editRun(t, root, "--env", "staging", "--set", refused.setting)
		if status != 2 || !strings.Contains(diagnostic, refused.want) {
			t.Errorf("%s exited %d: %s", refused.setting, status, diagnostic)
		}
		if writer.Writes != 0 {
			t.Errorf("%s wrote something", refused.setting)
		}
	}
}

// A relationship change writes the record and says the resolution is older than it.
func TestEditCLISaysTheResolutionIsStale(t *testing.T) {
	root := editTree(t)
	status, report, diagnostic, _ := editRun(t, root, "--env", "staging",
		"--set", "calls=demo-shop,demo-shopper")
	if status != 0 {
		t.Fatalf("edit exited %d: %s", status, diagnostic)
	}
	if !report.ResolutionStale || !strings.Contains(diagnostic, "resolution is older") || len(report.Files) == 0 {
		t.Fatalf("the report is %+v (%s)", report, diagnostic)
	}
}

// A team names no environment: the change rewrites the app record and every environment the app has.
func TestEditCLITeam(t *testing.T) {
	root := editTree(t)
	prod := filepath.Join(root, recordsFixture, "prod")
	if err := os.MkdirAll(prod, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{"instance.json": "prod.json", "release.json": "r41.json"} {
		if err := os.WriteFile(filepath.Join(prod, name), fixture(t, source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	status, report, diagnostic, writer := editRun(t, root, "--set", "team=payments")
	if status != 0 {
		t.Fatalf("edit exited %d: %s", status, diagnostic)
	}
	if report.Change != "team" || len(report.Instances) != 2 {
		t.Fatalf("the report is %+v", report)
	}
	app, written := writer.Contents["v2-records/demo-orders/app.json"]
	if !written {
		t.Fatal("the app record was not written")
	}
	decoded, err := kernel.DecodeApp(app)
	if err != nil || decoded.Team != "payments" {
		t.Fatalf("the app record is %s (%v)", app, err)
	}
	if _, written := writer.Contents["v2-apps/instances/demo-orders/prod/namespace.json"]; !written {
		t.Error("production was not re-rendered")
	}
}
