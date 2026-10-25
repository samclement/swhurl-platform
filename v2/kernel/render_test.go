package kernel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func previewFacts() (HTTPInstance, HTTPWorkload) {
	return HTTPInstance{"hello", "staging", Configuration{1, 128}, ""}, HTTPWorkload{"docker.io/nginxinc/nginx-unprivileged", "sha256:" + strings.Repeat("a", 64), 8080, "/", 101}
}

// No source, commit, media type, platform, selection or release identity is available here.
func TestRenderWithoutReleaseSemantics(t *testing.T) {
	s, w := previewFacts()
	p := profile(t)
	got, err := RenderHTTP(s, w, p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := RenderHTTP(s, w, p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, again) || len(got.Files) != 6 || len(got.Inventory) != 5 || len(got.Registrations) != 1 {
		t.Fatalf("unexpected resources: %+v", got)
	}
	original, _ := json.Marshal(got)
	p.Delivery.DependsOn[0] = "changed"
	got.Files[0].Document[0] = '!'
	got.Files[1].Path = "changed"
	got.Inventory[0].Owner = "changed"
	got.Registrations[0] = "changed"
	unchanged, _ := json.Marshal(again)
	if string(original) != string(unchanged) {
		t.Fatal("calls alias their resource output or profile")
	}
	fresh, err := RenderHTTP(s, w, profile(t))
	if err != nil || !reflect.DeepEqual(fresh, again) {
		t.Fatal("retained mutation across calls")
	}
	s.Configuration.Replicas = 0
	zero, err := RenderHTTP(s, w, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zero.Files[1].Document), `"replicas":0`) {
		t.Fatal("explicit zero lost")
	}
}

func TestRenderValidationAndOrder(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*HTTPInstance, *HTTPWorkload, *PlatformProfile)
		code   Code
		field  string
	}{
		{"names before workload", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { s.Application = "BAD"; w.Image = "BAD" }, Invalid, "application"},
		{"environment", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { s.Environment = "dev" }, Unsupported, "environment"},
		{"image before digest", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Image = "bad"; w.Digest = "bad" }, Invalid, "artifact.image"},
		{"digest algorithm", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Digest = "sha512:bad" }, Unsupported, "artifact.digest"},
		{"digest syntax", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Digest = "sha256:bad" }, Invalid, "artifact.digest"},
		{"port before path", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Port = 0; w.HealthPath = "bad" }, Invalid, "runtime.port"},
		{"port upper bound", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.Port = 65536 }, Invalid, "runtime.port"},
		{"path before uid", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.HealthPath = "//bad"; w.UID = 0 }, Invalid, "runtime.healthPath"},
		{"uid before configuration", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { w.UID = 0; s.Configuration.Replicas = -1 }, Invalid, "runtime.uid"},
		{"configuration order", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { s.Configuration = Configuration{-1, 0} }, Invalid, "configuration.replicas"},
		{"configuration before profile", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) {
			s.Configuration.MemoryMiB = 0
			p.SchemaVersion = 2
		}, Invalid, "configuration.memoryMiB"},
		{"profile", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { p.Workload.CPURequestMilli = 0 }, Invalid, "workload.cpuRequestMilli"},
		{"quota", func(s *HTTPInstance, w *HTTPWorkload, p *PlatformProfile) { s.Configuration.Replicas = 14 }, OverQuota, "quota.pods"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, w := previewFacts()
			p := profile(t)
			tt.change(&s, &w, &p)
			got, err := RenderHTTP(s, w, p)
			refusal(t, err, tt.code, tt.field)
			if !reflect.DeepEqual(got, ResourceSet{}) {
				t.Fatal("partial resources on refusal")
			}
		})
	}
}

func TestRenderQuotaBoundaries(t *testing.T) {
	for _, tt := range []struct {
		replicas, memory, cpu int32
		field                 string
	}{
		{13, 64, 10, ""}, {14, 64, 10, "quota.pods"}, {7, 256, 10, ""}, {8, 256, 10, "quota.requestsMemoryMiB"},
		{1, 128, 1001, "quota.requestsCpuMilli"}, {0, 1 << 30, 10, ""}, {1<<31 - 1, 1<<31 - 1, 10, "quota.pods"},
	} {
		s, w := previewFacts()
		s.Configuration = Configuration{tt.replicas, tt.memory}
		p := profile(t)
		p.Workload.CPURequestMilli = tt.cpu
		_, err := RenderHTTP(s, w, p)
		if tt.field == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			refusal(t, err, OverQuota, tt.field)
		}
	}
	s, w := previewFacts()
	p := profile(t)
	p.Quota.LimitsMemoryMiB = 255
	_, err := RenderHTTP(s, w, p)
	refusal(t, err, OverQuota, "quota.limitsMemoryMiB")
}

