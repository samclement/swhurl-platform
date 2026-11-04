package policy

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// Platform is the policy's limits and route/storage constraints, supplied from the validated profile.
// It is not another record model: source objects and chart values come from the existing compilation.
type Platform struct {
	Pods, RequestsCPUMilli, RequestsMemoryMiB, LimitsMemoryMiB int64
	StorageClass, CookieDomain, Middleware                     string
}

// Finding identifies a rule and location with fixed diagnostic text, never a container/env/Secret value.
type Finding struct {
	Rule   string `json:"rule"`
	Object string `json:"object"`
	Detail string `json:"detail"`
}

type check struct {
	facts     facts
	objects   []object
	workloads []workload
	findings  []Finding
}

func (c *check) add(rule, where, detail string) {
	c.findings = append(c.findings, Finding{rule, where, detail})
}

// Check inspects chart stdout plus the compiler's non-Helm resources. Missing workloads are violations,
// malformed typed objects are errors, and all findings are accumulated in deterministic order.
func Check(source []json.RawMessage, platform Platform, rendered []json.RawMessage) ([]Finding, error) {
	facts, err := compilationFacts(source, platform)
	if err != nil {
		return nil, err
	}
	objects, err := decodeObjects(rendered)
	if err != nil {
		return nil, err
	}
	c := check{facts: facts, objects: append(facts.source, objects...), findings: []Finding{}}
	c.checkNamespaces(objects)
	if err := c.readWorkloads(); err != nil {
		return nil, err
	}
	c.checkMain()
	c.checkStorage()
	c.checkRoutes()
	c.checkEvents()
	c.checkQuota()
	c.checkServices()
	c.checkQuotaObjects()
	c.checkNetworkPolicies()
	for _, w := range c.workloads {
		c.checkPod(w)
		c.checkSingleWriter(w)
		c.checkTokens(w)
	}
	slices.SortFunc(c.findings, func(a, b Finding) int {
		return strings.Compare(a.Rule+"\x00"+a.Object+"\x00"+a.Detail, b.Rule+"\x00"+b.Object+"\x00"+b.Detail)
	})
	return slices.Compact(c.findings), nil
}

func (c *check) readWorkloads() error {
	seen := map[string]bool{}
	for _, one := range c.objects {
		key := one.APIVersion + "/" + one.ref() + "/" + one.Metadata.Namespace
		if seen[key] {
			c.add("objects", one.ref(), "duplicate rendered object identity")
		}
		seen[key] = true
		w, yes, err := podOf(one)
		if err != nil {
			return errors.New("invalid_policy_workload")
		}
		if yes {
			c.workloads = append(c.workloads, w)
		}
	}
	return nil
}
func (c *check) isMain(w workload) bool {
	return w.Kind == "Deployment" && w.Metadata.Name == c.facts.release.Metadata.Name && c.inNamespace(w.Metadata)
}
func (c *check) isBackup(w workload) bool {
	return c.facts.backup.Name != "" && w.Kind == "CronJob" && w.Metadata.Name == c.facts.backup.Name && c.inNamespace(c.facts.backup) && c.inNamespace(w.Metadata)
}
func (c *check) checkMain() {
	count := 0
	for _, w := range c.workloads {
		if c.isMain(w) {
			count++
			c.checkRuntime(w)
			c.checkSelector(w)
		}
		if len(w.pod.Containers) == 0 {
			c.add("workload", w.ref(), "workload has no containers")
		}
	}
	if count != 1 {
		c.add("workload", "instance", "exactly one compiled application Deployment is required")
	}
}
func (c *check) checkRuntime(w workload) {
	expected := c.facts.release.Spec.Values.Controllers["main"]
	if valueOr(w.spec.Replicas, 1) != expected.Replicas {
		c.add("runtime", w.ref(), "replicas differ from the compiled instance")
	}
	count := 0
	for _, container := range w.pod.Containers {
		if container.Name != "main" {
			continue
		}
		count++
		wanted := c.facts.main.Image.Repository + "@" + c.facts.main.Image.Digest
		if container.Image != wanted {
			c.add("runtime", w.ref(), "application image differs from the compiled release")
		}
		if !sameJSON(container.Resources, c.facts.main.Resources) {
			c.add("runtime", w.ref(), "application resources differ from the compiled instance")
		}
		c.checkEnvironment(w, container)
		c.checkPodRuntime(w, container)
		c.checkMaterial(w, container)
	}
	if count != 1 {
		c.add("runtime", w.ref(), "exactly one main application container is required")
	}
}
func (c *check) checkSelector(w workload) {
	for _, one := range c.facts.source {
		if one.Kind == "NetworkPolicy" && one.Metadata.Name == c.facts.release.Metadata.Name && c.inNamespace(one.Metadata) {
			c.checkPodSelector(w, one)
			return
		}
	}
	c.add("network-policy", w.ref(), "compiled policy for the application namespace is missing")
}
func (c *check) checkPodSelector(w workload, one object) {
	var policy struct {
		PodSelector struct{ MatchLabels map[string]string }
	}
	if json.Unmarshal(one.Spec, &policy) != nil || !selects(policy.PodSelector.MatchLabels, w.labels) {
		c.add("network-policy", w.ref(), "chart pod labels do not match the compiled policy selector")
	}
}

// Omitted namespaces on known namespaced relationships use Helm's target namespace. Unknown kinds are
// not assigned a scope or a namespace; only an explicit foreign namespace is rejected for any object.
func (c *check) inNamespace(meta metadata) bool {
	return meta.Namespace == "" || meta.Namespace == c.facts.release.Metadata.Namespace
}

// Compiler-owned Flux image automation can live outside the app namespace. Only its known instance
// resources are scoped here; every explicit chart-output namespace must match the target.
func (c *check) checkNamespaces(rendered []object) {
	for _, one := range c.facts.source {
		if instanceResource(one.Kind) {
			c.checkNamespace(one)
		}
	}
	for _, one := range rendered {
		c.checkNamespace(one)
	}
}
func instanceResource(kind string) bool {
	switch kind {
	case "Pod", "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Service", "PersistentVolumeClaim",
		"NetworkPolicy", "Ingress", "ResourceQuota", "ConfigMap", "Secret", "ServiceAccount", "Stream", "Consumer":
		return true
	}
	return false
}
func (c *check) checkNamespace(one object) {
	if !c.inNamespace(one.Metadata) {
		c.add("identity", one.ref(), "object is outside its compiled namespace")
	}
}
func selects(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, wanted := range selector {
		actual, present := labels[key]
		if !present || actual != wanted {
			return false
		}
	}
	return true
}

func valueOr(value *int64, fallback int64) int64 {
	if value != nil {
		return *value
	}
	return fallback
}
func isTrue(value *bool) bool  { return value != nil && *value }
func isFalse(value *bool) bool { return value != nil && !*value }
