package notify_test

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/notify"
)

// checkerMessage is the checker's body as a request; high makes it a failure's.
func checkerMessage(t *testing.T, high bool) notify.Request {
	t.Helper()
	priority := "normal"
	if high {
		priority = "high"
	}
	return message(t, with(body, map[string]any{"priority": priority}), notify.Sender{})
}

// accepted is an outbox that has accepted the request for the team's two channels at now.
func accepted(t *testing.T, request notify.Request) notify.State {
	t.Helper()
	state, err := notify.DecodeState("")
	if err != nil {
		t.Fatal(err)
	}
	if queued, err := state.Accept(request, team, now); err != nil || !reflect.DeepEqual(queued, team) {
		t.Fatalf("Accept = %v, %v", queued, err)
	}
	return state
}

// test_a_message_becomes_one_saved_entry_per_channel_of_the_apps_team_before_anything_is_sent: one entry per
// channel, in the routes' order, and the same key is queued once.
func TestAMessageBecomesOneEntryPerChannelOfTheAppsTeam(t *testing.T) {
	request := checkerMessage(t, false)
	state := accepted(t, request)
	link := body["click"].(string)
	want := []notify.Entry{
		{Title: "orders/staging deployed", Message: "Flux UpgradeSucceeded", Priority: 3, Tags: []string{"white_check_mark"},
			Click: link, Key: "event:1@notify@payments-phone", Channel: "payments-phone", Queued: now, App: "orders", Env: "staging"},
		{Title: "orders/staging deployed", Message: "Flux UpgradeSucceeded", Priority: 3, Tags: []string{"white_check_mark"},
			Click: link, Key: "event:1@notify@payments-desk", Channel: "payments-desk", Queued: now, App: "orders", Env: "staging"},
	}
	if !reflect.DeepEqual(state.Pending, want) {
		t.Fatalf("pending = %+v", state.Pending)
	}
	if queued, err := state.Accept(request, team, now+1); err != nil || len(queued) != 0 || len(state.Pending) != 2 {
		t.Fatalf("the same key is queued once: %v, %v", queued, err)
	}
	if queued, _ := state.Accept(request, []string{"payments-desk", "payments-pager"}, now); !reflect.DeepEqual(queued, []string{"payments-pager"}) {
		t.Fatalf("a new channel gets its copy: %v", queued)
	}
	if queued, _ := state.Accept(request, nil, now); queued == nil || len(queued) != 0 {
		t.Fatalf("no channel is an empty answer, not null: %#v", queued)
	}
}

