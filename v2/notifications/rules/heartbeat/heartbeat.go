// Package heartbeat decides, from outside the cluster, whether the operator must hear that a watched CronJob has gone
// stale or recovered: the job's freshness, the transition from the incident the last run remembered, the notice, and
// the small document that remembers the incident between runs. Times are seconds since the epoch, supplied by the
// caller.
package heartbeat

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// Job is one watched CronJob.
type Job struct {
	Label           string // in titles: "<label> stale", "<label> recovered"
	Namespace, Name string
	MaxAge          float64 // seconds since the last successful Job before the job is stale
}

// Key is the job's name in the state document, namespace/name.
func (j Job) Key() string { return j.Namespace + "/" + j.Name }

// Watched is the jobs the heartbeat watches; the first's flag is the state document's top level. Add a job only once
// its CronJob is deployed and unsuspended, because a missing or suspended job is stale.
func Watched() []Job {
	return []Job{{Label: "notification checker", Namespace: "console", Name: "console-notifications", MaxAge: 600}}
}

// ReminderSeconds is how long a stale job waits for its next reminder.
const ReminderSeconds = 3600

// Flag is one job's incident: whether one is open, when it opened and when the operator was last told. The two times
// are zero when none is open.
type Flag struct {
	Alerting         bool
	Since, LastAlert float64
}

// Action is what a run does about one job.
type Action string

// The four actions: tell the operator a job went stale, remind them it still is, tell them it recovered, or nothing.
const (
	None    Action = "none"
	Alert   Action = "alert"
	Remind  Action = "remind"
	Recover Action = "recover"
)

// Decision is one job's action, why the job is stale (empty when it is fresh) and the flag to keep once the notice is
// accepted: the flag it was given when the action is None.
type Decision struct {
	Action Action
	Reason string
	Next   Flag
}

// errStatus is a CronJob document the decision cannot read: unknown is not stale, so nothing is decided.
var errStatus = errors.New("status is invalid")

// Decide is the action for one job. cronjob is its API document, nil when the CronJob is missing; the only error is a
// document whose spec or status cannot be read.
func Decide(cronjob []byte, flag Flag, now, maxAge float64) (Decision, error) {
	reason, err := staleness(cronjob, now, maxAge)
	if err != nil {
		return Decision{}, err
	}
	switch {
	case reason == "" && flag.Alerting:
		return Decision{Action: Recover, Next: Flag{}}, nil
	case reason == "":
		return Decision{Action: None, Next: flag}, nil
	case !flag.Alerting:
		return Decision{Alert, reason, Flag{true, now, now}}, nil
	case now-flag.LastAlert >= ReminderSeconds:
		return Decision{Remind, reason, Flag{true, flag.Since, now}}, nil
	}
	return Decision{None, reason, flag}, nil
}

// staleness says why a job is stale, or "" when it last succeeded within maxAge (a success in the future included).
func staleness(cronjob []byte, now, maxAge float64) (string, error) {
	if cronjob == nil {
		return "CronJob is missing", nil
	}
	var document struct {
		Spec struct {
			Suspend bool `json:"suspend"`
		} `json:"spec"`
		Status struct {
			LastSuccessfulTime any `json:"lastSuccessfulTime"`
		} `json:"status"`
	}
	if json.Unmarshal(cronjob, &document) != nil {
		return "", errStatus
	}
	if document.Spec.Suspend {
		return "CronJob is suspended", nil
	}
	last := document.Status.LastSuccessfulTime
	if empty(last) {
		return "no successful Job has completed", nil
	}
	text, _ := last.(string)
	taken, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return "last successful time is invalid", nil
	}
	age := now - (float64(taken.Unix()) + float64(taken.Nanosecond())/1e9)
	if age <= maxAge {
		return "", nil
	}
	return fmt.Sprintf("last success was %d minutes ago", int64(math.Floor(age/60))), nil
}

// empty is whether a JSON value says nothing: absent, null, false, zero, or an empty string, list or object.
func empty(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case bool:
		return !v
	case float64:
		return v == 0
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

// Notice is what the operator is told: ntfy's priority (4 for a stale job, 3 for a recovered one) and its one tag.
type Notice struct {
	Title, Message string
	Priority       int
	Tag            string
}

// Notice is the decision's notice about job, and false when the action is None.
func (d Decision) Notice(job Job) (Notice, bool) {
	switch d.Action {
	case Alert, Remind:
		return Notice{job.Label + " stale",
			fmt.Sprintf("The %s CronJob is stale: %s. Check with make verify-platform.", job.Name, d.Reason),
			4, "warning"}, true
	case Recover:
		return Notice{job.Label + " recovered", fmt.Sprintf("The %s CronJob is healthy again.", job.Name), 3,
			"white_check_mark"}, true
	}
	return Notice{}, false
}
