package observe_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

const (
	digest   = "sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0"
	commit   = "9f1c0e2d34b7a5c86e4f0d2b9a17c3e5d8b6f04a"
	other    = "0123456789abcdef0123456789abcdef01234567"
	selector = "app.kubernetes.io/instance=hello,app.kubernetes.io/controller=main"
)

// flux is a Flux object's document: a Ready condition about a generation, and what it applied.
type flux struct {
	generation  int64
	observed    int64 // the condition's observedGeneration; defaults to generation
	status      string
	suspended   bool
	applied     string
	noCondition bool
}

func fluxDocument(t *testing.T, f flux) []byte {
	t.Helper()
	observed := f.observed
	if observed == 0 {
		observed = f.generation
	}
	status := f.status
	if status == "" {
		status = "True"
	}
	document := map[string]any{
		"metadata": map[string]any{"generation": f.generation},
		"spec":     map[string]any{"suspend": f.suspended},
		"status":   map[string]any{"lastAppliedRevision": f.applied, "conditions": []any{}},
	}
	if !f.noCondition {
		document["status"].(map[string]any)["conditions"] = []any{
			map[string]any{"type": "Ready", "status": status, "observedGeneration": observed},
		}
	}
	return encode(t, document)
}

// deployment is a Deployment's document: an Available condition about a generation.
type deployment struct {
	generation  int64
	observed    int64 // status.observedGeneration; defaults to generation
	available   int64
	status      string
	noCondition bool
}

func deploymentDocument(t *testing.T, d deployment) []byte {
	t.Helper()
	observed := d.observed
	if observed == 0 {
		observed = d.generation
	}
	status := d.status
	if status == "" {
		status = "True"
	}
	document := map[string]any{
		"metadata": map[string]any{"generation": d.generation},
		"status": map[string]any{"observedGeneration": observed, "availableReplicas": d.available,
			"conditions": []any{}},
	}
	if !d.noCondition {
		document["status"].(map[string]any)["conditions"] = []any{
			map[string]any{"type": "Available", "status": status},
		}
	}
	return encode(t, document)
}

// pod is a pod's document: the container it runs and whether the node started it.
type pod struct {
	image     string
	started   bool
	container string
}

func podDocument(t *testing.T, p pod) []byte {
	t.Helper()
	name := p.container
	if name == "" {
		name = "main"
	}
	image := p.image
	if image == "" {
		image = "docker.io/nginxinc/nginx-unprivileged@" + digest
	}
	statuses := []any{}
	if p.started {
		statuses = append(statuses, map[string]any{"name": name, "imageID": "docker-pullable://" + image})
	}
	document := map[string]any{
		"metadata": map[string]any{"name": "hello-main-7c8f9d6b5-x2k4l"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "main", "image": image}}},
		"status":   map[string]any{"containerStatuses": statuses},
	}
	if name != "main" {
		document["spec"] = map[string]any{"containers": []any{map[string]any{"name": name, "image": image}}}
	}
	return encode(t, document)
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func target() observe.Target {
	return observe.Target{
		Namespace: "hello-staging",
		Unit:      observe.Ref{APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization", Namespace: "flux-system", Name: "app-hello-staging"},
		Release:   observe.Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease", Namespace: "hello-staging", Name: "hello"},
		Workload:  observe.Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "hello-staging", Name: "hello-main"},
		Pods:      observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: "hello-staging"},
		Selector:  selector,
		Container: "main",
		Digest:    digest,
		Replicas:  1,
		Revision:  commit,
	}
}

func present(ref observe.Ref, document []byte) observe.Read {
	return observe.Read{Ref: ref, Status: observe.Present, Object: document}
}

func absent(ref observe.Ref) observe.Read {
	return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}
}

func unavailable(ref observe.Ref, reason string) observe.Read {
	return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: reason}
}

