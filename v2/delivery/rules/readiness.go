package rules

// Readiness is what a promotion's submission is judged on: the digest the operator reviewed, the digest the
// source instance selects now, its currentness state, whether it is healthy, and the digests it runs.
type Readiness struct {
	ReviewedDigest, SelectedDigest string
	Currency                       string // a Currentness state: applied, unaffected, pending or unknown
	Healthy                        bool
	Running                        Running
}

// Running is the distinct digests of the started main containers, as read: Status is how the pod list was
// answered, and Digests means something only when it was Present.
type Running struct {
	Status  string
	Digests []string
}

// PromotionDecision is a submission-time decision, not a promise about a later manual merge.
type PromotionDecision struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

// PromotionReadiness requires fresh selected content to match the review and a healthy, current
// observation that proves that content running. No external provider or Python judgement is needed.
func PromotionReadiness(input Readiness) PromotionDecision {
	if !digestPattern().MatchString(input.ReviewedDigest) {
		return PromotionDecision{Reason: "reviewed_digest_invalid"}
	}
	if input.SelectedDigest != input.ReviewedDigest {
		return PromotionDecision{Reason: "reviewed_digest_changed"}
	}
	if input.Currency != "applied" && input.Currency != "unaffected" {
		return PromotionDecision{Reason: "revision_not_current"}
	}
	if !input.Healthy {
		return PromotionDecision{Reason: "instance_not_healthy"}
	}
	running := input.Running
	if running.Status != Present || len(running.Digests) != 1 || running.Digests[0] != input.ReviewedDigest {
		return PromotionDecision{Reason: "reviewed_digest_not_running"}
	}
	return PromotionDecision{Ready: true, Reason: "ready"}
}
