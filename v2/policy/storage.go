package policy

import (
	"encoding/json"
	"slices"
)

type claimSpec struct {
	StorageClassName string
	AccessModes      []string
	Resources        resources
}

func (c *check) dataClaim() string {
	data, present := c.facts.release.Spec.Values.Persistence["data"]
	if !present {
		return ""
	}
	if data.ExistingClaim != "" {
		return data.ExistingClaim
	}
	return c.facts.release.Metadata.Name
}
func (c *check) checkStorage() {
	claim, created := c.dataClaim(), false
	for _, one := range c.objects {
		if one.Kind != "PersistentVolumeClaim" || !c.inNamespace(one.Metadata) {
			continue
		}
		spec := c.checkClaim(one)
		if one.Metadata.Name == claim {
			created = true
			c.checkDataClaim(one, spec)
		}
	}
	data := c.facts.release.Spec.Values.Persistence["data"]
	if claim != "" && data.ExistingClaim == "" && !created {
		c.add("storage", "instance", "compiled data claim is missing")
	}
	if claim != "" {
		c.checkDataMount(claim)
	}
}
func (c *check) checkClaim(one object) claimSpec {
	var spec claimSpec
	if json.Unmarshal(one.Spec, &spec) != nil {
		c.add("storage-class", one.ref(), "invalid claim specification")
		return spec
	}
	if spec.StorageClassName != c.facts.platform.StorageClass || spec.StorageClassName == "" {
		c.add("storage-class", one.ref(), "claims must use the profile storage class")
	}
	return spec
}

func (c *check) checkDataClaim(one object, spec claimSpec) {
	expected := c.facts.release.Spec.Values.Persistence["data"]
	if !slices.Contains(spec.AccessModes, "ReadWriteOnce") {
		c.add("single-writer", one.ref(), "data claim must be ReadWriteOnce")
	}
	actual, ok := amount(spec.Resources.Requests["storage"], false)
	wanted, valid := amount(json.RawMessage(`"`+expected.Size+`"`), false)
	if !ok || !valid || actual != wanted {
		c.add("storage", one.ref(), "data claim size differs from the compiled instance")
	}
}
func (c *check) checkDataMount(claim string) {
	for _, w := range c.workloads {
		if !c.isMain(w) {
			continue
		}
		name := claimVolume(w.pod, claim)
		found := false
		for _, container := range w.pod.Containers {
			if container.Name != "main" {
				continue
			}
			mount, mounted := findMount(container, name, "/data")
			found = name != "" && mounted && !mount.ReadOnly
		}
		if !found {
			c.add("storage", w.ref(), "compiled data volume must be mounted at /data")
		}
	}
}
func claimVolume(p pod, claim string) string {
	for _, volume := range p.Volumes {
		if writableClaim(volume, claim) {
			return volume.Name
		}
	}
	return ""
}

func (c *check) rwoClaims() map[string]bool {
	result := map[string]bool{}
	if name := c.dataClaim(); name != "" {
		result[name] = true
	}
	for _, one := range c.objects {
		if one.Kind != "PersistentVolumeClaim" || !c.inNamespace(one.Metadata) {
			continue
		}
		var spec claimSpec
		if json.Unmarshal(one.Spec, &spec) == nil && slices.Contains(spec.AccessModes, "ReadWriteOnce") {
			result[one.Metadata.Name] = true
		}
	}
	return result
}
func (c *check) checkSingleWriter(w workload) {
	if !c.inNamespace(w.Metadata) {
		return
	}
	rwo := c.rwoClaims()
	for _, volume := range w.pod.Volumes {
		if volume.PersistentVolumeClaim == nil || !rwo[volume.PersistentVolumeClaim.ClaimName] {
			continue
		}
		c.singleWriter(w)
	}
}
func (c *check) singleWriter(w workload) {
	if valueOr(w.spec.Replicas, 1) > 1 {
		c.add("single-writer", w.ref(), "RWO workloads must run at most one replica")
	}
	if w.Kind == "Deployment" && w.spec.Strategy.Type != "Recreate" {
		c.add("single-writer", w.ref(), "RWO Deployment must stop the old pod first")
	}
	if valueOr(w.spec.Parallelism, 1) > 1 || valueOr(w.spec.JobTemplate.Spec.Parallelism, 1) > 1 {
		c.add("single-writer", w.ref(), "RWO jobs must run at most one pod")
	}
	if w.Kind == "CronJob" && w.spec.ConcurrencyPolicy != "Forbid" {
		c.add("single-writer", w.ref(), "RWO CronJob must forbid overlapping runs")
	}
}

func writableClaim(volume volume, claim string) bool {
	ref := volume.PersistentVolumeClaim
	return ref != nil && ref.ClaimName == claim && !ref.ReadOnly
}
