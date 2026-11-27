package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samclement/swhurl-platform/v2/connectivity/rules/calls"
	"github.com/samclement/swhurl-platform/v2/connectivity/rules/events"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// ResolutionRecordName is the resolution's file in the records tree, beside every app's folder.
const ResolutionRecordName = "calls.json"

// Records is one instance's three records, undecoded, as the resolution and the allocation read its declarations:
// what it calls, admits, subscribes to and may reach from its own record, what it runs and publishes from its
// release, and its team from its app's record.
type Records struct {
	Application, Environment string
	Instance, Release, App   []byte
}

// Resolve is the records' relationships as the resolution document states them, at the revision they were read at:
// the calls' pairs, waits and refusals, and the event subscriptions that connected or wait. Relationships are one
// decision, so they are one document.
func Resolve(revision string, instances []Records) (kernel.ConnectionsDocument, error) {
	declarations, err := callDeclarations(instances)
	if err != nil {
		return kernel.ConnectionsDocument{}, err
	}
	resolution, err := calls.Resolve(declarations)
	if err != nil {
		return kernel.ConnectionsDocument{}, err
	}
	answers, err := events.Resolve(eventDeclarations(instances))
	if err != nil {
		return kernel.ConnectionsDocument{}, err
	}
	document := connectionsFrom(revision, resolution)
	document.Subscriptions, document.EventWaiting = subscriptionsFrom(instances, answers)
	return document, nil
}

// EncodeConnectionsDocument is the resolution's bytes: indented, no HTML escaping, one trailing newline trimmed so
// the file is exactly the document.
func EncodeConnectionsDocument(document kernel.ConnectionsDocument) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// callDeclarations is every instance's side of the pairs it takes part in, read from its records. An instance with no
// release record states no kind, so a call to it is not connected.
func callDeclarations(instances []Records) ([]calls.Declaration, error) {
	declarations := make([]calls.Declaration, 0, len(instances))
	for _, instance := range instances {
		declaration, err := callDeclaration(instance)
		if err != nil {
			return nil, err
		}
		declarations = append(declarations, declaration)
	}
	return declarations, nil
}

// callDeclaration is one instance's side of every pair it takes part in: its own record states the declarations and
// its release the runtime facts.
func callDeclaration(instance Records) (calls.Declaration, error) {
	declaration := calls.Declaration{Application: instance.Application, Environment: instance.Environment}
	var settings struct {
		Calls, Allows []string
	}
	if err := json.Unmarshal(instance.Instance, &settings); err != nil {
		return calls.Declaration{}, fmt.Errorf("invalid: instance record: %s", "expected_an_object")
	}
	declaration.Calls, declaration.Allows = settings.Calls, settings.Allows
	var app struct {
		Team string
	}
	if err := json.Unmarshal(instance.App, &app); err == nil {
		declaration.Team = app.Team
	}
	var release struct {
		Runtime struct {
			Kind string
			Port int32
		}
	}
	if err := json.Unmarshal(instance.Release, &release); err == nil {
		declaration.Kind, declaration.Port = release.Runtime.Kind, release.Runtime.Port
	}
	return declaration, nil
}

// connectionsFrom is the document's shape of the calls' resolution, in the order the resolver produced.
func connectionsFrom(revision string, resolution calls.Resolution) kernel.ConnectionsDocument {
	document := kernel.ConnectionsDocument{Kind: "v2-calls", Version: 1, Revision: revision,
		Pairs:   make([]kernel.ConnectionsPair, 0, len(resolution.Pairs)),
		Waiting: make([]kernel.ConnectionsWaiting, 0, len(resolution.Waiting)),
		Refused: make([]kernel.ConnectionsRefusal, 0, len(resolution.Refused))}
	for _, pair := range resolution.Pairs {
		document.Pairs = append(document.Pairs,
			kernel.ConnectionsPair{Caller: pair.Caller, Callee: pair.Callee, Environment: pair.Environment, Port: pair.CalleePort})
	}
	for _, wait := range resolution.Waiting {
		document.Waiting = append(document.Waiting,
			kernel.ConnectionsWaiting{Caller: wait.Caller, Callee: wait.Callee, Environment: wait.Environment, Reason: wait.Reason})
	}
	for _, refusal := range resolution.Refused {
		document.Refused = append(document.Refused, kernel.ConnectionsRefusal{Application: refusal.Application,
			Callee: refusal.Callee, Environment: refusal.Environment, Reason: refusal.Reason})
	}
	return document
}