func TestSourceProvenanceChangesOnlyRealInputIdentities(t *testing.T) {
	s, r, p := instance(t, "prod.json"), releaseFile(t, "r41.json"), profile(t)
	before := compile(t, s, r, p)
	r.Source.Repository = "another/repository"
	r.Source.Commit = strings.Repeat("c", 40)
	r.ReleaseID = ""
	id, err := r.ID()
	if err != nil {
		t.Fatal(err)
	}
	s.Release = id
	after := compile(t, s, r, p)
	if before.ID != after.ID || !reflect.DeepEqual(before.Files, after.Files) || !reflect.DeepEqual(before.Inventory, after.Inventory) || !reflect.DeepEqual(before.Registrations, after.Registrations) {
		t.Fatal("provenance changed resources")
	}
	rev, _ := s.Revision()
	pid, _ := p.ID()
	if after.Inputs.Release != id || after.Inputs.Instance != rev || after.Inputs.Profile != pid || before.Inputs.Release == after.Inputs.Release || before.Inputs.Instance == after.Inputs.Instance {
		t.Fatal("real identities were not attached")
	}
}

func TestCompileCompleteRefusalPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*InstanceSpec, *ReleaseBundle, *PlatformProfile)
		code   Code
		field  string
	}{
		{"instance before release", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			s.Release = "bad"
			r.Source.Commit = "bad"
		}, Invalid, "release"},
		{"source before image", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Source.Commit = "bad"
			r.Artifact.Image = "bad"
		}, Invalid, "source.commit"},
		{"media before runtime", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Artifact.MediaType = "bad"
			r.Runtime.Port = 0
		}, Unsupported, "artifact.mediaType"},
		{"platform before runtime", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Artifact.Platform = "bad"
			r.Runtime.Port = 0
		}, Unsupported, "artifact.platform"},
		{"runtime before identity", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) { r.Runtime.UID = 0; r.ReleaseID = "bad" }, Invalid, "runtime.uid"},
		{"identity before profile", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Source.Commit = strings.Repeat("c", 40)
			p.Quota.Pods = 0
		}, IdentityMismatch, "releaseId"},
		{"release before profile", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) { r.Source.Commit = "bad"; p.Quota.Pods = 0 }, Invalid, "source.commit"},
		{"profile before application", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			r.Application = "other"
			r.ReleaseID = ""
			p.Quota.Pods = 0
		}, Invalid, "quota.pods"},
		{"application before selection", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) { r.Application = "other"; r.ReleaseID = "" }, WrongApplication, "application"},
		{"selection before quota", func(s *InstanceSpec, r *ReleaseBundle, p *PlatformProfile) {
			s.Release = ReleaseID("rel-sha256:" + strings.Repeat("0", 64))
			s.Configuration.Replicas = 14
		}, WrongRelease, "release"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, r, p := instance(t, "prod.json"), releaseFile(t, "r41.json"), profile(t)
			tt.change(&s, &r, &p)
			c, err := Compile(s, r, p)
			refusal(t, err, tt.code, tt.field)
			if !reflect.DeepEqual(c, Compilation{}) {
				t.Fatal("partial compilation")
			}
		})
	}
}

