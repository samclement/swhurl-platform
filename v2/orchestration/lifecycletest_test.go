package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/lifecycletest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// lifecycleCluster is a fake cluster with Flux's, the provisioner's and the kubelet's behaviour for the fixture: a new
// unit applies the namespace, the claim (bound to a new volume), the workload and its pod; deleting a MirrorPrune unit
// prunes the workload, and an Orphan one leaves it; a volume whose reclaim policy is Delete goes once its claim has.
// hold names a behaviour that does not happen, so a step's wait or check fails.
type lifecycleCluster struct {
	*kubetest.Fake
	units   map[string]*lifecycleFakeUnit
	hold    map[string]bool
	changes []string
	volumes int
	reclaim map[string]bool
	resumed int
}

type lifecycleFakeUnit struct {
	policy  string
	suspend bool
	token   string
}

const lifecycleMarker = "Fri Nov 13 09:00:00 UTC 2026\n"

func newLifecycleCluster(hold ...string) *lifecycleCluster {
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{}
	fake.Collections = map[observe.Ref][][]byte{lifecyclePodsRef(): {}}
	fake.Log = lifecycleMarker
	c := &lifecycleCluster{Fake: fake, units: map[string]*lifecycleFakeUnit{}, hold: map[string]bool{}, reclaim: map[string]bool{}}
	for _, h := range hold {
		c.hold[h] = true
	}
	return c
}

func lifecycleDocument(document map[string]any) []byte {
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err) // a test's own document
	}
	return encoded
}

func (c *lifecycleCluster) Read(ctx context.Context, ref observe.Ref) (observe.Read, error) {
	if c.hold["unreadable"] {
		return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: observe.ReasonForbidden}, nil
	}
	if ref.Kind != "Kustomization" {
		return c.Fake.Read(ctx, ref)
	}
	unit := c.units[ref.Name]
	if unit == nil {
		return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
	}
	ready := "True"
	if c.hold["ready"] || c.hold["ready-"+ref.Name] {
		ready = "Unknown"
	}
	if unit.token != "" && c.hold["unready-after-resume"] {
		if c.resumed++; c.resumed > 1 {
			ready = "False"
		}
	}
	return observe.Read{Ref: ref, Status: observe.Present, Object: lifecycleDocument(map[string]any{
		"metadata": map[string]any{"name": ref.Name, "namespace": ref.Namespace, "generation": 1},
		"spec":     map[string]any{"suspend": unit.suspend, "sourceRef": map[string]any{"kind": "GitRepository", "name": "swhurl-platform"}},
		"status": map[string]any{"observedGeneration": 1, "lastHandledReconcileAt": unit.token,
			"conditions": []any{map[string]any{"type": "Ready", "status": ready}}},
	})}, nil
}

