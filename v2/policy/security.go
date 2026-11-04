package policy

import (
	"regexp"
	"slices"
	"strings"
)

func (c *check) checkPod(w workload) {
	pod := w.pod
	if !isFalse(pod.AutomountServiceAccountToken) {
		c.add("no-sa-token", w.ref(), "automountServiceAccountToken must be false")
	}
	if pod.HostNetwork || pod.HostPID || pod.HostIPC {
		c.add("no-host-access", w.ref(), "host namespaces are forbidden")
	}
	for _, volume := range pod.Volumes {
		if len(volume.HostPath) > 0 {
			c.add("no-host-access", w.ref(), "hostPath volumes are forbidden")
		}
	}
	for _, container := range append(slices.Clone(pod.InitContainers), pod.Containers...) {
		c.checkContainer(w, container)
	}
}
func (c *check) checkContainer(w workload, container container) {
	where := w.ref() + "/" + container.Name
	ctx := container.SecurityContext
	if !effectiveNonRoot(ctx, w.pod.SecurityContext) {
		c.add("non-root", where, "runAsNonRoot must be true without a root UID")
	}
	if ctx.Privileged || !isFalse(ctx.AllowPrivilegeEscalation) {
		c.add("no-escalation", where, "privilege escalation is forbidden")
	}
	if len(ctx.Capabilities.Add) > 0 || !slices.Contains(ctx.Capabilities.Drop, "ALL") {
		c.add("no-escalation", where, "drop ALL capabilities and add none")
	}
	c.checkImage(where, container.Image)
	c.checkResources(where, container.Resources)
	c.checkOrdering(where, container.Env)
}
func (c *check) checkImage(where, image string) {
	digest := regexp.MustCompile(`@sha256:[0-9a-fA-F]{64}$`).MatchString(image)
	leaf := image[strings.LastIndex(image, "/")+1:]
	_, tag, _ := strings.Cut(leaf, ":")
	if !digest && (tag == "" || tag == "latest" || strings.Contains(image, "@")) {
		c.add("image-pinned", where, "image needs a version tag or digest")
	}
	if c.facts.environment == "prod" && !digest {
		c.add("prod-digest", where, "production images must pin a digest")
	}
}
func (c *check) checkOrdering(where string, env []environment) {
	defined := map[string]environment{}
	for _, entry := range env {
		if _, duplicate := defined[entry.Name]; duplicate {
			c.add("identity", where, "duplicate environment variable")
		}
		if entry.Value != nil {
			c.orderingUse(where, *entry.Value, defined)
		}
		defined[entry.Name] = entry
	}
}
func (c *check) orderingUse(where, value string, defined map[string]environment) {
	if strings.Contains(value, "$(HOST_IP)") {
		if !sameJSON(defined["HOST_IP"].ValueFrom, map[string]any{"fieldRef": map[string]any{"fieldPath": "status.hostIP"}}) {
			// The API version is optional on a downward-API field reference; test its semantic path below.
			if !hostIP(defined["HOST_IP"]) {
				c.add("otlp-host-ip", where, "HOST_IP must come from status.hostIP before use")
			}
		}
	}
	if strings.Contains(value, "$(SWHURL_ENV)") && defined["SWHURL_ENV"].Value == nil {
		c.add("identity", where, "SWHURL_ENV must be defined before use")
	}
}

func effectiveNonRoot(container, pod security) bool {
	nonRoot, uid := container.RunAsNonRoot, container.RunAsUser
	if nonRoot == nil {
		nonRoot = pod.RunAsNonRoot
	}
	if uid == nil {
		uid = pod.RunAsUser
	}
	return isTrue(nonRoot) && (uid == nil || *uid > 0)
}
