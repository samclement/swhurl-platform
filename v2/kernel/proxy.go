package kernel

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The platform's egress proxy is the one way out of the cluster: a Squid every instance reaches on a
// port of its own, so the proxy knows which instance is asking without trusting anything the app sends
// (docs/services.md#egress-proxy). This file renders the proxy's generated objects — the configuration
// it serves and the policy that admits each port from one namespace — from the platform's facts and
// the allocation.
//
// It does not render the proxy's deployment. The chart, image digest, replicas, probes and namespace
// are the platform's hand-written HelmRelease, which reads the ConfigMap rendered here; the facts the
// two must agree on (that file's valuesFrom, the Service's name and namespace, the pod labels, the
// health port) are asserted against it and the pinned chart, never guessed.
//
// Two facts decide what the configuration says. The allocation decides which instance sits on which
// port; the port is the identity, so it is decided once by `swhurl-v2 egress` and read here, never
// chosen or moved while rendering. The instance's grants decide the hosts and ports it may reach: they
// are enforced, so the configuration allows each instance its own grants and refuses everything else.
const (
	// The values ConfigMap the hand-written HelmRelease reads through valuesFrom, and the key it reads.
	proxyConfigName = "egress-proxy"
	proxyValuesKey  = "values.yaml"
	// The policy admitting each instance's port from that instance's namespace and nothing else.
	proxyPolicyName = "egress-port-per-instance"
	// Probes only: the kubelet connects here and sends no request, and every request is refused.
	proxyHealthPort = 3128
	// A grant without a port means HTTPS, as the platform states a grant.
	grantDefaultPort = 443
	// The platform's own objects: no instance owns them, so ownership conflicts name the platform.
	proxyOwner     = "platform"
	proxyLogFormat = "logformat egress instance=%{instance}note granted=%{granted}note client=%>a method=%rm host=%>rd:%>rP status=%>Hs/%Ss bytes=%<st ms=%tr"
	// The wildcard a grant may start with: "*.example.com" covers example.com and every name under it.
	grantWildcard = "*"
)

// EgressAllocation is one instance's place on the proxy: who it is, the port that names it, and the
// hosts it declares it needs. The allocation belongs to the platform; a renderer only states what it
// is given.
type EgressAllocation struct {
	Application, Environment string
	Port                     int32
	Grants                   []string
}

// RenderProxy builds the proxy's objects from the allocation: its configuration, with a port per
// instance and the hosts its grants allow, and a policy admitting each port from its own namespace.
func RenderProxy(allocations []EgressAllocation, profile PlatformProfile) (ResourceSet, error) {
	parsed, err := proxyAllocations(allocations, profile)
	if err != nil {
		return ResourceSet{}, err
	}
	folder := profile.Egress.Namespace
	return ResourceSet{
		Files: []File{
			{folder + "/configmap.json", encodeManifest(proxyConfigMap(parsed, profile))},
			{folder + "/networkpolicy.json", encodeManifest(proxyPolicy(parsed, profile))},
		},
		Inventory: proxyInventory(profile),
	}, nil
}

// parsedAllocation is an allocation with its grants read: the namespace the policy admits the port
// from, the port itself, and the hosts the configuration allows on each port.
type parsedAllocation struct {
	Namespace string
	Port      int32
	Grants    []parsedGrant
}

// parsedGrant is one host on one port, as Squid's dstdomain reads it: a leading dot in a wildcard grant
// covers the domain itself and every name under it.
type parsedGrant struct {
	Host string
	Port int32
}

// proxyAllocations validates the allocations in a fixed order and reads their grants: the profile
// states a proxy, then each allocation's names and port, then that no port is shared, then each
// allocation's grants. A port identifies one caller, so two instances on one port is a refusal rather
// than a configuration in which one of them answers for the other.
func proxyAllocations(allocations []EgressAllocation, profile PlatformProfile) ([]parsedAllocation, error) {
	if err := profile.Egress.validate(); err != nil {
		return nil, err
	}
	if profile.Egress.Namespace == "" {
		return nil, refuse(Unsupported, "egress", "the profile states no proxy")
	}
	parsed := make([]parsedAllocation, len(allocations))
	for i, allocation := range allocations {
		if err := validateNames(allocation.Application, allocation.Environment); err != nil {
			return nil, err
		}
		if allocation.Port < 1024 || allocation.Port > 65535 {
			return nil, refuse(Invalid, "egress.port", "expected an unprivileged port")
		}
		parsed[i] = parsedAllocation{Namespace: allocation.Application + "-" + allocation.Environment,
			Port: allocation.Port}
	}
	if err := distinctPorts(parsed); err != nil {
		return nil, err
	}
	for i, allocation := range allocations {
		grants, err := parsedGrants(allocation.Grants)
		if err != nil {
			return nil, err
		}
		parsed[i].Grants = grants
	}
	return parsed, nil
}

