package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// statusCluster is a selected-list API scenario. All discovery comes from lists; only explicit
// single status and newest backup owners use GETs. Candidates may include other namespaces/apps.
type statusCluster struct {
	objects                  map[observe.Ref]observe.Read
	lists                    []observe.ListRequest
	reads                    []observe.Ref
	failedKind, failedReason string
	refuse                   error
}

func (f *statusCluster) Read(_ context.Context, ref observe.Ref) (observe.Read, error) {
	f.reads = append(f.reads, ref)
	if f.refuse != nil {
		return observe.Read{}, f.refuse
	}
	if found, ok := f.objects[ref]; ok {
		return found, nil
	}
	return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
}
func (f *statusCluster) List(ctx context.Context, ref observe.Ref, selector string) (observe.List, error) {
	return f.list(ref, selector)
}
func (f *statusCluster) ListAllNamespaces(ctx context.Context, ref observe.Ref, selector string) (observe.List, error) {
	return f.list(ref, selector)
}
func (f *statusCluster) list(ref observe.Ref, selector string) (observe.List, error) {
	f.lists = append(f.lists, observe.ListRequest{Ref: ref, Selector: selector})
	if f.refuse != nil {
		return observe.List{}, f.refuse
	}
	if ref.Kind == f.failedKind {
		return observe.List{Ref: ref, Selector: selector, Status: observe.Unavailable, Reason: f.failedReason}, nil
	}
	items := [][]byte{}
	for candidate, read := range f.objects {
		if read.Status != observe.Present || candidate.Kind != ref.Kind || candidate.APIVersion != ref.APIVersion {
			continue
		}
		if ref.Namespace != "" && candidate.Namespace != ref.Namespace {
			continue
		}
		var document struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(read.Object, &document); err != nil {
			panic(err)
		}
		if statusMatches(document.Metadata.Labels, selector) {
			items = append(items, read.Object)
		}
	}
	return kube.DecodeCollection(ref, selector, statusJSON(items)), nil
}
func statusMatches(labels map[string]string, selector string) bool {
	for _, term := range strings.Split(selector, ",") {
		if key, negative := strings.CutPrefix(term, "!"); negative {
			if _, ok := labels[key]; ok {
				return false
			}
			continue
		}
		key, value, equal := strings.Cut(term, "=")
		actual, ok := labels[key]
		if !ok || (equal && actual != value) {
			return false
		}
	}
	return true
}
func statusJSON(value any) []byte {
	if items, ok := value.([][]byte); ok {
		raw := []json.RawMessage{}
		for _, item := range items {
			raw = append(raw, json.RawMessage(item))
		}
		value = map[string]any{"items": raw}
	}
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
func statusVariant(t *testing.T, application, environment string, database, exposed bool) Inputs {
	t.Helper()
	inputs := input(t, nil, nil)
	release, err := kernel.DecodeRelease(inputs.Release)
	if err != nil {
		t.Fatal(err)
	}
	release.Application, release.ReleaseID = application, ""
	if database {
		release.Database = "sqlite"
	}
	id, err := release.ID()
	if err != nil {
		t.Fatal(err)
	}
	release.ReleaseID = id
	inputs.Release, err = kernel.EncodeRelease(release)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := kernel.DecodeInstance(inputs.Instance)
	if err != nil {
		t.Fatal(err)
	}
	spec.Application, spec.Environment, spec.Release = application, environment, id
	if database {
		spec.Storage.SizeMiB = 128
		inputs.Egress = []byte(`{"kind":"v2-egress","version":1,"revision":"` + revision + `","instances":[{"application":"` + application + `","environment":"` + environment + `","port":30102,"grants":["swhurl-platform-backups-110927251694.s3.eu-west-2.amazonaws.com:443"]}]}`)
	}
	if exposed {
		spec.Grants.Exposure = "authenticated-web"
	}
	inputs.Instance, err = kernel.EncodeInstance(spec)
	if err != nil {
		t.Fatal(err)
	}
	app, err := kernel.DecodeApp(inputs.App)
	if err != nil {
		t.Fatal(err)
	}
	app.Application, app.AppID = application, ""
	inputs.App, err = kernel.EncodeApp(app)
	if err != nil {
		t.Fatal(err)
	}
	return inputs
}
func observationInputs(reader kube.Reader) StatusInputs {
	return StatusInputs{Reader: reader, Revision: revision, RecordsRoot: "v2-records", SharedPaths: []string{"v2/testdata/profile.json", "v2-records/calls.json", "platform/egress/allocation.json"},
		Source: "fake", Now: func() time.Time { return time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC) }, MaxAge: 26 * time.Hour}
}
func statusObject(ref observe.Ref, labels map[string]string) map[string]any {
	return map[string]any{"apiVersion": ref.APIVersion, "kind": ref.Kind, "metadata": map[string]any{"name": ref.Name, "namespace": ref.Namespace, "generation": 1, "labels": labels},
		"spec": map[string]any{}, "status": map[string]any{}}
}
func (f *statusCluster) put(ref observe.Ref, document map[string]any) {
	f.objects[ref] = observe.Read{Ref: ref, Status: observe.Present, Object: statusJSON(document)}
}
func addStatusInstance(t *testing.T, f *statusCluster, inputs Inputs) observe.Target {
	t.Helper()
	target, err := statusTarget(inputs, revision)
	if err != nil {
		t.Fatal(err)
	}
	owner := map[string]string{"kustomize.toolkit.fluxcd.io/name": target.Unit.Name, "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
	workloadLabels := map[string]string{"app.kubernetes.io/instance": target.Release.Name, "app.kubernetes.io/controller": "main"}
	for _, ref := range []observe.Ref{target.Unit, target.Release, target.Workload} {
		document := statusObject(ref, owner)
		document["spec"] = map[string]any{"suspend": false}
		document["status"] = map[string]any{"lastAppliedRevision": "main@sha1:" + revision, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}}
		if ref == target.Workload {
			document["metadata"].(map[string]any)["labels"] = workloadLabels
			document["status"] = map[string]any{"observedGeneration": 1, "availableReplicas": 1, "readyReplicas": 1, "conditions": []any{map[string]any{"type": "Available", "status": "True"}}}
		}
		f.put(ref, document)
	}
	pod := target.Pods
	pod.Name = "main-pod"
	document := statusObject(pod, workloadLabels)
	document["spec"] = map[string]any{"containers": []any{map[string]any{"name": "main", "image": "ghcr.io/test/app@" + target.Digest}}}
	document["status"] = map[string]any{"containerStatuses": []any{map[string]any{"name": "main", "imageID": "containerd://content", "ready": true}}}
	f.put(pod, document)
	if target.Route != nil {
		document := statusObject(target.Route.Ingress, owner)
		document["spec"] = map[string]any{"rules": []any{map[string]any{"host": "hello.test"}}}
		f.put(target.Route.Ingress, document)
		for _, certificate := range target.Route.Certificates {
			document = statusObject(certificate, owner)
			document["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}}
			f.put(certificate, document)
		}
	}
	if target.Backup != nil {
		addStatusBackup(f, target)
	}
	return target
}
func addStatusBackup(f *statusCluster, target observe.Target) {
	cron := statusObject(target.Backup.CronJob, map[string]string{"platform.swhurl.com/backup": "true"})
	cron["spec"] = map[string]any{"schedule": target.Backup.Schedule, "suspend": false}
	cron["status"] = map[string]any{"lastScheduleTime": "2026-11-03T09:00:00Z", "lastSuccessfulTime": "2026-11-03T09:01:00Z"}
	f.put(target.Backup.CronJob, cron)
	pod := target.Backup.Pods
	pod.Name = "backup-pod"
	document := statusObject(pod, map[string]string{"platform.swhurl.com/backup": "true"})
	document["metadata"].(map[string]any)["creationTimestamp"] = "2026-11-03T09:00:00Z"
	document["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"kind": "Job", "name": "same-run-name"}}
	f.put(pod, document)
	job := observe.Ref{APIVersion: "batch/v1", Kind: "Job", Namespace: target.Namespace, Name: "same-run-name"}
	document = statusObject(job, nil) // Intentionally no labels: the compiler guarantees none on Jobs.
	document["metadata"].(map[string]any)["creationTimestamp"] = "2026-11-03T09:00:00Z"
	document["status"] = map[string]any{"completionTime": "2026-11-03T09:01:00Z", "conditions": []any{map[string]any{"type": "Complete", "status": "True"}}}
	f.put(job, document)
}
func cluster() *statusCluster { return &statusCluster{objects: map[observe.Ref]observe.Read{}} }

func TestStatusAllAgreesWithSingleAndBatchesCoreReads(t *testing.T) {
	f := cluster()
	instances := []Inputs{statusVariant(t, "zulu", "staging", false, true), statusVariant(t, "alpha", "staging", false, false), statusVariant(t, "alpha", "prod", false, false)}
	targets := []observe.Target{}
	for _, inputs := range instances {
		targets = append(targets, addStatusInstance(t, f, inputs))
	}
	// The same release and pod names exist in alpha/prod and alpha/staging; only prod is failing.
	ref := targets[2].Pods
	ref.Name = "main-pod"
	var crashing map[string]any
	_ = json.Unmarshal(f.objects[ref].Object, &crashing)
	crashing["status"] = map[string]any{"containerStatuses": []any{map[string]any{"name": "main", "ready": false, "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}}}}
	f.put(ref, crashing)
	foreign := statusVariant(t, "foreign", "prod", false, true)
	addStatusInstance(t, f, foreign)
	options := observationInputs(f)
	bulk, err := StatusAll(t.Context(), options, instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reads) != 0 || len(f.lists) != 6 || len(bulk.Instances) != 3 {
		t.Fatalf("reads %v lists %v instances %d", f.reads, f.lists, len(bulk.Instances))
	}
	expected := []string{"alpha/prod", "alpha/staging", "zulu/staging"}
	byIdentity := map[string]Inputs{"alpha/prod": instances[2], "alpha/staging": instances[1], "zulu/staging": instances[0]}
	for i, entry := range bulk.Instances {
		key := entry.Application + "/" + entry.Environment
		if key != expected[i] {
			t.Fatalf("order: %s", key)
		}
		options.Inputs = byIdentity[key]
		single, err := Status(t.Context(), options)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(single, entry.Report) {
			t.Fatalf("%s single != bulk\nsingle %+v\nbulk %+v", key, single, entry.Report)
		}
	}
	if bulk.Instances[0].Report.State.Name != observe.StateFailing || bulk.Instances[1].Report.State.Name != observe.StateHealthy {
		t.Fatalf("states %+v", bulk.Instances)
	}
}

func TestStatusAllFixedListsAndKnownUnlabeledBackupJobs(t *testing.T) {
	for _, count := range []int{1, 3, 8} {
		f := cluster()
		instances := []Inputs{}
		for i := 0; i < count; i++ {
			app := "database-" + string(rune('a'+i))
			inputs := statusVariant(t, app, "staging", true, false)
			addStatusInstance(t, f, inputs)
			instances = append(instances, inputs)
		}
		result, err := StatusAll(t.Context(), observationInputs(f), instances)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.lists) != 6 || len(f.reads) != count {
			t.Fatalf("%d instances: %d lists %d reads", count, len(f.lists), len(f.reads))
		}
		for i, entry := range result.Instances {
			if entry.Report.Backup.State != observe.BackupCurrent || f.reads[i].Namespace != entry.Report.Target.Namespace || f.reads[i].Name != "same-run-name" || f.reads[i].Kind != "Job" {
				t.Fatalf("%+v reads %+v", entry.Report.Backup, f.reads)
			}
			options := observationInputs(f)
			options.Inputs = instances[i]
			single, err := Status(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(single, entry.Report) {
				t.Fatalf("single and bulk database differ: %+v / %+v", single.Backup, entry.Report.Backup)
			}
		}
	}
}

func TestStatusAllListFailuresAreNeverAbsence(t *testing.T) {
	for _, reason := range []string{observe.ReasonForbidden, observe.ReasonTooLarge, observe.ReasonIncomplete} {
		f := cluster()
		inputs := statusVariant(t, "hello", "staging", true, true)
		addStatusInstance(t, f, inputs)
		f.failedKind, f.failedReason = "Deployment", reason
		result, err := StatusAll(t.Context(), observationInputs(f), []Inputs{inputs})
		if err != nil {
			t.Fatal(err)
		}
		report := result.Instances[0].Report
		if report.State.Name != observe.StateUnknown || report.Live.Workload.Status != observe.Unavailable || report.Live.Workload.Reason != reason {
			t.Fatalf("%+v %+v", report.State, report.Live)
		}
		f.failedKind = "Pod"
		f.reads = nil
		result, err = StatusAll(t.Context(), observationInputs(f), []Inputs{inputs})
		if err != nil {
			t.Fatal(err)
		}
		if len(f.reads) != 0 || result.Instances[0].Report.Backup.State != observe.BackupUnavailable {
			t.Fatalf("%+v reads %v", result.Instances[0].Report.Backup, f.reads)
		}
	}
}

func TestStatusAllBackupMissingOwnersAndFailedVersusAbsentJobs(t *testing.T) {
	for _, scenario := range []string{"no owner", "older owner", "absent job", "failed job", "cross namespace"} {
		f := cluster()
		inputs := statusVariant(t, "hello", "staging", true, false)
		target := addStatusInstance(t, f, inputs)
		job := observe.Ref{APIVersion: "batch/v1", Kind: "Job", Namespace: target.Namespace, Name: "same-run-name"}
		if scenario == "absent job" || scenario == "cross namespace" {
			delete(f.objects, job)
		}
		if scenario == "failed job" {
			f.objects[job] = observe.Read{Ref: job, Status: observe.Unavailable, Reason: observe.ReasonForbidden}
		}
		if scenario == "cross namespace" {
			wrong := job
			wrong.Namespace = "foreign-staging"
			f.put(wrong, statusObject(wrong, nil))
		}
		if scenario == "no owner" || scenario == "older owner" {
			pod := target.Backup.Pods
			pod.Name = "newest-orphan"
			document := statusObject(pod, map[string]string{"platform.swhurl.com/backup": "true"})
			document["metadata"].(map[string]any)["creationTimestamp"] = "2026-11-03T09:30:00Z"
			f.put(pod, document)
			if scenario == "no owner" {
				old := target.Backup.Pods
				old.Name = "backup-pod"
				delete(f.objects, old)
			}
		}
		result, err := StatusAll(t.Context(), observationInputs(f), []Inputs{inputs})
		if err != nil {
			t.Fatal(err)
		}
		backup := result.Instances[0].Report.Backup
		switch scenario {
		case "no owner", "older owner":
			if len(f.reads) != 0 || backup.State != observe.BackupNotAssessed || backup.Reason != observe.ReasonNoOwner {
				t.Fatalf("%s: %+v reads %v", scenario, backup, f.reads)
			}
		case "failed job":
			if backup.State != observe.BackupUnavailable {
				t.Fatalf("%+v", backup)
			}
		default:
			if backup.State != observe.BackupCurrent || backup.NewestRun != nil || len(f.reads) != 1 || f.reads[0] != job {
				t.Fatalf("%s: %+v reads %v", scenario, backup, f.reads)
			}
		}
	}
}

func TestStatusAllEmptyInvalidAndRefusedInputs(t *testing.T) {
	f := cluster()
	result, err := StatusAll(t.Context(), observationInputs(f), nil)
	if err != nil || len(result.Instances) != 0 || len(f.lists) != 0 || len(f.reads) != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	bad := input(t, nil, nil)
	bad.Instance = []byte(`{}`)
	if _, err := StatusAll(t.Context(), observationInputs(f), []Inputs{bad}); err == nil || len(f.lists) != 0 {
		t.Fatalf("invalid input: %v", err)
	}
	good := input(t, nil, nil)
	if _, err := StatusAll(t.Context(), observationInputs(f), []Inputs{good, good}); err == nil {
		t.Fatal("duplicate instance accepted")
	}
	f.refuse = errors.New("refused: explicit test failure")
	if _, err := StatusAll(t.Context(), observationInputs(f), []Inputs{good}); !errors.Is(err, f.refuse) {
		t.Fatalf("%v", err)
	}
}

func TestRevisionRelevanceUsesRecordWriterAndCompilerPaths(t *testing.T) {
	inputs := statusVariant(t, "hello", "staging", false, false)
	f := cluster()
	target := addStatusInstance(t, f, inputs)
	old := strings.Repeat("b", 40)
	var unit map[string]any
	_ = json.Unmarshal(f.objects[target.Unit].Object, &unit)
	unit["status"].(map[string]any)["lastAppliedRevision"] = "main@sha1:" + old
	f.put(target.Unit, unit)
	compiled, err := Compile(inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		paths   []string
		current string
	}{
		{"other app v2 input", []string{"v2-records/other/staging/instance.json"}, "unaffected"},
		{"other app v2 rendered", []string{"v2-apps/instances/other/staging/helmrelease.json"}, "unaffected"},
		{"own instance not v1 apps", []string{"v2-records/hello/staging/instance.json"}, "pending"},
		{"own release", []string{"v2-records/hello/staging/release.json"}, "pending"},
		{"app record", []string{"v2-records/hello/app.json"}, "pending"},
		{"own output", []string{"v2-apps/instances/hello/staging/helmrelease.json"}, "pending"},
		{"registered unit json not yaml", compiled.Registrations, "pending"},
		{"removed generated file", []string{"v2-apps/instances/hello/staging/ingress.json"}, "pending"},
		{"renamed away previous path", []string{"archive/release.json", "v2-records/hello/staging/release.json"}, "pending"},
		{"own other env", []string{"v2-records/hello/prod/instance.json"}, "unaffected"},
		{"shared profile", []string{"v2/testdata/profile.json"}, "pending"},
		{"shared calls", []string{"v2-records/calls.json"}, "pending"},
		{"shared egress", []string{"platform/egress/allocation.json"}, "pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := observationInputs(f)
			options.Inputs = inputs
			options.Changes = &observe.Changes{Kind: "v2-changes", Version: 1, Comparisons: []observe.Comparison{{From: old, To: revision, Status: "complete", Paths: test.paths}}}
			single, err := Status(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			bulk, err := StatusAll(t.Context(), options, []Inputs{inputs})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(single, bulk.Instances[0].Report) || single.Currentness.State != test.current || single.Currentness.AppliedRevision != "main@sha1:"+old {
				t.Fatalf("currentness %+v, single/bulk equal %v", single.Currentness, reflect.DeepEqual(single, bulk.Instances[0].Report))
			}
		})
	}
}

func TestBulkCandidateIdentityAndOwnerFiltersAreNotInventory(t *testing.T) {
	inputs := statusVariant(t, "hello", "staging", false, true)
	f := cluster()
	target := addStatusInstance(t, f, inputs)
	wrong := statusObject(target.Release, map[string]string{"kustomize.toolkit.fluxcd.io/name": "other-unit", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"})
	f.put(target.Release, wrong)
	foreign := target.Workload
	foreign.Namespace = "other-staging"
	f.put(foreign, statusObject(foreign, map[string]string{"app.kubernetes.io/instance": "hello"}))
	delete(f.objects, target.Workload)
	result, err := StatusAll(t.Context(), observationInputs(f), []Inputs{inputs})
	if err != nil {
		t.Fatal(err)
	}
	report := result.Instances[0].Report
	if report.Live.Release.Status != observe.Absent || report.Live.Workload.Status != observe.Absent || len(result.Instances) != 1 {
		t.Fatalf("%+v", report.Live)
	}
}
