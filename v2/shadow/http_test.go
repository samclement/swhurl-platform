package shadow_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/shadow"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func choices() shadow.ResourceChoices {
	return shadow.ResourceChoices{CPURequestMilli: 10, MemoryMiB: 128}
}
func compare(t *testing.T, a, b []byte) shadow.HTTPComparison {
	t.Helper()
	got, err := shadow.CompareHTTP(a, b, choices())
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func finding(t *testing.T, c shadow.HTTPComparison, field string) shadow.Finding {
	t.Helper()
	for _, f := range c.Findings {
		if f.Field == field {
			return f
		}
	}
	t.Fatalf("missing %s", field)
	return shadow.Finding{}
}
func want(t *testing.T, c shadow.HTTPComparison, field, outcome, reason string) {
	t.Helper()
	f := finding(t, c, field)
	if f.Outcome != outcome || (reason != "" && f.Reason != reason) {
		t.Fatalf("%s: got %+v, want %s/%s", field, f, outcome, reason)
	}
}
func change(t *testing.T, b []byte, path string, value any, remove bool) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	keys := strings.Split(path, ".")
	at := doc
	for _, key := range keys[:len(keys)-1] {
		at = at[key].(map[string]any)
	}
	if remove {
		delete(at, keys[len(keys)-1])
	} else {
		at[keys[len(keys)-1]] = value
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const container = "spec.values.controllers.main.containers.main."
const controller = "spec.values.controllers.main."

// Composition is caller-owned. There are no source/build/platform claims or release IDs here.
func rendered(t *testing.T) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixture(t, "legacy-http"), &doc); err != nil {
		t.Fatal(err)
	}
	image := doc["spec"].(map[string]any)["values"].(map[string]any)["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)["image"].(map[string]any)
	b, err := os.ReadFile("../testdata/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := kernel.DecodeProfile(b)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := kernel.RenderHTTP(kernel.HTTPInstance{Application: "hello", Environment: "staging", Configuration: kernel.Configuration{Replicas: 1, MemoryMiB: 128}}, kernel.HTTPWorkload{Image: image["repository"].(string), Digest: image["digest"].(string), Port: 8080, HealthPath: "/", UID: 101}, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range resources.Files {
		if strings.HasSuffix(f.Path, "/helmrelease.json") {
			return f.Document
		}
	}
	t.Fatal("renderer returned no HelmRelease")
	return nil
}

func TestActualRenderCompositionAndExplicitScope(t *testing.T) {
	before, after := fixture(t, "legacy-http"), rendered(t)
	got := compare(t, before, after)
	expectedOrder := []string{"metadata", "chart", "image", "service.http", "probes", "replicas", "env.identity", "pod.securityContext", "pod.automountServiceAccountToken", "container.securityContext", "resources.cpuRequest", "resources.memory"}
	if !slices.Equal(got.AssessedScope, expectedOrder) {
		t.Fatalf("scope schema order: %v", got.AssessedScope)
	}
	if len(got.Findings) != 12 || len(got.AssessedScope) != 12 {
		t.Fatalf("incomplete findings: %+v", got)
	}
	for _, f := range got.Findings {
		expected := "equal"
		if f.Field == "image" || f.Field == "resources.memory" {
			expected = "intentional"
		}
		want(t, got, f.Field, expected, "")
	}
	want(t, got, "image", "intentional", "tag_beside_digest")
	want(t, got, "resources.memory", "intentional", "chosen_single_memory")
	for _, scope := range []string{"release.source_type_platform", "storage", "routes", "additional_env_config_mounts_containers", "foreignOwnership", "quota", "networkPolicy", "flux.units_registrations"} {
		if !slices.Contains(got.UnassessedScope, scope) {
			t.Errorf("omission claimed parity: %s", scope)
		}
	}
	// Storage, routes and arbitrary config may differ without being promoted into parity evidence.
	excluded := change(t, before, "spec.values.persistence", map[string]any{"data": map[string]any{"type": "persistentVolumeClaim"}}, false)
	excluded = change(t, excluded, "spec.values.ingress", map[string]any{}, false)
	excluded = change(t, excluded, container+"env.SWHURL_AUTH_KEYS", "do-not-echo", false)
	if !reflect.DeepEqual(got, compare(t, excluded, after)) {
		t.Fatal("excluded fields entered selected comparison")
	}
	output, _ := json.Marshal(got)
	if bytes.Contains(output, []byte("do-not-echo")) || bytes.Contains(output, []byte("nginx")) {
		t.Fatal("result leaked arbitrary input values")
	}
}

func TestPartialComparisonWithIncompleteEvidence(t *testing.T) {
	got := compare(t, fixture(t, "legacy-incomplete"), rendered(t))
	want(t, got, "image", "not_assessed", "missing_digest")
	want(t, got, "replicas", "not_assessed", "unresolved_replicas")
	want(t, got, "probes", "equal", "")
	want(t, got, "resources.memory", "intentional", "")
	if !slices.Contains(got.UnassessedScope, "storage") || slices.Contains(got.AssessedScope, "image") {
		t.Fatal("partial scope hidden")
	}
}

func TestRenderedMutationsChangeFindings(t *testing.T) {
	before, after := fixture(t, "legacy-http"), rendered(t)
	for _, tt := range []struct {
		name, path, field string
		value             any
		remove            bool
	}{
		{"image repository", container + "image.repository", "image", "docker.io/another/image", false},
		{"image digest", container + "image.digest", "image", "sha256:" + strings.Repeat("b", 64), false},
		{"probe path", container + "probes.readiness.spec.httpGet.path", "probes", "/wrong", false},
		{"startup setting", container + "probes.startup.spec.failureThreshold", "probes", 25, false},
		{"security setting", container + "securityContext.readOnlyRootFilesystem", "container.securityContext", false, false},
		{"extra security field", container + "securityContext.privileged", "container.securityContext", true, false},
		{"unknown security field", container + "securityContext.futureSecuritySetting", "container.securityContext", map[string]any{"enabled": true}, false},
		{"nested extra security field", container + "securityContext.capabilities.add", "container.securityContext", []any{"NET_ADMIN"}, false},
		{"missing security", container + "securityContext", "container.securityContext", nil, true},
		{"pod uid", "spec.values.defaultPodOptions.securityContext.runAsUser", "pod.securityContext", 102, false},
		{"extra pod security field", "spec.values.defaultPodOptions.securityContext.supplementalGroups", "pod.securityContext", []any{202}, false},
		{"automount", "spec.values.defaultPodOptions.automountServiceAccountToken", "pod.automountServiceAccountToken", true, false},
		{"replicas zero", controller + "replicas", "replicas", 0, false},
		{"service port", "spec.values.service.main.ports.http.port", "service.http", 9090, false},
		{"env identity", container + "env.SWHURL_APP", "env.identity", "other", false},
		{"metadata", "metadata.namespace", "metadata", "other", false},
		{"chart source", "spec.chart.spec.sourceRef.name", "chart", "other", false},
		{"extra chart source field", "spec.chart.spec.sourceRef.extra", "chart", "other", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mutant := change(t, after, tt.path, tt.value, tt.remove)
			got := compare(t, before, mutant)
			want(t, got, tt.field, "different", "")
		})
	}
	zero := change(t, after, controller+"replicas", 0, false)
	want(t, compare(t, zero, zero), "replicas", "equal", "")
}

