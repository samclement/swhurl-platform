package apptemplatetest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Each verdict is a pair of words: ok when the check passed, bad otherwise.
func TestVerdicts(t *testing.T) {
	for _, c := range []struct {
		pass, fail Line
		ok, bad    string
	}{
		{Unexposed(0, 0), Unexposed(1, 0), "worker has no Service or Ingress", "worker exposes a Service or Ingress"},
		{Protected("smoke-web.example", true, ""), Protected("smoke-web.example", false, "200 "),
			"https://smoke-web.example redirects to Google sign-in", "route not protected by sign-in: 200 "},
		{ClaimBound([]byte(`{"spec":{"storageClassName":"local-path-retain"},"status":{"phase":"Bound"}}`)),
			ClaimBound([]byte(`{"spec":{"storageClassName":"local-path"},"status":{"phase":"Bound"}}`)),
			"claim bound on local-path-retain", "claim not bound on local-path-retain"},
		{Written("Succeeded"), Written("Failed"), "persistent volume written (/data/app.db)",
			"no data written (/data/app.db missing or empty)"},
	} {
		if c.pass != (Line{OK, c.ok}) || c.fail != (Line{Bad, c.bad}) {
			t.Errorf("%+v %+v", c.pass, c.fail)
		}
	}
	if Unexposed(0, 1).Level != Bad || ClaimBound([]byte(`{"spec":{"storageClassName":"local-path-retain"},"status":{"phase":"Pending"}}`)).Level != Bad {
		t.Fatal("an Ingress or a Pending claim passed")
	}
	if UnitReady(Web) != (Line{OK, "smoke-web-prod unit Ready (HelmRelease installed, workload healthy)"}) {
		t.Fatal(UnitReady(Web))
	}
	if !SignInRedirect(302, "https://accounts.google.com/o/oauth2/auth?x") || SignInRedirect(301, "https://accounts.google.com/") ||
		SignInRedirect(302, "https://smoke-web.example/oauth2/start") || Answer(302, "https://x/") != "302 https://x/" {
		t.Fatal("the sign-in redirect")
	}
	if !Finished("Succeeded") || !Finished("Failed") || Finished("Running") {
		t.Fatal("a finished pod")
	}
}

// The web app's Deployment as the chart renders it from the fixture's HelmRelease.
const deployed = `{"spec":{"replicas":1,"strategy":{"type":"Recreate"},"template":{"spec":{
"automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":65532},
"containers":[{"name":"main","image":"ghcr.io/samclement/demo-shop@sha256:663a",
"env":[{"name":"DATABASE_PATH","value":"/data/app.db"},{"name":"HOST_IP","valueFrom":{"fieldRef":{"fieldPath":"status.hostIP"}}}]}]}}}}`

// The Deployment's verdicts: security first, then the digest, the database path and the single writer; each fails
// on its own and names what it found.
func TestDeployed(t *testing.T) {
	security, pinned := Deployed([]byte(deployed))
	want := []Line{{OK, "prod image pinned by digest"}, {OK, "DATABASE_PATH points into the volume"},
		{OK, "one replica, stop-first updates (single writer)"}}
	if security != (Line{OK, "web pod runs non-root without a service-account token"}) || !reflect.DeepEqual(pinned, want) {
		t.Fatalf("%v %v", security, pinned)
	}
	broken := strings.NewReplacer(`"automountServiceAccountToken":false`, `"automountServiceAccountToken":true`,
		`@sha256:663a`, `:v1`, `"/data/app.db"`, `"/tmp/app.db"`, `"replicas":1`, `"replicas":2`,
		`"Recreate"`, `"RollingUpdate"`).Replace(deployed)
	security, pinned = Deployed([]byte(broken))
	want = []Line{{Bad, "prod image not digest-pinned"}, {Bad, `DATABASE_PATH is "/tmp/app.db"`},
		{Bad, `replicas 2, strategy "RollingUpdate"`}}
	if security != (Line{Bad, "web pod security defaults missing"}) || !reflect.DeepEqual(pinned, want) {
		t.Fatalf("%v %v", security, pinned)
	}
	security, pinned = Deployed([]byte(`[]`))
	if security.Level != Bad || pinned[1].Text != "DATABASE_PATH is unset" || pinned[2].Text != `replicas unset, strategy ""` {
		t.Fatalf("%v %v", security, pinned)
	}
}

