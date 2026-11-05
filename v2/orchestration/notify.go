package orchestration

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/notify"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// NotifyCluster is what the notify service reaches the cluster through for one request or one delivery pass: who a
// token belongs to, its own ConfigMaps and Secrets, and a namespace's labels.
type NotifyCluster struct {
	Reviewer kube.Reviewer
	Data     kube.Data
	Reader   kube.Reader
}

// NotifyConnect opens a new connection to the cluster. A use case calls it at its first effect, so a refusal that
// needs no cluster is answered without one and a connection is never kept between requests.
type NotifyConnect func() (NotifyCluster, error)

// Notifier is the notify service's state between requests: the outbox as it was last read or saved, the lock that
// orders every change to it, each app instance's recent messages and the publisher a pass sends with. It holds no
// routes, team or address: those are read each time they are used.
type Notifier struct {
	lock      sync.Mutex
	outbox    *notify.State // nil: read it again before using it
	limit     notify.RateLimit
	publisher ntfy.Publisher
}

// NewNotifier is a notifier with nothing read yet, which sends with the publisher.
func NewNotifier(publisher ntfy.Publisher) *Notifier { return &Notifier{publisher: publisher} }

// Accepted is what became of one request: the request, who sent it (the caller's name once Kubernetes has said it,
// empty for an alert), the channels a copy was queued for, and the failure behind a 503, which names no token,
// secret or address. It is filled as far as the request got, a refusal included.
type Accepted struct {
	Request  notify.Request
	Caller   string
	Channels []string
	Failure  error
}

// AcceptMessage is POST /v1/notify: the caller is whoever Kubernetes says the bearer token belongs to, the message
// is decoded as that caller may send it, an app is held to its rate, and a copy is queued in the outbox for each
// channel of the app's team, saved before it answers. An error is a *notify.Refusal.
func (n *Notifier) AcceptMessage(ctx context.Context, connect NotifyConnect, authorization string, body []byte,
	now time.Time, randomKey string) (Accepted, error) {
	var accepted Accepted
	if err := notify.CheckBody(body); err != nil {
		return accepted, err
	}
	token, err := notify.Bearer(authorization)
	if err != nil {
		return accepted, err
	}
	cluster, err := connect()
	if err != nil {
		return accepted.failed(err, notify.CannotReview)
	}
	review, err := cluster.Reviewer.Review(ctx, token, notify.Audience)
	if err != nil {
		return accepted.failed(err, notify.CannotReview)
	}
	if accepted.Caller, err = notify.Identity(review.Authenticated, review.Audiences, review.Username); err != nil {
		return accepted, err
	}
	sender, err := notify.SenderOf(accepted.Caller)
	if err != nil {
		return accepted, err
	}
	if accepted.Request, err = notify.DecodeMessage(body, sender, randomKey); err != nil {
		return accepted, err
	}
	if sender.IsApp() {
		if err := n.take(sender.Instance, now); err != nil {
			return accepted, err
		}
	}
	return n.queue(ctx, cluster, accepted, now)
}

// AcceptAlert is POST /v1/alert: ClickStack's webhook proves itself with the secret in the body, compared with the
// cluster's, and the alert is queued for the channels of its app's team like a message. An error is a
// *notify.Refusal.
func (n *Notifier) AcceptAlert(ctx context.Context, connect NotifyConnect, body []byte, now time.Time) (Accepted, error) {
	var accepted Accepted
	if err := notify.CheckBody(body); err != nil {
		return accepted, err
	}
	alert, err := notify.DecodeAlert(body)
	if err == nil {
		err = alert.Signed()
	}
	if err != nil {
		return accepted, err
	}
	cluster, err := connect()
	if err != nil {
		return accepted.failed(err, notify.NoAlertSecret)
	}
	expected, err := alertSecret(ctx, cluster.Data)
	if err != nil {
		return accepted.failed(err, notify.NoAlertSecret)
	}
	if accepted.Request, err = alert.Request(expected); err != nil {
		return accepted, err
	}
	return n.queue(ctx, cluster, accepted, now)
}

// failed is the 503 for an effect that failed, keeping the failure for the log.
func (a Accepted) failed(failure error, reason string) (Accepted, error) {
	a.Failure = failure
	return a, notify.Unavailable(reason)
}

