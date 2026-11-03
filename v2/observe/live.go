package observe

import (
	"slices"
	"strings"
)

// Condition is a normalized condition without display text or an inferred default.
type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	ObservedGeneration *int64 `json:"observedGeneration"`
}

// FluxFacts preserves unavailable/absent/not-read separately from a present object's missing fields.
// Nil fields mean not stated/read, not false or an empty list. Kubernetes defaults omitted suspend to false;
// this report deliberately preserves whether the API actually stated it.
type FluxFacts struct {
	Status     string      `json:"status"`
	Reason     string      `json:"reason,omitempty"`
	Suspended  *bool       `json:"suspended"`
	Conditions []Condition `json:"conditions"`
}

// WorkloadFacts carries the actual ready count, not the available count used by the readiness rule.
type WorkloadFacts struct {
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	ReadyReplicas *int64 `json:"readyReplicas"`
}

// RunningFacts is the distinct sorted digests of started main containers. Empty means a successful
// pod list showed none; null means the list could not be read. Findings still describe incomplete pods.
type RunningFacts struct {
	Status  string   `json:"status"`
	Reason  string   `json:"reason,omitempty"`
	Digests []string `json:"digests"`
}

// LiveFacts are normalized facts from the reads already used by Explain, never additional API reads.
type LiveFacts struct {
	Unit     FluxFacts     `json:"unit"`
	Release  FluxFacts     `json:"release"`
	Workload WorkloadFacts `json:"workload"`
	Running  RunningFacts  `json:"running"`
}

func liveFacts(f facts) LiveFacts {
	running := RunningFacts{Status: f.pods.list.Status, Reason: f.pods.list.Reason}
	if running.Status == Present {
		running.Digests = []string{}
		for _, image := range f.pods.images {
			_, digest, _ := strings.Cut(image, "@")
			running.Digests = append(running.Digests, digest)
		}
		slices.Sort(running.Digests)
		running.Digests = slices.Compact(running.Digests)
	}
	return LiveFacts{Unit: fluxFacts(f.unit), Release: fluxFacts(f.release),
		Workload: WorkloadFacts{Status: f.workload.read.Status, Reason: f.workload.read.Reason,
			ReadyReplicas: f.workload.object.readyReplicas}, Running: running}
}

func fluxFacts(f fact) FluxFacts {
	result := FluxFacts{Status: f.read.Status, Reason: f.read.Reason}
	if f.read.Status != Present {
		return result
	}
	result.Suspended = f.flux.suspendValue
	if f.flux.conditions != nil {
		result.Conditions = []Condition{}
		for _, c := range f.flux.conditions {
			result.Conditions = append(result.Conditions, Condition(c))
		}
	}
	return result
}

// PromotionDecision is a submission-time decision, not a promise about a later manual merge.
type PromotionDecision struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

// PromotionReadiness requires fresh selected content to match the review and a healthy, current
// observation that proves that content running. No external provider or Python judgement is needed.
func PromotionReadiness(report Report, reviewedDigest string) PromotionDecision {
	if !digestPattern().MatchString(reviewedDigest) {
		return PromotionDecision{Reason: "reviewed_digest_invalid"}
	}
	if report.Target.Digest != reviewedDigest {
		return PromotionDecision{Reason: "reviewed_digest_changed"}
	}
	if report.Currentness.State != Applied && report.Currentness.State != "unaffected" {
		return PromotionDecision{Reason: "revision_not_current"}
	}
	if report.State.Name != StateHealthy {
		return PromotionDecision{Reason: "instance_not_healthy"}
	}
	if report.Live.Running.Status != Present || len(report.Live.Running.Digests) != 1 || report.Live.Running.Digests[0] != reviewedDigest {
		return PromotionDecision{Reason: "reviewed_digest_not_running"}
	}
	return PromotionDecision{Ready: true, Reason: "ready"}
}
