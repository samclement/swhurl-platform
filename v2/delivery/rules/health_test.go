package rules

import (
	"encoding/json"
	"strings"
	"testing"
)

var testDigest = "sha256:" + strings.Repeat("a", 64)

// healthyDeployment is app/staging Ready at generation 2, running the requested digest, with its unit Ready.
func healthyDeployment(t *testing.T) Deployment {
	labels := `"labels":{"app.kubernetes.io/instance":"app"}`
	return Deployment{Namespace: "app-staging", Name: "app",
		Unit: decoded(t, `{"spec":{},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`),
		Release: decoded(t, `{"metadata":{"generation":2},"spec":{"values":{"controllers":{"main":{"containers":{"main":
			{"image":{"repository":"ghcr.io/x/app","tag":"1","digest":"`+testDigest+`"}}}}}}},
			"status":{"conditions":[{"type":"Ready","status":"True","reason":"UpgradeSucceeded","observedGeneration":2}]}}`),
		Workloads: []map[string]any{decoded(t, `{"kind":"Deployment","metadata":{"name":"app","namespace":"app-staging",`+
			labels+`},"spec":{"replicas":1},"status":{"readyReplicas":1}}`)},
		Pods: []map[string]any{decoded(t, `{"metadata":{"namespace":"app-staging",`+labels+`},
			"spec":{"containers":[{"name":"main","image":"ghcr.io/x/app:1"}]},
			"status":{"containerStatuses":[{"name":"main","imageID":"docker-pullable://ghcr.io/x/app@`+testDigest+`"}]}}`)}}
}

