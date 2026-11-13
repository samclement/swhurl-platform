package orchestration

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
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

// appTemplateCluster is a fake cluster with Flux's, the chart's, the provisioner's and the kubelet's behaviour for
// the fixture: a new unit applies its instance (the labelled namespace and, for the web app, the workload, its pod
// and a claim bound to a new volume); deleting a unit prunes the workload and keeps the namespace and the claim; a
// volume whose reclaim policy is Delete goes once its claim has. hold names a behaviour that does not happen, so a
// step's wait or a check fails.
type appTemplateCluster struct {
	*kubetest.Fake
	units   map[string]bool
	hold    map[string]bool
	changes []string
	volumes int
	reclaim map[string]bool
}

func newAppTemplateCluster(hold ...string) *appTemplateCluster {
	fake := kubetest.New(kubetest.Answers{})
	fake.Objects = map[observe.Ref][]byte{}
	fake.Collections = map[observe.Ref][][]byte{appTemplatePodsRef(): {}, appTemplateServicesRef(): {}, appTemplateIngressesRef(): {}}
	c := &appTemplateCluster{Fake: fake, units: map[string]bool{}, hold: map[string]bool{}, reclaim: map[string]bool{}}
	for _, h := range hold {
		c.hold[h] = true
	}
	return c
}

func (c *appTemplateCluster) Read(ctx context.Context, ref observe.Ref) (observe.Read, error) {
	if c.hold["unreadable"] {
		return observe.Read{Ref: ref, Status: observe.Unavailable, Reason: observe.ReasonForbidden}, nil
	}
	if ref.Kind != "Kustomization" {
		return c.Fake.Read(ctx, ref)
	}
	if !c.units[ref.Name] {
		return observe.Read{Ref: ref, Status: observe.Absent, Reason: observe.ReasonNotFound}, nil
	}
	ready, message := "True", "Applied revision: main@sha1:0123"
	if c.hold["ready-"+ref.Name] {
		ready, message = "False", "health check failed after 3m0s: timeout waiting for: [HelmRelease/smoke-web-prod/smoke-web status: 'InProgress']"
	}
	return observe.Read{Ref: ref, Status: observe.Present, Object: lifecycleDocument(map[string]any{
		"metadata": map[string]any{"name": ref.Name, "namespace": ref.Namespace},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": ready, "message": message}}},
	})}, nil
}

func (c *appTemplateCluster) Create(ctx context.Context, ref observe.Ref, document []byte) error {
	c.changes = append(c.changes, "create "+ref.Kind+"/"+ref.Name)
	if err := c.Fake.Create(ctx, ref, document); err != nil {
		return err
	}
	if ref.Kind == "Pod" {
		phase := map[bool]string{true: "Failed", false: "Succeeded"}[c.hold["empty"]]
		if c.hold["reader"] {
			phase = "Pending"
		}
		c.Objects[ref] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": ref.Name}, "status": map[string]any{"phase": phase}})
		return nil
	}
	if !strings.Contains(string(document), `"./tests/fixtures/app-template/v2-apps/instances/`) {
		return errors.New("the unit does not point into the fixture")
	}
	c.units[ref.Name] = true
	app := strings.TrimSuffix(strings.TrimPrefix(ref.Name, "app-"), "-prod")
	label := app
	if c.hold["unlabelled"] {
		label = "other"
	}
	c.Objects[appTemplateNamespaceRef(app)] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": app + "-prod",
		"labels": map[string]any{apptemplatetest.AppLabel: label}}})
	if app == apptemplatetest.Worker && c.hold["exposed"] {
		c.Collections[appTemplateServicesRef()] = [][]byte{[]byte(`{"metadata":{"name":"smoke-worker","namespace":"smoke-worker-prod"}}`)}
	}
	if app == apptemplatetest.Web {
		c.applyWeb()
	}
	return nil
}

