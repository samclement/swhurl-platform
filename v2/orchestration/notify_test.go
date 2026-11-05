package orchestration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy/ntfytest"
	"github.com/samclement/swhurl-platform/v2/notify"
)

const (
	phoneAddress = "https://ntfy.example.test/private-phone"
	deskAddress  = "https://ntfy.example.test/private-desk"
)

var deliveredAt = time.Unix(1_800_000_000, 0)

// outboxCluster is a cluster whose outbox holds one message queued for both of payments' channels at deliveredAt,
// with the channels' Secrets the addresses names.
func outboxCluster(t *testing.T, addresses map[string]string) *kubetest.Fake {
	t.Helper()
	state, _ := notify.DecodeState("")
	request := notify.Request{Instance: notify.Instance{App: "orders", Env: "staging"}, Key: "event:1@notify",
		Notice: notify.Notice{Title: "orders/staging deployed", Message: "Flux UpgradeSucceeded", High: true}}
	if _, err := state.Accept(request, []string{"payments-phone", "payments-desk"}, seconds(deliveredAt)); err != nil {
		t.Fatal(err)
	}
	return clusterWithOutbox(t, state, addresses)
}

func clusterWithOutbox(t *testing.T, state notify.State, addresses map[string]string) *kubetest.Fake {
	t.Helper()
	encoded, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	fake := kubetest.New(kubetest.Answers{})
	fake.ConfigMapData = map[string][]kube.ConfigMap{"notify": {{Name: "notify-outbox", Data: map[string]string{"state.json": encoded}}}}
	fake.SecretData = map[string][]kube.Secret{"notify": {}}
	for channel, address := range addresses {
		fake.SecretData["notify"] = append(fake.SecretData["notify"], kube.Secret{Name: "channel-" + channel,
			Labels: map[string]string{notify.TransportLabel: "ntfy"}, Data: map[string][]byte{"address": []byte(address)}})
	}
	return fake
}

func connectTo(fake *kubetest.Fake) NotifyConnect {
	return func() (NotifyCluster, error) { return NotifyCluster{Reviewer: fake, Data: fake, Reader: fake}, nil }
}

