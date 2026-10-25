package kernel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// proxyAllocationsFixture is two instances on the platform's proxy: one reaching Stripe and every name
// under example.com, and one whose two grants are the same host written differently.
func proxyAllocationsFixture() []EgressAllocation {
	return []EgressAllocation{
		{Application: "shop", Environment: "staging", Port: 30101,
			Grants: []string{"api.stripe.com", "*.example.com:8443", "status.example.com:8443"}},
		{Application: "orders", Environment: "prod", Port: 30102,
			Grants: []string{"api.stripe.com:443", "API.Stripe.com."}},
	}
}

// valuesOf is the configuration's chart values, read back from the ConfigMap: they are JSON, so the
// test reads exactly what the chart will read.
func valuesOf(t *testing.T, document []byte) map[string]any {
	t.Helper()
	var configMap struct {
		Metadata struct{ Name, Namespace string }
		Data     map[string]string
	}
	if err := json.Unmarshal(document, &configMap); err != nil {
		t.Fatal(err)
	}
	if configMap.Metadata.Name != proxyConfigName || configMap.Metadata.Namespace != "egress" {
		t.Fatalf("the ConfigMap is %s/%s", configMap.Metadata.Namespace, configMap.Metadata.Name)
	}
	values := map[string]any{}
	if err := json.Unmarshal([]byte(configMap.Data[proxyValuesKey]), &values); err != nil {
		t.Fatalf("values are not JSON, which is YAML: %v", err)
	}
	return values
}

// squidOf is the configuration the values carry.
func squidOf(t *testing.T, document []byte) string {
	t.Helper()
	values := valuesOf(t, document)
	config, _ := values["configMaps"].(map[string]any)["config"].(map[string]any)
	data, _ := config["data"].(map[string]any)
	conf, _ := data["squid.conf"].(string)
	if conf == "" {
		t.Fatal("the values carry no squid.conf")
	}
	return conf
}

