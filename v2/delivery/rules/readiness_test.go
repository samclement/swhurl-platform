package rules

import (
	"strings"
	"testing"
)

// Each refusal names the first check that fails, in order: the review's shape, the selection moving since,
// the revision not landed, the instance unhealthy, and the reviewed content not proved running.
func TestPromotionReadiness(t *testing.T) {
	reviewed := "sha256:" + strings.Repeat("b", 64)
	other := "sha256:" + strings.Repeat("a", 64)
	ready := Readiness{ReviewedDigest: reviewed, SelectedDigest: reviewed, Currency: "applied", Healthy: true,
		Running: Running{Status: Present, Digests: []string{reviewed}}}
	for _, test := range []struct {
		name   string
		change func(*Readiness)
		reason string
	}{
		{"applied", func(*Readiness) {}, "ready"},
		{"unaffected", func(r *Readiness) { r.Currency = "unaffected" }, "ready"},
		{"empty review", func(r *Readiness) { r.ReviewedDigest = "" }, "reviewed_digest_invalid"},
		{"short review", func(r *Readiness) { r.ReviewedDigest = "sha256:short" }, "reviewed_digest_invalid"},
		{"selection moved", func(r *Readiness) { r.SelectedDigest = other }, "reviewed_digest_changed"},
		{"pending", func(r *Readiness) { r.Currency = "pending" }, "revision_not_current"},
		{"unknown", func(r *Readiness) { r.Currency = "unknown" }, "revision_not_current"},
		{"unhealthy", func(r *Readiness) { r.Healthy = false }, "instance_not_healthy"},
		{"pods unread", func(r *Readiness) { r.Running = Running{Status: Unavailable} }, "reviewed_digest_not_running"},
		{"none running", func(r *Readiness) { r.Running.Digests = []string{} }, "reviewed_digest_not_running"},
		{"other running", func(r *Readiness) { r.Running.Digests = []string{other} }, "reviewed_digest_not_running"},
		{"two running", func(r *Readiness) { r.Running.Digests = []string{other, reviewed} }, "reviewed_digest_not_running"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := ready
			input.Running.Digests = append([]string{}, ready.Running.Digests...)
			test.change(&input)
			got := PromotionReadiness(input)
			if got.Reason != test.reason || got.Ready != (test.reason == "ready") {
				t.Fatalf("%+v", got)
			}
		})
	}
}