// healthy is evidence that shows the target's intended release running.
func healthy(t *testing.T) observe.Evidence {
	t.Helper()
	target := target()
	return observe.Evidence{
		Source:     "api.swhurl.test",
		ObservedAt: "2026-10-25T09:14:00Z",
		Objects: []observe.Read{
			present(target.Unit, fluxDocument(t, flux{generation: 3, applied: "main@sha1:" + commit})),
			present(target.Release, fluxDocument(t, flux{generation: 2, applied: "5.2.1"})),
			present(target.Workload, deploymentDocument(t, deployment{generation: 4, available: 1})),
		},
		Pods: []observe.List{podList(t, target, podDocument(t, pod{started: true}))},
	}
}

func podList(t *testing.T, target observe.Target, items ...[]byte) observe.List {
	t.Helper()
	return observe.List{Ref: target.Pods, Selector: target.Selector, Status: observe.Present, Items: items}
}

// withRead replaces the read about one object, or adds it when the evidence had none.
func withRead(evidence observe.Evidence, read observe.Read) observe.Evidence {
	objects := make([]observe.Read, 0, len(evidence.Objects)+1)
	replaced := false
	for _, existing := range evidence.Objects {
		if existing.Ref == read.Ref {
			replaced = true
			objects = append(objects, read)
			continue
		}
		objects = append(objects, existing)
	}
	if !replaced {
		objects = append(objects, read)
	}
	evidence.Objects = objects
	return evidence
}

// without is evidence the caller did not collect for one object: not read, which is neither absence nor
// a failure.
func without(evidence observe.Evidence, ref observe.Ref) observe.Evidence {
	objects := make([]observe.Read, 0, len(evidence.Objects))
	for _, existing := range evidence.Objects {
		if existing.Ref != ref {
			objects = append(objects, existing)
		}
	}
	evidence.Objects = objects
	return evidence
}

// withPods replaces the pod list.
func withPods(evidence observe.Evidence, list observe.List) observe.Evidence {
	evidence.Pods = []observe.List{list}
	return evidence
}

// finding is one expected finding: its outcome and its reason.
type finding struct{ outcome, reason string }

// check asserts the named findings and the verdict; findings a case does not name are left to the
// healthy case, which asserts all of them.
func check(t *testing.T, report observe.Report, verdict observe.Verdict, want map[string]finding) {
	t.Helper()
	if report.Verdict != verdict {
		t.Errorf("verdict %s, want %s (findings %+v)", report.Verdict, verdict, report.Findings)
	}
	if len(report.Findings) != len(report.Assessed)+len(report.Unassessed) {
		t.Errorf("assessed %v and unassessed %v do not account for the findings", report.Assessed, report.Unassessed)
	}
	for field, expected := range want {
		found, ok := lookup(report, field)
		if !ok {
			t.Errorf("no finding for %s", field)
			continue
		}
		if found.Outcome != expected.outcome || found.Reason != expected.reason {
			t.Errorf("%s is %s/%s, want %s/%s", field, found.Outcome, found.Reason, expected.outcome, expected.reason)
		}
	}
}

func lookup(report observe.Report, field string) (observe.Finding, bool) {
	for _, finding := range report.Findings {
		if finding.Field == field {
			return finding, true
		}
	}
	return observe.Finding{}, false
}

