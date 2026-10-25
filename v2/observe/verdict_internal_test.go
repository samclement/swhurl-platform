package observe

import "testing"

// Every outcome the package can report has a sign, in the table the verdict depends on. Adding an
// outcome without classifying it would otherwise turn a negative fact into "unclear" and let an
// instance read as unknown instead of not running, so the sets are asserted here rather than by eye.
func TestEveryOutcomeHasASignedVerdict(t *testing.T) {
	for _, outcome := range []string{Reconciled, Applied, Settled, Present, Running, Ready, AsIntended} {
		if outcomeSign(outcome) != positive {
			t.Errorf("%s is not positive", outcome)
		}
	}
	for _, outcome := range []string{NotReconciled, NotApplied, NotSettled, Absent, Different, NotRunning, NotReady} {
		if outcomeSign(outcome) != negative {
			t.Errorf("%s is not negative", outcome)
		}
	}
	for _, outcome := range []string{Unavailable, NotRead, NotAssessed} {
		if outcomeSign(outcome) != unclear {
			t.Errorf("%s is not unclear", outcome)
		}
	}
	if outcomeSign("reconcile") != unclear {
		t.Error("an unrecognised outcome is not unclear")
	}
}

// The verdict's precedence is stated in the plan: a negative decides, then anything unclear, and only
// all-positive is running.
func TestTheVerdictPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name     string
		outcomes []string
		want     Verdict
	}{
		{"all positive", []string{Reconciled, Applied}, IsRunning},
		{"a negative", []string{Reconciled, Absent}, IsNotRunning},
		{"a negative after an unclear fact", []string{Unavailable, Different}, IsNotRunning},
		{"an unclear fact", []string{Reconciled, NotAssessed}, Unknown},
		{"an unavailable fact", []string{Unavailable}, Unknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			findings := make([]Finding, 0, len(tt.outcomes))
			for _, outcome := range tt.outcomes {
				findings = append(findings, Finding{Field: "unit", Outcome: outcome})
			}
			if got := verdict(findings); got != tt.want {
				t.Errorf("verdict %s, want %s", got, tt.want)
			}
		})
	}
}