func (c *lifecycleCluster) Create(ctx context.Context, ref observe.Ref, document []byte) error {
	c.changes = append(c.changes, "create "+ref.Kind+"/"+ref.Name)
	if err := c.Fake.Create(ctx, ref, document); err != nil {
		return err
	}
	if ref.Kind == "Pod" {
		phase := "Succeeded"
		if c.hold["reader"] {
			phase = "Pending"
		}
		c.Objects[ref] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": ref.Name}, "status": map[string]any{"phase": phase}})
		return nil
	}
	var unit struct {
		Spec struct {
			DeletionPolicy string `json:"deletionPolicy"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(document, &unit); err != nil {
		return err
	}
	c.units[ref.Name] = &lifecycleFakeUnit{policy: unit.Spec.DeletionPolicy}
	c.applyFixture()
	return nil
}

// applyFixture is what the fixture's kustomization applies: a claim that does not exist yet binds to a new volume.
func (c *lifecycleCluster) applyFixture() {
	labels := map[string]any{lifecycletest.Label: "true"}
	if c.hold["unlabelled"] {
		labels = map[string]any{}
	}
	c.Objects[lifecycleNamespaceRef()] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": lifecycletest.Namespace, "labels": labels}})
	if _, ok := c.Objects[lifecycleClaimRef()]; !ok {
		c.volumes++
		volume := fmt.Sprintf("pv-%d", c.volumes)
		c.Objects[lifecycleClaimRef()] = lifecycleDocument(map[string]any{
			"metadata": map[string]any{"name": lifecycletest.Claim, "namespace": lifecycletest.Namespace,
				"labels": map[string]any{"kustomize.toolkit.fluxcd.io/name": lifecycletest.Unit}},
			"spec": map[string]any{"volumeName": volume}, "status": map[string]any{"phase": "Bound"}})
		c.Objects[lifecycleVolumeRef(volume)] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": volume},
			"spec": map[string]any{"claimRef": map[string]any{"namespace": lifecycletest.Namespace, "name": lifecycletest.Claim}}})
	}
	replicas := 1
	if c.hold["available"] {
		replicas = 0
	}
	c.Objects[lifecycleDeploymentRef()] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": lifecycletest.Workload},
		"status": map[string]any{"availableReplicas": replicas}})
	c.Collections[lifecyclePodsRef()] = [][]byte{lifecycleDocument(map[string]any{
		"metadata": map[string]any{"name": fmt.Sprintf("writer-%d", c.volumes), "namespace": lifecycletest.Namespace},
		"spec":     map[string]any{"volumes": []any{map[string]any{"persistentVolumeClaim": map[string]any{"claimName": lifecycletest.Claim}}}},
		"status":   map[string]any{"phase": "Running"}})}
}

func (c *lifecycleCluster) Patch(ctx context.Context, ref observe.Ref, patch []byte) error {
	var change struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Suspend *bool `json:"suspend"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(patch, &change); err != nil {
		return err
	}
	if token := change.Metadata.Annotations[fluxRequestedAt]; token != "" {
		c.changes = append(c.changes, "patch "+ref.Kind+"/"+ref.Name+" requestedAt")
	} else {
		c.changes = append(c.changes, "patch "+ref.Kind+"/"+ref.Name+" "+string(patch))
	}
	if ref.Kind == "Kustomization" && c.hold["patch-unit"] {
		return &kube.Error{Code: kube.Failed, Subject: "write", Reason: observe.ReasonForbidden}
	}
	if err := c.Fake.Patch(ctx, ref, patch); err != nil {
		return err
	}
	if unit := c.units[ref.Name]; ref.Kind == "Kustomization" && unit != nil {
		if change.Spec.Suspend != nil {
			unit.suspend = *change.Spec.Suspend
		}
		if token := change.Metadata.Annotations[fluxRequestedAt]; token != "" {
			unit.token = token
		}
	}
	if ref.Kind == "PersistentVolume" {
		c.reclaim[ref.Name] = true
		c.settleVolumes()
	}
	return nil
}

func (c *lifecycleCluster) Delete(ctx context.Context, ref observe.Ref) error {
	c.changes = append(c.changes, "delete "+ref.Kind+"/"+ref.Name)
	if err := c.Fake.Delete(ctx, ref); err != nil {
		return err
	}
	switch ref.Kind {
	case "Kustomization":
		c.deleteUnit(ref.Name)
	case "Namespace":
		for _, gone := range []observe.Ref{ref, lifecycleClaimRef(), lifecycleDeploymentRef(), lifecycleReaderRef()} {
			delete(c.Objects, gone)
		}
		c.Collections[lifecyclePodsRef()] = [][]byte{}
	default:
		delete(c.Objects, ref)
	}
	c.settleVolumes()
	return nil
}

// deleteUnit is Flux's finalizer: a MirrorPrune unit prunes the workload and its pod, an Orphan one leaves them.
func (c *lifecycleCluster) deleteUnit(name string) {
	unit := c.units[name]
	if unit == nil || c.hold["unit-gone"] {
		return
	}
	delete(c.units, name)
	if (unit.policy == "MirrorPrune" && !c.hold["prune"]) || c.hold["orphan-prunes"] {
		delete(c.Objects, lifecycleDeploymentRef())
		if !c.hold["pods"] {
			c.Collections[lifecyclePodsRef()] = [][]byte{}
		}
	}
}

// settleVolumes is the provisioner: a volume whose reclaim policy is Delete goes once no claim is bound to it.
func (c *lifecycleCluster) settleVolumes() {
	bound := ""
	if claim, ok := c.Objects[lifecycleClaimRef()]; ok {
		bound, _ = lifecycletest.VolumeOf(claim)
	}
	for volume := range c.reclaim {
		if volume != bound && !c.hold["stuck-pv"] {
			delete(c.Objects, lifecycleVolumeRef(volume))
		}
	}
}

// remaining is what the run left behind: units, the namespace and volumes.
func (c *lifecycleCluster) remaining() []string {
	left := []string{}
	for name := range c.units {
		left = append(left, "Kustomization/"+name)
	}
	for ref := range c.Objects {
		if ref.Kind == "Namespace" || ref.Kind == "PersistentVolume" {
			left = append(left, ref.Kind+"/"+ref.Name)
		}
	}
	slices.Sort(left)
	return left
}

// lifecycleRunResult is one run: what it said, what it returned, and how long its waits took.
type lifecycleRunResult struct {
	said, lines []lifecycletest.Line
	err         error
	waited      time.Duration
}

func runLifecycle(ctx context.Context, cluster *lifecycleCluster, executor *kubetest.Executor) lifecycleRunResult {
	started := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	result := lifecycleRunResult{}
	result.lines, result.err = LifecycleTest(ctx, LifecycleTestInputs{
		Cluster: Cluster{Reader: cluster, Writer: cluster}, Secrets: cluster, Executor: executor,
		Now:   func() time.Time { return started.Add(result.waited) },
		Sleep: func(d time.Duration) { result.waited += d },
		Say:   func(line lifecycletest.Line) { result.said = append(result.said, line) }})
	return result
}

func markerExecutor() *kubetest.Executor {
	return kubetest.NewExecutor(kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte(lifecycleMarker)}})
}