// explain runs the projection, which must never fail on the table's evidence.
func explain(t *testing.T, evidence observe.Evidence) observe.Report {
	t.Helper()
	report, err := observe.Explain(target(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// The recorded instance is running: every one of the seven facts is established and positive, and the
// report lists what it does not cover.
func TestExplainReportsTheIntendedReleaseRunning(t *testing.T) {
	report := explain(t, healthy(t))
	check(t, report, observe.IsRunning, map[string]finding{
		"unit":          {observe.Reconciled, ""},
		"unit.revision": {observe.Applied, ""},
		"release":       {observe.Settled, ""},
		"workload":      {observe.Present, ""},
		"image":         {observe.Running, ""},
		"ready":         {observe.Ready, ""},
		"replicas":      {observe.AsIntended, ""},
	})
	if report.Source != "api.swhurl.test" || report.ObservedAt != "2026-10-25T09:14:00Z" || report.Target != target() {
		t.Fatalf("the report does not identify its evidence: %+v", report)
	}
	if len(report.Unassessed) != 0 || len(report.NotCovered) == 0 {
		t.Fatalf("assessed %v, not covered %v", report.Unassessed, report.NotCovered)
	}
	for _, fact := range []string{"history", "git_publication", "pods", "source_build"} {
		if !contains(report.NotCovered, fact) {
			t.Errorf("%q is not named as not covered", fact)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Flux applying from Git, Helm applying the chart and Kubernetes running the pods are three facts: each
// can fail on its own, and each failure is named.
func TestReconciliationAndReadinessAreSeparateFacts(t *testing.T) {
	target := target()
	for _, tt := range []struct {
		name   string
		change func(*observe.Evidence)
		want   map[string]finding
	}{
		{"the unit is suspended", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Unit, fluxDocument(t, flux{generation: 3, suspended: true, applied: "main@sha1:" + commit})))
		}, map[string]finding{"unit": {observe.NotReconciled, observe.ReasonSuspended}}},
		{"the unit's condition is about an older spec", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Unit, fluxDocument(t, flux{generation: 4, observed: 3, applied: "main@sha1:" + commit})))
		}, map[string]finding{"unit": {observe.NotReconciled, observe.ReasonStale}}},
		{"the unit has not reconciled", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Unit, fluxDocument(t, flux{generation: 3, status: "False", applied: "main@sha1:" + commit})))
		}, map[string]finding{"unit": {observe.NotReconciled, observe.ReasonNotTrue}}},
		{"the unit reports no condition", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Unit, fluxDocument(t, flux{generation: 3, noCondition: true})))
		}, map[string]finding{"unit": {observe.NotReconciled, observe.ReasonNoCondition}}},
		{"the release has not settled", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Release, fluxDocument(t, flux{generation: 2, status: "False"})))
		}, map[string]finding{"release": {observe.NotSettled, observe.ReasonNotTrue}}},
		{"the release is suspended", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Release, fluxDocument(t, flux{generation: 2, suspended: true})))
		}, map[string]finding{"release": {observe.NotSettled, observe.ReasonSuspended}}},
		{"the workload is not available", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Workload, deploymentDocument(t, deployment{generation: 4, available: 1, status: "False"})))
		}, map[string]finding{"ready": {observe.NotReady, observe.ReasonNotAvailable}}},
		{"the workload's availability is about an older spec", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Workload, deploymentDocument(t, deployment{generation: 5, observed: 4, available: 1})))
		}, map[string]finding{"ready": {observe.NotReady, observe.ReasonStale}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := healthy(t)
			tt.change(&evidence)
			check(t, explain(t, evidence), observe.IsNotRunning, tt.want)
		})
	}
}

// A fact that was not read, or could not be read, is never absence: the answer is unknown, and the
// finding says which it was.
func TestUnreadEvidenceIsNotAbsence(t *testing.T) {
	target := target()
	cases := []struct {
		name   string
		change func(*observe.Evidence)
		want   map[string]finding
	}{
		{"the unit was not read", func(e *observe.Evidence) { *e = without(*e, target.Unit) },
			map[string]finding{"unit": {observe.NotAssessed, observe.NotRead}, "unit.revision": {observe.NotAssessed, observe.NotRead}}},
		{"the unit read was forbidden", func(e *observe.Evidence) {
			*e = withRead(*e, unavailable(target.Unit, observe.ReasonForbidden))
		}, map[string]finding{"unit": {observe.Unavailable, observe.ReasonForbidden},
			"unit.revision": {observe.NotAssessed, observe.ReasonUnitDown}}},
		{"the workload was not read", func(e *observe.Evidence) { *e = without(*e, target.Workload) },
			map[string]finding{"workload": {observe.NotAssessed, observe.NotRead}, "ready": {observe.NotAssessed, observe.NotRead},
				"replicas": {observe.NotAssessed, observe.NotRead}}},
		{"the workload read timed out", func(e *observe.Evidence) {
			*e = withRead(*e, unavailable(target.Workload, observe.ReasonTimedOut))
		}, map[string]finding{"workload": {observe.Unavailable, observe.ReasonTimedOut},
			"image": {observe.NotAssessed, observe.ReasonWorkloadDown}}},
		{"the pod list was not read", func(e *observe.Evidence) { e.Pods = nil },
			map[string]finding{"image": {observe.NotAssessed, observe.NotRead}}},
		{"the pod list read was forbidden", func(e *observe.Evidence) {
			*e = withPods(*e, observe.List{Ref: target.Pods, Selector: target.Selector, Status: observe.Unavailable, Reason: observe.ReasonForbidden})
		}, map[string]finding{"image": {observe.NotAssessed, observe.ReasonPodsDown}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			evidence := healthy(t)
			tt.change(&evidence)
			report := explain(t, evidence)
			check(t, report, observe.Unknown, tt.want)
			for _, found := range report.Findings {
				if found.Outcome == observe.Absent {
					t.Errorf("%s reported absence from evidence that was never read", found.Field)
				}
			}
		})
	}
}