// A signed-in instance renders one more file than a private one: the route. Its host is derived from the
// platform's base domain, and its TLS and sign-in settings are the platform's, not the app's.
func TestExposedInstanceRendersTheRoute(t *testing.T) {
	instance, workload := previewFacts()
	instance.Exposure = "authenticated-web"
	profile := profile(t)
	got, err := RenderHTTP(instance, workload, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 7 || len(got.Inventory) != 6 {
		t.Fatalf("%d files, %d claims", len(got.Files), len(got.Inventory))
	}
	route := documentOf(t, got, "ingress.json")
	metadata := route["metadata"].(map[string]any)
	annotations := metadata["annotations"].(map[string]any)
	if metadata["name"] != "hello" || metadata["namespace"] != "hello-staging" {
		t.Fatalf("route metadata: %v", metadata)
	}
	if annotations["cert-manager.io/cluster-issuer"] != "letsencrypt-prod" ||
		annotations["traefik.ingress.kubernetes.io/router.middlewares"] != "ingress-oauth-auth-shared@kubernetescrd" {
		t.Fatalf("route annotations: %v", annotations)
	}
	spec := route["spec"].(map[string]any)
	if spec["ingressClassName"] != "traefik" {
		t.Fatalf("class: %v", spec["ingressClassName"])
	}
	rules := spec["rules"].([]any)
	host := rules[0].(map[string]any)["host"]
	if host != "staging-hello.homelab.swhurl.com" {
		t.Fatalf("derived host: %v", host)
	}
	path := rules[0].(map[string]any)["http"].(map[string]any)["paths"].([]any)[0].(map[string]any)
	backend := path["backend"].(map[string]any)["service"].(map[string]any)
	if path["path"] != "/" || path["pathType"] != "Prefix" || backend["name"] != "hello" ||
		backend["port"].(map[string]any)["number"] != float64(8080) {
		t.Fatalf("route path: %v", path)
	}
	tls := spec["tls"].([]any)[0].(map[string]any)
	if tls["secretName"] != "hello-tls" || tls["hosts"].([]any)[0] != "staging-hello.homelab.swhurl.com" {
		t.Fatalf("route TLS: %v", tls)
	}
	// The policy admits the ingress controller's pods on the app's port, and still denies all egress.
	policy := documentOf(t, got, "networkpolicy.json")["spec"].(map[string]any)
	ingress := policy["ingress"].([]any)
	if len(ingress) != 1 {
		t.Fatalf("policy ingress: %v", ingress)
	}
	rule := ingress[0].(map[string]any)
	source := rule["from"].([]any)[0].(map[string]any)
	namespace := source["namespaceSelector"].(map[string]any)["matchLabels"].(map[string]any)
	labels := source["podSelector"].(map[string]any)["matchLabels"].(map[string]any)
	if namespace["kubernetes.io/metadata.name"] != "kube-system" || labels["app.kubernetes.io/name"] != "traefik" {
		t.Fatalf("policy source: %v", source)
	}
	if port := rule["ports"].([]any)[0].(map[string]any); port["port"] != float64(8080) || port["protocol"] != "TCP" {
		t.Fatalf("policy port: %v", port)
	}
	if policy["egress"] == nil || len(policy["egress"].([]any)) != 0 {
		t.Fatalf("egress is not denied: %v", policy["egress"])
	}

	// The route is what the instance claims, so an ownership check sees it.
	claimed := map[string]bool{}
	for _, claim := range got.Inventory {
		claimed[claim.Object.Kind+" "+claim.Object.Namespace+"/"+claim.Object.Name] = true
	}
	if !claimed["Ingress hello-staging/hello"] {
		t.Fatalf("the route is not claimed: %v", claimed)
	}
}

// A private instance renders no route, and a profile that states no routing cannot render one: a preview
// fails rather than inventing a host or a middleware.
func TestRoutesNeedPlatformRouting(t *testing.T) {
	instance, workload := previewFacts()
	private, err := RenderHTTP(instance, workload, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(private.Files) != 6 {
		t.Fatalf("a private instance rendered %d files", len(private.Files))
	}
	instance.Exposure = "authenticated-web"
	without := profile(t)
	without.Routing = Routing{}
	if _, err := RenderHTTP(instance, workload, without); err != nil {
		refusal(t, err, Unsupported, "routing")
	} else {
		t.Fatal("an exposed instance rendered without platform routing")
	}
	instance.Exposure = "public"
	if _, err := RenderHTTP(instance, workload, profile(t)); err != nil {
		refusal(t, err, Unsupported, "grants.exposure")
	} else {
		t.Fatal("public exposure rendered")
	}
}

// documentOf is one rendered file, decoded.
func documentOf(t *testing.T, resources ResourceSet, name string) map[string]any {
	t.Helper()
	for _, file := range resources.Files {
		if strings.HasSuffix(file.Path, "/"+name) {
			var document map[string]any
			if err := json.Unmarshal(file.Document, &document); err != nil {
				t.Fatal(err)
			}
			return document
		}
	}
	t.Fatalf("no %s among the rendered files", name)
	return nil
}
