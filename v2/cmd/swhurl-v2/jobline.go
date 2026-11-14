package main

import (
	"fmt"

	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// writeJobEvent writes one job state change: the audit words the console has always logged, and the fields a
// dashboard counts — the kind of job, its outcome and, once it ended, how long it took.
func writeJobEvent(log *jsonLog, event orchestration.JobEvent) {
	mark, level := "[AUDIT]", "INFO"
	if event.State == "failed" || event.State == "interrupted" {
		mark, level = "[ERROR]", "ERROR"
	}
	message := fmt.Sprintf("%s %s %s %s: %s (job %d)", mark, event.Identity, event.Action, event.Target,
		event.State, event.ID)
	fields := []logField{{"event", "job"}, {"action", event.Action}, {"target", event.Target},
		{"state", event.State}, {"job", event.ID}}
	if event.State != "started" {
		fields = append(fields, logField{"duration_ms", event.Duration.Milliseconds()})
		if event.Link != "" {
			message += " " + event.Link
		}
	}
	log.writeFields(level, message, fields...)
}
