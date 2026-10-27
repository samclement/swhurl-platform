package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// readFixture is one document the restore is given, from the kernel's own fixtures.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// allocationOf is the egress allocation that gives the fixture instance its own proxy port.
func allocationOf() []byte {
	return []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[
		{"application":"demo-orders","environment":"staging","port":30102,
		 "grants":["swhurl-platform-backups-110927251694.s3.eu-west-2.amazonaws.com:443"]}]}`)
}

// restoreTarget is the target the compiled fixture documents derive: the app's own objects.
func restoreTarget() observe.Target {
	namespace := "demo-orders-staging"
	return observe.Target{
		Namespace: namespace,
		Unit: observe.Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization",
			Namespace: "flux-system", Name: "app-" + namespace},
		Release:  observe.Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease", Namespace: namespace, Name: "demo-orders"},
		Workload: observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: namespace, Name: "demo-orders"},
		Pods:     observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: namespace},
		Selector: "app.kubernetes.io/name=demo-orders", Container: "main", Replicas: 1,
	}
}

// readyPlan is what the restore command hands the use case after the bucket answered: an archive within its
// threshold whose metadata names this instance with both checksums and a table count.
func readyPlan(t *testing.T) observe.RestorePlan {
	t.Helper()
	plan := observe.RestoreOf(observe.RestoreInput{
		Namespace: "demo-orders-staging", Application: "demo-orders",
		Archive: observe.ArchiveReport{State: observe.BackupCurrent, Bucket: "backups",
			Prefix: "app-sqlite/demo-orders-staging/", Newest: "app-sqlite/demo-orders-staging/sqlite-20261026T082103Z.db.age",
			LastModified: "2026-10-26T08:21:10Z", Age: time.Hour, MaxAge: 26 * time.Hour},
		Metadata: &observe.ArchiveMetadata{SHA256: strings.Repeat("a", 64), PlainSHA256: strings.Repeat("b", 64),
			Tables: 2, Source: "demo-orders-staging/demo-orders"},
	})
	if !plan.Ready() {
		t.Fatalf("the fixture plan is %s/%s", plan.State, plan.Reason)
	}
	return plan
}

// inputs is one restore's inputs over the fixture records and a fake cluster.
func restoreInputs(t *testing.T, cluster *fakeCluster) RestoreInputs {
	t.Helper()
	started := time.Date(2026, 10, 27, 3, 4, 5, 0, time.UTC)
	clock := &clock{now: started}
	return RestoreInputs{
		Instance: readFixture(t, "sqlite.json"), Release: readFixture(t, "r43.json"),
		Profile: readFixture(t, "profile.json"), Egress: allocationOf(),
		Plan: readyPlan(t), Target: restoreTarget(),
		AgeIdentity: "AGE-SECRET-KEY-1" + strings.Repeat("Q", 58),
		ReadURL:     "https://backups.s3.eu-west-2.amazonaws.com/archive?X-Amz-Signature=0123",
		Cluster:     Cluster{Reader: cluster, Writer: cluster},
		Sleep:       clock.sleep, Now: clock.nowFunc,
	}
}

// clock is a test's own time: a sleep moves it forward, so a bounded wait ends without real waiting.
type clock struct{ now time.Time }

func (c *clock) sleep(d time.Duration) { c.now = c.now.Add(d) }
func (c *clock) nowFunc() time.Time    { return c.now }

// fakeCluster answers the five reads a restore makes and records every call in order. It is deliberately
// stateful: a patch changes the replica count that the next read answers, and creating the Job is what makes
// its pod findable, as the API server's controller would.
type fakeCluster struct {
	events   []string
	failOn   string // the event prefix that fails, and what it answers
	failErr  error
	replicas int32
	// neverAvailable reproduces a rollout that never finishes: the Deployment stays unavailable however long
	// the restore waits.
	neverAvailable bool
	// podsStay reproduces a pod that outlives its Job: nothing may then scale the app back up.
	podsStay    bool
	available   int32
	jobStates   []string
	restorePods int
	appPods     int
	log         string
}

func (f *fakeCluster) record(event string) error {
	f.events = append(f.events, event)
	if f.failOn != "" && strings.HasPrefix(event, f.failOn) {
		if f.failErr == nil {
			return errors.New("failed: write: status")
		}
		return f.failErr
	}
	return nil
}

func (f *fakeCluster) Read(_ context.Context, ref observe.Ref) (observe.Read, error) {
	if err := f.record("read:" + ref.Kind); err != nil {
		return observe.Read{}, err
	}
	switch ref.Kind {
	case "Deployment":
		available := f.replicas
		if f.available != 0 {
			available = f.available
		}
		if f.neverAvailable {
			available = 0
		}
		document := fmt.Sprintf(`{"metadata":{"generation":2},"spec":{"replicas":%d},`+
			`"status":{"observedGeneration":2,"availableReplicas":%d}}`, f.replicas, available)
		return observe.Read{Ref: ref, Status: observe.Present, Object: []byte(document)}, nil
	case "Job":
		state := f.nextJobState()
		return observe.Read{Ref: ref, Status: observe.Present, Object: []byte(`{"status":{"conditions":[{"type":"` +
			state + `","status":"True"}]}}`)}, nil
	}
	return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
}