// test_what_cannot_be_read_or_saved_is_answered_503_and_nothing_is_kept_in_memory_only (its full outbox): an
// outbox the Python service filled to 499 refuses two more copies and keeps what it had; a request that fits
// exactly is queued.
func TestAFullOutboxRefusesARequestAndChangesNothing(t *testing.T) {
	keys := make([]string, notify.MaxPending-1)
	for i := range keys {
		keys[i] = `{"key": "` + strconv.Itoa(i) + `"}`
	}
	state, err := notify.DecodeState(`{"version": 1, "seen": {}, "pending": [` + strings.Join(keys, ", ") + `]}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.Accept(checkerMessage(t, false), team, now)
	refused(t, err, 503, "the outbox is full; try again later")
	if len(state.Pending) != notify.MaxPending-1 {
		t.Fatalf("a refused request changes nothing: %d pending", len(state.Pending))
	}
	if queued, err := state.Accept(checkerMessage(t, false), team[:1], now); err != nil || len(queued) != 1 {
		t.Fatalf("the 500th copy fits: %v, %v", queued, err)
	}
	if queued, err := state.Accept(checkerMessage(t, false), team[:1], now); err != nil || len(queued) != 0 {
		t.Fatalf("a copy already queued is not counted again: %v, %v", queued, err)
	}
}

// test_entries_go_to_their_channels_address_and_are_remembered: each copy is taken in order, published as a high
// notice with its link, and its key remembered for History, so a repeated request is not sent twice.
func TestEntriesGoToTheirChannelAndAreRemembered(t *testing.T) {
	state := accepted(t, checkerMessage(t, true))
	state.Expire(now)
	var published []notify.Publication
	for {
		entry, ok := state.Take()
		if !ok {
			break
		}
		published = append(published, entry.Publication())
		if retry := state.Settle(entry, true, now); retry {
			t.Fatalf("a sent copy is done: %+v", entry)
		}
	}
	link := body["click"].(string)
	high := notify.Publication{Title: "orders/staging deployed", Message: "Flux UpgradeSucceeded", Priority: 4,
		Tags: []string{"warning"}, Click: link}
	if !reflect.DeepEqual(published, []notify.Publication{high, high}) {
		t.Fatalf("published = %+v", published)
	}
	wantSeen := map[string]float64{"event:1@notify@payments-phone": now + notify.History,
		"event:1@notify@payments-desk": now + notify.History}
	if len(state.Pending) != 0 || !reflect.DeepEqual(state.Seen, wantSeen) {
		t.Fatalf("state = %+v", state)
	}
	if queued, _ := state.Accept(checkerMessage(t, true), team, now+1); len(queued) != 0 {
		t.Fatalf("a repeated request is not sent twice: %v", queued)
	}
	state.Expire(now + notify.History)
	if queued, _ := state.Accept(checkerMessage(t, true), team, now+notify.History); len(queued) != 2 {
		t.Fatalf("a key is remembered for History only: %v", queued)
	}
}

// test_a_channel_that_fails_or_has_no_secret_is_retried_without_holding_up_the_others; and, as the shared delivery
// rule (test_a_copy_that_is_not_accepted_..._is_given_up_after_the_retry_window), a copy that has failed for
// ChannelRetry since it was queued is given up and remembered like a delivered one.
func TestAChannelThatFailsIsRetriedWithoutHoldingUpTheOthers(t *testing.T) {
	state := accepted(t, checkerMessage(t, false))
	var delivered []string
	var waiting []notify.Entry
	for entry, ok := state.Take(); ok; entry, ok = state.Take() {
		sent := entry.Channel != "payments-phone"
		if sent {
			delivered = append(delivered, entry.Channel)
		}
		if state.Settle(entry, sent, now) {
			waiting = append(waiting, entry)
		}
	}
	state.Requeue(waiting)
	if !reflect.DeepEqual(delivered, []string{"payments-desk"}) || len(state.Pending) != 1 ||
		state.Pending[0].Channel != "payments-phone" || len(state.Seen) != 1 {
		t.Fatalf("delivered %v, state %+v", delivered, state)
	}
	entry, _ := state.Take()
	if !state.Settle(entry, false, now+notify.ChannelRetry-1) {
		t.Fatal("a copy is retried until ChannelRetry")
	}
	if state.Settle(entry, false, now+notify.ChannelRetry) {
		t.Fatal("a copy that has failed for ChannelRetry is given up")
	}
	if until := state.Seen["event:1@notify@payments-phone"]; until != now+notify.ChannelRetry+notify.History {
		t.Fatalf("a given-up copy is remembered like a delivered one: %v", until)
	}
	held := 60.0
	entry.Key, entry.History = "own-history", &held
	state.Settle(entry, true, now)
	if state.Seen["own-history"] != now+60 {
		t.Fatalf("an entry's own history is kept: %v", state.Seen)
	}
}

// test_a_restarted_service_sends_what_the_last_one_accepted: the outbox read back is the one saved, and one of
// another version or shape is refused, never reset.
func TestARestartedServiceSendsWhatTheLastOneAccepted(t *testing.T) {
	state := accepted(t, checkerMessage(t, false))
	state.Settle(notify.Entry{Key: "event:0@notify@payments-phone"}, true, now)
	encoded, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := notify.DecodeState(encoded)
	if err != nil || !reflect.DeepEqual(restarted, state) {
		t.Fatalf("restarted = %+v, %v", restarted, err)
	}
	for _, raw := range []string{`{"version": 2}`, `{"version": 2, "seen": {}, "pending": []}`, `{"version": "1", "seen": {}, "pending": []}`,
		`{"seen": {}, "pending": []}`, `{"version": 1}`, `{"version": 1, "seen": [], "pending": []}`,
		`{"version": 1, "seen": {}, "pending": {}}`, `{"version": 1, "seen": null, "pending": []}`,
		`{"version": 1, "seen": {"k": "soon"}, "pending": []}`, `{"version": 1, "seen": {}, "pending": [7]}`,
		`{"version": 1, "seen": {}, "pending": [{"queued": "now"}]}`, `[]`, `null`, `{`} {
		if _, err := notify.DecodeState(raw); !errors.Is(err, notify.ErrInvalidOutbox) ||
			!strings.Contains(err.Error(), "refusing to reset delivery history") {
			t.Fatalf("DecodeState(%s) = %v", raw, err)
		}
	}
	empty, err := notify.DecodeState("")
	if err != nil || len(empty.Pending) != 0 || len(empty.Seen) != 0 || empty.Seen == nil {
		t.Fatalf("no outbox yet is a new one: %+v, %v", empty, err)
	}
}

// The outbox the Python service wrote is read as it wrote it: spaced or compact, with \u escapes, whole times as
// floats and copies missing fields.
func TestThePythonServicesOutboxIsReadAsItWroteIt(t *testing.T) {
	state, err := notify.DecodeState(`{"version": 1.0, "seen": {"event:0@notify@payments-phone": 1800007200.0}, "pending": [` +
		`{"title": "caf\u00e9 \ud83d\udea8", "message": "m", "priority": 4, "tags": ["warning"], "click": "https://x.test",` +
		` "key": "event:1@notify@payments-phone", "channel": "payments-phone", "queued": 1800000000.5, "app": "orders",` +
		` "env": "staging"}, {"key": "0", "history": 60}]}`)
	if err != nil {
		t.Fatal(err)
	}
	held := 60.0
	want := notify.State{Seen: map[string]float64{"event:0@notify@payments-phone": now + 7200}, Pending: []notify.Entry{
		{Title: "café 🚨", Message: "m", Priority: 4, Tags: []string{"warning"}, Click: "https://x.test",
			Key: "event:1@notify@payments-phone", Channel: "payments-phone", Queued: now + 0.5, App: "orders", Env: "staging"},
		{Key: "0", History: &held}}}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("state = %+v", state)
	}
}

// The outbox is written compact with every non-ASCII character escaped, as Python's json.dumps writes it, so its
// bound counts the bytes the Python service counted. Python writes a whole time as 1800000000.0; Go writes
// 1800000000, which both read as the same time.
func TestTheOutboxIsWrittenAsThePythonServiceWroteIt(t *testing.T) {
	request := notify.Request{Instance: notify.Instance{App: "orders", Env: "staging"}, Key: "k",
		Notice: notify.Notice{Title: "café 🚨\x7f <&>", Message: "line\n\ttab\b\f\x01\u2028", High: true, Link: "https://x.test"}}
	var state notify.State
	if _, err := state.Accept(request, []string{"phone"}, now); err != nil {
		t.Fatal(err)
	}
	state.Settle(notify.Entry{Key: "b"}, true, now)
	state.Settle(notify.Entry{Key: "a"}, true, now+0.25)
	encoded, err := state.Encode()
	want := `{"version":1,"seen":{"a":1800007200.25,"b":1800007200},"pending":[{"title":"caf\u00e9 \ud83d\udea8\u007f <&>",` +
		`"message":"line\n\ttab\b\f\u0001\u2028","priority":4,"tags":["warning"],"click":"https://x.test","key":"k@phone",` +
		`"channel":"phone","queued":1800000000,"app":"orders","env":"staging"}]}`
	if err != nil || encoded != want {
		t.Fatalf("Encode =\n%s, %v\nwant\n%s", encoded, err, want)
	}
	if encoded, _ := (notify.State{}).Encode(); encoded != `{"version":1,"seen":{},"pending":[]}` {
		t.Fatalf("an empty outbox = %s", encoded)
	}
}

// An outbox over MaxOutboxBytes once encoded is refused, counted as Python counted it: an escaped é is six bytes.
func TestAnOutboxOverItsBoundIsNotWritten(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", notify.MaxOutboxBytes), strings.Repeat("é", notify.MaxOutboxBytes/6+1)} {
		state := notify.State{Pending: []notify.Entry{{Message: text}}}
		if _, err := state.Encode(); !errors.Is(err, notify.ErrOutboxTooLarge) {
			t.Fatalf("Encode of a %d-character message = %v", len([]rune(text)), err)
		}
	}
	state := notify.State{Pending: []notify.Entry{{Message: strings.Repeat("é", notify.MaxOutboxBytes/6-50)}}}
	if _, err := state.Encode(); err != nil {
		t.Fatal(err)
	}
}

// A key is forgotten once its time is up, and only then: the Python service kept a key while its time was later
// than now.
func TestExpireForgetsOnlyKeysWhoseTimeIsUp(t *testing.T) {
	state := notify.State{Seen: map[string]float64{"past": now - 1, "now": now, "later": now + 1}}
	state.Expire(now)
	if !reflect.DeepEqual(state.Seen, map[string]float64{"later": now + 1}) {
		t.Fatalf("seen = %v", state.Seen)
	}
}

// Notice.fields and Notice.to_ntfy (notifications/notice.py): a copy of priority 4 or more is high, anything else
// normal, and ntfy is sent its fields then the topic, without a link it does not have.
func TestThePublicationIsWhatNtfyIsSent(t *testing.T) {
	for priority, want := range map[int]int{5: 4, 4: 4, 3: 3, 0: 3} {
		if got := (notify.Entry{Priority: priority}).Publication(); got.Priority != want {
			t.Fatalf("priority %d published as %+v", priority, got)
		}
	}
	entry := notify.Entry{Title: "orders/staging deployed", Message: "m <&>", Priority: 4, Tags: []string{"warning"},
		Click: "https://console.example.test/apps/orders/staging", Key: "k@phone", Channel: "phone", Queued: now}
	document, err := entry.Publication().Document("private-topic")
	want := `{"title":"orders/staging deployed","message":"m \u003c\u0026\u003e","priority":4,"tags":["warning"],` +
		`"click":"https://console.example.test/apps/orders/staging","topic":"private-topic"}`
	if err != nil || string(document) != want {
		t.Fatalf("Document = %s, %v", document, err)
	}
	document, _ = notify.Entry{Title: "t"}.Publication().Document("private-topic")
	if string(document) != `{"title":"t","message":"","priority":3,"tags":["white_check_mark"],"topic":"private-topic"}` {
		t.Fatalf("Document = %s", document)
	}
}