// applyWeb is what the web app's unit applies: the chart's Deployment and pod, and a claim that, the first time,
// binds to a new volume.
func (c *appTemplateCluster) applyWeb() {
	if _, ok := c.Objects[appTemplateClaimRef()]; !ok {
		c.volumes++
		volume := fmt.Sprintf("pv-%d", c.volumes)
		phase := map[bool]string{true: "Pending", false: "Bound"}[c.hold["pending-claim"]]
		c.Objects[appTemplateClaimRef()] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": "smoke-web"},
			"spec":   map[string]any{"volumeName": volume, "storageClassName": "local-path-retain"},
			"status": map[string]any{"phase": phase}})
		c.Objects[appTemplateVolumeRef(volume)] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": volume},
			"spec": map[string]any{"claimRef": map[string]any{"namespace": "smoke-web-prod", "name": "smoke-web"}}})
	}
	automount := c.hold["insecure"]
	c.Objects[appTemplateDeploymentRef()] = lifecycleDocument(map[string]any{"metadata": map[string]any{"name": "smoke-web"},
		"spec": map[string]any{"replicas": 1, "strategy": map[string]any{"type": "Recreate"}, "template": map[string]any{"spec": map[string]any{
			"automountServiceAccountToken": automount, "securityContext": map[string]any{"runAsNonRoot": true},
			"containers": []any{map[string]any{"name": "main", "image": "ghcr.io/samclement/demo-shop@sha256:663a",
				"env": []any{map[string]any{"name": "DATABASE_PATH", "value": "/data/app.db"}}}}}}}})
	path := "/healthz"
	if c.hold["unprobed"] {
		path = "/"
	}
	probe := map[string]any{"httpGet": map[string]any{"path": path, "port": 8080}}
	c.Collections[appTemplatePodsRef()] = [][]byte{lifecycleDocument(map[string]any{"metadata": map[string]any{"name": "smoke-web-abc", "namespace": "smoke-web-prod"},
		"spec":   map[string]any{"containers": []any{map[string]any{"name": "main", "readinessProbe": probe, "livenessProbe": probe, "startupProbe": probe}}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}})}
}

func (c *appTemplateCluster) Patch(ctx context.Context, ref observe.Ref, patch []byte) error {
	c.changes = append(c.changes, "patch "+ref.Kind+"/"+ref.Name+" "+string(patch))
	if err := c.Fake.Patch(ctx, ref, patch); err != nil {
		return err
	}
	if ref.Kind == "PersistentVolume" {
		c.reclaim[ref.Name] = true
		c.settleVolumes()
	}
	return nil
}

func (c *appTemplateCluster) Delete(ctx context.Context, ref observe.Ref) error {
	c.changes = append(c.changes, "delete "+ref.Kind+"/"+ref.Name)
	if err := c.Fake.Delete(ctx, ref); err != nil {
		return err
	}
	switch {
	case ref.Kind == "Kustomization" && !c.hold["unit-gone"]:
		delete(c.units, ref.Name)
		if ref.Name == apptemplatetest.Unit(apptemplatetest.Web) {
			delete(c.Objects, appTemplateDeploymentRef())
			c.Collections[appTemplatePodsRef()] = [][]byte{}
		}
	case ref.Kind == "Namespace":
		delete(c.Objects, ref)
		if ref.Name == "smoke-web-prod" {
			delete(c.Objects, appTemplateClaimRef())
			delete(c.Objects, appTemplateReaderRef())
		}
	case ref.Kind != "Kustomization":
		delete(c.Objects, ref)
	}
	c.settleVolumes()
	return nil
}

// settleVolumes is the provisioner: a volume whose reclaim policy is Delete goes once no claim is bound to it.
func (c *appTemplateCluster) settleVolumes() {
	_, bound := c.Objects[appTemplateClaimRef()]
	for volume := range c.reclaim {
		if !bound {
			delete(c.Objects, appTemplateVolumeRef(volume))
		}
	}
}