// Absence is a successful answer, and it decides: the target is not running, and the facts that depend
// on the missing object are not assessed rather than absent or equal.
func TestAbsenceDecidesAndLimitsWhatItTouches(t *testing.T) {
	target := target()
	for _, tt := range []struct {
		name   string
		change func(*observe.Evidence)
		want   map[string]finding
	}{
		{"the unit is absent", func(e *observe.Evidence) { *e = withRead(*e, absent(target.Unit)) },
			map[string]finding{"unit": {observe.Absent, observe.ReasonNotFound}, "unit.revision": {observe.NotAssessed, observe.ReasonUnitGone}}},
		{"the release is absent", func(e *observe.Evidence) { *e = withRead(*e, absent(target.Release)) },
			map[string]finding{"release": {observe.Absent, observe.ReasonNotFound}}},
		{"the workload is absent", func(e *observe.Evidence) { *e = withRead(*e, absent(target.Workload)) },
			map[string]finding{"workload": {observe.Absent, observe.ReasonNotFound}, "image": {observe.NotAssessed, observe.ReasonWorkloadGone},
				"ready": {observe.NotAssessed, observe.ReasonWorkloadGone}, "replicas": {observe.NotAssessed, observe.ReasonWorkloadGone}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := healthy(t)
			tt.change(&evidence)
			check(t, explain(t, evidence), observe.IsNotRunning, tt.want)
		})
	}
}

// The image fact is read from the pods, so a rollout, an unpinned image and a pod that has not pulled
// yet are told apart.
func TestTheRunningArtifactComesFromThePods(t *testing.T) {
	target := target()
	for _, tt := range []struct {
		name    string
		target  observe.Target
		list    func(t *testing.T, target observe.Target) observe.List
		want    finding
		verdict observe.Verdict
	}{
		{"the intended digest runs", target, func(t *testing.T, target observe.Target) observe.List {
			return podList(t, target, podDocument(t, pod{started: true}))
		}, finding{observe.Running, ""}, observe.IsRunning},
		{"another digest runs", target, func(t *testing.T, target observe.Target) observe.List {
			return podList(t, target, podDocument(t, pod{image: "docker.io/nginxinc/nginx-unprivileged@sha256:" + strings.Repeat("a", 64), started: true}))
		}, finding{observe.Different, observe.ReasonDigest}, observe.IsNotRunning},
		{"one of two pods runs another digest", target, func(t *testing.T, target observe.Target) observe.List {
			return podList(t, target, podDocument(t, pod{started: true}),
				podDocument(t, pod{image: "docker.io/other@sha256:" + strings.Repeat("b", 64), started: true}))
		}, finding{observe.Different, observe.ReasonDigest}, observe.IsNotRunning},
		{"the image is a tag, not a digest", target, func(t *testing.T, target observe.Target) observe.List {
			return podList(t, target, podDocument(t, pod{image: "docker.io/nginxinc/nginx-unprivileged:1.27-alpine", started: true}))
		}, finding{observe.Different, observe.ReasonUnpinnedPod}, observe.IsNotRunning},
		{"the pod has not pulled yet", target, func(t *testing.T, target observe.Target) observe.List {
			return podList(t, target, podDocument(t, pod{started: false}))
		}, finding{observe.NotRunning, observe.ReasonNotPulled}, observe.IsNotRunning},
		{"the selected pod does not run the container", target, func(t *testing.T, target observe.Target) observe.List {
			return podList(t, target, podDocument(t, pod{container: "sidecar", started: true}))
		}, finding{observe.Different, observe.ReasonNoContainer}, observe.IsNotRunning},
		{"no pod is scheduled", target, func(t *testing.T, target observe.Target) observe.List {
			return podList(t, target)
		}, finding{observe.NotRunning, observe.ReasonNoPods}, observe.IsNotRunning},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := withPods(healthy(t), tt.list(t, tt.target))
			report, err := observe.Explain(tt.target, evidence)
			if err != nil {
				t.Fatal(err)
			}
			check(t, report, tt.verdict, map[string]finding{"image": tt.want})
		})
	}
}