// policyOf is the NetworkPolicy's spec.
func policyOf(t *testing.T, document []byte) map[string]any {
	t.Helper()
	var policy struct {
		Metadata struct{ Name, Namespace string }
		Spec     map[string]any
	}
	if err := json.Unmarshal(document, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Metadata.Name != proxyPolicyName || policy.Metadata.Namespace != "egress" {
		t.Fatalf("the policy is %s/%s", policy.Metadata.Namespace, policy.Metadata.Name)
	}
	return policy.Spec
}

// The proxy's two objects are rendered from the allocation, claimed by the platform, and the same
// allocation renders the same bytes without sharing its slices with the caller.
func TestRenderProxy(t *testing.T) {
	allocations := proxyAllocationsFixture()
	got, err := RenderProxy(allocations, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 || got.Files[0].Path != "egress/configmap.json" ||
		got.Files[1].Path != "egress/networkpolicy.json" || len(got.Inventory) != 2 || got.Registrations != nil {
		t.Fatalf("unexpected resources: %+v", got)
	}
	for _, claim := range got.Inventory {
		if claim.Owner != proxyOwner || claim.Namespace != "egress" {
			t.Fatalf("the proxy's objects are claimed as %+v", claim)
		}
	}
	if got.Inventory[0].Kind != "ConfigMap" || got.Inventory[1].Kind != "NetworkPolicy" ||
		got.Inventory[1].Group != "networking.k8s.io" {
		t.Fatalf("the inventory is %+v", got.Inventory)
	}
	again, err := RenderProxy(allocations, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, again) {
		t.Fatal("the same allocation rendered differently twice")
	}
	// The output is fresh: a caller cannot change the next render through what it was given.
	original, _ := json.Marshal(got)
	allocations[0].Grants[0] = "changed.example"
	got.Files[0].Document[0] = '!'
	got.Inventory[0].Owner = "changed"
	unchanged, _ := json.Marshal(again)
	if string(original) != string(unchanged) {
		t.Fatal("the render aliases the caller's allocation or its own output")
	}
}

// The configuration names the caller by the port it arrived on and allows only that instance's own
// grants, at their ports, with everything else refused.
func TestSquidConfigEnforcesGrants(t *testing.T) {
	got, err := RenderProxy(proxyAllocationsFixture(), profile(t))
	if err != nil {
		t.Fatal(err)
	}
	want := `# Squid, one port per app instance: every request is logged with the instance that sent it, and
# refused unless a grant of that instance covers its host and port.
pid_filename none
pinger_enable off
cache deny all
via off
forwarded_for delete
shutdown_lifetime 3 seconds
logformat egress instance=%{instance}note granted=%{granted}note client=%>a method=%rm host=%>rd:%>rP status=%>Hs/%Ss bytes=%<st ms=%tr
http_port 3128 name=health
acl health myportname health
http_access deny health
# Requests only: the kubelet's probes connect every few seconds and send none.
acl request has request
access_log stdio:/dev/stdout logformat=egress request
cache_log stdio:/dev/stderr
acl port-443 port 443
acl port-8443 port 8443
http_port 30101 name=shop-staging
acl shop-staging myportname shop-staging
note instance shop-staging shop-staging
acl shop-staging-443 dstdomain -n api.stripe.com
note granted yes shop-staging shop-staging-443 port-443
acl shop-staging-8443 dstdomain -n .example.com
note granted yes shop-staging shop-staging-8443 port-8443
http_port 30102 name=orders-prod
acl orders-prod myportname orders-prod
note instance orders-prod orders-prod
acl orders-prod-443 dstdomain -n api.stripe.com
note granted yes orders-prod orders-prod-443 port-443
http_access allow shop-staging shop-staging-443 port-443
http_access allow shop-staging shop-staging-8443 port-8443
http_access allow orders-prod orders-prod-443 port-443
http_access deny all
`
	if conf := squidOf(t, got.Files[0].Document); conf != want {
		t.Fatalf("the configuration is:\n%s\nwant:\n%s", conf, want)
	}
	ports := valuesOf(t, got.Files[0].Document)["service"].(map[string]any)["main"].(map[string]any)["ports"].(map[string]any)
	if len(ports) != 2 || ports["p30101"].(map[string]any)["port"] != float64(30101) ||
		ports["p30102"].(map[string]any)["port"] != float64(30102) {
		t.Fatalf("the Service ports are %v", ports)
	}
}

// Each port is admitted from its own instance's namespace only, and the proxy states its own way out:
// the name service, and the ports its configuration may dial.
func TestProxyPolicy(t *testing.T) {
	got, err := RenderProxy(proxyAllocationsFixture(), profile(t))
	if err != nil {
		t.Fatal(err)
	}
	spec := policyOf(t, got.Files[1].Document)
	// Ingress only, as v1's generated policy is: the instance's own deny-all is the instance's policy,
	// and this namespace's egress is the platform's existing state rather than something v2 decides.
	if !reflect.DeepEqual(spec["policyTypes"], []any{"Ingress"}) {
		t.Fatalf("policyTypes are %v", spec["policyTypes"])
	}
	if _, rendered := spec["egress"]; rendered {
		t.Fatalf("the proxy's policy states its own egress: %v", spec["egress"])
	}
	selector := spec["podSelector"].(map[string]any)["matchLabels"].(map[string]any)
	if selector["app.kubernetes.io/name"] != "egress" {
		t.Fatalf("the policy selects %v, not the profile's proxy pods", selector)
	}
	ingress := spec["ingress"].([]any)
	if len(ingress) != 2 {
		t.Fatalf("ingress is %v", ingress)
	}
	for i, want := range []struct {
		namespace string
		port      float64
	}{{"shop-staging", 30101}, {"orders-prod", 30102}} {
		rule := ingress[i].(map[string]any)
		peer := rule["from"].([]any)[0].(map[string]any)
		if peer["namespaceSelector"].(map[string]any)["matchLabels"].(map[string]any)["kubernetes.io/metadata.name"] != want.namespace {
			t.Fatalf("ingress rule %d admits %v", i, peer)
		}
		if len(peer) != 1 {
			t.Fatalf("ingress rule %d admits more than a namespace: %v", i, peer)
		}
		ports := rule["ports"].([]any)
		if len(ports) != 1 || ports[0].(map[string]any)["port"] != want.port {
			t.Fatalf("ingress rule %d admits %v", i, ports)
		}
	}
}

// An instance that declares nothing reaches nothing: its port exists and names it, the configuration
// refuses every request, and the policy still admits only that instance's own namespace to the port.
func TestProxyWithoutGrantsDeniesEverything(t *testing.T) {
	got, err := RenderProxy([]EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101}}, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	conf := squidOf(t, got.Files[0].Document)
	if strings.Contains(conf, "http_access allow shop-staging") || !strings.HasSuffix(conf, "http_access deny all\n") {
		t.Fatalf("an instance without grants is allowed:\n%s", conf)
	}
	if !strings.Contains(conf, "http_port 30101 name=shop-staging\nacl shop-staging myportname shop-staging\n") {
		t.Fatalf("the instance has no port of its own:\n%s", conf)
	}
	ingress := policyOf(t, got.Files[1].Document)["ingress"].([]any)
	if len(ingress) != 1 || ingress[0].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"] != float64(30101) {
		t.Fatalf("the policy admits %v, want the instance's own port", ingress)
	}
}

