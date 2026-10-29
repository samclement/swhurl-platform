// Package events resolves which event subscriptions connect. A subscription is connected only when the
// publisher exists in the same environment and publishes that event; nothing here decides consent, because a
// subscription is the subscriber's own grant. It reads nothing: the caller supplies the declarations.
//
// The names NATS needs for one subscription are derived here — the publisher's stream, the subscriber's durable
// consumer on it, the filter subject and the Consumer object's name — so a rendered stream, a rendered consumer
// and a user's permission cannot disagree about which event a subscription is.
package events

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Declaration is one instance's event facts: the events its release publishes and the subscriptions its own
// record grants, each spelled `<publisher>.<event>`.
type Declaration struct {
	Application, Environment string
	Publishes                []string
	Subscriptions            []string
	// PublicationsKnown is whether Publishes is everything the release publishes. A record shape that cannot
	// state publications yet leaves it false, so a subscription to that app waits instead of being refused for an
	// event it may well publish.
	PublicationsKnown bool
}

// Subscription is one connected subscription, with every name it needs: the publisher's stream, this
// subscriber's durable consumer on that stream, the subject the stream filters on, and the Consumer object.
type Subscription struct {
	Publisher, Event string
	Stream, Consumer string
	Subject, Object  string
}

// Resolved is one declaration's answer: the subscriptions that connected, in the order the record states them,
// and those that wait for a publisher that is not there yet. A waiting subscription grants nothing and renders
// nothing until the publisher appears.
type Resolved struct {
	Connected []Subscription
	Pending   []string
}

// Error is a declaration that is wrong whatever any other instance says, named by its field and a fixed reason.
type Error struct {
	Application, Environment, Field, Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("invalid: %s/%s %s: %s", e.Application, e.Environment, e.Field, e.Reason)
}

// The fixed reasons a refused declaration carries.
const (
	InvalidSubscription = "invalid_subscription"
	SelfSubscription    = "self_subscription"
	Repeated            = "repeated"
	ConsumerCollision   = "consumer_collision"
	NotPublished        = "not_published"
)

// Resolve answers every declaration in the order it was given: one Resolved per Declaration, so a caller zips
// them by index. Each declaration is validated before it is resolved, so a malformed subscription is reported
// before a publisher that is merely absent.
func Resolve(declarations []Declaration) ([]Resolved, error) {
	byInstance, err := index(declarations)
	if err != nil {
		return nil, err
	}
	answers := make([]Resolved, 0, len(declarations))
	for _, declaration := range declarations {
		connected, err := declared(declaration)
		if err != nil {
			return nil, err
		}
		answer := Resolved{Connected: []Subscription{}, Pending: []string{}}
		for _, subscription := range connected {
			publishes, err := reached(byInstance, declaration, subscription)
			if err != nil {
				return nil, err
			}
			if publishes {
				answer.Connected = append(answer.Connected, subscription)
			} else {
				answer.Pending = append(answer.Pending, subscription.Publisher+"."+subscription.Event)
			}
		}
		answers = append(answers, answer)
	}
	return answers, nil
}

// index is every declaration by application and environment, refusing a repeat: two declarations for one
// instance would make its own publications ambiguous.
func index(declarations []Declaration) ([]Declaration, error) {
	seen := map[string]bool{}
	ordered := make([]Declaration, 0, len(declarations))
	for _, declaration := range declarations {
		if err := validateDeclaration(declaration); err != nil {
			return nil, err
		}
		key := declaration.Environment + "/" + declaration.Application
		if seen[key] {
			return nil, &Error{declaration.Application, declaration.Environment, "events", "repeated_instance"}
		}
		seen[key] = true
		ordered = append(ordered, declaration)
	}
	return ordered, nil
}

// validateDeclaration checks the instance's own facts: its names and the events it publishes.
func validateDeclaration(declaration Declaration) error {
	if !namePattern().MatchString(declaration.Application) {
		return &Error{declaration.Application, declaration.Environment, "application", "invalid_app"}
	}
	if declaration.Environment != "staging" && declaration.Environment != "prod" {
		return &Error{declaration.Application, declaration.Environment, "environment", "invalid_environment"}
	}
	for _, event := range declaration.Publishes {
		if !namePattern().MatchString(event) {
			return &Error{declaration.Application, declaration.Environment, "publishes", "invalid_event"}
		}
	}
	return nil
}

// declared turns one instance's subscription names into connected subscriptions, checking what can be checked
// without another instance: the spelling, the instance calling itself, a repeat, and two subscriptions whose
// Consumer objects would collide in one namespace.
func declared(declaration Declaration) ([]Subscription, error) {
	fail := func(reason string) error {
		return &Error{declaration.Application, declaration.Environment, "subscriptions", reason}
	}
	found, seen, objects := []Subscription{}, map[string]bool{}, map[string]string{}
	for _, name := range declaration.Subscriptions {
		publisher, event, split := strings.Cut(name, ".")
		if !split || !namePattern().MatchString(publisher) || !namePattern().MatchString(event) {
			return nil, fail(InvalidSubscription)
		}
		if publisher == declaration.Application {
			return nil, fail(SelfSubscription)
		}
		if seen[name] {
			return nil, fail(Repeated)
		}
		seen[name] = true
		object := Object(publisher, event)
		if _, taken := objects[object]; taken {
			// `a-b.c` and `a.b-c` name one Consumer object, so one subscription would silently stand for the
			// other: refuse rather than rename either.
			return nil, fail(ConsumerCollision)
		}
		objects[object] = name
		found = append(found, Subscription{Publisher: publisher, Event: event,
			Stream: Stream(declaration.Environment, publisher), Consumer: Consumer(declaration.Application, event),
			Subject: Subject(declaration.Environment, publisher, event), Object: object})
	}
	return found, nil
}

// reached is whether a subscription's publisher is there and publishes that event. An absent publisher is not
// an error — the subscription waits, and grants nothing meanwhile — nor is a publisher whose record cannot state
// its publications yet. A publisher that is there, states what it publishes and does not publish the event is
// one: the subscriber asked for something that will never arrive.
func reached(byInstance []Declaration, subscriber Declaration, subscription Subscription) (bool, error) {
	publisher, found := find(byInstance, subscriber.Environment, subscription.Publisher)
	switch {
	case !found, !publisher.PublicationsKnown:
		return false, nil
	case !slices.Contains(publisher.Publishes, subscription.Event):
		return false, &Error{subscriber.Application, subscriber.Environment, "subscriptions", NotPublished}
	}
	return true, nil
}

// find is one instance in one environment.
func find(byInstance []Declaration, environment, application string) (Declaration, bool) {
	for _, declaration := range byInstance {
		if declaration.Environment == environment && declaration.Application == application {
			return declaration, true
		}
	}
	return Declaration{}, false
}

// Stream is the publisher's stream: one per app per environment.
func Stream(environment, publisher string) string { return environment + "-" + publisher }

// Consumer is the durable consumer on a publisher's stream: one per subscriber and event, because a consumer
// remembers what one subscriber has seen.
func Consumer(subscriber, event string) string { return subscriber + "_" + event }

// Object is the Consumer object's name in the subscriber's namespace.
func Object(publisher, event string) string { return publisher + "-" + event }

// Subject is the subject an event travels on: only the publisher of that environment may publish it.
func Subject(environment, publisher, event string) string {
	return environment + "." + publisher + "." + event
}

func namePattern() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
}