// eventDeclarations is every instance's side of the event relationships: the events its release publishes and the
// subscriptions its own record grants. A release renders its stream and its publish variable from its publications,
// so an absent list means none, and a subscription to an instance that does not publish that event is refused rather
// than left waiting for something that will never come.
func eventDeclarations(instances []Records) []events.Declaration {
	declarations := make([]events.Declaration, 0, len(instances))
	for _, instance := range instances {
		declarations = append(declarations, events.Declaration{Application: instance.Application,
			Environment: instance.Environment, Publishes: declaredPublishes(instance),
			Subscriptions: declaredSubscriptions(instance), PublicationsKnown: true})
	}
	return declarations
}

// subscriptionsFrom is the connected subscriptions as the document states them, and the ones that wait, in the order
// the instances and their records give.
func subscriptionsFrom(instances []Records, answers []events.Resolved) ([]kernel.ConnectionsSubscription,
	[]kernel.ConnectionsWaiting) {
	connected := []kernel.ConnectionsSubscription{}
	waiting := []kernel.ConnectionsWaiting{}
	for index, instance := range instances {
		for _, subscription := range answers[index].Connected {
			connected = append(connected, kernel.ConnectionsSubscription{Subscriber: instance.Application,
				Publisher: subscription.Publisher, Event: subscription.Event, Environment: instance.Environment,
				Stream: subscription.Stream, Consumer: subscription.Consumer, Subject: subscription.Subject,
				Object: subscription.Object})
		}
		for _, name := range answers[index].Pending {
			publisher, _, _ := strings.Cut(name, ".")
			waiting = append(waiting, kernel.ConnectionsWaiting{Caller: instance.Application, Callee: publisher,
				Environment: instance.Environment, Reason: "publisher_not_ready"})
		}
	}
	return connected, waiting
}

// EventUsers is every instance that signs in to the broker, with what it may do. An instance that declares events
// and has no key is refused rather than left out: the list would otherwise say it needs nothing.
func EventUsers(instances []Records) ([]kernel.NATSUser, error) {
	facts := eventDeclarations(instances)
	answers, err := events.Resolve(facts)
	if err != nil {
		return nil, err
	}
	users := make([]kernel.NATSUser, 0, len(instances))
	for index, instance := range instances {
		key := EventsKey(instance.Instance)
		if key == "" {
			if len(facts[index].Publishes) > 0 || len(facts[index].Subscriptions) > 0 {
				return nil, fmt.Errorf("unsupported: users.key: %s/%s declares events and has no key",
					instance.Application, instance.Environment)
			}
			continue
		}
		users = append(users, kernel.NATSUser{Application: instance.Application, Environment: instance.Environment,
			NKey: key, Publishes: len(facts[index].Publishes) > 0, Subscriptions: userSubscriptions(answers[index])})
	}
	return users, nil
}

// EventsKey is the public key an instance's record says it signs in with, or empty when it has none. Every instance
// the platform renders has one, whether or not it declares events: the same identity reads its feature-flag
// overrides.
func EventsKey(instance []byte) string {
	var settings struct {
		Events struct{ Key string }
	}
	if err := json.Unmarshal(instance, &settings); err != nil {
		return ""
	}
	return settings.Events.Key
}

// declaredPublishes is the events an instance's release publishes, from the list its image's AsyncAPI document gave
// it.
func declaredPublishes(instance Records) []string {
	var release struct {
		Publishes []string
	}
	if err := json.Unmarshal(instance.Release, &release); err != nil || release.Publishes == nil {
		return []string{}
	}
	return release.Publishes
}

// declaredSubscriptions is the events an instance's own record grants it, each spelled `<publisher>.<event>`.
func declaredSubscriptions(instance Records) []string {
	var settings struct {
		Events struct{ Subscriptions []string }
	}
	if err := json.Unmarshal(instance.Instance, &settings); err != nil {
		return []string{}
	}
	return settings.Events.Subscriptions
}

// userSubscriptions is one instance's answer as the renderer's own type: a connected subscription carries every name
// it needs, so the user list, a rendered consumer and a rendered stream cannot disagree about one subscription.
func userSubscriptions(answer events.Resolved) []kernel.EventSubscription {
	found := make([]kernel.EventSubscription, 0, len(answer.Connected))
	for _, subscription := range answer.Connected {
		found = append(found, kernel.EventSubscription{Publisher: subscription.Publisher, Event: subscription.Event,
			Stream: subscription.Stream, Consumer: subscription.Consumer, Subject: subscription.Subject,
			Object: subscription.Object})
	}
	return found
}
