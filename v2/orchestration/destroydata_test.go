package orchestration

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/datadestroy"
	"github.com/samclement/swhurl-platform/v2/observe"
)

var (
	claimRef, _    = kube.RefOf("", "PersistentVolumeClaim", "apps", "data")
	volumeRef, _   = kube.RefOf("", "PersistentVolume", "", "pv-1")
	podsRef, _     = kube.RefOf("", "Pod", "apps", "")
	fluxUnitRef, _ = kube.RefOf("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "app-x")
)

// destroyCluster is a fake cluster with the provisioner's behaviour: a deleted claim goes, and a volume whose reclaim
// policy is Delete goes once its claim has, unless the cluster is stuck. Every change is recorded, with its patch,
// and failWrite makes the change of that verb ("patch" or "delete") fail.
type destroyCluster struct {
	*kubetest.Fake
	changes       []string
	stuck         bool
	reclaimDelete bool
	failWrite     string
	unavailable   map[observe.Ref]bool
}

func newDestroyCluster(claim string, pods ...string) *destroyCluster {
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{volumeRef: []byte(`{"metadata":{"name":"pv-1"},"status":{"phase":"Bound"}}`)}
	if claim != "" {
		fake.Objects[claimRef] = []byte(claim)
	}
	items := [][]byte{}
	for _, pod := range pods {
		items = append(items, []byte(pod))
	}
	fake.Collections = map[observe.Ref][][]byte{podsRef: items}
	return &destroyCluster{Fake: fake, unavailable: map[observe.Ref]bool{}}
}

func (c *destroyCluster) Read(ctx context.Context, ref observe.Ref) (observe.Read, error) {
	if c.unavailable[ref] {
		return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: observe.ReasonForbidden}, nil
	}
	if _, claimed := c.Objects[claimRef]; ref == volumeRef && c.reclaimDelete && !claimed && !c.stuck {
		return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
	}
	return c.Fake.Read(ctx, ref)
}

func (c *destroyCluster) Patch(ctx context.Context, ref observe.Ref, patch []byte) error {
	c.changes = append(c.changes, "patch "+ref.Kind+"/"+ref.Name+" "+string(patch))
	if c.failWrite == "patch" {
		return &kube.Error{Code: kube.Failed, Subject: "write", Reason: observe.ReasonForbidden}
	}
	c.reclaimDelete = ref == volumeRef && string(patch) == datadestroy.ReclaimDelete
	return c.Fake.Patch(ctx, ref, patch)
}

func (c *destroyCluster) Delete(ctx context.Context, ref observe.Ref) error {
	c.changes = append(c.changes, "delete "+ref.Kind+"/"+ref.Namespace+"/"+ref.Name)
	if c.failWrite == "delete" {
		return &kube.Error{Code: kube.Failed, Subject: "write", Reason: observe.ReasonForbidden}
	}
	if err := c.Fake.Delete(ctx, ref); err != nil {
		return err
	}
	if !c.stuck {
		delete(c.Objects, ref)
	}
	return nil
}

const boundClaim = `{"metadata":{"name":"data","namespace":"apps"},"spec":{"volumeName":"pv-1"}}`

func destroyPod(claim, phase string) string {
	return `{"metadata":{"name":"` + strings.ToLower("p-"+phase+"-"+claim) + `","namespace":"apps"},"status":{"phase":"` + phase +
		`"},"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"` + claim + `"}}]}}`
}

func destroy(t *testing.T, cluster *destroyCluster, text string, dryRun bool) (DestroyOutcome, error, time.Duration) {
	t.Helper()
	target, err := datadestroy.ParseTarget(text)
	if err != nil {
		t.Fatal(err)
	}
	var waited time.Duration
	started := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	outcome, err := DestroyData(context.Background(), DestroyInputs{Reader: cluster, Writer: cluster, Secrets: cluster,
		Target: target, DryRun: dryRun, Now: func() time.Time { return started.Add(waited) },
		Sleep: func(d time.Duration) { waited += d }})
	return outcome, err, waited
}