// remaining is what the run left behind: units, namespaces and volumes.
func (c *appTemplateCluster) remaining() []string {
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

// appTemplateRendered is what the command takes from the fixture's render.
func appTemplateRendered() apptemplatetest.Rendered {
	units := map[string][]byte{}
	for _, app := range apptemplatetest.Apps() {
		units[app] = []byte(`{"apiVersion":"kustomize.toolkit.fluxcd.io/v1","kind":"Kustomization","metadata":{"name":"app-` + app +
			`-prod","namespace":"flux-system"},"spec":{"path":"./tests/fixtures/app-template/v2-apps/instances/` + app + `/prod"}}`)
	}
	return apptemplatetest.Rendered{Units: units, Host: "smoke-web.homelab.swhurl.com", HealthPath: "/healthz"}
}

const appTemplateAddress = "https://smoke-web.homelab.swhurl.com/"

// signInRoute is the route as oauth2-proxy answers a browser that is not signed in, after the given answers.
func signInRoute(before ...route.Answer) *routetest.Fake {
	fake := routetest.New()
	fake.Answers[appTemplateAddress] = append(before, route.Answer{Status: 302, Location: "https://accounts.google.com/o/oauth2/auth?x"})
	return fake
}

// appTemplateResult is one run: what it said, what it returned, and how long its waits took.
type appTemplateResult struct {
	said, lines []apptemplatetest.Line
	err         error
	waited      time.Duration
}

func runAppTemplate(ctx context.Context, cluster *appTemplateCluster, prober route.Prober) appTemplateResult {
	started := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	result := appTemplateResult{}
	result.lines, result.err = AppTemplateTest(ctx, AppTemplateTestInputs{
		Cluster: Cluster{Reader: cluster, Writer: cluster}, Route: prober, Rendered: appTemplateRendered(),
		Now:   func() time.Time { return started.Add(result.waited) },
		Sleep: func(d time.Duration) { result.waited += d },
		Say:   func(line apptemplatetest.Line) { result.said = append(result.said, line) }})
	return result
}

// The lines of a passing run, in the order make live-test-app-template has always said its checks.
const appTemplatePassed = `[OK] smoke-worker-prod unit Ready (HelmRelease installed, workload healthy)
[OK] smoke-web-prod unit Ready (HelmRelease installed, workload healthy)
[OK] worker has no Service or Ingress
[OK] web pod runs non-root without a service-account token
[OK] https://smoke-web.homelab.swhurl.com redirects to Google sign-in
[OK] claim bound on local-path-retain
[OK] persistent volume written (/data/app.db)
[OK] prod image pinned by digest
[OK] DATABASE_PATH points into the volume
[OK] one replica, stop-first updates (single writer)
[OK] web pod Ready on its /healthz probes

App template test passed.
[INFO] Cleaned up app-template test instances`

// A passing run says each check in order, the passed line before the cleanup's, makes its changes in the order of
// the steps, asks the route until it redirects, and leaves nothing behind.
func TestAppTemplateTestPasses(t *testing.T) {
	cluster := newAppTemplateCluster()
	prober := signInRoute(route.Answer{Status: 503})
	result := runAppTemplate(context.Background(), cluster, prober)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if text := apptemplatetest.Text(result.lines); text != appTemplatePassed {
		t.Fatalf("lines:\n%s", text)
	}
	if !reflect.DeepEqual(result.said, result.lines) {
		t.Fatal("the lines said differ from the lines returned")
	}
	want := []string{
		"create Kustomization/app-smoke-worker-prod", "create Kustomization/app-smoke-web-prod",
		"delete Pod/reader", "create Pod/reader", "delete Pod/reader",
		// cleanup
		"delete Kustomization/app-smoke-worker-prod", "delete Kustomization/app-smoke-web-prod",
		"delete Namespace/smoke-worker-prod", "delete Namespace/smoke-web-prod",
		`patch PersistentVolume/pv-1 {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`,
	}
	if !reflect.DeepEqual(cluster.changes, want) {
		t.Fatalf("changes:\n%s", strings.Join(cluster.changes, "\n"))
	}
	if left := cluster.remaining(); len(left) != 0 {
		t.Fatalf("left behind %q", left)
	}
	if !reflect.DeepEqual(prober.Asked, []string{appTemplateAddress, appTemplateAddress}) || result.waited != appTemplateBetween {
		t.Fatalf("asked %v, waited %s", prober.Asked, result.waited)
	}
}

// Leftovers from an earlier run, or a cluster the preflight cannot read, refuse before any change.
func TestAppTemplateTestPreflightRefusesWithNoChanges(t *testing.T) {
	for _, c := range []struct {
		name    string
		arrange func(*appTemplateCluster)
		want    string
	}{
		{"worker namespace", func(c *appTemplateCluster) {
			c.Objects[appTemplateNamespaceRef("smoke-worker")] = []byte(`{"metadata":{"name":"smoke-worker-prod"}}`)
		}, "smoke-worker-prod already exists; clean up first"},
		{"web unit", func(c *appTemplateCluster) { c.units["app-smoke-web-prod"] = true }, "smoke-web-prod already exists; clean up first"},
		{"unreadable", func(c *appTemplateCluster) { c.hold["unreadable"] = true },
			"cannot check for leftovers: failed: Namespace smoke-worker-prod: forbidden"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cluster := newAppTemplateCluster()
			c.arrange(cluster)
			result := runAppTemplate(context.Background(), cluster, signInRoute())
			var preflight *apptemplatetest.Preflight
			if !errors.As(result.err, &preflight) || result.err.Error() != c.want {
				t.Fatalf("%v", result.err)
			}
			if len(cluster.changes) != 0 || len(cluster.Writes) != 0 || len(result.said) != 0 || result.lines != nil {
				t.Fatalf("changes %q, said %v", cluster.changes, result.said)
			}
		})
	}
}