// nextJobState is the next state the Job reports, the last one repeating.
func (f *fakeCluster) nextJobState() string {
	if len(f.jobStates) == 0 {
		return "Complete"
	}
	state := f.jobStates[0]
	if len(f.jobStates) > 1 {
		f.jobStates = f.jobStates[1:]
	}
	return state
}

func (f *fakeCluster) List(_ context.Context, ref observe.Ref, selector string) (observe.List, error) {
	if err := f.record("list:" + selector); err != nil {
		return observe.List{}, err
	}
	switch {
	case selector == kernel.RestoreLabel:
		return observe.List{Ref: ref, Status: observe.Present, Items: pods(f.restorePods)}, nil
	case strings.HasPrefix(selector, kernel.RestoreLabel+"="):
		// The log's pod: it exists once the Job does, and stays when the fake was told the pod outlives it.
		if f.podsStay {
			return observe.List{Ref: ref, Status: observe.Present, Items: pods(1)}, nil
		}
		for _, event := range f.events {
			if strings.HasPrefix(event, "create:Job") && !f.deleted("Job") {
				return observe.List{Ref: ref, Status: observe.Present, Items: pods(1)}, nil
			}
		}
		return observe.List{Ref: ref, Status: observe.Present, Items: pods(0)}, nil
	}
	return observe.List{Ref: ref, Status: observe.Present, Items: pods(f.appPods)}, nil
}

// deleted is whether this run has already deleted one kind.
func (f *fakeCluster) deleted(kind string) bool {
	created, removed := 0, 0
	for _, event := range f.events {
		if strings.HasPrefix(event, "create:"+kind) {
			created++
		}
		if strings.HasPrefix(event, "delete:"+kind) {
			removed++
		}
	}
	return removed >= created
}

