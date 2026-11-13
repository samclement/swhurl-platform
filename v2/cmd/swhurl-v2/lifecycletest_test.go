package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/lifecycletest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// fixtureCluster is the least of Flux and the provisioner a passing run needs: a unit is Ready and handles every
// request at once and applies the fixture, a MirrorPrune unit's deletion prunes the workload, the reader pod
// succeeds, and a volume whose reclaim policy is Delete goes with its claim.
type fixtureCluster struct {
	*kubetest.Fake
	policies map[string]string
	volumes  int
	reclaim  []observe.Ref
}

var (
	fixtureOrphanUnit = observe.Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization", Namespace: "flux-system",
		Name: "lifecycle-test-orphan"}
	fixtureNamespace  = observe.Ref{APIVersion: "v1", Kind: "Namespace", Name: "lifecycle-test"}
	fixtureClaim      = observe.Ref{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "lifecycle-test", Name: "data"}
	fixtureDeployment = observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "lifecycle-test", Name: "writer"}
	fixturePods       = observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: "lifecycle-test"}
)

func newFixtureCluster() *fixtureCluster {
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{}
	fake.Collections = map[observe.Ref][][]byte{fixturePods: {}}
	fake.Log = "Fri Nov 13 09:00:00 UTC 2026\n"
	return &fixtureCluster{Fake: fake, policies: map[string]string{}}
}

func (c *fixtureCluster) Create(ctx context.Context, ref observe.Ref, document []byte) error {
	if ref.Kind == "Pod" {
		c.Objects[ref] = []byte(`{"status":{"phase":"Succeeded"}}`)
		return c.Fake.Create(ctx, ref, document)
	}
	var unit struct {
		Spec struct {
			DeletionPolicy string `json:"deletionPolicy"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(document, &unit); err != nil {
		return err
	}
	c.policies[ref.Name] = unit.Spec.DeletionPolicy
	c.Objects[ref] = unitDocument(false, "")
	c.Objects[fixtureNamespace] = []byte(`{"metadata":{"labels":{"platform.swhurl.com/lifecycle-test":"true"}}}`)
	if _, bound := c.Objects[fixtureClaim]; !bound {
		c.volumes++
		volume := fmt.Sprintf("pv-%d", c.volumes)
		c.Objects[fixtureClaim] = []byte(`{"spec":{"volumeName":"` + volume + `"},"status":{"phase":"Bound"}}`)
		c.Objects[observe.Ref{APIVersion: "v1", Kind: "PersistentVolume", Name: volume}] = []byte(`{"spec":{"claimRef":{"namespace":"lifecycle-test"}}}`)
	}
	c.Objects[fixtureDeployment] = []byte(`{"status":{"availableReplicas":1}}`)
	c.Collections[fixturePods] = [][]byte{[]byte(`{"metadata":{"name":"writer-1","namespace":"lifecycle-test"},"status":{"phase":"Running"}}`)}
	return c.Fake.Create(ctx, ref, document)
}

// unitDocument is a unit that has handled the request token and is Ready.
func unitDocument(suspend bool, token string) []byte {
	return []byte(fmt.Sprintf(`{"metadata":{"generation":1},"spec":{"suspend":%t,"sourceRef":{"kind":"GitRepository","name":"swhurl-platform"}},`+
		`"status":{"observedGeneration":1,"lastHandledReconcileAt":%q,"conditions":[{"type":"Ready","status":"True"}]}}`, suspend, token))
}

func (c *fixtureCluster) Patch(ctx context.Context, ref observe.Ref, patch []byte) error {
	var change struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Suspend bool `json:"suspend"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(patch, &change); err != nil {
		return err
	}
	switch ref.Kind {
	case "Kustomization":
		c.Objects[ref] = unitDocument(change.Spec.Suspend, change.Metadata.Annotations["reconcile.fluxcd.io/requestedAt"])
	case "PersistentVolume":
		c.reclaim = append(c.reclaim, ref)
	}
	c.settle()
	return c.Fake.Patch(ctx, ref, patch)
}

// settle deletes each volume whose reclaim policy is Delete once its claim is gone.
func (c *fixtureCluster) settle() {
	for _, volume := range c.reclaim {
		if claim, bound := c.Objects[fixtureClaim]; !bound || !bytes.Contains(claim, []byte(`"`+volume.Name+`"`)) {
			delete(c.Objects, volume)
		}
	}
}

func (c *fixtureCluster) Delete(ctx context.Context, ref observe.Ref) error {
	delete(c.Objects, ref)
	switch {
	case ref.Kind == "Kustomization" && c.policies[ref.Name] == "MirrorPrune":
		delete(c.Objects, fixtureDeployment)
		c.Collections[fixturePods] = [][]byte{}
	case ref.Kind == "Namespace":
		delete(c.Objects, fixtureClaim)
		delete(c.Objects, fixtureDeployment)
	}
	c.settle()
	return c.Fake.Delete(ctx, ref)
}

func runLifecycleTest(cluster *kubetest.Fake, reader kube.Reader, writer kube.Writer, args ...string) (int, string, string) {
	var out, diagnostic bytes.Buffer
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	executor := kubetest.NewExecutor(kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte(cluster.Log)}})
	status := runWith(append([]string{"lifecycle-test"}, args...), &out, &diagnostic, cli{
		clock: func() time.Time { return now }, sleep: func(d time.Duration) { now = now.Add(d) },
		dataClusterOf: func() (dataCluster, error) {
			return dataCluster{reader: reader, writer: writer, secrets: cluster}, nil
		},
		executorOf: func() kube.Executor { return executor }})
	return status, out.String(), diagnostic.String()
}