// Zero replicas is a value: nothing is supposed to run, so the image is not assessed and the count
// agrees.
func TestZeroReplicasIsAValue(t *testing.T) {
	target := target()
	target.Replicas = 0
	evidence := withPods(healthy(t), podList(t, target))
	evidence = withRead(evidence, present(target.Workload, deploymentDocument(t, deployment{generation: 4, available: 0})))
	report, err := observe.Explain(target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	check(t, report, observe.Unknown, map[string]finding{
		"image":    {observe.NotAssessed, observe.ReasonNoReplicas},
		"replicas": {observe.AsIntended, ""},
	})
}

// The available replica count is compared with the intended one.
func TestReplicasAreCompared(t *testing.T) {
	target := target()
	evidence := withRead(healthy(t), present(target.Workload, deploymentDocument(t, deployment{generation: 4, available: 0})))
	check(t, explain(t, evidence), observe.IsNotRunning, map[string]finding{"replicas": {observe.Different, observe.ReasonReplicas}})
}

// The unit's applied revision is a distinct fact from its readiness, and is only claimed when the
// intent was supplied.
func TestTheAppliedRevisionIsCompared(t *testing.T) {
	target := target()
	for _, tt := range []struct {
		name    string
		applied string
		target  observe.Target
		want    finding
		verdict observe.Verdict
	}{
		{"the intended commit is applied", "main@sha1:" + commit, target, finding{observe.Applied, ""}, observe.IsRunning},
		{"another commit is applied", "main@sha1:" + other, target, finding{observe.Different, ""}, observe.IsNotRunning},
		{"nothing is applied yet", "", target, finding{observe.NotApplied, observe.ReasonNotAppliedRev}, observe.IsNotRunning},
		{"the revision is not a commit", "deadbeef", target, finding{observe.NotAssessed, observe.ReasonRevisionShape}, observe.Unknown},
		{"no commit was supplied", "main@sha1:" + commit, withRevision(target, ""), finding{observe.NotAssessed, observe.ReasonNotSupplied}, observe.Unknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := withRead(healthy(t), present(tt.target.Unit, fluxDocument(t, flux{generation: 3, applied: tt.applied})))
			report, err := observe.Explain(tt.target, evidence)
			if err != nil {
				t.Fatal(err)
			}
			check(t, report, tt.verdict, map[string]finding{"unit.revision": tt.want})
		})
	}
}

func withRevision(target observe.Target, revision string) observe.Target {
	target.Revision = revision
	return target
}

// A known negative decides the answer even while another fact is unavailable: unknown is not a way to
// avoid reporting what was seen.
func TestANegativeDecidesOverAnUnreadFact(t *testing.T) {
	target := target()
	evidence := healthy(t)
	evidence = withRead(evidence, unavailable(target.Release, observe.ReasonForbidden))
	evidence = withPods(evidence, podList(t, target, podDocument(t, pod{
		image: "docker.io/nginxinc/nginx-unprivileged@sha256:" + strings.Repeat("a", 64), started: true})))
	report := explain(t, evidence)
	check(t, report, observe.IsNotRunning, map[string]finding{
		"release": {observe.Unavailable, observe.ReasonForbidden},
		"image":   {observe.Different, observe.ReasonDigest},
	})
}

