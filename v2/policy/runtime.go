package policy

import (
	"encoding/json"
	"strings"
)

type httpRequest struct {
	Path, Scheme, Host string
	Port               json.RawMessage
	HTTPHeaders        []struct{ Name, Value string }
}
type probe struct {
	HTTPGet                         httpRequest
	PeriodSeconds, FailureThreshold int64
}

type service struct {
	Type        string
	ExternalIPs []string
	Selector    map[string]string
	Ports       []struct {
		Port       int64
		TargetPort json.RawMessage
	}
}

func (c *check) httpPort() int64 {
	return c.facts.release.Spec.Values.Service["main"].Ports["http"].Port
}
func (c *check) checkPodRuntime(w workload, container container) {
	wanted := c.facts.release.Spec.Values.DefaultPodOptions.SecurityContext.RunAsUser
	actual := container.SecurityContext.RunAsUser
	if actual == nil {
		actual = w.pod.SecurityContext.RunAsUser
	}
	if wanted != nil && !sameJSON(wanted, actual) {
		c.add("runtime", w.ref(), "application UID differs from the compiled runtime")
	}
	c.checkProbes(w, container)
}

func (c *check) checkProbes(w workload, container container) {
	actual := map[string]probe{"readiness": container.ReadinessProbe, "liveness": container.LivenessProbe, "startup": container.StartupProbe}
	for name, wanted := range c.facts.main.Probes {
		if !wanted.Enabled {
			continue
		}
		found := actual[name]
		if !sameHTTPRequest(found.HTTPGet, wanted.Spec.HTTPGet) || !explicitProbeSettings(found, wanted.Spec) {
			c.add("runtime", w.ref(), "application probes differ from the compiled health contract")
		}
	}
}
func explicitProbeSettings(actual, expected probe) bool {
	period := expected.PeriodSeconds == 0 || expected.PeriodSeconds == actual.PeriodSeconds
	failure := expected.FailureThreshold == 0 || expected.FailureThreshold == actual.FailureThreshold
	return period && failure
}
func (c *check) checkServices() {
	count := 0
	for _, one := range c.objects {
		if one.Kind != "Service" || !c.inNamespace(one.Metadata) {
			continue
		}
		count++
		var actual service
		if json.Unmarshal(one.Spec, &actual) != nil {
			c.add("runtime", one.ref(), "invalid service specification")
			continue
		}
		c.checkService(one, actual)
	}
	expected := 0
	if c.httpPort() > 0 {
		expected = 1
	}
	if count != expected {
		c.add("runtime", "instance", "services must match the compiled HTTP runtime")
	}
}
func (c *check) checkService(one object, actual service) {
	if serviceExternal(actual) {
		c.add("exposure", one.ref(), "application services must stay inside the cluster")
	}
	if one.Metadata.Name != c.facts.release.Metadata.Name || len(actual.Ports) != 1 {
		c.add("runtime", one.ref(), "service must match the compiled application")
		return
	}
	if !c.serviceSelectsMain(actual.Selector) {
		c.add("runtime", one.ref(), "service selector must select the intended application pods")
	}
	port := actual.Ports[0]
	correctTarget := c.serviceTarget(port.TargetPort)
	if c.httpPort() == 0 || port.Port != c.httpPort() || !correctTarget {
		c.add("runtime", one.ref(), "service port differs from the compiled runtime")
	}
}

func (c *check) serviceTarget(target json.RawMessage) bool {
	numeric, _ := json.Marshal(c.httpPort())
	if sameJSON(target, json.RawMessage(numeric)) {
		return true
	}
	if !sameJSON(target, json.RawMessage(`"http"`)) {
		return false
	}
	for _, w := range c.workloads {
		if !c.isMain(w) {
			continue
		}
		for _, container := range w.pod.Containers {
			if hasHTTPPort(container, c.httpPort()) {
				return true
			}
		}
	}
	return false
}
func hasHTTPPort(container container, expected int64) bool {
	for _, port := range container.Ports {
		if port.Name == "http" && port.ContainerPort == expected {
			return true
		}
	}
	return false
}

func serviceExternal(actual service) bool {
	return len(actual.ExternalIPs) > 0 || (actual.Type != "" && actual.Type != "ClusterIP")
}
func (c *check) serviceSelectsMain(selector map[string]string) bool {
	for _, w := range c.workloads {
		if c.isMain(w) && selects(selector, w.labels) {
			return true
		}
	}
	return false
}
func sameHTTPRequest(actual, expected httpRequest) bool {
	if actual.Scheme == "" {
		actual.Scheme = "HTTP"
	}
	if expected.Scheme == "" {
		expected.Scheme = "HTTP"
	}
	if actual.Path == "" {
		actual.Path = "/"
	}
	if expected.Path == "" {
		expected.Path = "/"
	}
	return actual.Scheme == expected.Scheme && actual.Host == expected.Host && actual.Path == expected.Path &&
		sameJSON(actual.Port, expected.Port) && sameJSON(requestHeaders(actual), requestHeaders(expected))
}

// Kubelet supplies Accept */* and a version-dependent User-Agent when absent. Explicit default Accept is
// equivalent; an empty override or a fixed User-Agent is not. Header names ignore case; same-name values
// retain order. Host headers remain distinct from the connection-host override in HTTPGet.host.
func requestHeaders(request httpRequest) map[string][]string {
	result := map[string][]string{}
	for _, header := range request.HTTPHeaders {
		name := strings.ToLower(header.Name)
		result[name] = append(result[name], header.Value)
	}
	accept := result["accept"]
	if len(accept) == 1 && accept[0] == "*/*" {
		delete(result, "accept")
	}
	return result
}