// The lines of make live-test-lifecycle's passing Python run on 13 November 2026, as printed, the volume's name aside.
const pythonLifecyclePassed = `
== Install disposable app ==
[OK] workload wrote data to pv-1

== Suspend and resume ==
[OK] unit suspended
[OK] workload still running while suspended
[OK] unit resumed and Ready

== Uninstall (delete app unit) ==
[OK] workload pruned
[OK] prune-protected namespace kept
[OK] prune-protected claim kept
[OK] data intact after uninstall

== Destroy data ==
[OK] destroy-data refuses without CONFIRM
[OK] claim untouched by refused destroy
[OK] claim destroyed
[OK] PV pv-1 and its data destroyed

== Orphan deletion policy (shared units) ==
[OK] workload kept running after its Orphan unit was deleted

Lifecycle test passed.
[INFO] Cleaned up lifecycle-test test resources
`

// A passing run prints Python's lines, byte for byte, and exits 0; a failed step exits 1 after its report and the
// cleanup's line.
func TestLifecycleTestExitsOnTheVerdicts(t *testing.T) {
	cluster := newFixtureCluster()
	status, out, diagnostic := runLifecycleTest(cluster.Fake, cluster, cluster, "--kubeconfig", "unread.json")
	if status != 0 || out != pythonLifecyclePassed || diagnostic != "" {
		t.Fatalf("%d %q\n%s", status, diagnostic, out)
	}
	// A unit that never becomes Ready: the install fails after its six minutes, and the cleanup still runs.
	never := kubetest.New(kubetest.Answers{})
	never.Objects = map[observe.Ref][]byte{}
	status, out, diagnostic = runLifecycleTest(never, never, never, "--kubeconfig", "unread.json")
	want := "\n== Install disposable app ==\n[BAD] Kustomization lifecycle-test not Ready after 6m0s\n" +
		"[INFO] Cleaned up lifecycle-test test resources\n"
	if status != 1 || out != want || diagnostic != "" {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
}

// Leftovers from an earlier run are one [ERROR] line and exit 1, with nothing created or printed on stdout.
func TestLifecycleTestRefusesLeftovers(t *testing.T) {
	cluster := newFixtureCluster()
	cluster.Objects[fixtureOrphanUnit] = unitDocument(false, "")
	status, out, diagnostic := runLifecycleTest(cluster.Fake, cluster, cluster, "--kubeconfig", "unread.json")
	if status != 1 || out != "" || len(cluster.Writes) != 0 ||
		diagnostic != "[ERROR] lifecycle-test namespace or lifecycle-test Kustomizations already exist; clean up first\n" {
		t.Fatalf("%d %q %q %v", status, out, diagnostic, cluster.Writes)
	}
}

// A dry run prints the plan and reads nothing, needing no kubeconfig; without one, a run is a usage error.
func TestLifecycleTestDryRunAndUsage(t *testing.T) {
	read := false
	seams := cli{readFile: func(string) ([]byte, error) { read = true; return nil, errors.New("no file") },
		dataClusterOf: func() (dataCluster, error) { read = true; return dataCluster{}, nil },
		executorOf:    func() kube.Executor { read = true; return nil }}
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"lifecycle-test", "--dry-run"}, &out, &diagnostic, seams)
	if status != 0 || out.String() != lifecycletest.Plan+"\n" || diagnostic.Len() != 0 || read {
		t.Fatalf("%d %q %q read %v", status, out.String(), diagnostic.String(), read)
	}
	out.Reset()
	if status := runWith([]string{"lifecycle-test"}, &out, &diagnostic, seams); status != 2 || out.Len() != 0 || read {
		t.Fatalf("no kubeconfig: %d read %v", status, read)
	}
}