// The lines of make live-test-lifecycle's passing Python run on 13 November 2026, the volume's name aside.
const lifecyclePassed = `
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
[INFO] Cleaned up lifecycle-test test resources`

// A passing run says Python's lines in Python's order, the passed line before the cleanup's, makes its changes in
// the order of the steps, and leaves nothing behind.
func TestLifecycleTestPassesWithPythonsLines(t *testing.T) {
	cluster := newLifecycleCluster()
	executor := markerExecutor()
	result := runLifecycle(context.Background(), cluster, executor)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if text := lifecycletest.Text(result.lines); text != lifecyclePassed {
		t.Fatalf("lines:\n%s", text)
	}
	if !reflect.DeepEqual(result.said, result.lines) {
		t.Fatal("the lines said differ from the lines returned")
	}
	want := []string{
		"create Kustomization/lifecycle-test",
		`patch Kustomization/lifecycle-test {"spec":{"suspend":true}}`,
		`patch Kustomization/lifecycle-test {"spec":{"suspend":false}}`,
		"patch Kustomization/lifecycle-test requestedAt",
		"delete Kustomization/lifecycle-test",
		"delete Pod/reader", "create Pod/reader", "delete Pod/reader",
		`patch PersistentVolume/pv-1 {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`,
		"delete PersistentVolumeClaim/data",
		"create Kustomization/lifecycle-test-orphan",
		"delete Kustomization/lifecycle-test-orphan",
		// cleanup
		"delete Kustomization/lifecycle-test", "delete Kustomization/lifecycle-test-orphan",
		"delete Namespace/lifecycle-test",
		`patch PersistentVolume/pv-2 {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`,
	}
	if !reflect.DeepEqual(cluster.changes, want) {
		t.Fatalf("changes:\n%s", strings.Join(cluster.changes, "\n"))
	}
	if left := cluster.remaining(); len(left) != 0 {
		t.Fatalf("left behind %q", left)
	}
	request := executor.Requests[0]
	if len(executor.Requests) != 1 || request.Namespace != "lifecycle-test" || request.Pod != "writer-1" ||
		request.Container != "writer" || !reflect.DeepEqual(request.Command, []string{"cat", "/data/marker"}) {
		t.Fatalf("exec %+v", executor.Requests)
	}
	if !slices.Equal(cluster.ReadLogs, []observe.Ref{lifecycleReaderRef()}) {
		t.Fatalf("logs read %v", cluster.ReadLogs)
	}
}