func webPod(name, ready, path, deleting string) []byte {
	probe := `{"httpGet":{"path":"` + path + `","port":8080}}`
	return []byte(`{"metadata":{"name":"` + name + `"` + deleting + `},"spec":{"containers":[{"name":"main",` +
		`"readinessProbe":` + probe + `,"livenessProbe":` + probe + `,"startupProbe":` + probe + `}]},` +
		`"status":{"conditions":[{"type":"Ready","status":"` + ready + `"}]}}`)
}

// The web pod is Ready on its declared probes only when a pod not being deleted is Ready and all three probes ask
// the health path; otherwise the line names why the last pod was not.
func TestProbedReady(t *testing.T) {
	deleting := `,"deletionTimestamp":"2026-11-13T09:00:00Z"`
	for _, c := range []struct {
		pods [][]byte
		want Line
	}{
		{[][]byte{webPod("a", "False", "/healthz", ""), webPod("b", "True", "/healthz", "")}, Line{OK, "web pod Ready on its /healthz probes"}},
		{[][]byte{webPod("a", "True", "/healthz", deleting)}, Line{Bad, "web pod not Ready on its /healthz probes: no pod"}},
		{[][]byte{webPod("a", "False", "/healthz", "")}, Line{Bad, "web pod not Ready on its /healthz probes: a is not Ready"}},
		{[][]byte{webPod("a", "True", "/", "")}, Line{Bad, "web pod not Ready on its /healthz probes: a has no readiness probe on /healthz"}},
		{nil, Line{Bad, "web pod not Ready on its /healthz probes: no pod"}},
	} {
		if got := ProbedReady(c.pods, "/healthz"); got != c.want {
			t.Errorf("%v", got)
		}
	}
}

// The lines read as Python printed them: the passed line after an empty line and only when nothing failed, and the
// cleanup's line last.
func TestTextAndConclusion(t *testing.T) {
	steps := []Line{UnitReady(Worker), Unexposed(0, 0)}
	lines := append(append(append([]Line{}, steps...), Conclusion(steps)...), CleanedUp())
	want := "[OK] smoke-worker-prod unit Ready (HelmRelease installed, workload healthy)\n[OK] worker has no Service or Ingress\n" +
		"\nApp template test passed.\n[INFO] Cleaned up app-template test instances"
	if text := Text(lines); text != want || !Passes(lines) {
		t.Fatalf("%q", text)
	}
	failed := append(steps, Failed("create Kustomization app-smoke-web-prod: forbidden"))
	if Conclusion(failed) != nil || Passes(failed) || Format(failed[2]) != "[BAD] create Kustomization app-smoke-web-prod: forbidden" {
		t.Fatal("a failed run concluded")
	}
	if Leftovers(Web).Error() != "smoke-web-prod already exists; clean up first" {
		t.Fatal(Leftovers(Web))
	}
}

// The names follow render's: unit app-<app>-prod in flux-system, namespace <app>-prod, the unit's file under the
// fixture's clusters/home.
func TestNames(t *testing.T) {
	if !reflect.DeepEqual(Apps(), []string{"smoke-worker", "smoke-web"}) || Namespace(Web) != "smoke-web-prod" ||
		Unit(Worker) != "app-smoke-worker-prod" || UnitFile(Web) != "tests/fixtures/app-template/clusters/home/app-smoke-web-prod.json" {
		t.Fatal("names")
	}
}

const renderedUnit = `{"apiVersion":"kustomize.toolkit.fluxcd.io/v1","kind":"Kustomization","metadata":{"name":"app-smoke-web-prod",` +
	`"namespace":"flux-system"},"spec":{"dependsOn":[{"name":"infra-base"}],"interval":"10m","path":"./v2-apps/instances/smoke-web/prod",` +
	`"prune":true,"sourceRef":{"kind":"GitRepository","name":"swhurl-platform"},"timeout":"3m","wait":true}}`

// The rendered unit keeps every field but its path, which points into the fixture tree; anything that is not the
// app's unit with a relative path is refused.
func TestFixtureUnit(t *testing.T) {
	unit, err := FixtureUnit(Web, []byte(renderedUnit))
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(unit, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(strings.Replace(renderedUnit, `"./v2-apps/`, `"./tests/fixtures/app-template/v2-apps/`, 1)), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s", unit)
	}
	for _, refused := range []string{`[]`, `{}`, strings.Replace(renderedUnit, "app-smoke-web-prod", "app-other-prod", 1),
		strings.Replace(renderedUnit, `"./v2-apps`, `"/v2-apps`, 1), strings.Replace(renderedUnit, `"flux-system"`, `"apps"`, 1)} {
		if _, err := FixtureUnit(Web, []byte(refused)); err == nil {
			t.Errorf("accepted %s", refused)
		}
	}
}

