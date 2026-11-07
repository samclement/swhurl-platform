package orchestration

import (
	"context"
	"errors"

	"github.com/samclement/swhurl-platform/v2/adapters/hoststate"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/heartbeat"
	"github.com/samclement/swhurl-platform/v2/notify"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// HeartbeatPorts is the heartbeat's effects: the watched CronJobs, the destination's Secret, the platform topic and
// the state file on the host.
type HeartbeatPorts struct {
	Cluster  kube.Reader
	Data     kube.Data
	Platform ntfy.Publisher
	State    hoststate.Store
}

// HeartbeatOptions supplies every fact: the clock, the jobs with their maximum ages, the state file, and whether
// this is a dry run, which reads the CronJobs and decides but reads no destination, posts nothing and saves nothing.
type HeartbeatOptions struct {
	Now       float64
	Jobs      []heartbeat.Job
	StatePath string
	DryRun    bool
}

// HeartbeatOutcome is what became of one job: its decision, the notice when one is due, whether it was sent and the
// state saved, or why the job failed. A failed job does not stop the others.
type HeartbeatOutcome struct {
	Job       heartbeat.Job
	Decision  heartbeat.Decision
	Notice    heartbeat.Notice
	Due, Sent bool
	Err       error
}

// errHeartbeatSave is a notice that was accepted but could not be remembered: the next run sends it again.
var errHeartbeatSave = errors.New("heartbeat state cannot be saved")

// NotificationsHeartbeat runs one pass over the jobs, each decided on its own. A state file that cannot be read or is
// invalid stops the pass before any job, leaving the file unchanged. A job's state is saved only after ntfy accepts
// its notice. No error names the destination or what a server answered.
func NotificationsHeartbeat(ctx context.Context, ports HeartbeatPorts, options HeartbeatOptions) ([]HeartbeatOutcome, error) {
	state, err := heartbeatState(ports.State, options.StatePath)
	if err != nil {
		return nil, err
	}
	outcomes := make([]HeartbeatOutcome, 0, len(options.Jobs))
	for _, job := range options.Jobs {
		outcomes = append(outcomes, heartbeatJob(ctx, ports, options, state, job))
	}
	return outcomes, nil
}

// heartbeatState is the remembered incidents: none when the file is absent.
func heartbeatState(store hoststate.Store, path string) (heartbeat.State, error) {
	document, found, err := store.Read(path)
	if err != nil {
		return nil, heartbeat.ErrUnreadable
	}
	if !found {
		return heartbeat.State{}, nil
	}
	return heartbeat.DecodeState(document)
}

// heartbeatJob reads, decides and, when a notice is due and this is no dry run, sends it and saves the job's new flag.
func heartbeatJob(ctx context.Context, ports HeartbeatPorts, options HeartbeatOptions, state heartbeat.State,
	job heartbeat.Job) HeartbeatOutcome {
	outcome := HeartbeatOutcome{Job: job}
	cronjob, err := heartbeatCronJob(ctx, ports.Cluster, job)
	if err != nil {
		outcome.Err = err
		return outcome
	}
	outcome.Decision, err = heartbeat.Decide(cronjob, state[job.Key()], options.Now, job.MaxAge)
	if err != nil {
		outcome.Err = errors.New(job.Label + " status is invalid")
		return outcome
	}
	outcome.Notice, outcome.Due = outcome.Decision.Notice(job)
	if !outcome.Due || options.DryRun {
		return outcome
	}
	if outcome.Err = heartbeatSend(ctx, ports, outcome.Notice); outcome.Err != nil {
		return outcome
	}
	state[job.Key()] = outcome.Decision.Next
	if ports.State.Replace(options.StatePath, state.Encode()) != nil {
		outcome.Err = errHeartbeatSave
		return outcome
	}
	outcome.Sent = true
	return outcome
}

// heartbeatCronJob is the job's CronJob document, nil when the API server says it is missing. Any other failure is
// an error: unknown is not stale.
func heartbeatCronJob(ctx context.Context, cluster kube.Reader, job heartbeat.Job) ([]byte, error) {
	read, err := cluster.Read(ctx, observe.Ref{APIVersion: "batch/v1", Kind: "CronJob", Namespace: job.Namespace, Name: job.Name})
	switch {
	case err == nil && read.Status == observe.Present:
		return append([]byte{}, read.Object...), nil
	case err == nil && read.Status == observe.Absent:
		return nil, nil
	}
	return nil, errors.New("cannot read " + job.Label)
}

// heartbeatSend reads the destination by name and publishes the notice to it, as ntfy's JSON publish API document.
func heartbeatSend(ctx context.Context, ports HeartbeatPorts, notice heartbeat.Notice) error {
	address, err := heartbeatDestination(ctx, ports.Data)
	if err != nil {
		return err
	}
	publication := notify.Publication{Title: notice.Title, Message: notice.Message, Priority: notice.Priority, Tags: []string{notice.Tag}}
	err = ports.Platform.Publish(ctx, address, publication)
	var failure *ntfy.Error
	if errors.As(err, &failure) {
		// The adapter's error names a code and a fixed reason, never the address or the answer.
		return errors.New("ntfy delivery failed; notification remains pending: " + failure.Error())
	}
	if err != nil {
		return errors.New("ntfy delivery failed; notification remains pending")
	}
	return nil
}

// heartbeatDestination is the platform topic's address. A Secret that is absent, unreadable or without the key is
// unavailable; one whose value is empty says so.
func heartbeatDestination(ctx context.Context, data kube.Data) (string, error) {
	secret, err := data.GetSecret(ctx, ntfyNamespace, ntfySecret)
	if err != nil || secret == nil {
		return "", errors.New("notification destination is unavailable")
	}
	address, has := secret.Data[ntfyPlatformKey]
	if !has {
		return "", errors.New("notification destination is unavailable")
	}
	if len(address) == 0 {
		return "", errors.New("notification destination is empty")
	}
	return string(address), nil
}