// Leftovers from an earlier run, or a cluster the preflight cannot read, refuse before any change.
func TestLifecycleTestPreflightRefusesWithNoChanges(t *testing.T) {
	for _, c := range []struct {
		name    string
		arrange func(*lifecycleCluster)
		want    string
	}{
		{"namespace", func(c *lifecycleCluster) {
			c.Objects[lifecycleNamespaceRef()] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": "lifecycle-test"}})
		}, "lifecycle-test namespace or lifecycle-test Kustomizations already exist; clean up first"},
		{"unit", func(c *lifecycleCluster) { c.units["lifecycle-test"] = &lifecycleFakeUnit{} },
			"lifecycle-test namespace or lifecycle-test Kustomizations already exist; clean up first"},
		{"orphan unit", func(c *lifecycleCluster) { c.units["lifecycle-test-orphan"] = &lifecycleFakeUnit{} },
			"lifecycle-test namespace or lifecycle-test Kustomizations already exist; clean up first"},
		{"unreadable", func(c *lifecycleCluster) { c.hold["unreadable"] = true },
			"cannot check for leftovers: failed: Namespace lifecycle-test: forbidden"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cluster := newLifecycleCluster()
			c.arrange(cluster)
			result := runLifecycle(context.Background(), cluster, markerExecutor())
			var preflight *lifecycletest.Preflight
			if !errors.As(result.err, &preflight) || result.err.Error() != c.want {
				t.Fatalf("%v", result.err)
			}
			if len(cluster.changes) != 0 || len(cluster.Writes) != 0 || len(result.said) != 0 || result.lines != nil {
				t.Fatalf("changes %q, said %v", cluster.changes, result.said)
			}
		})
	}
}

// A step that cannot go on is one failed line naming v2's reason; the steps after it do not run, there is no passed
// line, and the cleanup still removes the units, the labelled namespace and every volume the claim was bound to.
func TestLifecycleTestCleansUpAfterAFailedStep(t *testing.T) {
	for _, c := range []struct {
		hold, section, failed string
		exit                  int
		waited                time.Duration
		left                  []string
	}{
		{"ready", "Install disposable app", "Kustomization lifecycle-test not Ready after 6m0s", 0, lifecycleReady, nil},
		{"available", "Install disposable app", "deploy/writer in lifecycle-test not available after 3m0s", 0, lifecycleRollout, nil},
		{"exec", "Install disposable app", "cat /data/marker in pod writer-1 exited 1", 1, 0, nil},
		{"patch-unit", "Suspend and resume", "suspend Kustomization lifecycle-test: failed: write: forbidden", 0, 0, nil},
		{"unit-gone", "Uninstall (delete app unit)", "Kustomization lifecycle-test still present after 5m0s", 0, lifecycleDeleted,
			[]string{"Kustomization/lifecycle-test"}},
		{"reader", "Uninstall (delete app unit)", "pod lifecycle-test/reader not Succeeded after 2m0s", 0, lifecycleGone, nil},
		{"stuck-pv", "Destroy data", "destroy-data pvc/lifecycle-test/data: PV pv-1 still present after 2m0s", 0, destroyWait,
			[]string{"PersistentVolume/pv-1"}},
		{"pods", "Destroy data", "destroy-data pvc/lifecycle-test/data: PVC lifecycle-test/data is mounted by a pod; stop or uninstall the workload first",
			0, lifecycleGone, nil},
		{"ready-lifecycle-test-orphan", "Orphan deletion policy (shared units)", "Kustomization lifecycle-test-orphan not Ready after 6m0s",
			0, lifecycleReady, nil},
	} {
		t.Run(c.hold, func(t *testing.T) {
			cluster := newLifecycleCluster(c.hold)
			executor := kubetest.NewExecutor(kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte(lifecycleMarker), ExitCode: c.exit}})
			result := runLifecycle(context.Background(), cluster, executor)
			if result.err != nil {
				t.Fatal(result.err)
			}
			lines := result.lines
			end := lines[len(lines)-2:]
			if !reflect.DeepEqual(end, []lifecycletest.Line{lifecycletest.Failed(c.failed), lifecycletest.CleanedUp()}) {
				t.Fatalf("lines:\n%s", lifecycletest.Text(lines))
			}
			last := slices.IndexFunc(lines, func(line lifecycletest.Line) bool { return line.Text == c.section })
			if last < 0 || slices.ContainsFunc(lines[last+1:], func(line lifecycletest.Line) bool { return line.Level == lifecycletest.Section }) {
				t.Fatalf("the steps after %q ran:\n%s", c.section, lifecycletest.Text(lines))
			}
			if lifecycletest.Passes(lines) || strings.Contains(lifecycletest.Text(lines), lifecycletest.Passed) {
				t.Fatal("a failed run passed")
			}
			if result.waited < c.waited {
				t.Fatalf("waited %s, the step's wait is %s", result.waited, c.waited)
			}
			if left := cluster.remaining(); !reflect.DeepEqual(left, append([]string{}, c.left...)) {
				t.Fatalf("left behind %q", left)
			}
		})
	}
}

