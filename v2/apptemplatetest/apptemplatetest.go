// Package apptemplatetest decides live-test-app-template: the fixed names it touches, the rendered units it creates
// and the reader pod's document, what each object it reads says, and the verdict on each check. It reads the
// documents it is given and has no effects; the messages are the operator's, in the words make
// live-test-app-template has always used.
package apptemplatetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Plan is what the test does, printed by a dry run.
const Plan = `Deploy two throwaway instances v2 renders from the fixture records
(tests/fixtures/app-template, reconciled from the pushed Git revision) through
real Flux units, check them, and remove them:
  smoke-worker-prod  worker: runs, has no Service or Ingress
  smoke-web-prod     authenticated web app with SQLite, digest-pinned: route
                     redirects to sign-in, claim on local-path-retain, database
                     written, DATABASE_PATH set, one replica, stop-first updates,
                     Ready on its declared health probes
Touches only the app-smoke-*-prod units, their namespaces and the web app's volume.`

// Passed is the line a run with no failed check prints before its cleanup.
const Passed = "App template test passed."

// The fixture and the objects the test creates and reads: one unit per app in FluxNamespace, and in the web app's
// namespace its workload, its claim and the short-lived pod that reads the claim.
const (
	FluxNamespace = "flux-system"
	Fixture       = "tests/fixtures/app-template"
	Environment   = "prod"
	Worker        = "smoke-worker"
	Web           = "smoke-web"
	// AppLabel is the label render gives an instance's namespace, naming its app: the cleanup's guard.
	AppLabel = "platform.swhurl.com/app"
	// Workload and Claim are named after the app: the chart names the Deployment after its release, and render
	// names the claim after the app.
	Workload     = Web
	Claim        = Web
	Container    = "main"
	PodSelector  = "app.kubernetes.io/name=" + Web + ",app.kubernetes.io/controller=" + Container
	StorageClass = "local-path-retain"
	ReaderPod    = "reader"
	Database     = "/data/app.db"
	SignIn       = "https://accounts.google.com/"
)

// The rendered files the test reads, under the fixture: each app's unit, the web app's Ingress (its host) and its
// release record (the health path it declares).
const (
	IngressFile = Fixture + "/v2-apps/instances/" + Web + "/" + Environment + "/ingress.json"
	ReleaseFile = Fixture + "/records/" + Web + "/" + Environment + "/release.json"
)

// Apps is the two apps, in the order they are installed and checked.
func Apps() []string { return []string{Worker, Web} }

// Namespace is an app's instance namespace.
func Namespace(app string) string { return app + "-" + Environment }

// Unit is an app's Flux unit.
func Unit(app string) string { return "app-" + app + "-" + Environment }

// UnitFile is where render wrote an app's unit, under the fixture.
func UnitFile(app string) string { return Fixture + "/clusters/home/" + Unit(app) + ".json" }

// Level is how a line is shown.
type Level string

const (
	OK   Level = "ok"
	Bad  Level = "bad"
	Info Level = "info"
	// Done is the passed line, shown before the cleanup's.
	Done Level = "done"
)

// Line is one line of the report.
type Line struct {
	Level Level
	Text  string
}

// Preflight is why the test cannot start: leftovers from an earlier run, or a cluster it cannot read.
type Preflight struct{ Reason string }

func (p *Preflight) Error() string { return p.Reason }

// Leftovers is the refusal when an app's namespace or unit already exists.
func Leftovers(app string) error {
	return &Preflight{Reason: Namespace(app) + " already exists; clean up first"}
}

// Rendered is what the test takes from the render: each app's unit pointed into the fixture, the web app's host
// and the health path its release declares.
type Rendered struct {
	Units      map[string][]byte
	Host       string
	HealthPath string
}

