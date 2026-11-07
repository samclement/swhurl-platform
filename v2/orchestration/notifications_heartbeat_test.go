package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/hoststate/hoststatetest"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy/ntfytest"
	"github.com/samclement/swhurl-platform/v2/heartbeat"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// beatNow is 3 October 2026, 12:00 UTC, the Python tests' clock; beatURL is a destination that must never be logged.
const (
	beatNow   = 1791028800.0
	beatURL   = "https://ntfy.example/private-heartbeat-sentinel"
	beatState = "/home/op/.local/state/swhurl-platform/notification-heartbeat.json"
)

var reportSync = heartbeat.Job{Label: "report sync", Namespace: "reports", Name: "sync", MaxAge: 130 * 60}

// beatCronJob is a CronJob that last succeeded age seconds ago.
func beatCronJob(job heartbeat.Job, age float64) []byte {
	last := time.Unix(int64(beatNow-age), 0).UTC().Format(time.RFC3339)
	return fmt.Appendf(nil, `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"namespace":%q,"name":%q},`+
		`"spec":{"suspend":false},"status":{"lastSuccessfulTime":%q}}`, job.Namespace, job.Name, last)
}

func beatRef(job heartbeat.Job) observe.Ref {
	return observe.Ref{APIVersion: "batch/v1", Kind: "CronJob", Namespace: job.Namespace, Name: job.Name}
}

// beat is one pass's effects: the API server (each CronJob by its ref, the destination Secret), ntfy and the state file.
type beat struct {
	cluster  *kubetest.Fake
	platform *ntfytest.Fake
	state    *hoststatetest.Fake
}

func newBeat(cronjobs map[heartbeat.Job][]byte) *beat {
	cluster := kubetest.New(kubetest.Answers{})
	cluster.Objects = map[observe.Ref][]byte{}
	for job, document := range cronjobs {
		cluster.Objects[beatRef(job)] = document
	}
	cluster.SecretData = map[string][]kube.Secret{"console": {{Name: "notification-ntfy",
		Data: map[string][]byte{"NTFY_PLATFORM_URL": []byte(beatURL)}}}}
	return &beat{cluster, ntfytest.New(), hoststatetest.New()}
}

func (b *beat) run(t *testing.T, dry bool, jobs ...heartbeat.Job) []orchestration.HeartbeatOutcome {
	t.Helper()
	ports := orchestration.HeartbeatPorts{Cluster: b.cluster, Data: b.cluster, Platform: b.platform, State: b.state}
	outcomes, err := orchestration.NotificationsHeartbeat(context.Background(), ports,
		orchestration.HeartbeatOptions{Now: beatNow, Jobs: jobs, StatePath: beatState, DryRun: dry})
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range outcomes {
		if outcome.Err != nil && strings.Contains(outcome.Err.Error(), "private") {
			t.Fatalf("an error names the destination: %v", outcome.Err)
		}
	}
	return outcomes
}

func (b *beat) sentTitles() []string {
	var titles []string
	for _, sent := range b.platform.Sent {
		_, rest, _ := strings.Cut(string(sent.Document), `"title":"`)
		title, _, _ := strings.Cut(rest, `"`)
		titles = append(titles, title)
	}
	return titles
}

var checker = heartbeat.Watched()[0]

// A dry run reads the CronJob and decides, and reads no Secret, posts nothing and saves nothing.
func TestHeartbeatDryRunDoesNotReadTheSecretPostOrSave(t *testing.T) {
	b := newBeat(map[heartbeat.Job][]byte{checker: beatCronJob(checker, 601)})
	outcomes := b.run(t, true, checker)
	if len(outcomes) != 1 || !outcomes[0].Due || outcomes[0].Sent || outcomes[0].Err != nil ||
		outcomes[0].Notice.Title != "notification checker stale" || outcomes[0].Notice.Priority != 4 {
		t.Fatalf("%+v", outcomes)
	}
	if len(b.cluster.Reads) != 1 || len(b.cluster.Calls) != 0 || b.platform.Attempts != 0 || len(b.state.Replaced) != 0 {
		t.Fatalf("reads %v, calls %v, %d posts, saves %v", b.cluster.Reads, b.cluster.Calls, b.platform.Attempts, b.state.Replaced)
	}
}