func TestExactResourceChoicesAndQuantities(t *testing.T) {
	base := rendered(t)
	for _, tt := range []struct {
		name, path             string
		value                  any
		field, outcome, reason string
	}{
		{"whole cpu", container + "resources.requests.cpu", "1", "resources.cpuRequest", "different", "unexplained_cpu_request"},
		{"unchosen cpu", container + "resources.requests.cpu", "11m", "resources.cpuRequest", "different", "unexplained_cpu_request"},
		{"float cpu", container + "resources.requests.cpu", "0.01", "resources.cpuRequest", "not_assessed", "unsupported_quantity"},
		{"memory float", container + "resources.requests.memory", "0.125Gi", "resources.memory", "not_assessed", "unsupported_quantity"},
		{"other memory spelling", container + "resources.requests.memory", "128M", "resources.memory", "not_assessed", "unsupported_quantity"},
		{"changed limit", container + "resources.limits.memory", "256Mi", "resources.memory", "different", "unexplained_memory"},
		{"changed request", container + "resources.requests.memory", "64Mi", "resources.memory", "different", "unexplained_memory"},
		{"overflow", container + "resources.requests.cpu", "9223372036854775808m", "resources.cpuRequest", "not_assessed", "unsupported_quantity"},
		{"multiply overflow", container + "resources.requests.cpu", "9223372036854775807", "resources.cpuRequest", "not_assessed", "unsupported_quantity"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := compare(t, base, change(t, base, tt.path, tt.value, false))
			want(t, got, tt.field, tt.outcome, tt.reason)
		})
	}
	a := change(t, base, container+"resources.requests.cpu", "1", false)
	b := change(t, base, container+"resources.requests.cpu", "1000m", false)
	want(t, compare(t, a, b), "resources.cpuRequest", "equal", "same_quantity")
	a = change(t, base, container+"resources.requests.memory", "1Gi", false)
	a = change(t, a, container+"resources.limits.memory", "1Gi", false)
	b = change(t, base, container+"resources.requests.memory", "1024Mi", false)
	b = change(t, b, container+"resources.limits.memory", "1024Mi", false)
	want(t, compare(t, a, b), "resources.memory", "equal", "same_quantity")
	// A CPU difference is intentional only when the new request equals the explicit choice.
	old := change(t, base, container+"resources.requests.cpu", "5m", false)
	want(t, compare(t, old, base), "resources.cpuRequest", "intentional", "chosen_cpu_request")
	// Neither a changed prior limit nor a prior request above it is explained by the memory choice.
	for _, path := range []string{"resources.limits.memory", "resources.requests.memory"} {
		old := change(t, fixture(t, "legacy-http"), container+path, "256Mi", false)
		want(t, compare(t, old, base), "resources.memory", "different", "unexplained_memory")
	}
	// Direction matters: request consolidation is not a blanket memory ignore.
	want(t, compare(t, base, fixture(t, "legacy-http")), "resources.memory", "different", "")
}