// Decode reads the rendered files: each app's unit, the web app's Ingress and its release record.
func Decode(units map[string][]byte, ingress, release []byte) (Rendered, error) {
	rendered := Rendered{Units: map[string][]byte{}}
	for _, app := range Apps() {
		unit, err := FixtureUnit(app, units[app])
		if err != nil {
			return Rendered{}, err
		}
		rendered.Units[app] = unit
	}
	var route struct {
		Spec struct {
			Rules []struct {
				Host string `json:"host"`
			} `json:"rules"`
		} `json:"spec"`
	}
	if json.Unmarshal(ingress, &route) != nil || len(route.Spec.Rules) == 0 || route.Spec.Rules[0].Host == "" {
		return Rendered{}, errors.New(IngressFile + ": expected an Ingress with a host")
	}
	var declared struct {
		Runtime struct {
			HealthPath string `json:"healthPath"`
		} `json:"runtime"`
	}
	if json.Unmarshal(release, &declared) != nil || !strings.HasPrefix(declared.Runtime.HealthPath, "/") {
		return Rendered{}, errors.New(ReleaseFile + ": expected a release with a health path")
	}
	rendered.Host, rendered.HealthPath = route.Spec.Rules[0].Host, declared.Runtime.HealthPath
	return rendered, nil
}

// FixtureUnit is an app's rendered unit with only its path rewritten into the fixture: Flux applies the fixture's
// instance folder from Git rather than the platform's own.
func FixtureUnit(app string, document []byte) ([]byte, error) {
	var unit map[string]any
	if err := json.Unmarshal(document, &unit); err != nil || unit == nil {
		return nil, fmt.Errorf("%s: expected a Kustomization", UnitFile(app))
	}
	metadata, _ := unit["metadata"].(map[string]any)
	spec, _ := unit["spec"].(map[string]any)
	path, _ := spec["path"].(string)
	if unit["kind"] != "Kustomization" || metadata["name"] != Unit(app) || metadata["namespace"] != FluxNamespace ||
		!strings.HasPrefix(path, "./") {
		return nil, fmt.Errorf("%s: expected the Kustomization %s in %s with a relative path", UnitFile(app), Unit(app),
			FluxNamespace)
	}
	spec["path"] = "./" + Fixture + "/" + strings.TrimPrefix(path, "./")
	return json.Marshal(unit)
}

// ReaderDocument is the pod that checks the web app's database is on its claim and exits: it mounts the claim
// read-only with no service-account token, runs as the app's user, is never restarted, and states the resources
// the namespace's quota requires of every pod.
func ReaderDocument() []byte {
	encoded, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": ReaderPod, "namespace": Namespace(Web)},
		"spec": map[string]any{
			"automountServiceAccountToken": false,
			"restartPolicy":                "Never",
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532,
				"seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"volumes": []any{map[string]any{"name": "d", "persistentVolumeClaim": map[string]any{"claimName": Claim, "readOnly": true}}},
			"containers": []any{map[string]any{"name": ReaderPod, "image": "busybox:1.37", "command": []string{"test", "-s", Database},
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
					"capabilities": map[string]any{"drop": []string{"ALL"}}},
				"resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "16Mi"},
					"limits": map[string]any{"memory": "16Mi"}},
				"volumeMounts": []any{map[string]any{"name": "d", "mountPath": "/data", "readOnly": true}}}},
		},
	})
	if err != nil {
		return nil // a fixed shape that always encodes
	}
	return encoded
}

// ReclaimDelete is the patch that lets the provisioner delete a volume and its directory once its claim is gone.
const ReclaimDelete = `{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`

