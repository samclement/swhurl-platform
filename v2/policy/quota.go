package policy

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

type capacity struct{ pods, cpu, memory, limit float64 }

func (c *check) checkResources(where string, r resources) {
	for _, value := range []struct {
		raw json.RawMessage
		cpu bool
	}{
		{r.Requests["cpu"], true}, {r.Requests["memory"], false}, {r.Limits["memory"], false},
	} {
		if _, ok := amount(value.raw, value.cpu); !ok {
			c.add("resources", where, "positive CPU/memory requests and memory limit are required")
		}
	}
}

// Kubernetes quantity units used by resources, including numeric JSON CPU and binary/decimal memory.
// The range is bounded before arithmetic; unknown/nonfinite/negative amounts cannot reduce usage.
func amount(raw json.RawMessage, cpu bool) (float64, bool) {
	text := string(raw)
	if len(text) > 0 && text[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return 0, false
		}
	}
	multiplier := 1.0
	if cpu {
		multiplier = 1000
	}
	for _, unit := range []struct {
		suffix string
		value  float64
	}{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
		{"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"m", 0.001}, {"u", 0.000001}, {"n", 0.000000001},
	} {
		if strings.HasSuffix(text, unit.suffix) {
			text = strings.TrimSuffix(text, unit.suffix)
			multiplier *= unit.value
			break
		}
	}
	value, err := strconv.ParseFloat(text, 64)
	value *= multiplier
	if err != nil || !validAmount(value) {
		return 0, false
	}
	return value, true
}
func containerCapacity(r resources) capacity {
	cpu, _ := amount(r.Requests["cpu"], true)
	memory, _ := amount(r.Requests["memory"], false)
	limit, _ := amount(r.Limits["memory"], false)
	return capacity{cpu: cpu, memory: memory, limit: limit}
}
func (c *check) checkQuota() {
	total := capacity{}
	for _, w := range c.workloads {
		used := podCapacity(w.pod)
		copies := c.podCount(w)
		total.pods += copies
		total.cpu += used.cpu * copies
		total.memory += used.memory * copies
		total.limit += used.limit * copies
	}
	q := c.facts.platform
	if total.pods > float64(q.Pods) || total.cpu > float64(q.RequestsCPUMilli) ||
		total.memory > float64(q.RequestsMemoryMiB)*(1<<20) || total.limit > float64(q.LimitsMemoryMiB)*(1<<20) {
		c.add("quota", "instance", "rendered workloads including surge, init containers and backup exceed the profile quota")
	}
}
func podCapacity(p pod) capacity {
	regular, runningInit, initPeak := capacity{}, capacity{}, capacity{}
	for _, container := range p.Containers {
		regular = addCapacity(regular, containerCapacity(container.Resources))
	}
	for _, container := range p.InitContainers {
		used := addCapacity(runningInit, containerCapacity(container.Resources))
		if container.RestartPolicy == "Always" {
			runningInit = used
		}
		initPeak = maxCapacity(initPeak, used)
	}
	return maxCapacity(addCapacity(regular, runningInit), initPeak)
}
func addCapacity(a, b capacity) capacity {
	return capacity{cpu: a.cpu + b.cpu, memory: a.memory + b.memory, limit: a.limit + b.limit}
}
func maxCapacity(a, b capacity) capacity {
	return capacity{cpu: math.Max(a.cpu, b.cpu), memory: math.Max(a.memory, b.memory), limit: math.Max(a.limit, b.limit)}
}

func (c *check) podCount(w workload) float64 {
	switch w.Kind {
	case "DaemonSet":
		c.add("quota", w.ref(), "DaemonSet placement has no bounded replica count")
		return 1
	case "Pod":
		return 1
	case "CronJob":
		return c.cronCopies(w)
	case "Job":
		return c.parallelCopies(w, w.spec.Parallelism)
	}
	count := valueOr(w.spec.Replicas, 1)
	if count < 0 {
		c.add("quota", w.ref(), "replica count cannot be negative")
		return 0
	}
	if w.Kind != "Deployment" || w.spec.Strategy.Type == "Recreate" {
		return float64(count)
	}
	surge, valid := maxSurge(w.spec.Strategy.RollingUpdate.MaxSurge, count)
	if !valid {
		c.add("quota", w.ref(), "invalid rolling-update surge")
		return float64(count)
	}
	return float64(count) + surge
}
func maxSurge(raw json.RawMessage, replicas int64) (float64, bool) {
	if len(raw) == 0 {
		return math.Ceil(float64(replicas) * 0.25), true
	}
	text := string(raw)
	if raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return 0, false
		}
	}
	if strings.HasSuffix(text, "%") {
		percent, err := strconv.ParseFloat(strings.TrimSuffix(text, "%"), 64)
		return math.Ceil(float64(replicas) * percent / 100), err == nil && percent >= 0 && percent <= 100
	}
	number, err := strconv.ParseInt(text, 10, 32)
	return float64(number), err == nil && number >= 0
}

func (c *check) parallelCopies(w workload, parallelism *int64) float64 {
	count := valueOr(parallelism, 1)
	if count < 0 {
		c.add("quota", w.ref(), "parallelism cannot be negative")
		return 0
	}
	return float64(count)
}

func validAmount(value float64) bool { return value > 0 && value < 1e18 && !math.IsNaN(value) }

func (c *check) checkQuotaObjects() {
	count := 0
	for _, one := range c.objects {
		if one.Kind != "ResourceQuota" || !c.inNamespace(one.Metadata) {
			continue
		}
		count++
		var actual struct{ Hard map[string]json.RawMessage }
		if json.Unmarshal(one.Spec, &actual) != nil {
			c.add("quota", one.ref(), "invalid namespace quota")
			continue
		}
		c.checkQuotaHard(one, actual.Hard)
	}
	if count != 1 {
		c.add("quota", "instance", "exactly one profile-sized namespace quota is required")
	}
}
func (c *check) checkQuotaHard(one object, hard map[string]json.RawMessage) {
	q := c.facts.platform
	for _, expected := range []struct {
		key    string
		amount float64
		cpu    bool
	}{
		{"pods", float64(q.Pods), false}, {"requests.cpu", float64(q.RequestsCPUMilli), true},
		{"requests.memory", float64(q.RequestsMemoryMiB) * (1 << 20), false}, {"limits.memory", float64(q.LimitsMemoryMiB) * (1 << 20), false},
	} {
		actual, valid := amount(hard[expected.key], expected.cpu)
		if !valid || actual != expected.amount {
			c.add("quota", one.ref(), "namespace quota must match the explicit profile")
		}
	}
}

func (c *check) cronCopies(w workload) float64 {
	if w.spec.ConcurrencyPolicy != "Forbid" {
		c.add("quota", w.ref(), "CronJob must forbid overlap to have bounded resource usage")
	}
	return c.parallelCopies(w, w.spec.JobTemplate.Spec.Parallelism)
}
