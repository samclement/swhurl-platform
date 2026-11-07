package heartbeat_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/heartbeat"
)

// now is 3 October 2026, 12:00 UTC, the Python tests' clock.
const now = 1791028800.0

// cronjob is a CronJob document that last succeeded age seconds ago, as Python's isoformat writes the time; a nil
// age is a job with no success yet.
func cronjob(t *testing.T, age *float64, suspended bool) []byte {
	t.Helper()
	status := map[string]any{}
	if age != nil {
		at := time.Unix(0, int64((now-*age)*1e9)).UTC()
		status["lastSuccessfulTime"] = at.Format("2006-01-02T15:04:05-07:00")
	}
	document, err := json.Marshal(map[string]any{"spec": map[string]any{"suspend": suspended}, "status": status})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func ago(seconds float64) *float64 { return &seconds }

var (
	empty    = heartbeat.Flag{}
	alerting = heartbeat.Flag{Alerting: true, Since: now, LastAlert: now}
)

func decide(t *testing.T, document []byte, flag heartbeat.Flag, at, maxAge float64) heartbeat.Decision {
	t.Helper()
	decision, err := heartbeat.Decide(document, flag, at, maxAge)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func TestFreshMissingSuspendedAndOldChecker(t *testing.T) {
	if got := decide(t, cronjob(t, ago(60), false), empty, now, 600); got.Action != heartbeat.None {
		t.Fatalf("fresh: %+v", got)
	}
	if got := decide(t, nil, empty, now, 600); got.Action != heartbeat.Alert || got.Reason != "CronJob is missing" {
		t.Fatalf("missing: %+v", got)
	}
	if got := decide(t, cronjob(t, ago(60), true), empty, now, 600); got.Action != heartbeat.Alert || got.Reason != "CronJob is suspended" {
		t.Fatalf("suspended: %+v", got)
	}
	if got := decide(t, cronjob(t, ago(601), false), empty, now, 600); got.Action != heartbeat.Alert {
		t.Fatalf("old: %+v", got)
	}
}

func TestReminderIsHourlyAndRecoveryRequiresAPriorAlert(t *testing.T) {
	if got := decide(t, cronjob(t, nil, false), alerting, now+1800, 600); got.Action != heartbeat.None {
		t.Fatalf("half an hour on: %+v", got)
	}
	reminded := decide(t, cronjob(t, nil, false), alerting, now+3600, 600)
	if reminded.Action != heartbeat.Remind || reminded.Next != (heartbeat.Flag{Alerting: true, Since: now, LastAlert: now + 3600}) {
		t.Fatalf("an hour on: %+v", reminded)
	}
	recovered := decide(t, cronjob(t, ago(60), false), alerting, now+120, 600)
	if recovered.Action != heartbeat.Recover || recovered.Next != empty {
		t.Fatalf("recovered: %+v", recovered)
	}
	if got := decide(t, cronjob(t, ago(60), false), empty, now+120, 600); got.Action != heartbeat.None {
		t.Fatalf("fresh without an alert: %+v", got)
	}
}

func TestFreshnessBoundaryFutureSuccessAndInvalidTimestamp(t *testing.T) {
	for _, age := range []float64{600, -60} {
		if got := decide(t, cronjob(t, ago(age), false), empty, now, 600); got != (heartbeat.Decision{Action: heartbeat.None, Next: empty}) {
			t.Fatalf("age %v: %+v", age, got)
		}
	}
	// Python's fromisoformat also read a time without an offset, as local time; Kubernetes always writes one, and Go
	// reads RFC 3339 only.
	for _, last := range []string{`"not-a-time"`, `123`, `{"invalid":true}`, `"2026-10-03T11:59:00"`} {
		got := decide(t, []byte(`{"status":{"lastSuccessfulTime":`+last+`}}`), empty, now, 600)
		if got.Action != heartbeat.Alert || got.Reason != "last successful time is invalid" {
			t.Fatalf("%s: %+v", last, got)
		}
	}
	// What Python read as false is no success at all.
	for _, last := range []string{`null`, `""`, `0`, `false`, `[]`, `{}`} {
		got := decide(t, []byte(`{"status":{"lastSuccessfulTime":`+last+`}}`), empty, now, 600)
		if got.Reason != "no successful Job has completed" {
			t.Fatalf("%s: %+v", last, got)
		}
	}
	if got := decide(t, cronjob(t, nil, false), empty, now, 600); got.Action != heartbeat.Alert || got.Reason != "no successful Job has completed" {
		t.Fatalf("no success: %+v", got)
	}
	if got := decide(t, cronjob(t, ago(601), false), empty, now, 600); got.Reason != "last success was 10 minutes ago" {
		t.Fatalf("old: %+v", got)
	}
	if got := decide(t, []byte(`{"status":{"lastSuccessfulTime":"2026-10-03T11:49:59Z"}}`), empty, now, 600); got.Reason != "last success was 10 minutes ago" {
		t.Fatalf("Kubernetes' own spelling: %+v", got)
	}
}

// A decision returns a new flag; the one it was given is a value and is never changed.
func TestTransitionsLeaveIncomingStateUnchanged(t *testing.T) {
	for _, tt := range []struct {
		document []byte
		flag     heartbeat.Flag
		at       float64
		want     heartbeat.Action
	}{
		{cronjob(t, ago(60), false), empty, now, heartbeat.None},
		{nil, empty, now, heartbeat.Alert},
		{nil, alerting, now + heartbeat.ReminderSeconds - 1, heartbeat.None},
		{nil, alerting, now + heartbeat.ReminderSeconds, heartbeat.Remind},
		{cronjob(t, ago(60), false), alerting, now, heartbeat.Recover},
	} {
		before := tt.flag
		got := decide(t, tt.document, tt.flag, tt.at, 600)
		if got.Action != tt.want || tt.flag != before {
			t.Fatalf("%s at %v: %+v", tt.want, tt.at, got)
		}
		if tt.want == heartbeat.None && got.Next != before {
			t.Fatalf("nothing to do changed the flag: %+v", got)
		}
	}
	if got := decide(t, nil, empty, now, 600); got.Next != alerting {
		t.Fatalf("an alert opens the incident now: %+v", got)
	}
}

// A CronJob whose spec or status cannot be read is no decision: unknown is not stale.
func TestUnreadableCronJobIsNoDecision(t *testing.T) {
	for _, document := range []string{`[]`, `{"spec":[]}`, `{"spec":{"suspend":"yes"}}`, `{"status":"x"}`, ``} {
		if _, err := heartbeat.Decide([]byte(document), empty, now, 600); err == nil {
			t.Fatalf("%q decided", document)
		}
	}
}

func TestWatchedJobs(t *testing.T) {
	watched := heartbeat.Watched()
	if len(watched) != 1 || watched[0] != (heartbeat.Job{Label: "notification checker", Namespace: "console",
		Name: "console-notifications", MaxAge: 600}) || watched[0].Key() != "console/console-notifications" {
		t.Fatalf("%+v", watched)
	}
}

// The notices are Python's words, priorities and tags; nothing to do is no notice.
func TestNoticeWording(t *testing.T) {
	job := heartbeat.Watched()[0]
	stale := heartbeat.Notice{Title: "notification checker stale",
		Message:  "The console-notifications CronJob is stale: CronJob is missing. Check with make verify-platform.",
		Priority: 4, Tag: "warning"}
	for _, action := range []heartbeat.Action{heartbeat.Alert, heartbeat.Remind} {
		if got, due := (heartbeat.Decision{Action: action, Reason: "CronJob is missing"}).Notice(job); !due || got != stale {
			t.Fatalf("%s: %+v", action, got)
		}
	}
	recovered := heartbeat.Notice{Title: "notification checker recovered",
		Message: "The console-notifications CronJob is healthy again.", Priority: 3, Tag: "white_check_mark"}
	if got, due := (heartbeat.Decision{Action: heartbeat.Recover}).Notice(job); !due || got != recovered {
		t.Fatalf("recover: %+v", got)
	}
	if got, due := (heartbeat.Decision{Action: heartbeat.None, Reason: "x"}).Notice(job); due || got != (heartbeat.Notice{}) {
		t.Fatalf("none: %+v", got)
	}
}
