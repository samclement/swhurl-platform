package kernel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func previewFacts() (HTTPInstance, HTTPWorkload) {
	return HTTPInstance{Application: "hello", Environment: "staging", Configuration: Configuration{1, 128}}, HTTPWorkload{"docker.io/nginxinc/nginx-unprivileged", "sha256:" + strings.Repeat("a", 64), 8080, "/", 101}
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

// A connected pair renders both halves: the caller gets the callee's address and a token only the callee
// accepts, the callee gets its callers, the platform's keys through a ConfigMap of its own, and each side's
// policy admits the other's pods on the port the call uses.
func TestConnectedPairRendersBothHalves(t *testing.T) {
	caller, workload := previewFacts()
	caller.Exposure = "authenticated-web"
	caller.Calls = []Call{{Callee: "orders", Port: 8081}}
	caller.Callers = []string{"billing"}
	callee := HTTPInstance{Application: "orders", Environment: "staging", Configuration: Configuration{1, 128},
		Exposure: "authenticated-web", Callers: []string{"billing", "hello"}}
	profile := profile(t)

	rendered, err := RenderHTTP(caller, HTTPWorkload{"docker.io/x/y", "sha256:" + strings.Repeat("b", 64), 8080, "/", 101}, profile)
	if err != nil {
		t.Fatal(err)
	}
	values := documentOf(t, rendered, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	container := values["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)
	env := container["env"].(map[string]any)
	if env["SWHURL_SERVICE_ORDERS_URL"] != "http://orders.orders-staging.svc.cluster.local:8081" {
		t.Fatalf("the callee's address: %v", env["SWHURL_SERVICE_ORDERS_URL"])
	}
	if env["SWHURL_ALLOWED_CALLERS"] != "billing" {
		t.Fatalf("the caller's own callers: %v", env["SWHURL_ALLOWED_CALLERS"])
	}
	envFrom := container["envFrom"].([]any)[0].(map[string]any)["configMapRef"].(map[string]any)
	if envFrom["name"] != "hello-auth" {
		t.Fatalf("the auth ConfigMap reference: %v", envFrom)
	}
	tokens := values["persistence"].(map[string]any)["tokens"].(map[string]any)
	source := tokens["sources"].([]any)[0].(map[string]any)["serviceAccountToken"].(map[string]any)
	if source["audience"] != "orders" || source["expirationSeconds"] != float64(600) || source["path"] != "orders/token" {
		t.Fatalf("the projected token: %v", source)
	}
	if mount := tokens["globalMounts"].([]any)[0].(map[string]any); mount["path"] != "/var/run/swhurl/tokens" || mount["readOnly"] != true {
		t.Fatalf("the token mount: %v", mount)
	}
	// The policy reaches out only to the callee, on the callee's port, and admits only the caller's pods.
	callerPolicy := documentOf(t, rendered, "networkpolicy.json")["spec"].(map[string]any)
	egress := callerPolicy["egress"].([]any)
	// Two rules: the callee's pods on the call's port, and the DNS service that resolves its name.
	if len(egress) != 2 {
		t.Fatalf("caller egress: %v", egress)
	}
	to := egress[0].(map[string]any)["to"].([]any)[0].(map[string]any)
	// The peer is the cluster's pod network: this cluster honours address blocks, not pod selectors, and
	// the callee checks the caller's token.
	if to["ipBlock"].(map[string]any)["cidr"] != profile.Network.PodCIDR {
		t.Fatalf("caller egress peer: %v", to)
	}
	if port := egress[0].(map[string]any)["ports"].([]any)[0].(map[string]any); port["port"] != float64(8081) {
		t.Fatalf("caller egress port: %v", port)
	}

	// The callee side: the keys arrive in its own namespace, and its policy admits each caller's pods.
	rendered, err = RenderHTTP(callee, workload, profile)
	if err != nil {
		t.Fatal(err)
	}
	auth := documentOf(t, rendered, "auth.json")
	data := auth["data"].(map[string]any)
	if auth["kind"] != "ConfigMap" || data["SWHURL_AUTH_ISSUER"] != profile.Auth.Issuer || data["SWHURL_AUTH_KEYS"] != profile.Auth.Keys {
		t.Fatalf("the auth ConfigMap: %v", auth)
	}
	calleeValues := documentOf(t, rendered, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	calleeContainer := calleeValues["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)
	if calleeContainer["env"].(map[string]any)["SWHURL_ALLOWED_CALLERS"] != "billing,hello" {
		t.Fatalf("the callee's callers: %v", calleeContainer["env"])
	}
	calleePolicy := documentOf(t, rendered, "networkpolicy.json")["spec"].(map[string]any)
	ingress := calleePolicy["ingress"].([]any)
	// The route's ingress controller, and one rule for every caller on the callee's port: the address
	// block says where a caller may come from, and the token says who it is.
	if len(ingress) != 2 {
		t.Fatalf("callee ingress: %v", ingress)
	}
	if peer := ingress[1].(map[string]any)["from"].([]any)[0].(map[string]any)["ipBlock"].(map[string]any)["cidr"]; peer != profile.Network.PodCIDR {
		t.Fatalf("the callee's caller peer: %v", peer)
	}
	if len(calleePolicy["egress"].([]any)) != 0 {
		t.Fatalf("a callee with no calls egresses: %v", calleePolicy["egress"])
	}
	// The ConfigMap is a claimed object, so ownership sees it.
	claimed := map[string]bool{}
	for _, claim := range rendered.Inventory {
		claimed[claim.Object.Kind+" "+claim.Object.Namespace+"/"+claim.Object.Name] = true
	}
	if !claimed["ConfigMap orders-staging/orders-auth"] {
		t.Fatalf("the auth ConfigMap is not claimed: %v", claimed)
	}
}

// A callee needs the platform's keys: without them it could accept no caller it could check, so the
// preview is refused rather than rendered with an unverifiable consent.
func TestCallsNeedPlatformAuth(t *testing.T) {
	instance, workload := previewFacts()
	instance.Callers = []string{"orders"}
	without := profile(t)
	without.Auth = Auth{}
	if _, err := RenderHTTP(instance, workload, without); err != nil {
		refusal(t, err, Unsupported, "auth")
	} else {
		t.Fatal("a callee rendered without the platform's keys")
	}
	bad := profile(t)
	bad.Auth.Keys = "not a key set"
	if _, err := RenderHTTP(instance, workload, bad); err != nil {
		refusal(t, err, Invalid, "auth.keys")
	} else {
		t.Fatal("a key set that is not a JWKS was accepted")
	}
}

// A database instance gets v1's shape: a retained volume, one writer at a time, and a namespace Flux will
// not prune, because deleting an instance's files must not delete its data.
func TestSQLiteInstanceRendersItsVolume(t *testing.T) {
	instance, workload := previewFacts()
	instance.Database = "sqlite"
	instance.Storage = Storage{SizeMiB: 1024}
	// A database instance is compiled with an allocation: its backup must leave through the proxy.
	instance.Egress = &Egress{Port: 30101}
	profile := profile(t)
	rendered, err := RenderHTTP(instance, workload, profile)
	if err != nil {
		t.Fatal(err)
	}
	values := documentOf(t, rendered, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	controller := values["controllers"].(map[string]any)["main"].(map[string]any)
	if controller["strategy"] != "Recreate" {
		t.Fatalf("single writer: %v", controller["strategy"])
	}
	data := values["persistence"].(map[string]any)["data"].(map[string]any)
	if data["type"] != "persistentVolumeClaim" || data["storageClass"] != profile.Storage.Class ||
		data["accessMode"] != "ReadWriteOnce" || data["size"] != "1024Mi" || data["retain"] != true {
		t.Fatalf("the data volume: %v", data)
	}
	if mount := data["globalMounts"].([]any)[0].(map[string]any); mount["path"] != dataMount {
		t.Fatalf("the data mount: %v", mount)
	}
	container := controller["containers"].(map[string]any)["main"].(map[string]any)
	if container["env"].(map[string]any)["DATABASE_PATH"] != databasePath {
		t.Fatalf("the database path: %v", container["env"])
	}
	namespace := documentOf(t, rendered, "namespace.json")["metadata"].(map[string]any)
	if annotation := namespace["annotations"].(map[string]any)["kustomize.toolkit.fluxcd.io/prune"]; annotation != "disabled" {
		t.Fatalf("removal must not delete data: %v", namespace)
	}

	// A stateless instance keeps no volume, no strategy override and no prune exception.
	stateless, workload := previewFacts()
	plain, err := RenderHTTP(stateless, workload, profile)
	if err != nil {
		t.Fatal(err)
	}
	plainValues := documentOf(t, plain, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	if _, found := plainValues["persistence"].(map[string]any)["data"]; found {
		t.Fatal("a stateless instance rendered a data volume")
	}
	if strategy := plainValues["controllers"].(map[string]any)["main"].(map[string]any)["strategy"]; strategy != nil {
		t.Fatalf("a stateless instance set a strategy: %v", strategy)
	}
	if _, found := documentOf(t, plain, "namespace.json")["metadata"].(map[string]any)["annotations"]; found {
		t.Fatal("a stateless instance asked Flux not to prune its namespace")
	}
}

// A volume must match what the image needs and what the platform states, and one database admits one
// writer: each mismatch is refused rather than silently adjusted.
func TestStorageRefusals(t *testing.T) {
	instance, workload := previewFacts()
	profile := profile(t)
	instance.Storage = Storage{SizeMiB: 1024}
	if _, err := RenderHTTP(instance, workload, profile); err != nil {
		refusal(t, err, Invalid, "storage")
	} else {
		t.Fatal("a volume without a database rendered")
	}
	instance.Database = "sqlite"
	instance.Storage = Storage{}
	if _, err := RenderHTTP(instance, workload, profile); err != nil {
		refusal(t, err, Invalid, "storage.sizeMiB")
	} else {
		t.Fatal("a database without a size rendered")
	}
	instance.Storage = Storage{SizeMiB: 1024}
	withoutClass := profile
	withoutClass.Storage = PlatformStorage{}
	if _, err := RenderHTTP(instance, workload, withoutClass); err != nil {
		refusal(t, err, Unsupported, "storage.class")
	} else {
		t.Fatal("a database rendered without a storage class")
	}
	instance.Configuration.Replicas = 2
	if _, err := RenderHTTP(instance, workload, profile); err != nil {
		refusal(t, err, Invalid, "configuration.replicas")
	} else {
		t.Fatal("a database rendered with two writers")
	}
	other := instance
	other.Database = "postgres"
	if _, err := RenderHTTP(other, workload, profile); err != nil {
		refusal(t, err, Unsupported, "database")
	} else {
		t.Fatal("a database v2 does not support rendered")
	}
}

// An allocated instance reaches the internet only through its own port on the platform's proxy, and it can
// resolve a name at all only because DNS is admitted with it.
func TestEgressRendersTheProxyAndDNS(t *testing.T) {
	instance, workload := previewFacts()
	instance.Egress = &Egress{Port: 30120, Grants: []string{"api.stripe.com:443", "*.example.com:8443"}}
	profile := profile(t)
	rendered, err := RenderHTTP(instance, workload, profile)
	if err != nil {
		t.Fatal(err)
	}
	values := documentOf(t, rendered, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	container := values["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)
	env := container["env"].(map[string]any)
	address := "http://" + profile.Egress.Address + ":30120"
	if env["HTTP_PROXY"] != address || env["HTTPS_PROXY"] != address || env["NODE_USE_ENV_PROXY"] != "1" {
		t.Fatalf("the proxy variables: %v", env)
	}
	if env["NO_PROXY"] != "localhost,127.0.0.1,.svc,.cluster.local,$(HOST_IP)" {
		t.Fatalf("NO_PROXY: %v", env["NO_PROXY"])
	}
	hostIP := env["HOST_IP"].(map[string]any)["valueFrom"].(map[string]any)["fieldRef"].(map[string]any)
	if hostIP["fieldPath"] != "status.hostIP" {
		t.Fatalf("HOST_IP: %v", env["HOST_IP"])
	}
	if options := env["JAVA_TOOL_OPTIONS"].(string); !strings.Contains(options, "-Dhttp.proxyPort=30120") {
		t.Fatalf("the JVM's proxy properties: %v", options)
	}
	// The declared grants are what the proxy reads from its log for this instance.
	release := documentOf(t, rendered, "helmrelease.json")
	annotations := release["metadata"].(map[string]any)["annotations"].(map[string]any)
	if annotations["platform.swhurl.com/egress"] != "api.stripe.com:443,*.example.com:8443" {
		t.Fatalf("the egress annotation: %v", annotations)
	}
	// The policy admits the proxy's pods on that port and DNS, and nothing else.
	egress := documentOf(t, rendered, "networkpolicy.json")["spec"].(map[string]any)["egress"].([]any)
	if len(egress) != 2 {
		t.Fatalf("egress rules: %v", egress)
	}
	proxy := egress[1].(map[string]any)["to"].([]any)[0].(map[string]any)["namespaceSelector"].(map[string]any)["matchLabels"].(map[string]any)
	if proxy["kubernetes.io/metadata.name"] != "egress" {
		t.Fatalf("the proxy peer: %v", proxy)
	}
	if port := egress[1].(map[string]any)["ports"].([]any)[0].(map[string]any); port["port"] != float64(30120) {
		t.Fatalf("the proxy port: %v", port)
	}

	// Without an allocation there is no proxy variable and no proxy rule.
	stateless, _ := previewFacts()
	plain, err := RenderHTTP(stateless, workload, profile)
	if err != nil {
		t.Fatal(err)
	}
	plainValues := documentOf(t, plain, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	plainContainer := plainValues["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)
	if _, found := plainContainer["env"].(map[string]any)["HTTP_PROXY"]; found {
		t.Fatal("an unallocated instance was told to use the proxy")
	}
	if rules := documentOf(t, plain, "networkpolicy.json")["spec"].(map[string]any)["egress"].([]any); len(rules) != 0 {
		t.Fatalf("an unallocated instance egresses: %v", rules)
	}
}

// An allocation must be usable and the platform must state a proxy.
func TestEgressRefusals(t *testing.T) {
	instance, workload := previewFacts()
	profile := profile(t)
	instance.Egress = &Egress{Port: 80}
	if _, err := RenderHTTP(instance, workload, profile); err != nil {
		refusal(t, err, Invalid, "egress.port")
	} else {
		t.Fatal("a privileged proxy port rendered")
	}
	instance.Egress = &Egress{Port: 30120}
	without := profile
	without.Egress = EgressProxy{}
	if _, err := RenderHTTP(instance, workload, without); err != nil {
		refusal(t, err, Unsupported, "egress")
	} else {
		t.Fatal("an allocation without a platform proxy rendered")
	}
}
