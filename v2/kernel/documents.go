package kernel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The two documents a compilation takes beside its records: the platform's egress allocation, which says
// which port names each instance to the proxy, and the resolution, which says which pairs are connected.
// Both are versioned documents that name the commit they were read at, so a compilation's evidence includes
// where its relationships came from. They are decoded here rather than in a command because a command may
// not be the only reader: the use cases above the kernel take them too, and a command sits above those.

// EgressDocument is the platform's allocation: one port per instance, and the hosts each declares it needs.
type EgressDocument struct {
	Kind      string             `json:"kind"`
	Version   int                `json:"version"`
	Revision  string             `json:"revision"`
	Instances []EgressAllocation `json:"instances"`
}

// EgressAllocation is one instance's place on the proxy. It is used both as an input (a previous
// allocation, kept) and as an output (the allocation written), so the document and the decision share it.
type EgressAllocation struct {
	Application string   `json:"application"`
	Environment string   `json:"environment"`
	Port        int32    `json:"port"`
	Grants      []string `json:"grants"`
}

// DecodeEgressDocument reads an allocation and checks what every reader of it depends on: a document this
// platform wrote, the commit it was allocated at, and an allocation this kernel could render.
func DecodeEgressDocument(data []byte) (EgressDocument, error) {
	var document EgressDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return EgressDocument{}, fmt.Errorf("invalid: egress: single_json_object_required")
	}
	if document.Version != 1 || document.Kind != "v2-egress" {
		return EgressDocument{}, fmt.Errorf("unsupported: egress: only v2-egress version 1")
	}
	if !commitPattern.MatchString(document.Revision) {
		return EgressDocument{}, fmt.Errorf("invalid: egress: revision_required")
	}
	for _, allocated := range document.Instances {
		if allocated.Port < 1024 || allocated.Port > 65535 || allocated.Application == "" || allocated.Environment == "" {
			return EgressDocument{}, fmt.Errorf("invalid: egress: allocation_required")
		}
	}
	return document, nil
}

// EncodeEgressDocument is the document's bytes: indented, no HTML escaping, one trailing newline trimmed so
// the file is exactly the document.
func EncodeEgressDocument(document EgressDocument) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// EgressOf picks one instance's own way out out of an allocation. An instance the document does not name has
// none, which is a refusal: an instance compiled with --egress asked for one.
func (d EgressDocument) EgressOf(application, environment string) (*Egress, error) {
	for _, allocated := range d.Instances {
		if allocated.Application == application && allocated.Environment == environment {
			return &Egress{Port: allocated.Port, Grants: slices.Clone(allocated.Grants)}, nil
		}
	}
	return nil, fmt.Errorf("missing: egress: the instance has no allocation")
}

// ConnectionsDocument is the resolution: every connected pair, every declaration that waits, every refusal.
type ConnectionsDocument struct {
	Kind     string               `json:"kind"`
	Version  int                  `json:"version"`
	Revision string               `json:"revision"`
	Pairs    []ConnectionsPair    `json:"pairs"`
	Waiting  []ConnectionsWaiting `json:"waiting"`
	Refused  []ConnectionsRefusal `json:"refused"`
	// Subscriptions are the event subscriptions that connected. They sit in the same document as the pairs
	// because they are the same decision — which relationships an instance takes part in — and a compilation
	// takes one document rather than two.
	Subscriptions []ConnectionsSubscription `json:"subscriptions"`
}

// ConnectionsSubscription is one connected subscription with every name a stream, a consumer and a user's
// permission need, so no reader derives one the resolver did not agree with.
type ConnectionsSubscription struct {
	Subscriber  string `json:"subscriber"`
	Publisher   string `json:"publisher"`
	Event       string `json:"event"`
	Environment string `json:"environment"`
	Stream      string `json:"stream"`
	Consumer    string `json:"consumer"`
	Subject     string `json:"subject"`
	Object      string `json:"object"`
}

// ConnectionsPair is one connected pair: the callee's port is the caller's fact, so a caller renders its
// address and its rule from the pair alone.
type ConnectionsPair struct {
	Caller      string `json:"caller"`
	Callee      string `json:"callee"`
	Environment string `json:"environment"`
	Port        int32  `json:"port"`
}

type ConnectionsWaiting struct {
	Caller      string `json:"caller"`
	Callee      string `json:"callee"`
	Environment string `json:"environment"`
	Reason      string `json:"reason"`
}

type ConnectionsRefusal struct {
	Application string `json:"application"`
	Callee      string `json:"callee"`
	Environment string `json:"environment"`
	Reason      string `json:"reason"`
}

// ConnectionsOf reads the resolution document the resolver wrote and keeps what one instance takes part in: the
// pairs it calls or is called in, and the subscriptions that connected. The document carries every name a
// rendered consumer needs, so a compilation cannot derive one the resolver did not agree with.
func ConnectionsOf(data []byte, application, environment string) (Connections, error) {
	document, err := resolution(data)
	if err != nil {
		return Connections{}, err
	}
	connections := Connections{}
	for _, pair := range document.Pairs {
		if pair.Environment != environment {
			continue
		}
		if pair.Caller == application {
			connections.Calls = append(connections.Calls, Call{Callee: pair.Callee, Port: pair.Port})
		}
		if pair.Callee == application {
			connections.Callers = append(connections.Callers, pair.Caller)
		}
	}
	slices.SortFunc(connections.Calls, func(a, b Call) int {
		if a.Callee != b.Callee {
			return strings.Compare(a.Callee, b.Callee)
		}
		return int(a.Port - b.Port)
	})
	slices.Sort(connections.Callers)
	connections.Subscriptions = subscriptionsOf(document.Subscriptions, application, environment)
	return connections, nil
}

// SubscribersOf is the apps that receive one instance's events: every connected subscription in which it is the
// publisher, in the resolver's order. A removal refuses while one remains, because the subscriber's Consumer names a
// Stream the removed instance's own folder would take with it.
func SubscribersOf(data []byte, application, environment string) ([]string, error) {
	document, err := resolution(data)
	if err != nil {
		return nil, err
	}
	subscribers := []string{}
	for _, subscription := range document.Subscriptions {
		if subscription.Publisher == application && subscription.Environment == environment {
			subscribers = append(subscribers, subscription.Subscriber)
		}
	}
	return subscribers, nil
}

// resolution reads a resolution document and checks the facts every reader of it depends on.
func resolution(data []byte) (ConnectionsDocument, error) {
	var document ConnectionsDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return ConnectionsDocument{}, fmt.Errorf("invalid: calls: single_json_object_required")
	}
	if document.Version != 1 || document.Kind != "v2-calls" {
		return ConnectionsDocument{}, fmt.Errorf("unsupported: calls: only v2-calls version 1")
	}
	if !commitPattern.MatchString(document.Revision) {
		return ConnectionsDocument{}, fmt.Errorf("invalid: calls: revision_required")
	}
	return document, nil
}

// subscriptionsOf is one instance's connected subscriptions, in the document's order, which is the resolver's.
func subscriptionsOf(document []ConnectionsSubscription, application, environment string) []EventSubscription {
	found := []EventSubscription{}
	for _, subscription := range document {
		if subscription.Subscriber != application || subscription.Environment != environment {
			continue
		}
		found = append(found, EventSubscription{Publisher: subscription.Publisher, Event: subscription.Event,
			Stream: subscription.Stream, Consumer: subscription.Consumer, Subject: subscription.Subject,
			Object: subscription.Object})
	}
	return found
}
