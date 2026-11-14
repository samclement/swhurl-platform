package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// A job's lines: the audit words the console has always written, and the fields a dashboard counts. A start has no
// duration; an end has its outcome, how long it ran and the pull request it opened.
func TestAJobLineCarriesItsKindOutcomeAndDuration(t *testing.T) {
	var out bytes.Buffer
	log := requestLogger(&out, "console")
	writeJobEvent(log, orchestration.JobEvent{ID: 7, Action: "scale", Target: "hello-ts/staging",
		Identity: "sam@swhurl.com", State: "started"})
	writeJobEvent(log, orchestration.JobEvent{ID: 7, Action: "scale", Target: "hello-ts/staging",
		Identity: "sam@swhurl.com", State: "succeeded", Link: "https://github.com/owner/repo/pull/42",
		Duration: 2500 * time.Millisecond})
	writeJobEvent(log, orchestration.JobEvent{ID: 8, Action: "reconcile", Target: "gw",
		Identity: "sam@swhurl.com", State: "failed", Duration: 90 * time.Second})
	writeJobEvent(log, orchestration.JobEvent{ID: 9, Action: "reconcile", Target: "gw",
		Identity: "sam@swhurl.com", State: "interrupted", Duration: 3 * time.Minute})

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines %q", lines)
	}
	started := line(t, lines[0])
	if started["level"] != "INFO" || started["event"] != "job" || started["action"] != "scale" ||
		started["state"] != "started" || started["job"] != float64(7) {
		t.Fatalf("started %v", started)
	}
	if _, set := started["duration_ms"]; set {
		t.Fatalf("a start states a duration: %v", started)
	}
	if started["message"] != "[AUDIT] sam@swhurl.com scale hello-ts/staging: started (job 7)" {
		t.Fatalf("message %q", started["message"])
	}

	ended := line(t, lines[1])
	if ended["level"] != "INFO" || ended["state"] != "succeeded" || ended["duration_ms"] != float64(2500) {
		t.Fatalf("ended %v", ended)
	}
	if ended["message"] != "[AUDIT] sam@swhurl.com scale hello-ts/staging: succeeded (job 7) "+
		"https://github.com/owner/repo/pull/42" {
		t.Fatalf("message %q", ended["message"])
	}

	for _, text := range lines[2:] {
		failed := line(t, text)
		if failed["level"] != "ERROR" || failed["state"] == "started" || failed["duration_ms"] == nil ||
			!strings.HasPrefix(failed["message"].(string), "[ERROR] sam@swhurl.com reconcile gw: ") {
			t.Fatalf("failure %v", failed)
		}
	}
}
