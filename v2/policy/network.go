package policy

import "encoding/json"

func (c *check) checkNetworkPolicies() {
	expected := map[string]json.RawMessage{}
	for _, one := range c.facts.source {
		if one.Kind == "NetworkPolicy" {
			expected[c.networkIdentity(one)] = one.Spec
		}
	}
	for _, one := range c.objects {
		if one.Kind != "NetworkPolicy" {
			continue
		}
		wanted, declared := expected[c.networkIdentity(one)]
		if !declared || !sameJSON(one.Spec, wanted) {
			c.add("network-policy", one.ref(), "network access must match the compiled resolved relationships")
		}
		var actual struct {
			PodSelector struct{ MatchLabels map[string]string }
		}
		if json.Unmarshal(one.Spec, &actual) != nil || len(actual.PodSelector.MatchLabels) == 0 {
			c.add("network-policy", one.ref(), "policies must select workload labels, not every pod in the namespace")
		}
	}
}

func (c *check) networkIdentity(one object) string {
	namespace := one.Metadata.Namespace
	if namespace == "" {
		namespace = c.facts.release.Metadata.Namespace
	}
	return namespace + "/" + one.ref()
}