// distinctPorts refuses a port two instances would share.
func distinctPorts(allocations []parsedAllocation) error {
	seen := make(map[int32]bool, len(allocations))
	for _, allocation := range allocations {
		if seen[allocation.Port] {
			return refuse(Invalid, "egress.port", "two instances cannot share one port: the port names the caller")
		}
		seen[allocation.Port] = true
	}
	return nil
}

// parsedGrants reads one instance's grants: sorted by port then host, without a repeat, and without a
// host a wildcard grant on the same port already covers.
func parsedGrants(grants []string) ([]parsedGrant, error) {
	parsed := make([]parsedGrant, 0, len(grants))
	for _, text := range grants {
		grant, err := parseGrant(text)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(parsed, grant) {
			parsed = append(parsed, grant)
		}
	}
	slices.SortFunc(parsed, func(a, b parsedGrant) int {
		if a.Port != b.Port {
			return int(a.Port - b.Port)
		}
		return strings.Compare(a.Host, b.Host)
	})
	return slices.DeleteFunc(parsed, func(grant parsedGrant) bool {
		return slices.ContainsFunc(parsed, func(other parsedGrant) bool {
			return other.Port == grant.Port && other.Host != grant.Host && wildcardCovers(other.Host, grant.Host)
		})
	}), nil
}

// wildcardCovers is whether a wildcard grant already allows the host: ".example.com" allows
// example.com and every name under it.
func wildcardCovers(wildcard, host string) bool {
	return strings.HasPrefix(wildcard, ".") && (host == wildcard[1:] || strings.HasSuffix(host, wildcard))
}

// grantHostPattern is a grant's host: a DNS name whose top label is letters, so an IP address is not
// one, and no port, path or user part. A wildcard's "*." is removed before this matches.
var grantHostPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// parseGrant reads a grant as the platform states one: a host name with an optional port, 443 when
// none is given, and a leading *. for any name under a domain. Only a host name is ever checked, so a
// grant that is not one is refused rather than allowed as written.
func parseGrant(text string) (parsedGrant, error) {
	host, port := strings.TrimSpace(text), int64(grantDefaultPort)
	if name, number, found := strings.Cut(host, ":"); found {
		host = name
		given, err := strconv.ParseInt(number, 10, 32)
		if err != nil || given < 1 || given > 65535 {
			return parsedGrant{}, refuse(Invalid, "egress.grant", "expected a port from 1 to 65535 after the host")
		}
		port = given
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if name, wildcard := strings.CutPrefix(host, grantWildcard+"."); wildcard {
		host = "." + name
	}
	if !grantHostPattern.MatchString(strings.TrimPrefix(host, ".")) {
		return parsedGrant{}, refuse(Invalid, "egress.grant", "expected a host name or *.domain, optionally with a port")
	}
	return parsedGrant{Host: host, Port: int32(port)}, nil
}

// grantedPorts is every port any grant names, in order: the configuration declares one port ACL per
// such port, once, however many instances are granted it.
func grantedPorts(allocations []parsedAllocation) []int32 {
	seen := map[int32]bool{}
	ports := make([]int32, 0, len(allocations))
	for _, allocation := range allocations {
		for _, grant := range allocation.Grants {
			if !seen[grant.Port] {
				seen[grant.Port] = true
				ports = append(ports, grant.Port)
			}
		}
	}
	slices.Sort(ports)
	return ports
}

// grantPorts is the distinct ports one instance's grants name, in order.
func grantPorts(grants []parsedGrant) []int32 {
	ports := make([]int32, 0, len(grants))
	for _, grant := range grants {
		if len(ports) == 0 || ports[len(ports)-1] != grant.Port {
			ports = append(ports, grant.Port)
		}
	}
	return ports
}

// hostsAt is the hosts one instance's grants allow on a port, in order.
func hostsAt(grants []parsedGrant, port int32) []string {
	hosts := make([]string, 0, len(grants))
	for _, grant := range grants {
		if grant.Port == port {
			hosts = append(hosts, grant.Host)
		}
	}
	return hosts
}

// proxyConfigMap is the ConfigMap the proxy's HelmRelease reads: the values the pinned chart turns
// into the Service's ports and the configuration it mounts. Its values are JSON, which is YAML, so the
// standard library writes them exactly; the chart's own ConfigMap still shows squid.conf as Squid's
// own text.
func proxyConfigMap(allocations []parsedAllocation, profile PlatformProfile) manifest {
	values := manifest{
		"configMaps": manifest{"config": manifest{"data": manifest{"squid.conf": squidConfig(allocations)}}},
		"service":    manifest{"main": manifest{"ports": servicePorts(allocations)}},
	}
	return manifest{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": manifest{"name": proxyConfigName, "namespace": profile.Egress.Namespace,
			// Reloader restarts the proxy when this changes, and the chart's checksum restarts it when
			// Flux changes the values; the label keeps the proxy watching its own configuration.
			"labels": manifest{"reconcile.fluxcd.io/watch": "Enabled"}},
		"data": manifest{proxyValuesKey: string(encodeManifest(values))}}
}