// Nothing is rendered from an allocation the platform cannot state: no proxy, a shared port, an
// unreadable name, or a grant that is not a host.
func TestProxyRefusals(t *testing.T) {
	for _, tt := range []struct {
		name        string
		allocations []EgressAllocation
		change      func(*PlatformProfile)
		code        Code
		field       string
	}{
		{"no proxy", nil, func(p *PlatformProfile) { p.Egress = EgressProxy{} }, Unsupported, "egress"},
		{"partial proxy", nil, func(p *PlatformProfile) { p.Egress.Address = "" }, Invalid, "egress.address"},
		{"application", []EgressAllocation{{Application: "Shop", Environment: "staging", Port: 30101}}, nil, Invalid, "application"},
		{"environment", []EgressAllocation{{Application: "shop", Environment: "dev", Port: 30101}}, nil, Unsupported, "environment"},
		{"port low", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 80}}, nil, Invalid, "egress.port"},
		{"port high", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 65536}}, nil, Invalid, "egress.port"},
		{"shared port", []EgressAllocation{
			{Application: "shop", Environment: "staging", Port: 30101},
			{Application: "orders", Environment: "staging", Port: 30101},
		}, nil, Invalid, "egress.port"},
		{"shared port before grants", []EgressAllocation{
			{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"*"}},
			{Application: "orders", Environment: "staging", Port: 30101},
		}, nil, Invalid, "egress.port"},
		{"names before ports", []EgressAllocation{{Application: "Shop", Environment: "staging", Port: 80}}, nil, Invalid, "application"},
		{"empty grant", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{""}}}, nil, Invalid, "egress.grant"},
		{"ip address", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"10.0.0.1"}}}, nil, Invalid, "egress.grant"},
		{"wildcard alone", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"*"}}}, nil, Invalid, "egress.grant"},
		{"wildcard inside", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"api.*.example.com"}}}, nil, Invalid, "egress.grant"},
		{"missing port", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"api.stripe.com:"}}}, nil, Invalid, "egress.grant"},
		{"port zero", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"api.stripe.com:0"}}}, nil, Invalid, "egress.grant"},
		{"port high", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"api.stripe.com:70000"}}}, nil, Invalid, "egress.grant"},
		{"path", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"api.stripe.com/v1"}}}, nil, Invalid, "egress.grant"},
		{"scheme", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"https://api.stripe.com"}}}, nil, Invalid, "egress.grant"},
		{"no top label", []EgressAllocation{{Application: "shop", Environment: "staging", Port: 30101, Grants: []string{"localhost"}}}, nil, Invalid, "egress.grant"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := profile(t)
			if tt.change != nil {
				tt.change(&p)
			}
			got, err := RenderProxy(tt.allocations, p)
			refusal(t, err, tt.code, tt.field)
			if !reflect.DeepEqual(got, ResourceSet{}) {
				t.Fatal("a refusal rendered something")
			}
		})
	}
}

// A grant is kept as the platform states it, with its port, so the same host written two ways is one
// line, and an instance that declares every name under a domain does not also declare one of them.
func TestGrantReading(t *testing.T) {
	for _, tt := range []struct {
		grant    string
		host     string
		wantPort int32
	}{
		{"api.stripe.com", "api.stripe.com", 443},
		{"API.STRIPE.COM", "api.stripe.com", 443},
		{"api.stripe.com.", "api.stripe.com", 443},
		{"api.stripe.com:8443", "api.stripe.com", 8443},
		{" *.stripe.com ", ".stripe.com", 443},
		{"*.stripe.com:8443", ".stripe.com", 8443},
	} {
		got, err := parseGrant(tt.grant)
		if err != nil || got.Host != tt.host || got.Port != tt.wantPort {
			t.Fatalf("parseGrant(%q) = %+v, %v", tt.grant, got, err)
		}
	}
	// A wildcard grant covers the domain itself and every name under it.
	for _, covered := range [][2]string{
		{".example.com", "example.com"}, {".example.com", "api.example.com"}, {".example.com", ".api.example.com"},
	} {
		if !wildcardCovers(covered[0], covered[1]) {
			t.Fatalf("%s does not cover %s", covered[0], covered[1])
		}
	}
	for _, open := range [][2]string{
		{"api.example.com", "api.example.com"}, {".example.com", "example.org"}, {".example.com", "notexample.com"},
	} {
		if wildcardCovers(open[0], open[1]) {
			t.Fatalf("%s covers %s", open[0], open[1])
		}
	}
}
