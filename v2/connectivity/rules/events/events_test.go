package events_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/samclement/swhurl-platform/v2/events"
)

func declaration(application, environment string, publishes, subscriptions []string) events.Declaration {
	return events.Declaration{Application: application, Environment: environment,
		Publishes: publishes, Subscriptions: subscriptions, PublicationsKnown: true}
}

// reason is the refusal's fixed reason; the application and field say whose fact was wrong.
func reason(t *testing.T, err error, want, application, field string) {
	t.Helper()
	var refusal *events.Error
	if !errors.As(err, &refusal) || refusal.Reason != want || refusal.Application != application || refusal.Field != field {
		t.Fatalf("error = %v, want %q on %s's %s", err, want, application, field)
	}
}

// A subscription connects when the publisher is in the same environment and publishes the event, and it carries
// every name the stream, the consumer and the permission need.
func TestResolveConnects(t *testing.T) {
	answers, err := events.Resolve([]events.Declaration{
		declaration("orders", "staging", []string{"created", "cancelled"}, nil),
		declaration("notifier", "staging", nil, []string{"orders.created"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 2 || len(answers[0].Connected) != 0 || len(answers[0].Pending) != 0 {
		t.Fatalf("a publisher has nothing to resolve: %+v", answers)
	}
	want := events.Subscription{Publisher: "orders", Event: "created", Stream: "staging-orders",
		Consumer: "notifier_created", Subject: "staging.orders.created", Object: "orders-created"}
	if len(answers[1].Connected) != 1 || answers[1].Connected[0] != want {
		t.Fatalf("connected %+v, want %+v", answers[1].Connected, want)
	}
	if len(answers[1].Pending) != 0 {
		t.Fatalf("pending %v", answers[1].Pending)
	}
}

// The other environment is another publisher: a staging subscriber waits for a production app, it does not
// read its events.
func TestEnvironmentsDoNotMix(t *testing.T) {
	answers, err := events.Resolve([]events.Declaration{
		declaration("orders", "prod", []string{"created"}, nil),
		declaration("notifier", "staging", nil, []string{"orders.created"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(answers[1].Connected) != 0 || !slices.Equal(answers[1].Pending, []string{"orders.created"}) {
		t.Fatalf("a staging subscriber read production: %+v", answers[1])
	}
}

// An absent publisher is a wait, not an error; a publisher that is there and does not publish the event is.
func TestAbsentWaitsAndUnpublishedRefuses(t *testing.T) {
	answers, err := events.Resolve([]events.Declaration{
		declaration("notifier", "staging", nil, []string{"orders.created", "missing.created"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"orders.created", "missing.created"}; !slices.Equal(answers[0].Pending, want) {
		t.Fatalf("pending %v, want %v", answers[0].Pending, want)
	}
	_, err = events.Resolve([]events.Declaration{
		declaration("orders", "staging", []string{"created"}, nil),
		declaration("notifier", "staging", nil, []string{"orders.cancelled"}),
	})
	reason(t, err, events.NotPublished, "notifier", "subscriptions")
}

// Every declaration that is wrong whatever any other instance says.
func TestDeclarationRefusals(t *testing.T) {
	for _, tt := range []struct {
		name         string
		declarations []events.Declaration
		reason       string
		application  string
		field        string
	}{
		{"a malformed subscription", []events.Declaration{
			declaration("notifier", "staging", nil, []string{"orders"})}, events.InvalidSubscription, "notifier", "subscriptions"},
		{"an app name that is not one", []events.Declaration{
			declaration("notifier", "staging", nil, []string{"Orders.created"})}, events.InvalidSubscription, "notifier", "subscriptions"},
		{"an event name that is not one", []events.Declaration{
			declaration("notifier", "staging", nil, []string{"orders.created_at"})}, events.InvalidSubscription, "notifier", "subscriptions"},
		{"subscribing to itself", []events.Declaration{
			declaration("notifier", "staging", []string{"created"}, []string{"notifier.created"})}, events.SelfSubscription, "notifier", "subscriptions"},
		{"a repeated subscription", []events.Declaration{
			declaration("notifier", "staging", nil, []string{"orders.created", "orders.created"}),
			declaration("orders", "staging", []string{"created"}, nil)}, events.Repeated, "notifier", "subscriptions"},
		{"consumer names that collide", []events.Declaration{
			declaration("notifier", "staging", nil, []string{"a-b.c", "a.b-c"}),
			declaration("a-b", "staging", []string{"c"}, nil), declaration("a", "staging", []string{"b-c"}, nil)},
			events.ConsumerCollision, "notifier", "subscriptions"},
		{"an invalid event it publishes", []events.Declaration{
			declaration("orders", "staging", []string{"Created"}, nil)}, eventsInvalid, "orders", "publishes"},
		{"an environment that is not one", []events.Declaration{
			declaration("orders", "preview", nil, nil)}, eventsEnvironment, "orders", "environment"},
		{"an application that is not one", []events.Declaration{
			declaration("Orders", "staging", nil, nil)}, eventsInvalidApp, "Orders", "application"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := events.Resolve(tt.declarations)
			reason(t, err, tt.reason, tt.application, tt.field)
		})
	}
	// Two declarations for one instance would make its own publications ambiguous.
	_, err := events.Resolve([]events.Declaration{
		declaration("orders", "staging", []string{"created"}, nil),
		declaration("orders", "staging", []string{"cancelled"}, nil),
	})
	reason(t, err, "repeated_instance", "orders", "events")
}

// A declaration is validated before a publisher is looked for, so a malformed subscription is reported even when
// nothing publishes anything, and the caller's order is the answer's order.
func TestValidationPrecedesResolution(t *testing.T) {
	_, err := events.Resolve([]events.Declaration{
		declaration("notifier", "staging", nil, []string{"missing.created", "orders"}),
	})
	reason(t, err, events.InvalidSubscription, "notifier", "subscriptions")
	answers, err := events.Resolve([]events.Declaration{
		declaration("zebra", "prod", nil, []string{"orders.created"}),
		declaration("orders", "staging", []string{"created"}, nil),
		declaration("notifier", "staging", nil, []string{"orders.created"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 3 || len(answers[0].Pending) != 1 || len(answers[1].Connected) != 0 || len(answers[2].Connected) != 1 {
		t.Fatalf("answers are not one per declaration in order: %+v", answers)
	}
}

// The name helpers are the platform's spelling, and a caller that keeps them cannot drift from a rendered one.
func TestNames(t *testing.T) {
	for _, tt := range []struct{ got, want string }{
		{events.Stream("staging", "orders"), "staging-orders"},
		{events.Consumer("notifier", "created"), "notifier_created"},
		{events.Subject("staging", "orders", "created"), "staging.orders.created"},
		{events.Object("orders", "created"), "orders-created"},
	} {
		if tt.got != tt.want {
			t.Errorf("%q, want %q", tt.got, tt.want)
		}
	}
}

// The resolver reads nothing and keeps nothing: the same input gives the same answer, and a result does not
// alias the declarations it came from.
func TestResolveIsPure(t *testing.T) {
	declarations := []events.Declaration{
		declaration("orders", "staging", []string{"created"}, nil),
		declaration("notifier", "staging", nil, []string{"orders.created"}),
	}
	first, err := events.Resolve(declarations)
	if err != nil {
		t.Fatal(err)
	}
	again, err := events.Resolve(declarations)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("not repeatable: %+v %+v", first, again)
	}
	declarations[0].Publishes[0] = "changed"
	if again, _ := events.Resolve([]events.Declaration{
		declaration("orders", "staging", []string{"created"}, nil),
		declaration("notifier", "staging", nil, []string{"orders.created"}),
	}); !reflect.DeepEqual(first, again) {
		t.Fatal("an answer depends on the caller's later edits")
	}
}

// eventsInvalid and eventsEnvironment are the reasons the shared name helpers check, named here so a change to
// one is visible in this test.
const (
	eventsInvalid     = "invalid_event"
	eventsInvalidApp  = "invalid_app"
	eventsEnvironment = "invalid_environment"
)