// servicePorts is one Service port per allocation, keyed as the chart expects. The proxy's health port
// is the HelmRelease's own, not a value here.
func servicePorts(allocations []parsedAllocation) manifest {
	ports := manifest{}
	for _, allocation := range sortedByPort(allocations) {
		ports[fmt.Sprintf("p%d", allocation.Port)] = manifest{"port": allocation.Port}
	}
	return ports
}

// squidConfig is the proxy's configuration: a port per instance, which names the caller; the hosts each
// instance's grants allow at each port; and a refusal of everything else, so nothing leaves the cluster
// unlogged and ungranted.
func squidConfig(allocations []parsedAllocation) string {
	lines := []string{
		"# Squid, one port per app instance: every request is logged with the instance that sent it, and",
		"# refused unless a grant of that instance covers its host and port.",
		"pid_filename none",
		"pinger_enable off",
		"cache deny all",
		"via off",
		"forwarded_for delete",
		"shutdown_lifetime 3 seconds",
		proxyLogFormat,
		fmt.Sprintf("http_port %d name=health", proxyHealthPort),
		"acl health myportname health",
		"http_access deny health",
		"# Requests only: the kubelet's probes connect every few seconds and send none.",
		"acl request has request",
		"access_log stdio:/dev/stdout logformat=egress request",
		"cache_log stdio:/dev/stderr",
	}
	for _, port := range grantedPorts(allocations) {
		lines = append(lines, fmt.Sprintf("acl port-%d port %d", port, port))
	}
	byPort := sortedByPort(allocations)
	for _, allocation := range byPort {
		lines = append(lines, instanceLines(allocation)...)
	}
	for _, allocation := range byPort {
		lines = append(lines, allowLines(allocation)...)
	}
	return strings.Join(append(lines, "http_access deny all"), "\n") + "\n"
}

// instanceLines are one instance's port, the name the proxy knows it by, and the hosts each of its
// grants allows, annotated so the log says whether a grant covered a request.
func instanceLines(allocation parsedAllocation) []string {
	lines := []string{
		fmt.Sprintf("http_port %d name=%s", allocation.Port, allocation.Namespace),
		fmt.Sprintf("acl %s myportname %s", allocation.Namespace, allocation.Namespace),
		fmt.Sprintf("note instance %s %s", allocation.Namespace, allocation.Namespace),
	}
	for _, port := range grantPorts(allocation.Grants) {
		lines = append(lines,
			fmt.Sprintf("acl %s-%d dstdomain -n %s", allocation.Namespace, port, strings.Join(hostsAt(allocation.Grants, port), " ")),
			fmt.Sprintf("note granted yes %s %s-%d port-%d", allocation.Namespace, allocation.Namespace, port, port))
	}
	return lines
}

// allowLines are what one instance may do: reach a host its own grant names, on that grant's port.
func allowLines(allocation parsedAllocation) []string {
	lines := make([]string, 0, len(allocation.Grants))
	for _, port := range grantPorts(allocation.Grants) {
		lines = append(lines, fmt.Sprintf("http_access allow %s %s-%d port-%d",
			allocation.Namespace, allocation.Namespace, port, port))
	}
	return lines
}

// proxyPolicy admits each instance's port from that instance's namespace only. It states ingress only,
// as v1's generated policy does: the proxy's own egress is the platform's existing state, and the
// default-deny the operator decided is the instance's (a set of pods the proxy never is).
func proxyPolicy(allocations []parsedAllocation, profile PlatformProfile) manifest {
	ingress := []any{}
	for _, allocation := range sortedByPort(allocations) {
		ingress = append(ingress, manifest{
			"from": []any{manifest{"namespaceSelector": manifest{"matchLabels": manifest{
				"kubernetes.io/metadata.name": allocation.Namespace}}}},
			"ports": []any{manifest{"port": allocation.Port, "protocol": "TCP"}},
		})
	}
	return manifest{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": metadata(proxyPolicyName, profile.Egress.Namespace),
		"spec": manifest{"podSelector": manifest{"matchLabels": labelManifest(profile.Egress.Labels)},
			"policyTypes": []any{"Ingress"}, "ingress": ingress}}
}

// proxyInventory is what the proxy's objects claim: two objects in the platform's namespace, owned by
// the platform rather than by an instance, so a second writer of either is reported.
func proxyInventory(profile PlatformProfile) []Claim {
	return []Claim{
		{Object{"", "ConfigMap", profile.Egress.Namespace, proxyConfigName}, proxyOwner},
		{Object{"networking.k8s.io", "NetworkPolicy", profile.Egress.Namespace, proxyPolicyName}, proxyOwner},
	}
}

// sortedByPort is the allocations in port order, which is the order every rendered list uses.
func sortedByPort(allocations []parsedAllocation) []parsedAllocation {
	sorted := slices.Clone(allocations)
	slices.SortFunc(sorted, func(a, b parsedAllocation) int { return int(a.Port - b.Port) })
	return sorted
}