func TestDestroyDataInOrder(t *testing.T) {
	cluster := newDestroyCluster(boundClaim, destroyPod("other", "Running"))
	outcome, err, _ := destroy(t, cluster, "pvc/apps/data", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{
		`patch PersistentVolume/pv-1 {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`,
		"delete PersistentVolumeClaim/apps/data",
	}; !reflect.DeepEqual(cluster.changes, want) {
		t.Fatalf("changes %q", cluster.changes)
	}
	if outcome.Volume != "pv-1" || len(outcome.Made) != 3 {
		t.Fatalf("outcome %+v", outcome)
	}
}

func TestDestroyDataReleasedVolume(t *testing.T) {
	cluster := newDestroyCluster("")
	cluster.Objects[volumeRef] = []byte(`{"metadata":{"name":"pv-1"},"status":{"phase":"Released"}}`)
	outcome, err, _ := destroy(t, cluster, "pv/pv-1", false)
	if err != nil || outcome.Volume != "pv-1" {
		t.Fatalf("%+v, %v", outcome, err)
	}
	if want := []string{`patch PersistentVolume/pv-1 {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`}; !reflect.DeepEqual(cluster.changes, want) {
		t.Fatalf("changes %q", cluster.changes)
	}
}

// Every refusal is a *datadestroy.Refusal with Python's words, and changes nothing.
func TestDestroyDataRefusals(t *testing.T) {
	released := func(c *destroyCluster) {
		c.Objects[volumeRef] = []byte(`{"metadata":{"name":"pv-1"},"status":{"phase":"Bound"}}`)
	}
	helm := `{"metadata":{"name":"data","namespace":"apps","annotations":{"meta.helm.sh/release-name":"app"}},"spec":{"volumeName":"pv-1"}}`
	flux := `{"metadata":{"name":"data","namespace":"apps","labels":{"kustomize.toolkit.fluxcd.io/name":"app-x"}},"spec":{"volumeName":"pv-1"}}`
	for _, c := range []struct {
		name, target, message string
		cluster               *destroyCluster
		arrange               func(*destroyCluster)
	}{
		{"claim missing", "pvc/apps/data", "PVC apps/data not found", newDestroyCluster(""), nil},
		{"mounted by a running pod", "pvc/apps/data", "PVC apps/data is mounted by a pod; stop or uninstall the workload first",
			newDestroyCluster(boundClaim, destroyPod("data", "Running")), nil},
		{"mounted by a pending pod", "pvc/apps/data", "PVC apps/data is mounted by a pod; stop or uninstall the workload first",
			newDestroyCluster(boundClaim, destroyPod("data", "Pending")), nil},
		{"helm release installed", "pvc/apps/data", "Helm release apps/app still exists and would recreate the claim; uninstall it first",
			newDestroyCluster(helm), func(c *destroyCluster) {
				c.SecretData = map[string][]kube.Secret{"apps": {{Name: "sh.helm.release.v1.app.v1",
					Labels: map[string]string{"owner": "helm", "name": "app"}}}}
			}},
		{"flux unit manages it", "pvc/apps/data", "Flux Kustomization app-x still manages the claim; remove it from Git first",
			newDestroyCluster(flux), func(c *destroyCluster) {
				c.Objects[fluxUnitRef] = []byte(`{"metadata":{"name":"app-x","namespace":"flux-system"}}`)
			}},
		{"volume missing", "pv/pv-9", "PV pv-9 not found", newDestroyCluster(""), nil},
		{"volume bound", "pv/pv-1", "PV pv-1 is not Released; destroy its claim with pvc/<namespace>/<name> instead", newDestroyCluster(""), released},
	} {
		if c.arrange != nil {
			c.arrange(c.cluster)
		}
		for _, dryRun := range []bool{false, true} {
			_, err, _ := destroy(t, c.cluster, c.target, dryRun)
			var refusal *datadestroy.Refusal
			if !errors.As(err, &refusal) || err.Error() != c.message {
				t.Fatalf("%s: %v", c.name, err)
			}
			if len(c.cluster.Writes) != 0 {
				t.Fatalf("%s: changed %v", c.name, c.cluster.Writes)
			}
		}
	}
}

// Finished pods, another claim's pods, a release Helm no longer records and a unit that is gone do not block.
func TestDestroyDataRetiredOwnersDoNotBlock(t *testing.T) {
	owned := `{"metadata":{"name":"data","namespace":"apps","annotations":{"meta.helm.sh/release-name":"app"},
		"labels":{"kustomize.toolkit.fluxcd.io/name":"app-x"}},"spec":{"volumeName":"pv-1"}}`
	cluster := newDestroyCluster(owned, destroyPod("data", "Succeeded"), destroyPod("data", "Failed"), destroyPod("other", "Running"))
	cluster.SecretData = map[string][]kube.Secret{"apps": {{Name: "sh.helm.release.v1.other.v1",
		Labels: map[string]string{"owner": "helm", "name": "other"}}}}
	if _, err, _ := destroy(t, cluster, "pvc/apps/data", false); err != nil {
		t.Fatal(err)
	}
	if want := "secretnames owner=helm,name=app"; !slices.Contains(cluster.Calls, want) {
		t.Fatalf("calls %v", cluster.Calls)
	}
}

// A read that fails is not an absence: the claim, the pods, Helm's records and the unit each refuse to guess, and
// nothing is changed. This is where Python treated a unit it could not read as gone.
func TestDestroyDataFailedReadsChangeNothing(t *testing.T) {
	flux := `{"metadata":{"name":"data","namespace":"apps","annotations":{"meta.helm.sh/release-name":"app"},
		"labels":{"kustomize.toolkit.fluxcd.io/name":"app-x"}},"spec":{"volumeName":"pv-1"}}`
	for name, arrange := range map[string]func(*destroyCluster){
		"claim":  func(c *destroyCluster) { c.unavailable[claimRef] = true },
		"unit":   func(c *destroyCluster) { c.unavailable[fluxUnitRef] = true },
		"volume": func(c *destroyCluster) { c.unavailable[volumeRef] = true },
		"secrets": func(c *destroyCluster) {
			c.Failing = map[string]error{"secretnames owner=helm,name=app": errors.New("failed: secrets: forbidden")}
		},
	} {
		cluster := newDestroyCluster(flux)
		arrange(cluster)
		target := "pvc/apps/data"
		if name == "volume" {
			target = "pv/pv-1"
		}
		_, err, _ := destroy(t, cluster, target, false)
		var refusal *datadestroy.Refusal
		if err == nil || errors.As(err, &refusal) || len(cluster.Writes) != 0 {
			t.Fatalf("%s: %v, changed %v", name, err, cluster.Writes)
		}
	}
	cluster := newDestroyCluster(boundClaim)
	cluster.Collections = nil
	cluster.Answers.Pods = podsRef
	cluster.Answers.ListReason = observe.ReasonForbidden
	if _, err, _ := destroy(t, cluster, "pvc/apps/data", false); err == nil || err.Error() != "failed: pods: forbidden" ||
		len(cluster.Writes) != 0 {
		t.Fatalf("pods: %v, changed %v", err, cluster.Writes)
	}
}

func TestDestroyDataDryRunChecksAndChangesNothing(t *testing.T) {
	cluster := newDestroyCluster(boundClaim)
	outcome, err, _ := destroy(t, cluster, "pvc/apps/data", true)
	if err != nil || outcome.Volume != "pv-1" || len(outcome.Made) != 0 || len(cluster.Writes) != 0 {
		t.Fatalf("%+v, %v, changed %v", outcome, err, cluster.Writes)
	}
	if !slices.Contains(cluster.Selectors, "") {
		t.Fatal("the dry run did not read the pods")
	}
}

// A refused change stops the run there and is an error, not a refusal; a claim that does not go is a timeout after
// the bound.
func TestDestroyDataFailedChanges(t *testing.T) {
	for verb, want := range map[string][]string{
		"patch":  {`patch PersistentVolume/pv-1 {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`},
		"delete": {`patch PersistentVolume/pv-1 {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`, "delete PersistentVolumeClaim/apps/data"},
	} {
		cluster := newDestroyCluster(boundClaim)
		cluster.failWrite = verb
		_, err, _ := destroy(t, cluster, "pvc/apps/data", false)
		var refusal *datadestroy.Refusal
		if err == nil || errors.As(err, &refusal) || !strings.HasSuffix(err.Error(), ": failed: write: forbidden") {
			t.Fatalf("%s: %v", verb, err)
		}
		if !reflect.DeepEqual(cluster.changes, want) {
			t.Fatalf("%s: changes %q", verb, cluster.changes)
		}
	}
	stuck := newDestroyCluster(boundClaim)
	stuck.stuck = true
	_, err, waited := destroy(t, stuck, "pvc/apps/data", false)
	if err == nil || err.Error() != "PVC apps/data still present after 2m0s" || waited < destroyWait {
		t.Fatalf("%v after %s", err, waited)
	}
	gone := newDestroyCluster(boundClaim)
	gone.unavailable[volumeRef] = true
	gone.Objects[volumeRef] = []byte(`{"metadata":{"name":"pv-1"}}`)
	// The checks read only the claim; the volume's reads fail from the first wait on.
	_, err, _ = destroy(t, gone, "pvc/apps/data", false)
	if err == nil || err.Error() != "PV pv-1 still present after 2m0s (last read: forbidden)" {
		t.Fatal(err)
	}
}