func decoded(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// Each case changes the healthy deployment so that one rule decides, in ReleaseHealthOf's order. DeploymentHealthOf
// agrees except where the unit is not Ready.
func TestDeploymentHealth(t *testing.T) {
	healthy := DeploymentVerdict{Health: DeploymentHealthy, Detail: "Workload is Ready and deployment is current"}
	missing := DeploymentVerdict{Health: DeploymentUnhealthy, Detail: "Desired workload is missing or replica status is unavailable"}
	rollback := DeploymentVerdict{Health: DeploymentHealthy, Detail: "Previous working version is healthy after rollback; requested upgrade failed", RolledBack: true}
	stalled := func(detail string) DeploymentVerdict {
		return DeploymentVerdict{Health: DeploymentStalled, Detail: detail}
	}
	notReady := stalled("HelmRelease is not current/Ready (UpgradeSucceeded)")
	otherImage := stalled("Running image differs from the requested digest")
	ready := func(d *Deployment) map[string]any { return condition(d.Release, "Ready") }
	setConditions := func(d *Deployment, raw string) {
		field(d.Release, "status")["conditions"] = decoded(t, `{"c":`+raw+`}`)["c"]
	}
	for _, test := range []struct {
		name    string
		change  func(*Deployment)
		want    DeploymentVerdict
		release *DeploymentVerdict // ReleaseHealthOf's verdict when it differs
	}{
		{"healthy", func(*Deployment) {}, healthy, nil},
		{"no release, unit suspended", func(d *Deployment) { d.Release, field(d.Unit, "spec")["suspend"] = nil, true },
			DeploymentVerdict{Health: DeploymentUnjudged, Detail: "No release yet and deployment reconciliation is suspended"}, nil},
		{"no release", func(d *Deployment) { d.Release = nil }, stalled("HelmRelease has not been created"), nil},
		{"uninstalling", func(d *Deployment) { field(d.Release, "metadata")["deletionTimestamp"] = "2026-11-22T00:00:00Z" },
			DeploymentVerdict{Health: DeploymentUnjudged, Detail: "Uninstall is in progress"}, nil},
		{"no replicas wanted", func(d *Deployment) { field(d.Release, "spec", "values", "controllers", "main")["replicas"] = 0.0 },
			DeploymentVerdict{Health: DeploymentStopped, Detail: ""}, nil},
		{"workload elsewhere", func(d *Deployment) { field(d.Workloads[0], "metadata")["namespace"] = "app-prod" }, missing, nil},
		{"another controller's workload", func(d *Deployment) {
			field(d.Workloads[0], "metadata", "labels")["app.kubernetes.io/controller"] = "worker"
		}, missing, nil},
		{"replicas not stated", func(d *Deployment) { delete(field(d.Workloads[0], "spec"), "replicas") }, missing, nil},
		{"replicas not ready", func(d *Deployment) {
			field(d.Workloads[0], "status")["readyReplicas"] = 0.0
			field(d.Pods[0], "status")["containerStatuses"] = decoded(t, `{"c":[{"name":"main","state":{"waiting":{"reason":"ImagePullBackOff"}}}]}`)["c"]
		}, DeploymentVerdict{Health: DeploymentUnhealthy, Detail: "Deployment/app 0/1 Ready; ImagePullBackOff"}, nil},
		{"release suspended", func(d *Deployment) { field(d.Release, "spec")["suspend"] = true },
			DeploymentVerdict{Health: DeploymentSuspended, Detail: "Healthy workload; deployment reconciliation is suspended"}, nil},
		{"unit suspended", func(d *Deployment) { field(d.Unit, "spec")["suspend"] = true },
			DeploymentVerdict{Health: DeploymentSuspended, Detail: "Healthy workload; deployment reconciliation is suspended"}, nil},
		{"rolled back, unit blocked", func(d *Deployment) {
			setConditions(d, `[{"type":"Ready","status":"False"},{"type":"Remediated","status":"True","reason":"RollbackSucceeded","observedGeneration":2}]`)
			field(d.Unit, "status")["conditions"] = []any{}
		}, rollback, nil},
		{"rollback at an older generation", func(d *Deployment) {
			setConditions(d, `[{"type":"Ready","status":"False","reason":"UpgradeFailed"},{"type":"Remediated","status":"True","reason":"RollbackSucceeded","observedGeneration":1}]`)
		}, stalled("HelmRelease is not current/Ready (UpgradeFailed)"), nil},
		{"rollback still reconciling", func(d *Deployment) {
			setConditions(d, `[{"type":"Remediated","status":"True","reason":"RollbackSucceeded"},{"type":"Reconciling","status":"True"}]`)
		}, stalled("HelmRelease is not current/Ready (Unknown)"), nil},
		{"ready at an older generation", func(d *Deployment) { ready(d)["observedGeneration"] = 1.0 }, notReady, nil},
		{"ready without a generation", func(d *Deployment) { delete(ready(d), "observedGeneration") }, notReady, nil},
		{"not ready", func(d *Deployment) { ready(d)["status"] = "False" }, notReady, nil},
		{"another digest running", func(d *Deployment) {
			field(d.Pods[0], "status")["containerStatuses"] = []any{map[string]any{"name": "main", "imageID": "ghcr.io/x/app@sha256:b"}}
		}, otherImage, nil},
		{"only a deleting pod", func(d *Deployment) { field(d.Pods[0], "metadata")["deletionTimestamp"] = "2026-11-22T00:00:00Z" }, otherImage, nil},
		{"pinned spec image wins over its image ID", func(d *Deployment) {
			field(d.Pods[0], "spec")["containers"] = []any{map[string]any{"name": "main", "image": "ghcr.io/x/app@sha256:b"}}
		}, otherImage, nil},
		{"sidecar image ignored", func(d *Deployment) {
			statuses := field(d.Pods[0], "status")
			statuses["containerStatuses"] = append(list(statuses, "containerStatuses"), map[string]any{"name": "proxy", "imageID": "proxy@sha256:c"})
		}, healthy, nil},
		{"ready after rollback with another digest", func(d *Deployment) {
			ready(d)["reason"] = "RollbackSucceeded"
			field(d.Pods[0], "status")["containerStatuses"] = []any{map[string]any{"name": "main", "imageID": "ghcr.io/x/app@sha256:b"}}
		}, healthy, nil},
		{"tag only, nothing running", func(d *Deployment) {
			delete(field(d.Release, "spec", "values", "controllers", "main", "containers", "main", "image"), "digest")
			field(d.Pods[0], "status")["containerStatuses"] = []any{}
		}, healthy, nil},
		{"unit not ready", func(d *Deployment) { condition(d.Unit, "Ready")["status"] = "False" },
			stalled("Flux unit is blocked or has not finished applying the request"), &healthy},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := healthyDeployment(t)
			test.change(&d)
			want := test.want
			if test.release != nil {
				want = *test.release
			}
			if got := DeploymentHealthOf(d); got != test.want {
				t.Errorf("DeploymentHealthOf = %+v, want %+v", got, test.want)
			}
			if got := ReleaseHealthOf(d); got != want {
				t.Errorf("ReleaseHealthOf = %+v, want %+v", got, want)
			}
		})
	}
}

func TestRequestedImage(t *testing.T) {
	for raw, want := range map[string]string{
		`{"repository":"ghcr.io/x/app","tag":"1","digest":"sha256:a"}`: "ghcr.io/x/app:1@sha256:a",
		`{"repository":"ghcr.io/x/app"}`:                               "ghcr.io/x/app",
		`{"tag":"1"}`:                                                  "?:1",
	} {
		release := decoded(t, `{"spec":{"values":{"controllers":{"main":{"containers":{"main":{"image":`+raw+`}}}}}}}`)
		if got := RequestedImage(release); got != want {
			t.Errorf("RequestedImage(%s) = %q, want %q", raw, got, want)
		}
	}
}
