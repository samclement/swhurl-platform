package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samclement/swhurl-platform/v2/calls"
	"github.com/samclement/swhurl-platform/v2/events"
)

// resolutionDocument is the versioned document `resolve` writes and `compile --calls` reads: every connected
// pair, every declaration that waits, every refusal, and every event subscription that connected, from one
// pinned snapshot. Relationships are one decision, so they are one document.
type resolutionDocument struct {
	Kind          string                 `json:"kind"`
	Version       int                    `json:"version"`
	Revision      string                 `json:"revision"`
	Pairs         []pairDocument         `json:"pairs"`
	Waiting       []waitingDocument      `json:"waiting"`
	Refused       []refusedDocument      `json:"refused"`
	Subscriptions []subscriptionDocument `json:"subscriptions"`
	// Waiting and Refused are the calls' and the events' own answers; a subscription that waits or is refused is
	// named here so a report says which of an instance's declarations are not connected.
	EventWaiting []waitingDocument `json:"eventWaiting"`
	EventRefused []refusedDocument `json:"eventRefused"`
}

// subscriptionDocument is one connected subscription with every name a stream, a consumer and a user's
// permission need.
type subscriptionDocument struct {
	Subscriber  string `json:"subscriber"`
	Publisher   string `json:"publisher"`
	Event       string `json:"event"`
	Environment string `json:"environment"`
	Stream      string `json:"stream"`
	Consumer    string `json:"consumer"`
	Subject     string `json:"subject"`
	Object      string `json:"object"`
}

// pairDocument is one connected pair: who calls whom, in which environment, and on which port the
// callee listens, so a caller can render its address and egress rule from this document alone.
type pairDocument struct {
	Caller      string `json:"caller"`
	Callee      string `json:"callee"`
	Environment string `json:"environment"`
	Port        int32  `json:"port"`
}
type waitingDocument struct {
	Caller      string `json:"caller"`
	Callee      string `json:"callee"`
	Environment string `json:"environment"`
	Reason      string `json:"reason"`
}
type refusedDocument struct {
	Application string `json:"application"`
	Callee      string `json:"callee"`
	Environment string `json:"environment"`
	Reason      string `json:"reason"`
}

