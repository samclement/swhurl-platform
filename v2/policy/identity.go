package policy

import (
	"encoding/json"
	"strings"
)

func hostIP(entry environment) bool {
	var source struct{ FieldRef struct{ FieldPath string } }
	return json.Unmarshal(entry.ValueFrom, &source) == nil && source.FieldRef.FieldPath == "status.hostIP"
}
func envValue(entry environment) any {
	if entry.Value != nil {
		return *entry.Value
	}
	return map[string]json.RawMessage{"valueFrom": entry.ValueFrom}
}
func (c *check) checkEnvironment(w workload, container container) {
	found := map[string]environment{}
	for _, env := range container.Env {
		found[env.Name] = env
	}
	for name, expected := range c.facts.main.Env {
		actual, present := found[name]
		if !present || !sameJSON(expected, envValue(actual)) {
			if name == "HOST_IP" && hostIP(actual) {
				continue
			}
			c.add(environmentRule(name), w.ref(), "application environment differs from compiled facts")
		}
	}
	c.checkExtraEnvironment(w, found)
	c.checkEventSeed(w, container.EnvFrom)
	if !sameJSON(container.EnvFrom, c.facts.main.EnvFrom) {
		c.add("identity", w.ref(), "environment references differ from compiled facts")
	}
}
func (c *check) checkExtraEnvironment(w workload, found map[string]environment) {
	for name := range found {
		if !strings.HasPrefix(name, "SWHURL_") {
			continue
		}
		if _, expected := c.facts.main.Env[name]; !expected {
			c.add(environmentRule(name), w.ref(), "undeclared platform environment variable")
		}
	}
}

func environmentRule(name string) string {
	if strings.HasPrefix(name, "SWHURL_EVENTS_") {
		return "events"
	}
	return "identity"
}
func (c *check) checkMaterial(w workload, container container) {
	for _, expected := range c.facts.release.Spec.Values.Persistence {
		if expected.Type != "configMap" {
			continue
		}
		for _, wanted := range expected.GlobalMounts {
			if !configMapMounted(w.pod, container, expected.Name, wanted.Path) {
				c.add("identity", w.ref(), "compiled verification files must be mounted read-only")
			}
		}
	}
}
func configMapMounted(p pod, container container, name, path string) bool {
	for _, volume := range p.Volumes {
		if volume.ConfigMap == nil || volume.ConfigMap.Name != name {
			continue
		}
		mount, found := findMount(container, volume.Name, path)
		if found && wholeReadOnlyMount(mount) {
			return true
		}
	}
	return false
}
func findMount(container container, name, path string) (mount, bool) {
	for _, mount := range container.VolumeMounts {
		if mount.Name == name && mount.MountPath == path {
			return mount, true
		}
	}
	return mount{}, false
}

func tokens(p pod) []token {
	result := []token{}
	for _, volume := range p.Volumes {
		for _, source := range volume.Projected.Sources {
			if source.ServiceAccountToken != nil {
				result = append(result, *source.ServiceAccountToken)
			}
		}
	}
	return result
}
func (c *check) checkTokens(w workload) {
	actual := tokens(w.pod)
	if c.isBackup(w) {
		if len(actual) != 0 {
			c.add("call-tokens", w.ref(), "backup must carry no service-account token")
		}
		return
	}
	expected := []token{}
	for _, source := range c.facts.release.Spec.Values.Persistence["tokens"].Sources {
		if source.ServiceAccountToken != nil {
			expected = append(expected, *source.ServiceAccountToken)
		}
	}
	if !sameJSON(actual, expected) {
		c.add("call-tokens", w.ref(), "tokens must exactly match resolved audiences, paths and lifetimes")
	}
	for _, token := range actual {
		c.checkTokenMount(w, token)
	}
}
func (c *check) checkTokenMount(w workload, wanted token) {
	name := tokenVolume(w.pod, wanted)
	for _, container := range w.pod.Containers {
		mount, present := findMount(container, name, "/var/run/swhurl/tokens")
		if name == "" || !present || !wholeReadOnlyMount(mount) {
			c.add("call-tokens", w.ref(), "token volume must be mounted read-only at the platform path")
		}
	}
}
func tokenVolume(p pod, wanted token) string {
	for _, volume := range p.Volumes {
		for _, source := range volume.Projected.Sources {
			if source.ServiceAccountToken != nil && *source.ServiceAccountToken == wanted {
				return volume.Name
			}
		}
	}
	return ""
}

func (c *check) checkEventSeed(w workload, actual []envSource) {
	for _, expected := range c.facts.main.EnvFrom {
		if expected.SecretRef == nil {
			continue
		}
		found := false
		for _, source := range actual {
			if sameJSON(source.SecretRef, expected.SecretRef) {
				found = true
			}
		}
		if !found {
			c.add("events", w.ref(), "compiled broker seed Secret reference is missing")
		}
	}
}

func wholeReadOnlyMount(mount mount) bool {
	return mount.ReadOnly && mount.SubPath == "" && mount.SubPathExpr == ""
}
