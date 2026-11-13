package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// goingCluster is a scripted cluster in which a deleted claim takes its volume with it, as the provisioner does once
// the reclaim policy is Delete.
type goingCluster struct{ *kubetest.Fake }

func (g goingCluster) Delete(ctx context.Context, ref observe.Ref) error {
	if err := g.Fake.Delete(ctx, ref); err != nil {
		return err
	}
	clear(g.Objects)
	return nil
}

func destroyCommandCluster() goingCluster {
	claim, _ := kube.RefOf("", "PersistentVolumeClaim", "apps", "data")
	volume, _ := kube.RefOf("", "PersistentVolume", "", "pv-1")
	pods, _ := kube.RefOf("", "Pod", "apps", "")
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{
		claim:  []byte(`{"metadata":{"name":"data","namespace":"apps"},"spec":{"volumeName":"pv-1"}}`),
		volume: []byte(`{"metadata":{"name":"pv-1"},"status":{"phase":"Bound"}}`),
	}
	fake.Collections = map[observe.Ref][][]byte{pods: {}}
	return goingCluster{fake}
}

func runDestroyData(t *testing.T, cluster goingCluster, args ...string) (int, string, string, []string, bool) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	var read []string
	connected := false
	status := runWith(append([]string{"destroy-data"}, args...), &out, &diagnostic, cli{
		readFile: func(path string) ([]byte, error) { read = append(read, path); return nil, errors.New("no file") },
		sleep:    func(_ time.Duration) {},
		dataClusterOf: func() (dataCluster, error) {
			connected = true
			return dataCluster{reader: cluster, writer: cluster, secrets: cluster}, nil
		}})
	return status, out.String(), diagnostic.String(), read, connected
}

// A target of the wrong shape or a confirmation that differs is refused before the kubeconfig is read, so it reaches
// no cluster; the words are make destroy-data's.
func TestDestroyDataRefusesBeforeTheCluster(t *testing.T) {
	for _, c := range []struct{ target, confirm, diagnostic string }{
		{"pvc/ns/data", "", "[ERROR] destroy-data permanently deletes pvc/ns/data and its data. Re-run with CONFIRM=pvc/ns/data\n"},
		{"pvc/ns/data", "pvc/ns/other", "[ERROR] destroy-data permanently deletes pvc/ns/data and its data. Re-run with CONFIRM=pvc/ns/data\n"},
		{"pv/name", "pv/other", "[ERROR] destroy-data permanently deletes pv/name and its data. Re-run with CONFIRM=pv/name\n"},
		{"pvc/ns", "pvc/ns", "[ERROR] Use pvc/<namespace>/<name>\n"},
		{"secret/ns/x", "secret/ns/x", "destroy-data pvc/<ns>/<name> | pv/<name>     (CONFIRM=<same target>)\n"},
	} {
		cluster := destroyCommandCluster()
		status, out, diagnostic, read, connected := runDestroyData(t, cluster, "--target", c.target, "--confirm", c.confirm,
			"--kubeconfig", "kubeconfig.json")
		if status != 2 || out != "" || diagnostic != c.diagnostic || len(read) != 0 || connected {
			t.Fatalf("%s %q: %d %q %q, read %v, connected %v", c.target, c.confirm, status, out, diagnostic, read, connected)
		}
	}
	if status, _, _, _, connected := runDestroyData(t, destroyCommandCluster(), "--confirm", "pv/x", "--kubeconfig", "k.json"); status != 2 || connected {
		t.Fatalf("no target: %d, connected %v", status, connected)
	}
}

func TestDestroyDataPlansThenDestroys(t *testing.T) {
	cluster := destroyCommandCluster()
	args := []string{"--target", "pvc/apps/data", "--confirm", "pvc/apps/data", "--kubeconfig", "k.json"}
	status, out, diagnostic, _, _ := runDestroyData(t, cluster, append(args, "--dry-run")...)
	if want := "Plan (destroy-data pvc/apps/data): all checks passed\n" +
		"  would set PV pv-1's reclaim policy to Delete\n" +
		"  would delete PVC apps/data and wait for it to go\n" +
		"  would wait for PV pv-1 and its data to go\n"; status != 0 || out != want || diagnostic != "" || len(cluster.Writes) != 0 {
		t.Fatalf("dry run: %d %q %q, changed %v", status, out, diagnostic, cluster.Writes)
	}
	status, out, diagnostic, _, _ = runDestroyData(t, cluster, args...)
	if status != 0 || out != "[OK] Destroyed pvc/apps/data (PV pv-1 and its data)\n" || diagnostic != "" || len(cluster.Writes) != 2 {
		t.Fatalf("%d %q %q, changed %v", status, out, diagnostic, cluster.Writes)
	}
}

// A refusal from the cluster is exit 2; a read or a change that failed is exit 1.
func TestDestroyDataExitStatuses(t *testing.T) {
	mounted := destroyCommandCluster()
	pods, _ := kube.RefOf("", "Pod", "apps", "")
	mounted.Collections[pods] = [][]byte{[]byte(`{"metadata":{"name":"app","namespace":"apps"},"status":{"phase":"Running"},` +
		`"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"data"}}]}}`)}
	args := []string{"--target", "pvc/apps/data", "--confirm", "pvc/apps/data", "--kubeconfig", "k.json"}
	status, _, diagnostic, _, _ := runDestroyData(t, mounted, args...)
	if status != 2 || diagnostic != "[ERROR] PVC apps/data is mounted by a pod; stop or uninstall the workload first\n" || len(mounted.Writes) != 0 {
		t.Fatalf("mounted: %d %q", status, diagnostic)
	}
	refused := destroyCommandCluster()
	refused.Refuse = &kube.Error{Code: kube.Failed, Subject: "write", Reason: observe.ReasonForbidden}
	status, _, diagnostic, _, _ = runDestroyData(t, refused, args...)
	if status != 1 || diagnostic == "" {
		t.Fatalf("refused: %d %q", status, diagnostic)
	}
}