// Every fact positive is the only way to "running": one unassessed fact leaves it unknown.
func TestOnlyAllPositiveFactsAreRunning(t *testing.T) {
	evidence := healthy(t)
	evidence.Pods = nil
	if report := explain(t, evidence); report.Verdict != observe.Unknown {
		t.Fatalf("verdict %s", report.Verdict)
	}
	if report := explain(t, healthy(t)); report.Verdict != observe.IsRunning {
		t.Fatalf("verdict %s", report.Verdict)
	}
}

// The report is reproducible, and each call owns what it returns.
func TestTheReportIsDeterministicAndOwned(t *testing.T) {
	evidence := healthy(t)
	first := explain(t, evidence)
	second := explain(t, evidence)
	if len(first.Findings) != len(second.Findings) {
		t.Fatal("different finding counts")
	}
	for i := range first.Findings {
		if first.Findings[i] != second.Findings[i] {
			t.Fatalf("finding %d differs: %+v %+v", i, first.Findings[i], second.Findings[i])
		}
	}
	first.Findings[0].Outcome = observe.Absent
	first.NotCovered[0] = "changed"
	again := explain(t, evidence)
	if again.Findings[0].Outcome != observe.Reconciled || again.NotCovered[0] == "changed" {
		t.Fatal("a caller changed a later report")
	}
	if len(again.Assessed) == 0 || len(again.Unassessed) != 0 {
		t.Fatalf("assessed %v, unassessed %v", again.Assessed, again.Unassessed)
	}
}

// A target that does not say what to look at, or says it in a shape the API cannot have, is invalid
// input rather than a gap.
func TestAnInvalidTargetIsRefused(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*observe.Target)
		field  string
		reason string
	}{
		{"an unusable namespace", func(target *observe.Target) { target.Namespace = "Hello" }, "namespace", "expected_a_name"},
		{"an unusable api version", func(target *observe.Target) { target.Unit.APIVersion = "apps" }, "unit", "expected_an_api_version"},
		{"an unusable kind", func(target *observe.Target) { target.Unit.Kind = "kustomization" }, "unit", "expected_a_kind"},
		{"an unusable name", func(target *observe.Target) { target.Workload.Name = "-main" }, "workload", "expected_an_object_name"},
		{"a workload in another namespace", func(target *observe.Target) { target.Workload.Namespace = "hello-prod" }, "workload", "namespace_mismatch"},
		{"a named pod collection", func(target *observe.Target) { target.Pods.Name = "hello-main" }, "pods", "name_not_used"},
		{"two roles naming one object", func(target *observe.Target) { target.Pods = target.Workload }, "target", "repeated_ref"},
		{"an unusable container", func(target *observe.Target) { target.Container = "Main" }, "container", "expected_an_object_name"},
		{"an unusable selector", func(target *observe.Target) { target.Selector = "app in (hello)" }, "selector", "expected_a_simple_label_selector"},
		{"a tag where a digest belongs", func(target *observe.Target) { target.Digest = "1.27-alpine" }, "digest", "expected_a_sha256_digest"},
		{"a negative replica count", func(target *observe.Target) { target.Replicas = -1 }, "replicas", "expected_a_nonnegative_count"},
		{"a revision that is not a commit", func(target *observe.Target) { target.Revision = "main" }, "revision", "expected_a_commit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			target := target()
			tt.change(&target)
			if _, err := observe.Explain(target, healthy(t)); !refusal(err, tt.field, tt.reason) {
				t.Fatalf("got %v, want %s/%s", err, tt.field, tt.reason)
			}
		})
	}
}

func refusal(err error, field, reason string) bool {
	var invalid *observe.Error
	return errors.As(err, &invalid) && invalid.Field == field && invalid.Reason == reason
}