// A stale job is posted to the destination the Secret names, as ntfy's JSON document, and only then saved.
func TestHeartbeatStaleAlertPersistsStateOnlyAfterNtfyAccepts(t *testing.T) {
	b := newBeat(map[heartbeat.Job][]byte{checker: beatCronJob(checker, 601)})
	outcomes := b.run(t, false, checker)
	if !outcomes[0].Sent || outcomes[0].Err != nil {
		t.Fatalf("%+v", outcomes)
	}
	want := `{"title":"notification checker stale","message":"The console-notifications CronJob is stale: last success ` +
		`was 10 minutes ago. Check with make verify-platform.","priority":4,"tags":["warning"],"topic":"private-heartbeat-sentinel"}`
	if received := b.platform.Received(beatURL); len(received) != 1 || string(received[0]) != want {
		t.Fatalf("posted %q", received)
	}
	if got := string(b.state.Files[beatState]); got != `{"alerting_since":1791028800.0,"last_alert":1791028800.0}`+"\n" {
		t.Fatalf("saved %s", got)
	}
	if !slices.Equal(b.cluster.Calls, []string{"get secret notification-ntfy"}) {
		t.Fatalf("calls %v", b.cluster.Calls)
	}
	// A refused send saves nothing, so the next run sends it again.
	refused := newBeat(map[heartbeat.Job][]byte{checker: beatCronJob(checker, 601)})
	refused.platform.Refusing[beatURL] = true
	outcomes = refused.run(t, false, checker)
	if outcomes[0].Sent || outcomes[0].Err == nil || len(refused.state.Replaced) != 0 {
		t.Fatalf("%+v, saves %v", outcomes, refused.state.Replaced)
	}
	if outcomes[0].Err.Error() != "ntfy delivery failed; notification remains pending: failed: publish: status_403" {
		t.Fatalf("%v", outcomes[0].Err)
	}
}

// A reminder an hour after the last alert is posted and saved with the incident's original start.
func TestHeartbeatReminderKeepsTheIncidentStart(t *testing.T) {
	b := newBeat(map[heartbeat.Job][]byte{checker: beatCronJob(checker, 2*3600)})
	b.state.Files[beatState] = []byte(`{"alerting_since":1791021600.0,"last_alert":1791025200.0}` + "\n")
	outcomes := b.run(t, false, checker)
	if !outcomes[0].Sent || outcomes[0].Err != nil || !slices.Equal(b.sentTitles(), []string{"notification checker stale"}) {
		t.Fatalf("%+v, sent %v", outcomes, b.sentTitles())
	}
	if got := string(b.state.Files[beatState]); got != `{"alerting_since":1791021600.0,"last_alert":1791028800.0}`+"\n" {
		t.Fatalf("saved %s", got)
	}
}

// A CronJob that cannot be read is unknown, not stale: nothing is sent or saved, and the job fails.
func TestHeartbeatFailedClusterReadLeavesExistingStateUnchanged(t *testing.T) {
	b := newBeat(nil)
	b.cluster.Answers.Failed = beatRef(checker)
	original := []byte(`{"alerting_since":12,"last_alert":13}` + "\n")
	b.state.Files[beatState] = original
	outcomes := b.run(t, false, checker)
	if outcomes[0].Err == nil || outcomes[0].Err.Error() != "cannot read notification checker" || outcomes[0].Due {
		t.Fatalf("%+v", outcomes)
	}
	if len(b.cluster.Calls) != 0 || b.platform.Attempts != 0 || len(b.state.Replaced) != 0 ||
		string(b.state.Files[beatState]) != string(original) {
		t.Fatalf("calls %v, %d posts, saves %v", b.cluster.Calls, b.platform.Attempts, b.state.Replaced)
	}
	// A refused request is a failed read too.
	b.cluster.Refuse = &kube.Error{Code: kube.RefusedRequest, Subject: "resource", Reason: "x"}
	if outcomes := b.run(t, false, checker); outcomes[0].Err == nil || len(b.state.Replaced) != 0 {
		t.Fatalf("%+v", outcomes)
	}
}