// resolve reads a snapshot's declarations and reports which pairs are connected. Consent is a declaration's
// fact, so this needs no cluster, no preview and no credential: a worker can be declared and still refuse.
func (c cli) resolve(r request) int {
	data, status := c.read(r.snapshot, "snapshot")
	if status != 0 {
		return status
	}
	pinned, err := decodeSnapshot(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	declarations, err := declarationsOf(pinned)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	resolution, err := calls.Resolve(declarations)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	eventResolution, err := events.Resolve(eventDeclarationsOf(pinned))
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document := resolutionFrom(pinned.Revision, resolution)
	document.Subscriptions, document.EventWaiting = subscriptionsFrom(pinned, eventResolution)
	encoded, err := encodeResolution(document)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	fmt.Fprintf(c.diagnostic, "resolve: %d connected pair(s), %d waiting, %d refused; %d event subscription(s), %d waiting\n",
		len(document.Pairs), len(document.Waiting), len(document.Refused), len(document.Subscriptions),
		len(document.EventWaiting))
	return emit(c.out, c.diagnostic, encoded)
}

// declarationsOf is every instance's side of the pairs it takes part in, read from the records the
// snapshot holds. An instance with no release record states no kind, so a call to it is not connected.
func declarationsOf(pinned snapshot) ([]calls.Declaration, error) {
	declarations := make([]calls.Declaration, 0, len(pinned.Instances))
	for _, instance := range pinned.Instances {
		declaration, err := declarationOf(instance)
		if err != nil {
			return nil, err
		}
		declarations = append(declarations, declaration)
	}
	return declarations, nil
}

// declarationOf is one instance's side of every pair it takes part in, from whichever record shape the export
// read: v2's own pair states the declarations and nests the runtime facts, a predecessor's records state them
// at the top level.
func declarationOf(instance snapshotInstance) (calls.Declaration, error) {
	declaration := calls.Declaration{Application: instance.Application, Environment: instance.Environment}
	var settings struct {
		Calls, Allows []string
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil {
		return calls.Declaration{}, fmt.Errorf("invalid: instance record: %s", "expected_an_object")
	}
	declaration.Calls, declaration.Allows = settings.Calls, settings.Allows
	var app struct {
		Team string
	}
	if err := json.Unmarshal(instance.Records["app"], &app); err == nil {
		declaration.Team = app.Team
	}
	var release struct {
		Kind    string
		Port    int32
		Runtime struct {
			Kind string
			Port int32
		}
	}
	if err := json.Unmarshal(instance.Records["release"], &release); err == nil {
		declaration.Kind, declaration.Port = release.Kind, release.Port
		if instance.Format == "v2" {
			declaration.Kind, declaration.Port = release.Runtime.Kind, release.Runtime.Port
		}
	}
	return declaration, nil
}

// resolutionFrom is the document's shape of a resolution, in the order the resolver produced.
func resolutionFrom(revision string, resolution calls.Resolution) resolutionDocument {
	document := resolutionDocument{Kind: "v2-calls", Version: 1, Revision: revision,
		Pairs: make([]pairDocument, 0, len(resolution.Pairs)), Waiting: make([]waitingDocument, 0, len(resolution.Waiting)),
		Refused: make([]refusedDocument, 0, len(resolution.Refused))}
	for _, pair := range resolution.Pairs {
		document.Pairs = append(document.Pairs, pairDocument{pair.Caller, pair.Callee, pair.Environment, pair.CalleePort})
	}
	for _, wait := range resolution.Waiting {
		document.Waiting = append(document.Waiting, waitingDocument{wait.Caller, wait.Callee, wait.Environment, wait.Reason})
	}
	for _, refusal := range resolution.Refused {
		document.Refused = append(document.Refused, refusedDocument{refusal.Application, refusal.Callee, refusal.Environment, refusal.Reason})
	}
	return document
}

func encodeResolution(document resolutionDocument) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// eventDeclarationsOf is every instance's side of the event relationships: the events its release publishes and
// the subscriptions its own record grants. Both record shapes state the publications a release has — a v2 release
// renders its stream and its publish variable from them, so an absent list means none — and a subscription to an
// instance that does not publish that event is refused rather than left waiting for something that will never come.
func eventDeclarationsOf(pinned snapshot) []events.Declaration {
	declarations := make([]events.Declaration, 0, len(pinned.Instances))
	for _, instance := range pinned.Instances {
		declarations = append(declarations, events.Declaration{Application: instance.Application,
			Environment: instance.Environment, Publishes: declaredPublishes(instance),
			Subscriptions: declaredSubscriptions(instance), PublicationsKnown: true})
	}
	return declarations
}

// subscriptionsFrom is the connected subscriptions as the document states them, and the ones that wait, in the
// order the instances and their records give.
func subscriptionsFrom(pinned snapshot, answers []events.Resolved) ([]subscriptionDocument, []waitingDocument) {
	connected := []subscriptionDocument{}
	waiting := []waitingDocument{}
	for index, instance := range pinned.Instances {
		for _, subscription := range answers[index].Connected {
			connected = append(connected, subscriptionDocument{Subscriber: instance.Application,
				Publisher: subscription.Publisher, Event: subscription.Event, Environment: instance.Environment,
				Stream: subscription.Stream, Consumer: subscription.Consumer, Subject: subscription.Subject,
				Object: subscription.Object})
		}
		for _, name := range answers[index].Pending {
			publisher, _, _ := strings.Cut(name, ".")
			waiting = append(waiting, waitingDocument{Caller: instance.Application, Callee: publisher,
				Environment: instance.Environment, Reason: "publisher_not_ready"})
		}
	}
	return connected, waiting
}
