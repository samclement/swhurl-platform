package observe_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// compiledIngress is the route the compiler renders for an exposed instance: one host and its TLS secret.
func compiledIngress(t *testing.T) []byte {
	t.Helper()
	return encode(t, map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
		"metadata": map[string]any{"name": "hello", "namespace": "hello-staging"},
		"spec": map[string]any{"rules": []any{map[string]any{"host": "staging-hello.homelab.swhurl.com"}},
			"tls": []any{map[string]any{"hosts": []any{"staging-hello.homelab.swhurl.com"}, "secretName": "hello-tls"}}}})
}

// routed is the target with the route the compiled Ingress describes.
func routed(t *testing.T) observe.Target {
	t.Helper()
	route, err := observe.RouteTargetOf(compiledIngress(t))
	if err != nil {
		t.Fatal(err)
	}
	target := target()
	target.Route = &route
	return target
}

// certificateDocument is a Certificate with a Ready condition about the generation it observed.
func certificateDocument(t *testing.T, status string, observed int64) []byte {
	t.Helper()
	return encode(t, map[string]any{"metadata": map[string]any{"generation": 1},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": status,
			"reason": "Ready", "observedGeneration": observed}}}})
}

// The route is the compiled Ingress and the certificate is named after its TLS secret, as cert-manager names it.
func TestRouteTargetOfTheCompiledIngress(t *testing.T) {
	route, err := observe.RouteTargetOf(compiledIngress(t))
	if err != nil {
		t.Fatal(err)
	}
	want := observe.Ref{APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: "hello-staging", Name: "hello"}
	if route.Ingress != want {
		t.Fatalf("ingress %+v", route.Ingress)
	}
	certificate := observe.Ref{APIVersion: "cert-manager.io/v1", Kind: "Certificate", Namespace: "hello-staging", Name: "hello-tls"}
	if len(route.Certificates) != 1 || route.Certificates[0] != certificate {
		t.Fatalf("certificates %+v", route.Certificates)
	}
	if _, err := observe.RouteTargetOf(encode(t, map[string]any{"apiVersion": "v1", "kind": "Service"})); err == nil ||
		!strings.Contains(err.Error(), "route: expected_an_ingress") {
		t.Fatalf("another kind: %v", err)
	}
}

// Each answer about the route keeps its meaning: a present Ingress gives its hosts, an absent certificate is
// absent, a failed read is unavailable rather than absent, a read not made is not assessed, and a condition about
// an older generation is not readiness. None of it changes the state.
func TestRoutesAndCertificates(t *testing.T) {
	target := routed(t)
	ingress, certificate := target.Route.Ingress, target.Route.Certificates[0]
	routedEvidence := func(reads ...observe.Read) observe.Evidence {
		evidence := healthy(t)
		for _, read := range reads {
			evidence = withRead(evidence, read)
		}
		return evidence
	}
	for _, tt := range []struct {
		name             string
		evidence         observe.Evidence
		route, tls       string
		routeWhy, tlsWhy string
		hosts            []string
	}{
		{"both ready", routedEvidence(present(ingress, compiledIngress(t)), present(certificate, certificateDocument(t, "True", 1))),
			observe.Present, observe.Ready, "", "", []string{"staging-hello.homelab.swhurl.com"}},
		{"certificate absent", routedEvidence(present(ingress, compiledIngress(t)), absent(certificate)),
			observe.Present, observe.Absent, "", observe.ReasonNotFound, []string{"staging-hello.homelab.swhurl.com"}},
		{"reads failed", routedEvidence(unavailable(ingress, observe.ReasonForbidden), unavailable(certificate, observe.ReasonTimedOut)),
			observe.Unavailable, observe.Unavailable, observe.ReasonForbidden, observe.ReasonTimedOut, []string{}},
		{"not read", healthy(t), observe.NotAssessed, observe.NotAssessed, observe.NotRead, observe.NotRead, []string{}},
		{"stale certificate", routedEvidence(absent(ingress), present(certificate, certificateDocument(t, "True", 0))),
			observe.Absent, observe.NotReady, observe.ReasonNotFound, observe.ReasonStale, []string{}},
		{"certificate not ready", routedEvidence(present(ingress, compiledIngress(t)), present(certificate, certificateDocument(t, "False", 1))),
			observe.Present, observe.NotReady, "", observe.ReasonNotTrue, []string{"staging-hello.homelab.swhurl.com"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report, err := observe.Explain(target, tt.evidence)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Routes) != 1 || len(report.Certificates) != 1 {
				t.Fatalf("routes %+v, certificates %+v", report.Routes, report.Certificates)
			}
			route, tls := report.Routes[0], report.Certificates[0]
			if route.Name != "hello" || route.Outcome != tt.route || route.Reason != tt.routeWhy || strings.Join(route.Hosts, ",") != strings.Join(tt.hosts, ",") {
				t.Errorf("route %+v", route)
			}
			if tls.Name != "hello-tls" || tls.Outcome != tt.tls || tls.Reason != tt.tlsWhy {
				t.Errorf("certificate %+v", tls)
			}
			if report.State.Name != observe.StateHealthy {
				t.Errorf("the route changed the state: %+v", report.State)
			}
		})
	}
}

