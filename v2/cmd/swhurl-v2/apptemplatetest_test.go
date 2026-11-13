package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/adapters/route"
	"github.com/samclement/swhurl-platform/v2/adapters/route/routetest"
	"github.com/samclement/swhurl-platform/v2/apptemplatetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// checkoutFile reads a path of this checkout, as the command does from the repository's root.
func checkoutFile(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(path)))
}

// readyUnits is the least of Flux a failed run needs: every unit it creates is there and never Ready.
type readyUnits struct{ *kubetest.Fake }

func (c readyUnits) Create(ctx context.Context, ref observe.Ref, document []byte) error {
	c.Objects[ref] = []byte(`{"status":{"conditions":[{"type":"Ready","status":"False","message":"dependency 'flux-system/platform-backup' is not ready"}]}}`)
	return c.Fake.Create(ctx, ref, document)
}

func (c readyUnits) Delete(ctx context.Context, ref observe.Ref) error {
	delete(c.Objects, ref)
	return c.Fake.Delete(ctx, ref)
}

func runAppTemplateTest(cluster *kubetest.Fake, reader kube.Reader, writer kube.Writer, prober route.Prober,
	read func(string) ([]byte, error), args ...string) (int, string, string) {
	var out, diagnostic bytes.Buffer
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	status := runWith(append([]string{"app-template-test"}, args...), &out, &diagnostic, cli{readFile: read,
		clock: func() time.Time { return now }, sleep: func(d time.Duration) { now = now.Add(d) }, prober: prober,
		dataClusterOf: func() (dataCluster, error) {
			return dataCluster{reader: reader, writer: writer, secrets: cluster}, nil
		}})
	return status, out.String(), diagnostic.String()
}

// The committed render is what the command reads: a unit that is never Ready fails the install after its ten minutes
// with what its Ready condition said, exit 1, and the cleanup still runs.
func TestAppTemplateTestExitsOnAFailedStep(t *testing.T) {
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{}
	cluster := readyUnits{fake}
	status, out, diagnostic := runAppTemplateTest(fake, cluster, cluster, routetest.New(), checkoutFile, "--kubeconfig", "unread.json")
	want := "[BAD] smoke-worker-prod unit not Ready after 10m0s: dependency 'flux-system/platform-backup' is not ready\n" +
		"[INFO] Cleaned up app-template test instances\n"
	if status != 1 || out != want || diagnostic != "" {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
	if len(fake.Writes) < 2 || fake.Writes[0].Name != "app-smoke-worker-prod" || fake.Writes[1].Name != "app-smoke-web-prod" {
		t.Fatalf("writes %v", fake.Writes)
	}
}

// Leftovers from an earlier run are one [ERROR] line and exit 1, with nothing created or printed on stdout.
func TestAppTemplateTestRefusesLeftovers(t *testing.T) {
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{{APIVersion: "v1", Kind: "Namespace", Name: "smoke-web-prod"}: []byte(`{}`)}
	status, out, diagnostic := runAppTemplateTest(fake, fake, fake, routetest.New(), checkoutFile, "--kubeconfig", "unread.json")
	if status != 1 || out != "" || len(fake.Writes) != 0 || diagnostic != "[ERROR] smoke-web-prod already exists; clean up first\n" {
		t.Fatalf("%d %q %q %v", status, out, diagnostic, fake.Writes)
	}
}

// A fixture the command cannot read, or one that is not render's, is refused before the cluster is touched.
func TestAppTemplateTestRefusesAnUnreadableFixture(t *testing.T) {
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{}
	missing := func(path string) ([]byte, error) {
		if path == apptemplatetest.IngressFile {
			return nil, os.ErrNotExist
		}
		return checkoutFile(path)
	}
	status, out, diagnostic := runAppTemplateTest(fake, fake, fake, routetest.New(), missing, "--kubeconfig", "unread.json")
	if status != 1 || out != "" || diagnostic != "io: fixture ingress: cannot read input file\n" || len(fake.Reads)+len(fake.Writes) != 0 {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
	other := func(path string) ([]byte, error) {
		document, err := checkoutFile(path)
		return bytes.ReplaceAll(document, []byte("app-smoke-worker-prod"), []byte("app-other-prod")), err
	}
	status, _, diagnostic = runAppTemplateTest(fake, fake, fake, routetest.New(), other, "--kubeconfig", "unread.json")
	if status != 2 || !strings.Contains(diagnostic, "expected the Kustomization app-smoke-worker-prod") || len(fake.Reads)+len(fake.Writes) != 0 {
		t.Fatalf("%d %q", status, diagnostic)
	}
}

// A dry run prints the plan and reads nothing, needing no kubeconfig; without one, a run is a usage error.
func TestAppTemplateTestDryRunAndUsage(t *testing.T) {
	read := false
	seams := cli{readFile: func(string) ([]byte, error) { read = true; return nil, errors.New("no file") },
		dataClusterOf: func() (dataCluster, error) { read = true; return dataCluster{}, nil }}
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"app-template-test", "--dry-run"}, &out, &diagnostic, seams)
	if status != 0 || out.String() != apptemplatetest.Plan+"\n" || diagnostic.Len() != 0 || read {
		t.Fatalf("%d %q %q read %v", status, out.String(), diagnostic.String(), read)
	}
	out.Reset()
	if status := runWith([]string{"app-template-test"}, &out, &diagnostic, seams); status != 2 || out.Len() != 0 || read {
		t.Fatalf("no kubeconfig: %d read %v", status, read)
	}
}
