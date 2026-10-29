package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samclement/swhurl-platform/v2/events"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// usersDocument is what `users` writes: the server's user list, the revision its facts were read at, and the
// object it claims. The list itself is the evidence — keys and permissions, never a seed.
type usersDocument struct {
	Kind      string         `json:"kind"`
	Version   int            `json:"version"`
	Revision  string         `json:"revision"`
	Users     int            `json:"users"`
	Files     []kernel.File  `json:"files"`
	Inventory []kernel.Claim `json:"inventory"`
}

// users renders the NATS server's user list from one pinned snapshot. The two service identities are the
// platform's own published public keys, given as the files they are; the instances' facts come from the snapshot,
// so the list says what that revision of Git says.
func (c cli) users(r request) int {
	data, status := c.read(r.snapshot, "snapshot")
	if status != 0 {
		return status
	}
	pinned, err := decodeSnapshot(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	controller, status := c.publicKey(r.controllerKey, "controller key")
	if status != 0 {
		return status
	}
	console, status := c.publicKey(r.consoleKey, "console key")
	if status != 0 {
		return status
	}
	instances, err := usersFactOf(pinned)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	rendered, err := kernel.RenderUsers(kernel.NATSUsers{ControllerKey: controller, ConsoleKey: console,
		Instances: instances})
	if err != nil {
		return fail(c.diagnostic, err)
	}
	document := usersDocument{Kind: "v2-users", Version: 1, Revision: pinned.Revision, Users: len(instances) + 2,
		Files: rendered.Files, Inventory: rendered.Inventory}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fail(c.diagnostic, err)
	}
	encoded := bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
	fmt.Fprintf(c.diagnostic, "users: %d user(s), %d file(s)\n", document.Users, len(document.Files))
	if r.out != "" {
		return c.writeFiles(r.out, rendered.Files)
	}
	return emit(c.out, c.diagnostic, encoded)
}

// usersFactOf is every instance that signs in, with what it may do. An instance that declares events and has no
// key is refused rather than left out: the list would otherwise say it needs nothing.
func usersFactOf(pinned snapshot) ([]kernel.NATSUser, error) {
	facts := eventsFacts(pinned)
	answers, err := events.Resolve(facts)
	if err != nil {
		return nil, err
	}
	users := make([]kernel.NATSUser, 0, len(pinned.Instances))
	for index, instance := range pinned.Instances {
		key := eventsKey(instance)
		if key == "" {
			if len(facts[index].Publishes) > 0 || len(facts[index].Subscriptions) > 0 {
				return nil, fmt.Errorf("unsupported: users.key: an_instance_that_declares_events_needs_a_key")
			}
			continue
		}
		users = append(users, kernel.NATSUser{Application: instance.Application, Environment: instance.Environment,
			NKey: key, Publishes: len(facts[index].Publishes) > 0, Subscriptions: subscriptions(answers[index])})
	}
	return users, nil
}

// publicKey reads one published nkey file: the platform's controller or console identity, which is public
// material with the seed elsewhere.
func (c cli) publicKey(path, what string) (string, int) {
	data, status := c.read(path, what)
	if status != 0 {
		return "", status
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fail(c.diagnostic, fmt.Errorf("invalid: %s: empty", what))
	}
	return key, 0
}

// eventsFacts is what each instance's records state about events: the events its release publishes and the
// subscriptions its own record grants.
func eventsFacts(pinned snapshot) []events.Declaration {
	facts := make([]events.Declaration, 0, len(pinned.Instances))
	for _, instance := range pinned.Instances {
		facts = append(facts, events.Declaration{Application: instance.Application,
			Environment: instance.Environment, Publishes: declaredPublishes(instance),
			Subscriptions: declaredSubscriptions(instance),
			// A v2-owned release cannot state its publications until the instance side of this domain lands, so a
			// subscription to one waits rather than being refused for an event its image may well publish.
			PublicationsKnown: instance.Format != "v2"})
	}
	return facts
}

// eventsKey is the public key an instance signs in with, or empty when it has none. Every instance v1 renders has
// one, whether or not it declares events: the same identity reads its feature-flag overrides.
func eventsKey(instance snapshotInstance) string {
	var settings struct {
		Allocations struct {
			EventsKey string `json:"eventsKey"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil {
		return ""
	}
	return settings.Allocations.EventsKey
}

// declaredPublishes is the events an instance's release publishes, from the list its image's AsyncAPI document
// gave it.
func declaredPublishes(instance snapshotInstance) []string {
	var release struct {
		Publishes []string
	}
	if err := json.Unmarshal(instance.Records["release"], &release); err != nil || release.Publishes == nil {
		return []string{}
	}
	return release.Publishes
}

// declaredSubscriptions is the events an instance's own record grants it, each spelled `<publisher>.<event>`.
func declaredSubscriptions(instance snapshotInstance) []string {
	var settings struct {
		Subscriptions []string
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil || settings.Subscriptions == nil {
		return []string{}
	}
	return settings.Subscriptions
}

// subscriptions is one instance's answer as the renderer's own type: a connected subscription carries the names
// the permission is spelled with, so the user list and a rendered consumer cannot disagree.
func subscriptions(answer events.Resolved) []kernel.NATSSubscription {
	found := make([]kernel.NATSSubscription, 0, len(answer.Connected))
	for _, subscription := range answer.Connected {
		found = append(found, kernel.NATSSubscription{Stream: subscription.Stream, Consumer: subscription.Consumer,
			Subject: subscription.Subject})
	}
	return found
}