// take counts one message from an app instance against its rate.
func (n *Notifier) take(instance notify.Instance, now time.Time) error {
	n.lock.Lock()
	defer n.lock.Unlock()
	return n.limit.Take(instance, seconds(now))
}

// queue reads the channels of the request's team and queues a copy for each in the outbox, saved before it returns.
// Nothing is saved when nothing was queued; an outbox that could not be read or saved is read again next time, so
// what is in memory is never ahead of the ConfigMap.
func (n *Notifier) queue(ctx context.Context, cluster NotifyCluster, accepted Accepted, now time.Time) (Accepted, error) {
	channels, err := teamChannels(ctx, cluster, accepted.Request.Instance)
	if err != nil {
		return accepted.failed(err, notify.CannotRoute)
	}
	n.lock.Lock()
	defer n.lock.Unlock()
	state, err := n.loaded(ctx, cluster.Data)
	if err != nil {
		return accepted.failed(err, notify.CannotSave)
	}
	queued, err := state.Accept(accepted.Request, channels, seconds(now))
	if err != nil {
		return accepted, err
	}
	if len(queued) > 0 {
		if err := n.save(ctx, cluster.Data, state); err != nil {
			return accepted.failed(err, notify.CannotSave)
		}
	}
	accepted.Channels = queued
	return accepted, nil
}

// teamChannels are the channels of the team whose label the instance's namespace carries: none for a namespace that
// does not exist, carries no team, or a team the routes do not name.
func teamChannels(ctx context.Context, cluster NotifyCluster, instance notify.Instance) ([]string, error) {
	ref, err := kube.RefOf("", "Namespace", "", instance.Namespace())
	if err != nil {
		return nil, err
	}
	read, err := cluster.Reader.Read(ctx, ref)
	if err != nil {
		return nil, err
	}
	team, hasTeam := "", false
	switch read.Status {
	case observe.Present:
		if team, hasTeam, err = notify.TeamOf(read.Object); err != nil {
			return nil, err
		}
	case observe.Absent:
	default:
		return nil, errors.New("namespace: " + read.Reason)
	}
	routes, err := readRoutes(ctx, cluster.Data)
	if err != nil {
		return nil, err
	}
	return routes.Channels(team, hasTeam), nil
}

// errNoRoutesKey is a routes ConfigMap without its document.
var errNoRoutesKey = errors.New("the routes ConfigMap has no " + notify.RoutesKey)

// readRoutes is the routes document; no routes ConfigMap is routes that name no team.
func readRoutes(ctx context.Context, data kube.Data) (notify.Routes, error) {
	found, err := data.ConfigMaps(ctx, notify.Namespace, kube.Selector{Name: notify.RoutesName})
	if err != nil || len(found) == 0 {
		return notify.Routes{}, err
	}
	raw, ok := found[0].Data[notify.RoutesKey]
	if !ok {
		return notify.Routes{}, errNoRoutesKey
	}
	return notify.DecodeRoutes(raw)
}

// errNoAlertSecret is a cluster without the webhook's Secret, or a Secret without its key.
var errNoAlertSecret = errors.New("no " + notify.AlertSecret + " Secret with a " + notify.AlertSecretKey + " key")

// alertSecret is the secret ClickStack's webhook must repeat.
func alertSecret(ctx context.Context, data kube.Data) (string, error) {
	found, err := data.Secrets(ctx, notify.Namespace, kube.Selector{Name: notify.AlertSecret})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", errNoAlertSecret
	}
	secret, ok := found[0].Data[notify.AlertSecretKey]
	if !ok {
		return "", errNoAlertSecret
	}
	return string(secret), nil
}

// errNoOutbox is a cluster without the outbox's ConfigMap, which the platform creates: the service never does.
var errNoOutbox = errors.New("no " + notify.OutboxName + " ConfigMap")

// loaded is the outbox, read from its ConfigMap when the notifier holds none. The caller holds the lock.
func (n *Notifier) loaded(ctx context.Context, data kube.Data) (*notify.State, error) {
	if n.outbox != nil {
		return n.outbox, nil
	}
	found, err := data.ConfigMaps(ctx, notify.Namespace, kube.Selector{Name: notify.OutboxName})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, errNoOutbox
	}
	state, err := notify.DecodeState(found[0].Data[notify.OutboxKey])
	if err != nil {
		return nil, err
	}
	n.outbox = &state
	return n.outbox, nil
}