func savedOutbox(t *testing.T, fake *kubetest.Fake) notify.State {
	t.Helper()
	state, err := notify.DecodeState(fake.ConfigMapData["notify"][0].Data["state.json"])
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// savingPublisher records, at each publication, the pending keys the saved outbox held: what a crash would resend.
type savingPublisher struct {
	ntfy.Publisher
	fake  *kubetest.Fake
	t     *testing.T
	saved [][]string
}

func (p *savingPublisher) Publish(ctx context.Context, address string, publication notify.Publication) error {
	var keys []string
	for _, entry := range savedOutbox(p.t, p.fake).Pending {
		keys = append(keys, entry.Key)
	}
	p.saved = append(p.saved, keys)
	return p.Publisher.Publish(ctx, address, publication)
}

func TestADeliveryPassSendsEachCopyToItsChannelsAddressAndRemembersIt(t *testing.T) {
	fake := outboxCluster(t, map[string]string{"payments-phone": phoneAddress, "payments-desk": deskAddress})
	published := ntfytest.New()
	publisher := &savingPublisher{Publisher: published, fake: fake, t: t}
	notifier := NewNotifier(publisher)
	done, err := notifier.Deliver(context.Background(), connectTo(fake), deliveredAt)
	if err != nil || len(done) != 2 || done[0].Err != nil || done[1].Err != nil {
		t.Fatalf("pass: %+v, %v", done, err)
	}
	if got := []string{published.Sent[0].Address, published.Sent[1].Address}; !reflect.DeepEqual(got, []string{phoneAddress, deskAddress}) {
		t.Fatalf("sent to %v", got)
	}
	if !strings.Contains(string(published.Sent[0].Document), `"priority":4`) {
		t.Fatalf("a high message is sent high: %s", published.Sent[0].Document)
	}
	want := [][]string{{"event:1@notify@payments-phone", "event:1@notify@payments-desk"}, {"event:1@notify@payments-desk"}}
	if !reflect.DeepEqual(publisher.saved, want) {
		t.Fatalf("the outbox is saved before each send, and after each copy: %v", publisher.saved)
	}
	state := savedOutbox(t, fake)
	until := seconds(deliveredAt) + notify.History
	if len(state.Pending) != 0 || !reflect.DeepEqual(state.Seen, map[string]float64{
		"event:1@notify@payments-phone": until, "event:1@notify@payments-desk": until}) {
		t.Fatalf("saved %+v", state)
	}
	calls := len(fake.Calls)
	if done, err := notifier.Deliver(context.Background(), connectTo(fake), deliveredAt); err != nil || done != nil {
		t.Fatalf("an empty outbox: %+v, %v", done, err)
	}
	if len(fake.Calls) != calls {
		t.Fatalf("an empty outbox reads and saves nothing: %v", fake.Calls[calls:])
	}
}

func TestAChannelThatFailsOrHasNoSecretIsRetriedWithoutHoldingUpTheOthers(t *testing.T) {
	fake := outboxCluster(t, map[string]string{"payments-desk": deskAddress})
	published := ntfytest.New()
	notifier := NewNotifier(published)
	done, err := notifier.Deliver(context.Background(), connectTo(fake), deliveredAt)
	if err != nil || len(done) != 2 || !errors.Is(done[0].Err, errNoChannel) || !done[0].Retry || done[1].Err != nil {
		t.Fatalf("pass: %+v, %v", done, err)
	}
	if len(published.Sent) != 1 || published.Sent[0].Address != deskAddress {
		t.Fatalf("the desk's copy is sent: %+v", published.Sent)
	}
	state := savedOutbox(t, fake)
	if len(state.Pending) != 1 || state.Pending[0].Channel != "payments-phone" {
		t.Fatalf("the phone's copy waits: %+v", state.Pending)
	}

	// The phone's server refuses it until the retry window has passed; then it is given up and remembered.
	fake.SecretData["notify"] = append(fake.SecretData["notify"], kube.Secret{Name: "channel-payments-phone",
		Labels: map[string]string{notify.TransportLabel: "ntfy"}, Data: map[string][]byte{"address": []byte(phoneAddress)}})
	published.Refusing[phoneAddress] = true
	done, err = notifier.Deliver(context.Background(), connectTo(fake), deliveredAt.Add(time.Hour))
	var refused *ntfy.Error
	if err != nil || len(done) != 1 || !errors.As(done[0].Err, &refused) || !done[0].Retry {
		t.Fatalf("a refusing server is retried: %+v, %v", done, err)
	}
	later := deliveredAt.Add(notify.ChannelRetry * time.Second)
	done, err = notifier.Deliver(context.Background(), connectTo(fake), later)
	if err != nil || len(done) != 1 || done[0].Retry {
		t.Fatalf("given up after the retry window: %+v, %v", done, err)
	}
	state = savedOutbox(t, fake)
	if len(state.Pending) != 0 || state.Seen["event:1@notify@payments-phone"] != seconds(later)+notify.History {
		t.Fatalf("a copy given up is remembered: %+v", state)
	}
}

func TestChannelSecretsThatCannotBeReadAreAFailedCopy(t *testing.T) {
	fake := outboxCluster(t, map[string]string{"payments-phone": phoneAddress, "payments-desk": deskAddress})
	fake.Failing = map[string]error{"secrets " + notify.TransportLabel + "=ntfy": errors.New("forbidden")}
	done, err := NewNotifier(ntfytest.New()).Deliver(context.Background(), connectTo(fake), deliveredAt)
	if err != nil || len(done) != 2 || !errors.Is(done[0].Err, errCannotReadChannels) || !errors.Is(done[1].Err, errCannotReadChannels) {
		t.Fatalf("pass: %+v, %v", done, err)
	}
	if reads := strings.Count(strings.Join(fake.Calls, "\n"), "secrets "); reads != 2 {
		t.Fatalf("each copy tries the read again: %v", fake.Calls)
	}
	if len(savedOutbox(t, fake).Pending) != 2 {
		t.Fatal("both copies wait")
	}
}

func TestARestartedServiceSendsWhatTheLastOneAcceptedAndRefusesAnotherVersion(t *testing.T) {
	fake := outboxCluster(t, map[string]string{"payments-phone": phoneAddress, "payments-desk": deskAddress})
	published := ntfytest.New()
	if _, err := NewNotifier(published).Deliver(context.Background(), connectTo(fake), deliveredAt); err != nil || len(published.Sent) != 2 {
		t.Fatalf("a new notifier sends the saved outbox: %d, %v", len(published.Sent), err)
	}
	fake.ConfigMapData["notify"][0].Data["state.json"] = `{"version":2,"seen":{},"pending":[]}`
	calls := len(fake.Calls)
	_, err := NewNotifier(published).Deliver(context.Background(), connectTo(fake), deliveredAt)
	if !errors.Is(err, notify.ErrInvalidOutbox) || !strings.Contains(err.Error(), "refusing to reset delivery history") {
		t.Fatalf("another version: %v", err)
	}
	if fake.Calls[len(fake.Calls)-1] != "configmaps notify-outbox" || len(fake.Calls) != calls+1 {
		t.Fatalf("it is never reset: %v", fake.Calls[calls:])
	}
}

func TestAnOutboxThatCannotBeSavedIsReadAgain(t *testing.T) {
	fake := outboxCluster(t, map[string]string{"payments-phone": phoneAddress, "payments-desk": deskAddress})
	fake.Failing = map[string]error{"set notify-outbox": errors.New("conflict")}
	published := ntfytest.New()
	notifier := NewNotifier(published)
	if _, err := notifier.Deliver(context.Background(), connectTo(fake), deliveredAt); err == nil || len(published.Sent) != 0 {
		t.Fatalf("nothing is sent before the outbox is saved: %v, %d", err, len(published.Sent))
	}
	fake.Failing = nil
	if _, err := notifier.Deliver(context.Background(), connectTo(fake), deliveredAt); err != nil || len(published.Sent) != 2 {
		t.Fatalf("the next pass reads the ConfigMap again and sends: %v, %d", err, len(published.Sent))
	}
	if reads := strings.Count(strings.Join(fake.Calls, "\n"), "configmaps notify-outbox"); reads != 2 {
		t.Fatalf("read again after the failed save: %v", fake.Calls)
	}
}

// failingCheckpointData simulates an API failure after earlier delivery checkpoints succeeded.
type failingCheckpointData struct {
	kube.Data
	writes int
	failAt int
}

func (d *failingCheckpointData) SetConfigMapKey(ctx context.Context, namespace, name, key, value, manager string) error {
	d.writes++
	if d.writes == d.failAt {
		return errors.New("checkpoint unavailable")
	}
	return d.Data.SetConfigMapKey(ctx, namespace, name, key, value, manager)
}

func TestEarlierRetriesSurviveLaterCheckpointsAndRestart(t *testing.T) {
	fake := outboxCluster(t, map[string]string{"payments-phone": phoneAddress, "payments-desk": deskAddress})
	state := savedOutbox(t, fake)
	third := state.Pending[1]
	third.Key = "event:2@notify@payments-desk"
	state.Pending = append(state.Pending, third)
	fake = clusterWithOutbox(t, state, map[string]string{"payments-phone": phoneAddress, "payments-desk": deskAddress})
	data := &failingCheckpointData{Data: fake, failAt: 3}
	connect := func() (NotifyCluster, error) { return NotifyCluster{Data: data}, nil }
	published := ntfytest.New()
	published.Refusing[phoneAddress] = true
	_, err := NewNotifier(published).Deliver(context.Background(), connect, deliveredAt)
	if err == nil || data.writes != 3 {
		t.Fatalf("the last checkpoint must fail: %v, writes %d", err, data.writes)
	}
	state = savedOutbox(t, fake)
	if len(state.Pending) != 2 || state.Pending[1].Key != "event:1@notify@payments-phone" {
		t.Fatalf("a durable checkpoint lost the earlier retry: %+v", state)
	}
	delete(published.Refusing, phoneAddress)
	_, err = NewNotifier(published).Deliver(context.Background(), connectTo(fake), deliveredAt)
	if err != nil || len(published.Received(phoneAddress)) != 1 {
		t.Fatalf("restart must retry the earlier failed copy: %v, sent %+v", err, published.Sent)
	}
}

func TestAPassThatCannotConnectSendsNothing(t *testing.T) {
	refused := errors.New("invalid: in-cluster: token is not mounted")
	done, err := NewNotifier(ntfytest.New()).Deliver(context.Background(),
		func() (NotifyCluster, error) { return NotifyCluster{}, refused }, deliveredAt)
	if !errors.Is(err, refused) || done != nil {
		t.Fatalf("%+v, %v", done, err)
	}
}
