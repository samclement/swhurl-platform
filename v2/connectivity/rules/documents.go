// Package rules is connectivity's pure model: the settings a change to an instance's declarations writes, and its
// reading of its two documents: the platform's egress allocation, which says which port names each instance to the
// proxy, and the resolution, which says which pairs are connected. Both documents are versioned
// documents that name the commit they were read at, so a compilation's evidence includes where its relationships
// came from. Their shapes are the kernel's model types; this package owns their bytes and what one instance takes
// from them, so every use case and command that reads them reads them the same way. It also owns the types of what an
// instance's offered documents say (Offers), which the appsource adapter decodes and the console's pages word.
package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// DecodeEgressDocument reads an allocation and checks what every reader of it depends on: a document this
// platform wrote, the commit it was allocated at, and an allocation the kernel could render.
func DecodeEgressDocument(data []byte) (kernel.EgressDocument, error) {
	var document kernel.EgressDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return kernel.EgressDocument{}, fmt.Errorf("invalid: egress: single_json_object_required")
	}
	if document.Version != 1 || document.Kind != "v2-egress" {
		return kernel.EgressDocument{}, fmt.Errorf("unsupported: egress: only v2-egress version 1")
	}
	if !revisionPattern.MatchString(document.Revision) {
		return kernel.EgressDocument{}, fmt.Errorf("invalid: egress: revision_required")
	}
	for _, allocated := range document.Instances {
		if allocated.Port < 1024 || allocated.Port > 65535 || allocated.Application == "" || allocated.Environment == "" {
			return kernel.EgressDocument{}, fmt.Errorf("invalid: egress: allocation_required")
		}
	}
	return document, nil
}

// EncodeEgressDocument is the document's bytes: indented, no HTML escaping, one trailing newline trimmed so
// the file is exactly the document.
func EncodeEgressDocument(document kernel.EgressDocument) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// InstanceEgress reads the allocation's place for one instance. The document is given whole and an instance takes it only
// when it records something that instance may reach: a port kept for an instance with nothing to reach is not a
// reason to put it behind the proxy, and a database's backup grant is what makes it use one. A caller with no
// allocation passes nil and gets nil, and an instance the document does not name takes none; whether an instance may
// be without one is the renderer's refusal (a database must leave through the proxy), not this function's.
func InstanceEgress(document []byte, application, environment string) (*kernel.Egress, error) {
	if document == nil {
		return nil, nil
	}
	allocation, err := DecodeEgressDocument(document)
	if err != nil {
		return nil, err
	}
	own, err := allocation.EgressOf(application, environment)
	if err != nil || len(own.Grants) == 0 {
		return nil, nil
	}
	return own, nil
}

// DecodeConnectionsDocument reads a resolution document and checks the facts every reader of it depends on.
func DecodeConnectionsDocument(data []byte) (kernel.ConnectionsDocument, error) {
	var document kernel.ConnectionsDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return kernel.ConnectionsDocument{}, fmt.Errorf("invalid: calls: single_json_object_required")
	}
	if document.Version != 1 || document.Kind != "v2-calls" {
		return kernel.ConnectionsDocument{}, fmt.Errorf("unsupported: calls: only v2-calls version 1")
	}
	if !revisionPattern.MatchString(document.Revision) {
		return kernel.ConnectionsDocument{}, fmt.Errorf("invalid: calls: revision_required")
	}
	return document, nil
}

// ConnectionsOf reads the resolution document the resolver wrote and keeps what one instance takes part in: the
// pairs it calls or is called in, and the subscriptions that connected. The document carries every name a
// rendered consumer needs, so a compilation cannot derive one the resolver did not agree with.
func ConnectionsOf(data []byte, application, environment string) (kernel.Connections, error) {
	document, err := DecodeConnectionsDocument(data)
	if err != nil {
		return kernel.Connections{}, err
	}
	connections := kernel.Connections{}
	for _, pair := range document.Pairs {
		if pair.Environment != environment {
			continue
		}
		if pair.Caller == application {
			connections.Calls = append(connections.Calls, kernel.Call{Callee: pair.Callee, Port: pair.Port})
		}
		if pair.Callee == application {
			connections.Callers = append(connections.Callers, pair.Caller)
		}
	}
	slices.SortFunc(connections.Calls, func(a, b kernel.Call) int {
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
	document, err := DecodeConnectionsDocument(data)
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

// subscriptionsOf is one instance's connected subscriptions, in the document's order, which is the resolver's.
func subscriptionsOf(document []kernel.ConnectionsSubscription, application, environment string) []kernel.EventSubscription {
	found := []kernel.EventSubscription{}
	for _, subscription := range document {
		if subscription.Subscriber != application || subscription.Environment != environment {
			continue
		}
		found = append(found, kernel.EventSubscription{Publisher: subscription.Publisher, Event: subscription.Event,
			Stream: subscription.Stream, Consumer: subscription.Consumer, Subject: subscription.Subject,
			Object: subscription.Object})
	}
	return found
}