// save writes the outbox to its ConfigMap key. One that cannot be written is forgotten, so the next use reads what
// the ConfigMap holds instead of trusting a copy that may be ahead of it. The caller holds the lock.
func (n *Notifier) save(ctx context.Context, data kube.Data, state *notify.State) error {
	encoded, err := state.Encode()
	if err == nil {
		err = data.SetConfigMapKey(ctx, notify.Namespace, notify.OutboxName, notify.OutboxKey, encoded, notify.FieldManager)
	}
	if err != nil {
		n.outbox = nil
	}
	return err
}

// seconds is a time as the outbox writes it: seconds since the epoch.
func seconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// Delivered is what became of one copy a delivery pass offered: sent when Err is nil; otherwise failed, and either
// kept to be retried or given up and remembered like a sent one. Err names no address.
type Delivered struct {
	Entry notify.Entry
	Err   error
	Retry bool
}

// The failures of a copy that are not the publisher's.
var (
	errNoChannel          = errors.New("no ntfy channel Secret with that name in the cluster")
	errCannotReadChannels = errors.New("cannot read the channel Secrets")
)

// Deliver runs one delivery pass: it forgets the keys whose time is up, saves the outbox before sending anything,
// then offers every pending copy to its channel's address, in order, saving after each copy that is done. A copy
// that fails is retried at the end of the queue for notify.ChannelRetry without holding up the others. An outbox
// with nothing pending reads nothing more and saves nothing. The error is the outbox's: it could not be read, is of
// another version (refused, never reset) or could not be saved; the copies done before it are reported.
func (n *Notifier) Deliver(ctx context.Context, connect NotifyConnect, now time.Time) ([]Delivered, error) {
	cluster, err := connect()
	if err != nil {
		return nil, err
	}
	n.lock.Lock()
	defer n.lock.Unlock()
	state, err := n.loaded(ctx, cluster.Data)
	if err != nil {
		return nil, err
	}
	at := seconds(now)
	state.Expire(at)
	if len(state.Pending) == 0 {
		return nil, nil
	}
	if err := n.save(ctx, cluster.Data, state); err != nil {
		return nil, err
	}
	pass := &delivery{data: cluster.Data, publisher: n.publisher}
	var done []Delivered
	hasRetries := false
	// Offer each original copy once. Put retries back immediately so every later checkpoint
	// still contains them, including when a later save fails or the process stops mid-pass.
	for remaining := len(state.Pending); remaining > 0; remaining-- {
		entry, _ := state.Take()
		sent := pass.send(ctx, entry)
		retry := state.Settle(entry, sent == nil, at)
		done = append(done, Delivered{entry, sent, retry})
		if retry {
			hasRetries = true
			state.Requeue([]notify.Entry{entry})
		} else if err := n.save(ctx, cluster.Data, state); err != nil {
			return done, err
		}
	}
	if hasRetries {
		return done, n.save(ctx, cluster.Data, state)
	}
	return done, nil
}

// delivery is one pass's sending: the channels' addresses, read from their Secrets when the first copy is sent.
type delivery struct {
	data      kube.Data
	publisher ntfy.Publisher
	addresses map[string]string // nil: not read yet
}

// send publishes one copy to its channel's address.
func (d *delivery) send(ctx context.Context, entry notify.Entry) error {
	if d.addresses == nil {
		found, err := channelAddresses(ctx, d.data)
		if err != nil {
			return errCannotReadChannels
		}
		d.addresses = found
	}
	address, ok := d.addresses[entry.Channel]
	if !ok {
		return errNoChannel
	}
	return d.publisher.Publish(ctx, address, entry.Publication())
}

// channelAddresses is each ntfy channel's address, from the Secrets labelled for the ntfy transport: channel-<name>
// with an address.
func channelAddresses(ctx context.Context, data kube.Data) (map[string]string, error) {
	secrets, err := data.Secrets(ctx, notify.Namespace, kube.Selector{Labels: notify.TransportLabel + "=ntfy"})
	if err != nil {
		return nil, err
	}
	found := map[string]string{}
	for _, secret := range secrets {
		channel, ok := notify.ChannelOf(secret.Name)
		if address := secret.Data[notify.AddressKey]; ok && len(address) > 0 {
			found[channel] = string(address)
		}
	}
	return found, nil
}