// An instance the records give no route reads none, and reports empty lists rather than a gap; evidence about a
// route it did not expect is refused.
func TestNoRouteExpected(t *testing.T) {
	report := explain(t, healthy(t))
	if report.Routes == nil || report.Certificates == nil || len(report.Routes)+len(report.Certificates) != 0 {
		t.Fatalf("routes %v, certificates %v", report.Routes, report.Certificates)
	}
	stray := routed(t).Route.Ingress
	if _, err := observe.Explain(target(), withRead(healthy(t), absent(stray))); !refusal(err, "evidence.objects", "unexpected_ref") {
		t.Fatalf("a route the target did not ask for: %v", err)
	}
}

// A container that is not ready with a waiting or terminated reason is a problem: its pod, container and reason,
// and the first 160 characters of its message. A ready container and one not ready without a reason are not.
func TestProblems(t *testing.T) {
	long := strings.Repeat("é", 200)
	pod := encode(t, map[string]any{
		"metadata": map[string]any{"name": "hello-main-1"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "main", "image": "docker.io/x@" + digest}}},
		"status": map[string]any{"containerStatuses": []any{
			map[string]any{"name": "main", "ready": false, "imageID": "docker-pullable://docker.io/x@" + digest,
				"state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": long}}},
			map[string]any{"name": "sidecar", "ready": false, "state": map[string]any{"terminated": map[string]any{"reason": "OOMKilled"}}},
			map[string]any{"name": "proxy", "ready": false, "state": map[string]any{"running": map[string]any{}}},
			map[string]any{"name": "helper", "ready": true, "state": map[string]any{"running": map[string]any{}}},
		}},
	})
	report := explain(t, withPods(healthy(t), podList(t, target(), pod)))
	want := []observe.Problem{
		{Pod: "hello-main-1", Container: "main", Reason: "CrashLoopBackOff", Message: strings.Repeat("é", 160)},
		{Pod: "hello-main-1", Container: "sidecar", Reason: "OOMKilled"},
	}
	if len(report.Problems) != len(want) {
		t.Fatalf("problems %+v", report.Problems)
	}
	for i := range want {
		if report.Problems[i] != want[i] {
			t.Errorf("problem %d is %+v, want %+v", i, report.Problems[i], want[i])
		}
	}
	if healthyReport := explain(t, healthy(t)); healthyReport.Problems == nil || len(healthyReport.Problems) != 0 {
		t.Fatalf("a healthy pod's problems: %v", healthyReport.Problems)
	}
}

// Version 3 carries the state, the problems, the routes, the certificates and what the route target read.
func TestEncodeReportCarriesTheStatusFacts(t *testing.T) {
	target := routed(t)
	evidence := withRead(withRead(healthy(t), present(target.Route.Ingress, compiledIngress(t))),
		present(target.Route.Certificates[0], certificateDocument(t, "True", 1)))
	evidence = withPods(evidence, podList(t, target, containerPod(t, false,
		map[string]any{"waiting": map[string]any{"reason": "ImagePullBackOff", "message": "not found"}})))
	report, err := observe.Explain(target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	data, err := observe.EncodeReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Version int `json:"version"`
		State   struct {
			Name, Field, Reason string
		} `json:"state"`
		Problems     []map[string]string `json:"problems"`
		Routes       []map[string]any    `json:"routes"`
		Certificates []map[string]string `json:"certificates"`
		Target       struct {
			Route struct {
				Ingress      map[string]string   `json:"ingress"`
				Certificates []map[string]string `json:"certificates"`
			} `json:"route"`
		} `json:"target"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != 5 || document.State.Name != observe.StateFailing || document.State.Field != "containers" ||
		document.State.Reason != observe.ReasonContainers {
		t.Fatalf("version %d, state %+v", document.Version, document.State)
	}
	if len(document.Problems) != 1 || document.Problems[0]["reason"] != "ImagePullBackOff" || document.Problems[0]["container"] != "main" ||
		document.Problems[0]["message"] != "not found" {
		t.Fatalf("problems %v", document.Problems)
	}
	if len(document.Routes) != 1 || document.Routes[0]["outcome"] != observe.Present || len(document.Routes[0]["hosts"].([]any)) != 1 {
		t.Fatalf("routes %v", document.Routes)
	}
	if len(document.Certificates) != 1 || document.Certificates[0]["outcome"] != observe.Ready || document.Certificates[0]["name"] != "hello-tls" {
		t.Fatalf("certificates %v", document.Certificates)
	}
	if document.Target.Route.Ingress["kind"] != "Ingress" || len(document.Target.Route.Certificates) != 1 ||
		document.Target.Route.Certificates[0]["apiVersion"] != "cert-manager.io/v1" {
		t.Fatalf("target route %+v", document.Target.Route)
	}
}