// A missing CronJob is stale; a send that fails is an error that does not repeat the destination, and nothing is saved.
func TestHeartbeatMissingCronJobAlertsAndSecretNeverAppearsInErrors(t *testing.T) {
	b := newBeat(nil)
	b.platform.Fail = errors.New("failed for " + beatURL)
	outcomes := b.run(t, false, checker)
	if outcomes[0].Decision.Reason != "CronJob is missing" || outcomes[0].Sent || outcomes[0].Err == nil {
		t.Fatalf("%+v", outcomes)
	}
	if outcomes[0].Err.Error() != "ntfy delivery failed; notification remains pending" || len(b.state.Replaced) != 0 {
		t.Fatalf("%v, saves %v", outcomes[0].Err, b.state.Replaced)
	}
}

// A fresh job with no incident reads no Secret and saves nothing.
func TestHeartbeatFreshJobDoesNothing(t *testing.T) {
	b := newBeat(map[heartbeat.Job][]byte{checker: beatCronJob(checker, 60)})
	outcomes := b.run(t, false, checker)
	if outcomes[0].Due || outcomes[0].Err != nil || outcomes[0].Decision.Action != heartbeat.None ||
		len(b.cluster.Calls) != 0 || len(b.state.Replaced) != 0 {
		t.Fatalf("%+v", outcomes)
	}
}

// Each job is judged by its own maximum age and remembered under its own flag: the first at the top level, the other
// under others.
func TestHeartbeatEachJobUsesItsOwnMaximumAgeAndFlag(t *testing.T) {
	state := hoststatetest.New()
	pass := func(checkerAge, syncAge float64) []string {
		b := newBeat(map[heartbeat.Job][]byte{checker: beatCronJob(checker, checkerAge), reportSync: beatCronJob(reportSync, syncAge)})
		b.state = state
		for _, outcome := range b.run(t, false, checker, reportSync) {
			if outcome.Err != nil {
				t.Fatal(outcome.Err)
			}
		}
		return b.sentTitles()
	}
	if titles := pass(60, 129*60); len(titles) != 0 || len(state.Files) != 0 {
		t.Fatalf("%v %v", titles, state.Files)
	}
	if titles := pass(60, 131*60); !slices.Equal(titles, []string{"report sync stale"}) {
		t.Fatalf("%v", titles)
	}
	want := `{"alerting_since":null,"last_alert":null,"others":{"reports/sync":{"alerting_since":1791028800.0,"last_alert":1791028800.0}}}` + "\n"
	if got := string(state.Files[beatState]); got != want {
		t.Fatalf("saved %s", got)
	}
	if titles := pass(601, 131*60); !slices.Equal(titles, []string{"notification checker stale"}) {
		t.Fatalf("%v", titles)
	}
	if titles := pass(60, 60); !slices.Equal(titles, []string{"notification checker recovered", "report sync recovered"}) {
		t.Fatalf("%v", titles)
	}
	want = `{"alerting_since":null,"last_alert":null,"others":{"reports/sync":{"alerting_since":null,"last_alert":null}}}` + "\n"
	if got := string(state.Files[beatState]); got != want {
		t.Fatalf("saved %s", got)
	}
}