// A unit that is not Ready, or a step that cannot go on, is one failed line; the checks after it do not run, there is
// no passed line, and the cleanup still removes the units, the labelled namespaces and the volume.
func TestAppTemplateTestCleansUpAfterAFailedStep(t *testing.T) {
	for _, c := range []struct {
		hold, failed string
		checks       int
		waited       time.Duration
		left         []string
	}{
		{"ready-app-smoke-worker-prod", "smoke-worker-prod unit not Ready after 10m0s: health check failed after 3m0s: timeout waiting for: " +
			"[HelmRelease/smoke-web-prod/smoke-web status: 'InProgress']", 0, appTemplateReady, nil},
		{"ready-app-smoke-web-prod", "smoke-web-prod unit not Ready after 10m0s: health check failed after 3m0s: timeout waiting for: " +
			"[HelmRelease/smoke-web-prod/smoke-web status: 'InProgress']", 1, appTemplateReady, nil},
		{"reader", "pod smoke-web-prod/reader not finished after 2m0s", 6, appTemplateReader, nil},
		{"unit-gone", "", 11, appTemplateDeleted, []string{"Kustomization/app-smoke-web-prod", "Kustomization/app-smoke-worker-prod"}},
	} {
		t.Run(c.hold, func(t *testing.T) {
			cluster := newAppTemplateCluster(c.hold)
			result := runAppTemplate(context.Background(), cluster, signInRoute())
			if result.err != nil {
				t.Fatal(result.err)
			}
			lines := result.lines
			if c.failed != "" {
				want := []apptemplatetest.Line{apptemplatetest.Failed(c.failed), apptemplatetest.CleanedUp()}
				if !reflect.DeepEqual(lines[len(lines)-2:], want) || len(lines) != c.checks+2 || apptemplatetest.Passes(lines) {
					t.Fatalf("lines:\n%s", apptemplatetest.Text(lines))
				}
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

// A unit that cannot be created fails the install, and the cleanup still runs.
func TestAppTemplateTestCleansUpAfterAFailedCreate(t *testing.T) {
	cluster := &refusingCreate{newAppTemplateCluster()}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	lines, err := AppTemplateTest(context.Background(), AppTemplateTestInputs{Cluster: Cluster{Reader: cluster, Writer: cluster},
		Route: signInRoute(), Rendered: appTemplateRendered(), Now: func() time.Time { return now },
		Sleep: func(d time.Duration) { now = now.Add(d) }})
	want := "[BAD] create Kustomization app-smoke-worker-prod: failed: write: forbidden\n[INFO] Cleaned up app-template test instances"
	if err != nil || apptemplatetest.Text(lines) != want {
		t.Fatalf("%v\n%s", err, apptemplatetest.Text(lines))
	}
	if !slices.Contains(cluster.changes, "delete Kustomization/app-smoke-web-prod") {
		t.Fatalf("not cleaned up: %q", cluster.changes)
	}
}

// refusingCreate is a cluster that refuses every Create.
type refusingCreate struct{ *appTemplateCluster }

func (c *refusingCreate) Create(_ context.Context, ref observe.Ref, _ []byte) error {
	c.changes = append(c.changes, "create "+ref.Kind+"/"+ref.Name)
	return &kube.Error{Code: kube.Failed, Subject: "write", Reason: observe.ReasonForbidden}
}

// A failed check is a failed line, and the test goes on to the next.
func TestAppTemplateTestFailedChecksGoOn(t *testing.T) {
	for _, c := range []struct {
		hold, bad string
		prober    *routetest.Fake
		waited    time.Duration
	}{
		{"exposed", "worker exposes a Service or Ingress", signInRoute(), 0},
		{"insecure", "web pod security defaults missing", signInRoute(), 0},
		{"unprotected", "route not protected by sign-in: 200 ", unprotectedRoute(), (appTemplateTries - 1) * appTemplateBetween},
		{"unreachable", "route not protected by sign-in: failed: route: unreachable", routetest.New(), (appTemplateTries - 1) * appTemplateBetween},
		{"pending-claim", "claim not bound on local-path-retain", signInRoute(), 0},
		{"empty", "no data written (/data/app.db missing or empty)", signInRoute(), 0},
		{"unprobed", "web pod not Ready on its /healthz probes: smoke-web-abc has no readiness probe on /healthz", signInRoute(), 0},
	} {
		t.Run(c.hold, func(t *testing.T) {
			cluster := newAppTemplateCluster(c.hold)
			result := runAppTemplate(context.Background(), cluster, c.prober)
			text := apptemplatetest.Text(result.lines)
			if !strings.Contains(text, "\n[BAD] "+c.bad+"\n") || strings.Contains(text, apptemplatetest.Passed) ||
				len(result.lines) != len(strings.Split(appTemplatePassed, "\n"))-2 {
				t.Fatalf("lines:\n%s", text)
			}
			if result.waited < c.waited || len(c.prober.Asked) > appTemplateTries {
				t.Fatalf("waited %s, asked %d", result.waited, len(c.prober.Asked))
			}
			if left := cluster.remaining(); len(left) != 0 {
				t.Fatalf("left behind %q", left)
			}
		})
	}
}

func unprotectedRoute() *routetest.Fake {
	fake := routetest.New()
	fake.Answers[appTemplateAddress] = []route.Answer{{Status: 200}}
	return fake
}

// The cleanup deletes a namespace only when render's label names its app, and a volume only when its claim lived in
// the web app's namespace.
func TestAppTemplateTestGuards(t *testing.T) {
	cluster := newAppTemplateCluster("unlabelled")
	result := runAppTemplate(context.Background(), cluster, signInRoute())
	if result.err != nil || !apptemplatetest.Passes(result.lines) {
		t.Fatalf("%v\n%s", result.err, apptemplatetest.Text(result.lines))
	}
	if slices.ContainsFunc(cluster.changes, func(change string) bool { return strings.HasPrefix(change, "delete Namespace/") }) {
		t.Fatal("an unlabelled namespace was deleted")
	}
	// The claim stays with its namespace, so its volume is not released: it is still there, and only patched.
	if left := cluster.remaining(); !reflect.DeepEqual(left, []string{"Namespace/smoke-web-prod", "Namespace/smoke-worker-prod", "PersistentVolume/pv-1"}) {
		t.Fatalf("left %q", left)
	}

	cluster = newAppTemplateCluster()
	cluster.Objects[appTemplateVolumeRef("pv-1")] = []byte(`{"metadata":{"name":"pv-1"},"spec":{"claimRef":{"namespace":"demo-shop-prod"}}}`)
	cluster.volumes = 1
	cluster.Objects[appTemplateClaimRef()] = []byte(`{"spec":{"volumeName":"pv-1","storageClassName":"local-path-retain"},"status":{"phase":"Bound"}}`)
	run := &appTemplateRun{liveCluster: liveCluster{context.Background(), cluster, time.Now, func(time.Duration) {}},
		inputs: AppTemplateTestInputs{Cluster: Cluster{Reader: cluster, Writer: cluster}}}
	run.cleanup()
	if slices.ContainsFunc(cluster.changes, func(change string) bool { return strings.HasPrefix(change, "patch PersistentVolume/") }) {
		t.Fatalf("another namespace's volume was released: %q", cluster.changes)
	}
}

// A cancelled run stops its step and still cleans up.
func TestAppTemplateTestCleansUpWhenCancelled(t *testing.T) {
	cluster := newAppTemplateCluster("ready-app-smoke-worker-prod")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := runAppTemplate(ctx, cluster, signInRoute())
	want := []apptemplatetest.Line{
		apptemplatetest.Failed("smoke-worker-prod unit not Ready: wait stopped: context canceled: health check failed after 3m0s: " +
			"timeout waiting for: [HelmRelease/smoke-web-prod/smoke-web status: 'InProgress']"),
		apptemplatetest.CleanedUp(),
	}
	if result.err != nil || !reflect.DeepEqual(result.lines, want) {
		t.Fatalf("%v\n%s", result.err, apptemplatetest.Text(result.lines))
	}
	if left := cluster.remaining(); len(left) != 0 {
		t.Fatalf("left behind %q", left)
	}
}