// A check whose wait times out is a failed check, and the test goes on to the next; the pods' own wait is not a check.
func TestLifecycleTestCheckWaitsAreChecks(t *testing.T) {
	for _, c := range []struct {
		hold, bad string
		waited    time.Duration
	}{
		{"unready-after-resume", "unit not Ready after resume", lifecycleResumed},
		{"prune", "workload not pruned", lifecycleGone},
		{"orphan-prunes", "Orphan unit deletion removed the workload", 0},
	} {
		t.Run(c.hold, func(t *testing.T) {
			cluster := newLifecycleCluster(c.hold)
			result := runLifecycle(context.Background(), cluster, markerExecutor())
			text := lifecycletest.Text(result.lines)
			if !strings.Contains(text, "\n[BAD] "+c.bad+"\n") || strings.Contains(text, lifecycletest.Passed) {
				t.Fatalf("lines:\n%s", text)
			}
			_, after, _ := strings.Cut(text, "[BAD] "+c.bad)
			if c.hold != "orphan-prunes" && !strings.Contains(after, "[OK]") {
				t.Fatalf("the test stopped at a failed check:\n%s", text)
			}
			if result.waited < c.waited {
				t.Fatalf("waited %s", result.waited)
			}
		})
	}
}

// The cleanup deletes the namespace only when it carries the test's label.
func TestLifecycleTestKeepsAnUnlabelledNamespace(t *testing.T) {
	cluster := newLifecycleCluster("unlabelled")
	result := runLifecycle(context.Background(), cluster, markerExecutor())
	if result.err != nil || !lifecycletest.Passes(result.lines) {
		t.Fatalf("%v\n%s", result.err, lifecycletest.Text(result.lines))
	}
	if slices.Contains(cluster.changes, "delete Namespace/lifecycle-test") {
		t.Fatal("an unlabelled namespace was deleted")
	}
	if _, ok := cluster.Objects[lifecycleNamespaceRef()]; !ok {
		t.Fatal("the namespace is gone")
	}
}

// A cancelled run stops its step and still cleans up: Python's cleanup ran on an interrupt too.
func TestLifecycleTestCleansUpWhenCancelled(t *testing.T) {
	cluster := newLifecycleCluster("ready")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := runLifecycle(ctx, cluster, markerExecutor())
	want := []lifecycletest.Line{
		{Level: lifecycletest.Section, Text: "Install disposable app"},
		lifecycletest.Failed("Kustomization lifecycle-test not Ready: wait stopped: context canceled"),
		lifecycletest.CleanedUp(),
	}
	if result.err != nil || !reflect.DeepEqual(result.lines, want) {
		t.Fatalf("%v\n%s", result.err, lifecycletest.Text(result.lines))
	}
	if left := cluster.remaining(); len(left) != 0 {
		t.Fatalf("left behind %q", left)
	}
}