func (f *fakeCluster) Patch(_ context.Context, ref observe.Ref, patch []byte) error {
	if err := f.record("patch:" + ref.Kind + ":" + string(patch)); err != nil {
		return err
	}
	var change struct {
		Spec struct {
			Replicas *int32 `json:"replicas"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(patch, &change); err == nil && change.Spec.Replicas != nil {
		f.replicas = *change.Spec.Replicas
	}
	return nil
}

func (f *fakeCluster) Create(_ context.Context, ref observe.Ref, _ []byte) error {
	return f.record("create:" + ref.Kind + ":" + ref.Name)
}

func (f *fakeCluster) Delete(_ context.Context, ref observe.Ref) error {
	return f.record("delete:" + ref.Kind + ":" + ref.Name)
}

func (f *fakeCluster) Logs(_ context.Context, ref observe.Ref, _ int64) (string, error) {
	if err := f.record("logs:" + ref.Name); err != nil {
		return "", err
	}
	return f.log, nil
}

// pods is n listed pods, cheaply: only their names are read.
func pods(n int) [][]byte {
	items := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, []byte(`{"metadata":{"name":"pod-`+strconv.Itoa(i)+`"}}`))
	}
	return items
}

// Restore order, once: suspend, stop, wait, secret, job, wait, log, delete, delete, scale back, resume, wait.
func TestRestorePerformsTheSequence(t *testing.T) {
	cluster := &fakeCluster{replicas: 1, log: "[OK] Restored the archive into /data/app.db (2 tables, integrity ok)\n"}
	inputs := restoreInputs(t, cluster)
	outcome, err := Restore(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != RestoreRestored || outcome.App.State != AppRunning || !outcome.App.Available ||
		outcome.App.Replicas != 1 || outcome.Reason != "" {
		t.Fatalf("outcome %+v", outcome)
	}
	if outcome.Job.State != JobComplete || !strings.Contains(outcome.Job.Log, "2 tables") ||
		outcome.Job.LogReason != "" {
		t.Fatalf("the job's evidence: %+v", outcome.Job)
	}
	var names []string
	for _, step := range outcome.Steps {
		names = append(names, step.Name)
	}
	if got, want := strings.Join(names, " "), "suspend scale-down pods-gone secret job job-finished log "+
		"job-result delete-job delete-secret scale-up resume available"; got != want {
		t.Fatalf("steps\n%s\nwant\n%s", got, want)
	}
	for i, step := range outcome.Steps {
		if step.Outcome != StepDone {
			t.Fatalf("step %d (%s) is %s: %s", i, step.Name, step.Outcome, step.Detail)
		}
	}
	// The order is the safety contract: suspend before stopping, and resume only after scaling back.
	for _, pair := range [][2]string{
		{"patch:HelmRelease:{\"spec\":{\"suspend\":true}}", "patch:Deployment:{\"spec\":{\"replicas\":0}}"},
		{"create:Secret:", "create:Job:"},
		{"delete:Job:", "delete:Secret:"},
		{"patch:Deployment:{\"spec\":{\"replicas\":1}}", "patch:HelmRelease:{\"spec\":{\"suspend\":false}}"},
	} {
		first, second := indexOf(cluster.events, pair[0]), indexOf(cluster.events, pair[1])
		if first < 0 || second < 0 || first > second {
			t.Fatalf("%s must come before %s:\n%s", pair[0], pair[1], strings.Join(cluster.events, "\n"))
		}
	}
	if !strings.Contains(outcome.Job.Name, "demo-orders-restore-") {
		t.Fatalf("the Job's name is %q", outcome.Job.Name)
	}
}

// indexOf is the first event with this prefix, or -1.
func indexOf(events []string, prefix string) int {
	for i, event := range events {
		if strings.HasPrefix(event, prefix) {
			return i
		}
	}
	return -1
}

// Every failure from the first change onward puts the app back, and a failure before the first change is a
// refusal with nothing written.
func TestRestoreUndoesAndRefuses(t *testing.T) {
	for _, tt := range []struct {
		name     string
		prepare  func(*fakeCluster)
		want     []string // the steps recorded after the failing one
		appState string
		reason   string
		// replicas is the live count the run leaves the Deployment at: 1 when it was put back, 0 when a pod
		// that may still hold the claim stopped it.
		replicas  int32
		suspended bool
	}{
		{"app pods do not stop", func(f *fakeCluster) { f.appPods = 1 }, []string{"scale-up", "resume"}, AppUnknown, "pods-gone", 1, false},
		{"secret cannot be created", func(f *fakeCluster) { f.failOn = "create:Secret" },
			[]string{"delete-secret", "scale-up", "resume"}, AppUnknown, "secret", 1, false},
		{"job cannot be created", func(f *fakeCluster) { f.failOn = "create:Job" },
			[]string{"delete-job", "delete-secret", "scale-up", "resume"}, AppUnknown, "job", 1, false},
		{"job reports failure", func(f *fakeCluster) { f.jobStates = []string{"Running", "Failed"} },
			[]string{"delete-job", "delete-secret", "scale-up", "resume"}, AppUnknown, "job-result", 1, false},
		{"job times out", func(f *fakeCluster) { f.jobStates = []string{"Running"} },
			[]string{"log", "delete-job", "delete-secret", "scale-up", "resume"}, AppUnknown, "job-finished", 1, false},
		{"job cannot be deleted", func(f *fakeCluster) { f.failOn = "delete:Job" },
			[]string{"delete-job", "delete-secret"}, AppStopped, "delete-job", 0, true},
		{"the restore pod outlives its Job", func(f *fakeCluster) { f.podsStay = true },
			[]string{"delete-job", "delete-secret"}, AppStopped, "delete-job", 0, true},
		{"the app does not scale back", func(f *fakeCluster) { f.failOn = "patch:Deployment:{\"spec\":{\"replicas\":1}}" },
			[]string{"scale-up", "resume"}, AppStopped, "scale-up", 0, false},
		{"the release does not resume", func(f *fakeCluster) { f.failOn = "patch:HelmRelease:{\"spec\":{\"suspend\":false}}" },
			[]string{"resume"}, AppStopped, "resume", 1, true},
		{"the app does not come back", func(f *fakeCluster) { f.neverAvailable = true },
			nil, AppUnknown, "available", 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &fakeCluster{replicas: 1, log: "[OK] Restored\n"}
			tt.prepare(cluster)
			outcome, err := Restore(context.Background(), restoreInputs(t, cluster))
			if err == nil {
				t.Fatal("a failed restore returned no error")
			}
			var refusal *Refusal
			if errors.As(err, &refusal) {
				t.Fatalf("a failure part-way was reported as a refusal: %v", err)
			}
			if outcome.State != RestoreFailed || outcome.App.State != tt.appState || outcome.Reason != tt.reason {
				t.Fatalf("outcome %+v, want state %s reason %s", outcome, tt.appState, tt.reason)
			}
			// Everything after the first failed step is the undo, recorded whether it succeeded or not.
			var names []string
			failed := false
			for _, step := range outcome.Steps {
				if failed {
					names = append(names, step.Name)
				}
				if step.Outcome == StepFailed && !failed {
					failed = true
				}
			}
			if !failed || !sameSteps(names, tt.want) {
				t.Fatalf("after the failure the run did %v, want %v\n%+v", names, tt.want, outcome.Steps)
			}
			// The live count, not the outcome's copy of the target: a run that could not put the app back must
			// leave it where it is and say so.
			if cluster.replicas != tt.replicas || outcome.App.Suspended != tt.suspended {
				t.Fatalf("the app is at %d replicas, suspended %v; want %d/%v",
					cluster.replicas, outcome.App.Suspended, tt.replicas, tt.suspended)
			}
		})
	}
}

// sameSteps is whether two step lists are the same, in order.
func sameSteps(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Nothing changed is a refusal: a plan that is not ready, a restore already running, or an app that is not
// there. Each is reported by type, and no call is made that could change anything.
func TestRestoreRefusesBeforeChangingAnything(t *testing.T) {
	stale := readyPlan(t)
	stale.State, stale.Reason = observe.RestoreStale, observe.ReasonNotRestorableStale
	noDeployment := func(f *fakeCluster) { f.failOn = "read:Deployment" }
	for _, tt := range []struct {
		name    string
		plan    observe.RestorePlan
		prepare func(*fakeCluster)
	}{
		{"plan not ready", stale, nil},
		{"restore in progress", readyPlan(t), func(f *fakeCluster) { f.restorePods = 1 }},
		{"deployment unreadable", readyPlan(t), noDeployment},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &fakeCluster{replicas: 1}
			if tt.prepare != nil {
				tt.prepare(cluster)
			}
			inputs := restoreInputs(t, cluster)
			inputs.Plan = tt.plan
			_, err := Restore(context.Background(), inputs)
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("error %v is not a refusal", err)
			}
			for _, event := range cluster.events {
				if strings.HasPrefix(event, "patch:") || strings.HasPrefix(event, "create:") ||
					strings.HasPrefix(event, "delete:") {
					t.Fatalf("a refused restore changed something: %s", event)
				}
			}
		})
	}
}

// The pod's log is evidence, not the decision: an unreadable log does not fail a restore that completed.
func TestRestoreReadsTheLogAsEvidence(t *testing.T) {
	cluster := &fakeCluster{replicas: 1, log: "[OK] Restored the archive\n"}
	outcome, err := Restore(context.Background(), restoreInputs(t, cluster))
	if err != nil || outcome.Job.Log == "" {
		t.Fatalf("log %q, %v", outcome.Job.Log, err)
	}

	cluster = &fakeCluster{replicas: 1, failOn: "logs:"}
	unreadable, err := Restore(context.Background(), restoreInputs(t, cluster))
	if err != nil || unreadable.State != RestoreRestored {
		t.Fatalf("an unreadable log failed a restore: %+v %v", unreadable, err)
	}
	logged := stepNamed(unreadable.Steps, "log")
	if logged.Outcome != StepNotRead || logged.Detail == "" || unreadable.Job.LogReason == "" {
		t.Fatalf("the log step is %+v, reason %q", logged, unreadable.Job.LogReason)
	}
}

// stepNamed is one recorded step by name.
func stepNamed(steps []RestoreStep, name string) RestoreStep {
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}
	return RestoreStep{}
}
