package policy

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

type scenario struct {
	Source, Rendered []json.RawMessage
	Platform         Platform
}

func fixtureScenario(t *testing.T) scenario {
	t.Helper()
	raw, err := os.ReadFile("testdata/chart-prod.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture scenario
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
func document(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	return object
}
func mutate(t *testing.T, documents []json.RawMessage, kind string, change func(map[string]any)) {
	t.Helper()
	for i, raw := range documents {
		object := document(t, raw)
		if object["kind"] != kind {
			continue
		}
		change(object)
		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		documents[i] = encoded
		return
	}
	t.Fatalf("fixture has no %s", kind)
}
func spec(object map[string]any) map[string]any { return object["spec"].(map[string]any) }
func podMap(object map[string]any) map[string]any {
	return spec(spec(object)["template"].(map[string]any))
}
func mainMap(object map[string]any) map[string]any {
	return podMap(object)["containers"].([]any)[0].(map[string]any)
}
func rules(t *testing.T, fixture scenario) []string {
	t.Helper()
	findings, err := Check(fixture.Source, fixture.Platform, fixture.Rendered)
	if err != nil {
		t.Fatal(err)
	}
	result := []string{}
	for _, finding := range findings {
		result = append(result, finding.Rule)
	}
	return result
}
func TestPinnedChartFixtureIsAValidContract(t *testing.T) {
	fixture := fixtureScenario(t)
	if got := rules(t, fixture); len(got) != 0 {
		t.Fatalf("unmodified real chart: %v", got)
	}
	first, _ := Check(fixture.Source, fixture.Platform, fixture.Rendered)
	second, _ := Check(fixture.Source, fixture.Platform, fixture.Rendered)
	if !slices.Equal(first, second) {
		t.Fatal("findings depend on history")
	}
}
func TestActualChartPodMutations(t *testing.T) {
	cases := []struct {
		name, rule string
		change     func(map[string]any)
	}{
		{"tag", "prod-digest", func(o map[string]any) { mainMap(o)["image"] = "example/app:1" }},
		{"latest", "image-pinned", func(o map[string]any) { mainMap(o)["image"] = "example/app:latest" }},
		{"root-uid", "non-root", func(o map[string]any) { mainMap(o)["securityContext"].(map[string]any)["runAsUser"] = 0 }},
		{"root", "non-root", func(o map[string]any) { mainMap(o)["securityContext"].(map[string]any)["runAsNonRoot"] = false }},
		{"escalate", "no-escalation", func(o map[string]any) {
			mainMap(o)["securityContext"].(map[string]any)["allowPrivilegeEscalation"] = true
		}},
		{"capabilities", "no-escalation", func(o map[string]any) {
			mainMap(o)["securityContext"].(map[string]any)["capabilities"] = map[string]any{"add": []any{"NET_ADMIN"}}
		}},
		{"resources", "resources", func(o map[string]any) { delete(mainMap(o), "resources") }},
		{"sa", "no-sa-token", func(o map[string]any) { delete(podMap(o), "automountServiceAccountToken") }},
		{"host", "no-host-access", func(o map[string]any) { podMap(o)["hostPID"] = true }},
		{"hostpath", "no-host-access", func(o map[string]any) {
			podMap(o)["volumes"] = append(podMap(o)["volumes"].([]any), map[string]any{"hostPath": map[string]any{"path": "/"}})
		}},
		{"replicas", "single-writer", func(o map[string]any) { spec(o)["replicas"] = 2 }},
		{"rolling", "single-writer", func(o map[string]any) { spec(o)["strategy"] = map[string]any{"type": "RollingUpdate"} }},
		{"tokens", "call-tokens", func(o map[string]any) { podMap(o)["volumes"] = []any{} }},
		{"spoof-backup", "call-tokens", func(o map[string]any) {
			o["metadata"].(map[string]any)["labels"] = map[string]any{"platform.swhurl.com/backup": "true"}
			podMap(o)["volumes"] = []any{}
		}},
		{"identity", "identity", func(o map[string]any) { mainMap(o)["env"] = []any{} }},
		{"data-mount", "storage", func(o map[string]any) { mainMap(o)["volumeMounts"] = []any{} }},
		{"verify-mount", "identity", func(o map[string]any) { mainMap(o)["volumeMounts"] = []any{} }},
		{"selector", "network-policy", func(o map[string]any) { spec(o)["template"].(map[string]any)["metadata"] = map[string]any{} }},
		{"quota", "quota", func(o map[string]any) { spec(o)["replicas"] = 1000 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fixtureScenario(t)
			mutate(t, fixture.Rendered, "Deployment", tc.change)
			if got := rules(t, fixture); !slices.Contains(got, tc.rule) {
				t.Fatalf("want %s, got %v", tc.rule, got)
			}
		})
	}
}
func TestInitContainerSecurityAndQuota(t *testing.T) {
	fixture := fixtureScenario(t)
	mutate(t, fixture.Rendered, "Deployment", func(o map[string]any) {
		podMap(o)["initContainers"] = []any{map[string]any{"name": "unsafe-init", "image": "example/init:latest",
			"securityContext": map[string]any{"runAsNonRoot": false}, "resources": map[string]any{
				"requests": map[string]any{"cpu": "3000m", "memory": "8192Mi"}, "limits": map[string]any{"memory": "8192Mi"}}}}
	})
	got := rules(t, fixture)
	for _, rule := range []string{"non-root", "no-escalation", "image-pinned", "prod-digest", "quota"} {
		if !slices.Contains(got, rule) {
			t.Errorf("missing %s: %v", rule, got)
		}
	}
}
func TestEnvironmentOrderingEventsAndAudienceExactness(t *testing.T) {
	for _, variable := range []string{"HOST_IP", "SWHURL_ENV", "SWHURL_EVENTS_NKEY"} {
		fixture := fixtureScenario(t)
		mutate(t, fixture.Rendered, "Deployment", func(o map[string]any) {
			env := mainMap(o)["env"].([]any)
			kept := []any{}
			for _, raw := range env {
				if raw.(map[string]any)["name"] != variable {
					kept = append(kept, raw)
				}
			}
			if variable == "SWHURL_ENV" {
				kept = append([]any{map[string]any{"name": "EARLY", "value": "$(SWHURL_ENV)"}}, kept...)
			}
			mainMap(o)["env"] = kept
		})
		want := "identity"
		if variable == "HOST_IP" {
			want = "otlp-host-ip"
		}
		if variable == "SWHURL_EVENTS_NKEY" {
			want = "events"
		}
		if got := rules(t, fixture); !slices.Contains(got, want) {
			t.Fatalf("%s: %v", variable, got)
		}
	}
	for _, field := range []string{"audience", "expirationSeconds", "path"} {
		fixture := fixtureScenario(t)
		mutate(t, fixture.Rendered, "Deployment", func(o map[string]any) {
			for _, raw := range podMap(o)["volumes"].([]any) {
				volume := raw.(map[string]any)
				projected, ok := volume["projected"].(map[string]any)
				if !ok {
					continue
				}
				token := projected["sources"].([]any)[0].(map[string]any)["serviceAccountToken"].(map[string]any)
				if field == "expirationSeconds" {
					token[field] = 3600
				} else {
					token[field] = "wrong"
				}
			}
		})
		if got := rules(t, fixture); !slices.Contains(got, "call-tokens") {
			t.Fatalf("%s: %v", field, got)
		}
	}
}
func TestStorageRoutesBackupAndEvents(t *testing.T) {
	fixture := fixtureScenario(t)
	mutate(t, fixture.Rendered, "PersistentVolumeClaim", func(o map[string]any) { spec(o)["storageClassName"] = "wrong" })
	if got := rules(t, fixture); !slices.Contains(got, "storage-class") {
		t.Fatal(got)
	}
	for _, raw := range fixture.Source {
		one := document(t, raw)
		if one["kind"] == "Stream" {
			one["metadata"].(map[string]any)["name"] = "unexpected-stream"
			extra, _ := json.Marshal(one)
			fixture.Rendered = append(fixture.Rendered, extra)
		}
	}
	if got := rules(t, fixture); !slices.Contains(got, "events") {
		t.Fatal(got)
	}
	mutate(t, fixture.Source, "CronJob", func(o map[string]any) {
		template := spec(o)["jobTemplate"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)
		spec(template)["volumes"] = append(spec(template)["volumes"].([]any), map[string]any{"name": "bad-token", "projected": map[string]any{
			"sources": []any{map[string]any{"serviceAccountToken": map[string]any{"audience": "notify", "path": "notify/token", "expirationSeconds": 600}}}}})
	})
	if got := rules(t, fixture); !slices.Contains(got, "call-tokens") {
		t.Fatal(got)
	}
}
func TestNoWorkloadMalformedAndAccumulatedDeterministicFailures(t *testing.T) {
	fixture := fixtureScenario(t)
	if got := rules(t, scenario{Source: fixture.Source, Platform: fixture.Platform}); !slices.Contains(got, "workload") {
		t.Fatal("no workload passed")
	}
	if findings, err := Check(fixture.Source, fixture.Platform, []json.RawMessage{json.RawMessage(`{"kind":"Deployment","spec":[]}`)}); findings != nil || err == nil {
		t.Fatal("malformed workload passed")
	}
	mutate(t, fixture.Rendered, "Deployment", func(o map[string]any) {
		delete(podMap(o), "automountServiceAccountToken")
		mainMap(o)["image"] = "latest"
		mainMap(o)["env"] = []any{}
	})
	first, _ := Check(fixture.Source, fixture.Platform, fixture.Rendered)
	slices.Reverse(fixture.Rendered)
	second, _ := Check(fixture.Source, fixture.Platform, fixture.Rendered)
	if !slices.Equal(first, second) || len(first) < 4 {
		t.Fatalf("not deterministic and accumulated: %v / %v", first, second)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "sensitive-sentinel") {
		t.Fatal("value in diagnostic")
	}
}

func TestRoutesTLSAndPrivateExposure(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		change     func(map[string]any)
	}{
		{"tls", "ingress-tls", func(o map[string]any) { delete(spec(o), "tls") }},
		{"middleware", "exposure", func(o map[string]any) { delete(o["metadata"].(map[string]any), "annotations") }},
		{"domain", "exposure", func(o map[string]any) { spec(o)["rules"].([]any)[0].(map[string]any)["host"] = "outside.example" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fixtureScenario(t)
			mutate(t, fixture.Source, "Ingress", tc.change)
			if got := rules(t, fixture); !slices.Contains(got, tc.rule) {
				t.Fatal(got)
			}
		})
	}
	fixture := fixtureScenario(t)
	mutate(t, fixture.Source, "Namespace", func(o map[string]any) {
		o["metadata"].(map[string]any)["labels"].(map[string]any)["platform.swhurl.com/exposure"] = "private"
	})
	if got := rules(t, fixture); !slices.Contains(got, "exposure") {
		t.Fatal(got)
	}
}
func TestRuntimeServiceProbesUIDAndEventsSeed(t *testing.T) {
	cases := []struct {
		kind, rule string
		change     func(map[string]any)
	}{
		{"Service", "runtime", func(o map[string]any) { spec(o)["ports"].([]any)[0].(map[string]any)["targetPort"] = 8888 }},
		{"Service", "exposure", func(o map[string]any) { spec(o)["type"] = "NodePort" }},
		{"Deployment", "runtime", func(o map[string]any) {
			mainMap(o)["readinessProbe"].(map[string]any)["httpGet"].(map[string]any)["path"] = "/wrong"
		}},
		{"Deployment", "runtime", func(o map[string]any) { mainMap(o)["securityContext"].(map[string]any)["runAsUser"] = 0 }},
		{"Deployment", "events", func(o map[string]any) { delete(mainMap(o), "envFrom") }},
	}
	for _, tc := range cases {
		fixture := fixtureScenario(t)
		mutate(t, fixture.Rendered, tc.kind, tc.change)
		if got := rules(t, fixture); !slices.Contains(got, tc.rule) {
			t.Fatalf("%s: %v", tc.rule, got)
		}
	}
}
func TestEffectivePodContextAndSafeInit(t *testing.T) {
	fixture := fixtureScenario(t)
	mutate(t, fixture.Rendered, "Deployment", func(o map[string]any) {
		delete(mainMap(o)["securityContext"].(map[string]any), "runAsNonRoot")
		podMap(o)["initContainers"] = []any{mainMap(o)}
	})
	if got := rules(t, fixture); len(got) != 0 {
		t.Fatalf("pod inheritance and valid init rejected: %v", got)
	}
}
func TestQuantitySemanticsAndInitMaxima(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		cpu  bool
		want float64
	}{{`"0.25"`, true, 250}, {`250`, true, 250000}, {`"1Gi"`, false, 1 << 30}, {`"1000m"`, true, 1000}, {`"1e3"`, false, 1000}} {
		got, ok := amount(json.RawMessage(tc.raw), tc.cpu)
		if !ok || got != tc.want {
			t.Fatalf("%s: %v/%v", tc.raw, got, ok)
		}
	}
	for _, raw := range []string{`"-1Gi"`, `"NaN"`, `"1e999"`, `"bad"`, `null`, `"0Mi"`} {
		if _, ok := amount(json.RawMessage(raw), false); ok {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestQuotaObjectsAndWorkloadScopedPolicy(t *testing.T) {
	for _, kind := range []string{"ResourceQuota", "NetworkPolicy"} {
		fixture := fixtureScenario(t)
		mutate(t, fixture.Source, kind, func(o map[string]any) {
			if kind == "ResourceQuota" {
				spec(o)["hard"].(map[string]any)["pods"] = "0"
			} else {
				spec(o)["podSelector"] = map[string]any{}
			}
		})
		want := "quota"
		if kind == "NetworkPolicy" {
			want = "network-policy"
		}
		if got := rules(t, fixture); !slices.Contains(got, want) {
			t.Fatal(got)
		}
	}
	fixture := fixtureScenario(t)
	fixture.Rendered = append(fixture.Rendered, json.RawMessage(`{"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"bypass"},"spec":{"podSelector":{"matchLabels":{"app":"other"}},"egress":[{}]}}`))
	if got := rules(t, fixture); !slices.Contains(got, "network-policy") {
		t.Fatal(got)
	}
}

func TestRestartableInitCountsBesideLaterInitAndApp(t *testing.T) {
	var p pod
	raw := `{"containers":[{"resources":{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"memory":"256Mi"}}}],"initContainers":[
        {"restartPolicy":"Always","resources":{"requests":{"cpu":"200m","memory":"64Mi"},"limits":{"memory":"128Mi"}}},
        {"resources":{"requests":{"cpu":"400m","memory":"512Mi"},"limits":{"memory":"768Mi"}}}]}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	got := podCapacity(p)
	want := capacity{cpu: 600, memory: 576 * (1 << 20), limit: 896 * (1 << 20)}
	if got != want {
		t.Fatalf("restartable init quota: %v, want %v", got, want)
	}
}

func TestOverlappingCronJobCannotClaimBoundedQuota(t *testing.T) {
	fixture := fixtureScenario(t)
	for _, raw := range fixture.Source {
		one := document(t, raw)
		if one["kind"] != "CronJob" {
			continue
		}
		one["metadata"].(map[string]any)["name"] = "extra-job"
		spec(one)["concurrencyPolicy"] = "Allow"
		extra, err := json.Marshal(one)
		if err != nil {
			t.Fatal(err)
		}
		fixture.Rendered = append(fixture.Rendered, extra)
	}
	if got := rules(t, fixture); !slices.Contains(got, "quota") {
		t.Fatalf("unbounded CronJob quota passed: %v", got)
	}
}