// Decode takes both units, the Ingress's host and the release's health path, and refuses a file without them.
func TestDecode(t *testing.T) {
	worker := strings.ReplaceAll(renderedUnit, "smoke-web", "smoke-worker")
	units := map[string][]byte{Worker: []byte(worker), Web: []byte(renderedUnit)}
	ingress := []byte(`{"spec":{"rules":[{"host":"smoke-web.homelab.swhurl.com"}]}}`)
	release := []byte(`{"runtime":{"kind":"web","healthPath":"/healthz"}}`)
	rendered, err := Decode(units, ingress, release)
	if err != nil || rendered.Host != "smoke-web.homelab.swhurl.com" || rendered.HealthPath != "/healthz" || len(rendered.Units) != 2 ||
		!strings.Contains(string(rendered.Units[Worker]), `"./tests/fixtures/app-template/v2-apps/instances/smoke-worker/prod"`) {
		t.Fatalf("%+v %v", rendered, err)
	}
	for name, c := range map[string]struct {
		units            map[string][]byte
		ingress, release []byte
	}{
		"no worker unit": {map[string][]byte{Web: []byte(renderedUnit)}, ingress, release},
		"no host":        {units, []byte(`{"spec":{"rules":[]}}`), release},
		"no health path": {units, ingress, []byte(`{"runtime":{"kind":"worker","healthPath":""}}`)},
	} {
		if _, err := Decode(c.units, c.ingress, c.release); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
}

// What each read object says: the Ready condition's message, the namespace's app label, and a volume's claim
// namespace.
func TestFacts(t *testing.T) {
	if ReadyMessage([]byte(`{"status":{"conditions":[{"type":"Reconciling","message":"x"},{"type":"Ready","status":"False","message":"health check failed"}]}}`)) != "health check failed" ||
		ReadyMessage([]byte(`{}`)) != "" {
		t.Fatal("the Ready message")
	}
	if !Labelled([]byte(`{"metadata":{"labels":{"platform.swhurl.com/app":"smoke-web"}}}`), Web) ||
		Labelled([]byte(`{"metadata":{"labels":{"platform.swhurl.com/app":"smoke-web"}}}`), Worker) || Labelled([]byte(`{}`), Web) {
		t.Fatal("the label guard")
	}
	if !Claimed([]byte(`{"spec":{"claimRef":{"namespace":"smoke-web-prod"}}}`)) ||
		Claimed([]byte(`{"spec":{"claimRef":{"namespace":"demo-shop-prod"}}}`)) || Claimed([]byte(`{"spec":{}}`)) {
		t.Fatal("the volume guard")
	}
}

// The reader pod mounts the web app's claim read-only, tests the database is non-empty, and states the resources
// the namespace's quota requires.
func TestReaderDocument(t *testing.T) {
	var reader struct {
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			AutomountServiceAccountToken bool
			RestartPolicy                string
			Volumes                      []struct {
				PersistentVolumeClaim struct {
					ClaimName string
					ReadOnly  bool
				}
			}
			Containers []struct {
				Image     string
				Command   []string
				Resources struct{ Requests, Limits map[string]string }
			}
		}
	}
	if err := json.Unmarshal(ReaderDocument(), &reader); err != nil {
		t.Fatal(err)
	}
	container := reader.Spec.Containers[0]
	if reader.Metadata.Name != "reader" || reader.Metadata.Namespace != "smoke-web-prod" || reader.Spec.AutomountServiceAccountToken ||
		reader.Spec.RestartPolicy != "Never" || reader.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "smoke-web" ||
		!reader.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly || container.Image != "busybox:1.37" ||
		!reflect.DeepEqual(container.Command, []string{"test", "-s", "/data/app.db"}) ||
		container.Resources.Requests["cpu"] == "" || container.Resources.Requests["memory"] == "" || container.Resources.Limits["memory"] == "" {
		t.Fatalf("%+v", reader)
	}
}