// One job that cannot be read does not stop the other from being decided and sent.
func TestHeartbeatOneUnreadableJobDoesNotHideTheOther(t *testing.T) {
	b := newBeat(map[heartbeat.Job][]byte{checker: beatCronJob(checker, 601)})
	b.cluster.Answers.Failed = beatRef(reportSync)
	outcomes := b.run(t, false, checker, reportSync)
	if !outcomes[0].Sent || outcomes[1].Err == nil || outcomes[1].Err.Error() != "cannot read report sync" {
		t.Fatalf("%+v", outcomes)
	}
	if titles := b.sentTitles(); !slices.Equal(titles, []string{"notification checker stale"}) {
		t.Fatalf("%v", titles)
	}
}

// A state file that cannot be read or is invalid stops the pass before any job, and is left as it was.
func TestHeartbeatInvalidStateStopsBeforeAnyJob(t *testing.T) {
	for _, tt := range []struct {
		document   string
		unreadable bool
		want       error
	}{
		{`{"alerting_since":true,"last_alert":1}`, false, heartbeat.ErrInvalid},
		{`{not json`, false, heartbeat.ErrUnreadable},
		{``, true, heartbeat.ErrUnreadable},
	} {
		b := newBeat(map[heartbeat.Job][]byte{checker: beatCronJob(checker, 601)})
		b.state.Files[beatState] = []byte(tt.document)
		b.state.Unreadable[beatState] = tt.unreadable
		ports := orchestration.HeartbeatPorts{Cluster: b.cluster, Data: b.cluster, Platform: b.platform, State: b.state}
		outcomes, err := orchestration.NotificationsHeartbeat(context.Background(), ports,
			orchestration.HeartbeatOptions{Now: beatNow, Jobs: []heartbeat.Job{checker}, StatePath: beatState})
		if !errors.Is(err, tt.want) || outcomes != nil || len(b.cluster.Reads) != 0 || len(b.state.Replaced) != 0 {
			t.Fatalf("%q: %v %+v, reads %v", tt.document, err, outcomes, b.cluster.Reads)
		}
	}
}

// A destination that is absent, unreadable, without its key, empty or not an HTTPS topic sends nothing and saves
// nothing, and no error repeats it.
func TestHeartbeatMissingEmptyOrInvalidDestination(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*kubetest.Fake)
		want  string
	}{
		{"absent", func(f *kubetest.Fake) { f.SecretData = nil }, "notification destination is unavailable"},
		{"unreadable", func(f *kubetest.Fake) {
			f.Failing = map[string]error{"get secret notification-ntfy": &kube.Error{Code: kube.Failed, Subject: "secret", Reason: "forbidden"}}
		}, "notification destination is unavailable"},
		{"no key", func(f *kubetest.Fake) { f.SecretData["console"][0].Data = map[string][]byte{} }, "notification destination is unavailable"},
		{"empty", func(f *kubetest.Fake) { f.SecretData["console"][0].Data["NTFY_PLATFORM_URL"] = nil }, "notification destination is empty"},
		{"not a topic", func(f *kubetest.Fake) {
			f.SecretData["console"][0].Data["NTFY_PLATFORM_URL"] = []byte("https://ntfy.example/private/topic")
		}, "ntfy delivery failed; notification remains pending: refused_request: address: not_an_https_topic"},
	} {
		b := newBeat(nil)
		tt.setup(b.cluster)
		outcomes := b.run(t, false, checker)
		if outcomes[0].Err == nil || outcomes[0].Err.Error() != tt.want || len(b.platform.Sent) != 0 || len(b.state.Replaced) != 0 {
			t.Fatalf("%s: %+v", tt.name, outcomes)
		}
	}
}

// An accepted notice that cannot be saved is a failure: the next run sends it again.
func TestHeartbeatSaveFailureIsAFailure(t *testing.T) {
	b := newBeat(nil)
	b.state.Unwritable[beatState] = true
	outcomes := b.run(t, false, checker)
	if outcomes[0].Sent || outcomes[0].Err == nil || outcomes[0].Err.Error() != "heartbeat state cannot be saved" ||
		len(b.platform.Sent) != 1 {
		t.Fatalf("%+v", outcomes)
	}
}