func TestUnresolvedFactsAndOtherShapes(t *testing.T) {
	base := rendered(t)
	for _, tt := range []struct {
		path          string
		value         any
		remove        bool
		field, reason string
	}{
		{container + "image.digest", nil, true, "image", "missing_digest"},
		{controller + "replicas", nil, true, "replicas", "unresolved_replicas"},
		{controller + "replicas", "{{ .Values.replicas }}", false, "replicas", "unresolved_replicas"},
	} {
		got := compare(t, base, change(t, base, tt.path, tt.value, tt.remove))
		want(t, got, tt.field, "not_assessed", tt.reason)
	}
	for _, tt := range []struct {
		path  string
		value any
	}{
		{"spec.chart.spec.version", "5.2.2"}, {"spec.chart.spec.chart", "other"},
		{controller + "type", "cronjob"},
		{"spec.values.controllers.extra", map[string]any{}},
		{controller + "containers.sidecar", map[string]any{}},
	} {
		got := compare(t, base, change(t, base, tt.path, tt.value, false))
		want(t, got, "probes", "not_assessed", "outside_http_shape")
		if len(got.AssessedScope) != 2 {
			t.Fatalf("other shapes claimed coverage: %+v", got)
		}
	}
	got := compare(t, base, change(t, base, "spec.chart.spec.version", "5.2.2", false))
	want(t, got, "chart", "different", "")
}