// object is what the test reads of a unit, a namespace, a claim or a volume.
type object struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		StorageClassName string `json:"storageClassName"`
		ClaimRef         *struct {
			Namespace string `json:"namespace"`
		} `json:"claimRef"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

func decode(document []byte) (object, bool) {
	var read object
	return read, json.Unmarshal(document, &read) == nil
}

// ReadyMessage is what a unit's Ready condition says: why it is not Ready yet.
func ReadyMessage(unit []byte) string {
	read, _ := decode(unit)
	for _, condition := range read.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Message
		}
	}
	return ""
}

// Labelled is whether the namespace carries render's label naming this app: the only namespaces cleanup deletes.
func Labelled(namespace []byte, app string) bool {
	read, ok := decode(namespace)
	return ok && read.Metadata.Labels[AppLabel] == app
}

// Claimed is whether a volume's claim lived in the web app's namespace: the only volume cleanup may delete.
func Claimed(volume []byte) bool {
	read, ok := decode(volume)
	return ok && read.Spec.ClaimRef != nil && read.Spec.ClaimRef.Namespace == Namespace(Web)
}

// check is one verdict: ok when it passed, bad otherwise.
func check(passed bool, ok, bad string) Line {
	if passed {
		return Line{OK, ok}
	}
	return Line{Bad, bad}
}

// UnitReady is the install's verdict on one app's unit.
func UnitReady(app string) Line {
	return Line{OK, Namespace(app) + " unit Ready (HelmRelease installed, workload healthy)"}
}

// Unexposed is the verdict on the worker's namespace: no Service and no Ingress.
func Unexposed(services, ingresses int) Line {
	return check(services == 0 && ingresses == 0, "worker has no Service or Ingress", "worker exposes a Service or Ingress")
}

// SignInRedirect is whether the route's answer is the sign-in redirect: a 302 to Google.
func SignInRedirect(status int, location string) bool {
	return status == 302 && strings.HasPrefix(location, SignIn)
}

// Answer is a route's answer as the failed check names it: the status and where it redirects.
func Answer(status int, location string) string {
	return fmt.Sprintf("%d %s", status, location)
}

// Protected is the verdict on the web app's route, with its last answer when it was not the sign-in redirect.
func Protected(host string, redirected bool, last string) Line {
	return check(redirected, "https://"+host+" redirects to Google sign-in", "route not protected by sign-in: "+last)
}

// ClaimBound is the verdict on the web app's claim: Bound, on the retained storage class.
func ClaimBound(claim []byte) Line {
	read, ok := decode(claim)
	return check(ok && read.Spec.StorageClassName == StorageClass && read.Status.Phase == "Bound",
		"claim bound on "+StorageClass, "claim not bound on "+StorageClass)
}

// Written is the verdict on the database file the reader pod tested, from the pod's final phase.
func Written(phase string) Line {
	return check(phase == "Succeeded", "persistent volume written ("+Database+")",
		"no data written ("+Database+" missing or empty)")
}

// Finished is whether a pod that is never restarted has exited: Succeeded or Failed.
func Finished(phase string) bool { return phase == "Succeeded" || phase == "Failed" }

// Failed is a step that could not go on: what failed, in v2's words. The steps after it do not run.
func Failed(reason string) Line { return Line{Bad, reason} }

// CleanedUp is the cleanup's one line, printed whatever the steps found.
func CleanedUp() Line { return Line{Info, "Cleaned up app-template test instances"} }

// Conclusion is the passed line when no line is a failed check, and nothing otherwise.
func Conclusion(lines []Line) []Line {
	if !Passes(lines) {
		return nil
	}
	return []Line{{Done, Passed}}
}

// Passes is whether no line is a failed check.
func Passes(lines []Line) bool {
	return !slices.ContainsFunc(lines, func(line Line) bool { return line.Level == Bad })
}

// Format is one line as the operator reads it: the passed line follows an empty line.
func Format(line Line) string {
	if line.Level == Done {
		return "\n" + line.Text
	}
	return fmt.Sprintf("[%s] %s", strings.ToUpper(string(line.Level)), line.Text)
}

// Text is the lines as the operator reads them, one per line.
func Text(lines []Line) string {
	text := make([]string, 0, len(lines))
	for _, line := range lines {
		text = append(text, Format(line))
	}
	return strings.Join(text, "\n")
}
