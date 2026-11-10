package kube_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// jobPod is the ref a job's pod is read at.
var jobPod = observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: "console", Name: "console-5f7c9d"}

// recordsFake is a console holding the job record ConfigMap and one pod, in the console's own namespace.
func recordsFake(data map[string]string, pods map[observe.Ref][]byte) *kubetest.Fake {
	return &kubetest.Fake{
		ConfigMapData: map[string][]kube.ConfigMap{"console": {{Name: "console-jobs", Data: data}}},
		Objects:       pods, Answers: kubetest.Answers{Object: jobPod},
	}
}

// TestJobRecordsLoads keeps the store's read as the console always had it: the ConfigMap's values, and an absent
// ConfigMap is no records rather than a failure.
func TestJobRecordsLoads(t *testing.T) {
	records := kube.JobRecords{Data: recordsFake(map[string]string{"job-10": `{"id":10}`, "job-2": `{"id":2}`}, nil)}
	found, err := records.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`{"id":10}`, `{"id":2}`}; !reflect.DeepEqual(found, want) {
		t.Fatalf("Load = %v, want %v", found, want)
	}
	absent := kube.JobRecords{Data: &kubetest.Fake{}}
	if found, err := absent.Load(context.Background()); err != nil || found != nil {
		t.Fatalf("Load of an absent ConfigMap = %v, %v; want nil, nil", found, err)
	}
}

// TestJobRecordsSave writes and deletes in one patch, so a pod finishing its jobs and the one replacing it never undo
// each other's keys.
func TestJobRecordsSave(t *testing.T) {
	fake := recordsFake(map[string]string{"job-1": `{"id":1}`, "job-2": `{"id":2}`}, nil)
	records := kube.JobRecords{Data: fake}
	three := `{"id":3}`
	if err := records.Save(context.Background(), map[int]*string{3: &three, 1: nil}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"job-2": `{"id":2}`, "job-3": three}; !maps.Equal(fake.ConfigMapData["console"][0].Data, want) {
		t.Fatalf("the ConfigMap holds %v, want %v", fake.ConfigMapData["console"][0].Data, want)
	}
	if want := []string{"set console-jobs"}; !reflect.DeepEqual(fake.Calls, want) {
		t.Fatalf("calls = %v, want %v", fake.Calls, want)
	}
}

// TestJobRecordsAlive reads the pod that ran a job: present is alive, gone is not, and an unreadable answer is an
// error the registry counts as alive.
func TestJobRecordsAlive(t *testing.T) {
	ctx := context.Background()
	present := kube.JobRecords{Data: &kubetest.Fake{}, Pods: recordsFake(nil, map[observe.Ref][]byte{jobPod: []byte("{}")})}
	if alive, err := present.Alive(ctx, jobPod.Name); err != nil || !alive {
		t.Fatalf("Alive of a present pod = %v, %v; want true", alive, err)
	}
	gone := kube.JobRecords{Data: &kubetest.Fake{}, Pods: recordsFake(nil, map[observe.Ref][]byte{})}
	if alive, err := gone.Alive(ctx, "console-gone"); err != nil || alive {
		t.Fatalf("Alive of an absent pod = %v, %v; want false", alive, err)
	}
	unreadable := kube.JobRecords{Data: &kubetest.Fake{}, Pods: &kubetest.Fake{Refuse: errors.New("unavailable")}}
	if alive, err := unreadable.Alive(ctx, jobPod.Name); err == nil || alive {
		t.Fatalf("Alive of an unreadable pod = %v, %v; want an error, as the registry counts it alive", alive, err)
	}
}