func TestAmbiguousJSONAndWrongTypesFailSafely(t *testing.T) {
	base := rendered(t)
	bad := [][]byte{[]byte("null"), []byte("[]"), []byte("{}"), append(bytes.Clone(base), []byte(" {}")...), []byte("not-json"), []byte("{\"x\":\"\xff\"}"), []byte(`{"do-not-echo":1,"do-not-echo":2}`)}
	bad = append(bad, bytes.Replace(base, []byte(`"replicas":1`), []byte(`"replicas":1,"\u0072eplicas":2`), 1))
	for _, escape := range []string{`"\ud800"`, `"\udfff"`, `"\ud800\u0061"`} {
		bad = append(bad, bytes.Replace(base, []byte(`"SWHURL_APP":"hello"`), []byte(`"SWHURL_APP":`+escape), 1))
	}
	bad = append(bad, bytes.Replace(base, []byte(`"replicas":1`), []byte(`"replicas":1,"extra":{"x":1,"x":2}`), 1))
	for _, tt := range []struct {
		path  string
		value any
	}{
		{"spec", []any{}}, {"metadata.name", 5}, {"spec.values.controllers.main", false},
		{controller + "replicas", true}, {controller + "replicas", 1.5}, {controller + "replicas", -1}, {controller + "replicas", int64(1) << 32}, {controller + "replicas", nil},
		{container + "image.digest", false}, {container + "image.digest", nil}, {container + "resources.requests.cpu", 10},
		{container + "securityContext", []any{}}, {container + "securityContext.privileged", "do-not-echo"},
		{container + "securityContext.capabilities.drop", "do-not-echo"},
		{container + "securityContext.seccompProfile", "do-not-echo"},
		{"spec.values.defaultPodOptions.securityContext.seccompProfile.type", false},
		{"spec.values.defaultPodOptions.automountServiceAccountToken", "do-not-echo"},
		{"spec.values", nil}, {controller + "pod", "do-not-echo"},
		{container + "env.SWHURL_APP", map[string]any{"valueFrom": "do-not-echo"}},
	} {
		bad = append(bad, change(t, base, tt.path, tt.value, false))
	}
	for i, input := range bad {
		for _, reverse := range []bool{false, true} {
			a, b := input, base
			if reverse {
				a, b = b, a
			}
			got, err := shadow.CompareHTTP(a, b, choices())
			var safe *shadow.Error
			if !errors.As(err, &safe) || !reflect.DeepEqual(got, shadow.HTTPComparison{}) {
				t.Fatalf("case %d reversed=%v: non-error or partial success: %+v %v", i, reverse, got, err)
			}
			if strings.Contains(err.Error(), "do-not-echo") {
				t.Fatal("unsafe diagnostic")
			}
		}
	}
	for _, c := range []shadow.ResourceChoices{{}, {CPURequestMilli: -1, MemoryMiB: 128}, {CPURequestMilli: 10, MemoryMiB: 0}} {
		got, err := shadow.CompareHTTP(base, base, c)
		if err == nil || !reflect.DeepEqual(got, shadow.HTTPComparison{}) {
			t.Fatal("invalid choices accepted")
		}
	}
}