// Contradictory or malformed evidence is refused: absence with content, a failure with no reason, an
// answer about an object the target did not ask for, and a bare document are not gaps in meaning.
func TestContradictoryEvidenceIsRefused(t *testing.T) {
	target := target()
	for _, tt := range []struct {
		name   string
		change func(*observe.Evidence)
		field  string
		reason string
	}{
		{"absence carrying content", func(e *observe.Evidence) {
			*e = withRead(*e, observe.Read{Ref: target.Workload, Status: observe.Absent, Reason: observe.ReasonNotFound, Object: []byte(`{}`)})
		}, "evidence.objects", "absent_read_malformed"},
		{"absence for another reason", func(e *observe.Evidence) {
			*e = withRead(*e, observe.Read{Ref: target.Workload, Status: observe.Absent, Reason: observe.ReasonForbidden})
		}, "evidence.objects", "absent_read_malformed"},
		{"a present read with no content", func(e *observe.Evidence) {
			*e = withRead(*e, observe.Read{Ref: target.Workload, Status: observe.Present})
		}, "evidence.objects", "present_read_incomplete"},
		{"a failure with no fixed reason", func(e *observe.Evidence) {
			*e = withRead(*e, observe.Read{Ref: target.Workload, Status: observe.Unavailable, Reason: "it broke"})
		}, "evidence.objects", "unavailable_read_malformed"},
		{"the caller's own not-read status", func(e *observe.Evidence) {
			*e = withRead(*e, observe.Read{Ref: target.Workload, Status: observe.NotRead})
		}, "evidence.objects", "unsupported_status"},
		{"two reads about one object", func(e *observe.Evidence) {
			e.Objects = append(e.Objects, present(target.Workload, deploymentDocument(t, deployment{generation: 1})))
		}, "evidence.objects", "duplicate_ref"},
		{"a read the target did not ask for", func(e *observe.Evidence) {
			e.Objects = append(e.Objects, present(observe.Ref{APIVersion: "v1", Kind: "Namespace", Name: "hello-staging"}, []byte(`{}`)))
		}, "evidence.objects", "unexpected_ref"},
		{"a list with another selector", func(e *observe.Evidence) {
			other := podList(t, target)
			other.Selector = "app.kubernetes.io/instance=other"
			e.Pods = append(e.Pods, other)
		}, "evidence.pods", "unexpected_list"},
		{"two lists for the same pods", func(e *observe.Evidence) {
			e.Pods = append(e.Pods, podList(t, target))
		}, "evidence.pods", "duplicate_list"},
		{"a read about the pod collection", func(e *observe.Evidence) {
			e.Objects = append(e.Objects, absent(target.Pods))
		}, "evidence.objects", "unexpected_ref"},
		{"an unavailable list carrying items", func(e *observe.Evidence) {
			*e = withPods(*e, observe.List{Ref: target.Pods, Selector: target.Selector,
				Status: observe.Unavailable, Reason: observe.ReasonForbidden, Items: [][]byte{[]byte(`{}`)}})
		}, "evidence.pods", "unavailable_read_malformed"},
		{"a list with a present status and a reason", func(e *observe.Evidence) {
			*e = withPods(*e, observe.List{Ref: target.Pods, Selector: target.Selector, Status: observe.Present, Reason: observe.ReasonNotFound})
		}, "evidence.pods", "present_read_incomplete"},
		{"a list reported absent", func(e *observe.Evidence) {
			*e = withPods(*e, observe.List{Ref: target.Pods, Selector: target.Selector, Status: observe.Absent, Reason: observe.ReasonNotFound})
		}, "evidence.pods", "unsupported_status"},
		{"a document that is not JSON", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Workload, []byte("not json")))
		}, "workload", "single_utf8_json_required"},
		{"a document with trailing content", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Workload, []byte(`{"metadata":{"generation":1}} {}`)))
		}, "workload", "single_utf8_json_required"},
		{"a document with a wrongly typed field", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Workload, []byte(`{"metadata":{"generation":"four"}}`)))
		}, "workload", "incorrect_type_or_range"},
		{"an object the API server never wrote", func(e *observe.Evidence) {
			*e = withRead(*e, present(target.Workload, []byte(`{"metadata":{}}`)))
		}, "workload", "missing_generation"},
		{"a pod document that is not JSON", func(e *observe.Evidence) {
			*e = withPods(*e, podList(t, target, []byte(`{`)))
		}, "pods", "single_utf8_json_required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := healthy(t)
			tt.change(&evidence)
			if _, err := observe.Explain(target, evidence); !refusal(err, tt.field, tt.reason) {
				t.Fatalf("got %v, want %s/%s", err, tt.field, tt.reason)
			}
		})
	}
}