func TestOverridesDisabledControllersAndUnmodelledSpellings(t *testing.T) {
	base := rendered(t)
	// app-template merges controllers.<name>.pod over defaultPodOptions, so the selected pod
	// subtrees stop being the effective ones: an override must never read as parity.
	for _, mutation := range []struct {
		name, path string
		value      any
	}{
		{"controller pod override", controller + "pod", map[string]any{"securityContext": map[string]any{"runAsNonRoot": false, "runAsUser": 0}, "automountServiceAccountToken": true, "hostNetwork": true}},
		{"pod options strategy", "spec.values.defaultPodOptionsStrategy", "merge"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			got := compare(t, base, change(t, base, mutation.path, mutation.value, false))
			want(t, got, "pod.securityContext", "not_assessed", "outside_http_shape")
			want(t, got, "pod.automountServiceAccountToken", "not_assessed", "outside_http_shape")
			// The rest of the workload stays comparable.
			want(t, got, "image", "equal", "")
			want(t, got, "container.securityContext", "equal", "")
		})
	}
	// An empty override changes no effective value, so the selected subtrees remain comparable.
	empty := change(t, base, controller+"pod", map[string]any{}, false)
	want(t, compare(t, base, empty), "pod.securityContext", "equal", "")
	// A disabled controller renders no workload at all.
	disabled := change(t, base, controller+"enabled", false, false)
	got := compare(t, base, disabled)
	want(t, got, "image", "not_assessed", "outside_http_shape")
	if len(got.AssessedScope) != 2 {
		t.Fatalf("a disabled controller claimed coverage: %+v", got)
	}
	// A valuesFrom-style HelmRelease carries no values object: outside coverage, not an error.
	uncovered := compare(t, base, change(t, base, "spec.values", nil, true))
	want(t, uncovered, "image", "not_assessed", "outside_http_shape")
	want(t, uncovered, "chart", "equal", "")
	// Another chart is outside coverage too, with its values still present and well-typed.
	other := change(t, base, "spec.chart.spec.chart", "podinfo", false)
	want(t, compare(t, base, other), "probes", "not_assessed", "outside_http_shape")
	// Absent quantities are unassessed because they are absent, not a spelling problem.
	for _, tt := range []struct{ path, field string }{
		{"resources.requests.cpu", "resources.cpuRequest"},
		{"resources.limits.memory", "resources.memory"},
	} {
		got := compare(t, base, change(t, base, container+tt.path, nil, true))
		want(t, got, tt.field, "not_assessed", "missing_quantity")
	}
	// A named probe port is valid Kubernetes outside this policy's grammar.
	named := change(t, base, container+"probes.readiness.spec.httpGet.port", "http", false)
	want(t, compare(t, base, named), "probes", "not_assessed", "outside_http_shape")
}

func TestUnknownSecurityFieldsAreNotDroppedOrCoerced(t *testing.T) {
	base := rendered(t)
	a := change(t, base, container+"securityContext.futureField", nil, false)
	b := change(t, base, container+"securityContext.futureField", map[string]any{}, false)
	want(t, compare(t, a, b), "container.securityContext", "different", "")
}

func TestValidUnicodeAndEscapedBackslashes(t *testing.T) {
	base := rendered(t)
	for _, text := range []string{`"\ud83d\ude00"`, `"\\ud800"`, `"😀"`} {
		extra := []byte(`"replicas":1,"extra":` + text)
		input := bytes.Replace(base, []byte(`"replicas":1`), extra, 1)
		compare(t, input, input)
	}
}

func TestDeterminismAndOwnedResults(t *testing.T) {
	before, after := fixture(t, "legacy-http"), rendered(t)
	a, b := bytes.Clone(before), bytes.Clone(after)
	first := compare(t, before, after)
	wire, _ := json.Marshal(first)
	for range 50 {
		got, _ := json.Marshal(compare(t, before, after))
		if !bytes.Equal(wire, got) {
			t.Fatal("nondeterministic findings")
		}
	}
	if !bytes.Equal(a, before) || !bytes.Equal(b, after) {
		t.Fatal("input mutation")
	}
	before[0] = '!'
	after[0] = '!'
	unchanged, _ := json.Marshal(first)
	if !bytes.Equal(wire, unchanged) {
		t.Fatal("result aliases bytes")
	}
	first.Findings[0].Outcome = "mutated"
	first.AssessedScope[0] = "mutated"
	first.UnassessedScope[0] = "mutated"
	again, _ := json.Marshal(compare(t, a, b))
	if !bytes.Equal(wire, again) {
		t.Fatal("results alias each other")
	}
	if bytes.Contains(wire, []byte("null")) {
		t.Fatal("null lists")
	}
}
