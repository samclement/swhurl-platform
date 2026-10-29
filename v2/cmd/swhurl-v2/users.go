package main

import (
	"bytes"
	"encoding/json"
	"fmt"

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
	profile, status := c.profile(r.profile)
	if status != 0 {
		return status
	}
	data, status := c.read(r.snapshot, "snapshot")
	if status != 0 {
		return status
	}
	pinned, err := decodeSnapshot(data)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	instances, err := usersFactOf(pinned)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	// The two service identities are the platform's own, so they come from the profile with the broker's
	// address and the Secret that decrypts an instance's seed.
	rendered, err := kernel.RenderUsers(kernel.NATSUsers{ControllerKey: profile.NATS.ControllerKey,
		ConsoleKey: profile.NATS.ConsoleKey, Instances: instances})
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
				return nil, fmt.Errorf("unsupported: users.key: %s/%s declares events and has no key",
					instance.Application, instance.Environment)
			}
			continue
		}
		users = append(users, kernel.NATSUser{Application: instance.Application, Environment: instance.Environment,
			NKey: key, Publishes: len(facts[index].Publishes) > 0, Subscriptions: subscriptions(answers[index])})
	}
	return users, nil
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

// eventsKey is the public key an instance signs in with, or empty when it has none. Every instance the platform
// renders has one, whether or not it declares events: the same identity reads its feature-flag overrides. A
// predecessor states it with its other allocations; a v2 instance states it in its own `events` block.
func eventsKey(instance snapshotInstance) string {
	var settings struct {
		Allocations struct {
			EventsKey string `json:"eventsKey"`
		} `json:"allocations"`
		Events struct{ Key string }
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil {
		return ""
	}
	if settings.Events.Key != "" {
		return settings.Events.Key
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
// A predecessor states them at the top level; a v2 instance states them in its own `events` block.
func declaredSubscriptions(instance snapshotInstance) []string {
	var settings struct {
		Subscriptions []string
		Events        struct{ Subscriptions []string }
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil {
		return []string{}
	}
	if settings.Subscriptions == nil {
		return settings.Events.Subscriptions
	}
	return settings.Subscriptions
}

// subscriptions is one instance's answer as the renderer's own type: a connected subscription carries every name
// it needs, so the user list, a rendered consumer and a rendered stream cannot disagree about one subscription.
func subscriptions(answer events.Resolved) []kernel.EventSubscription {
	found := make([]kernel.EventSubscription, 0, len(answer.Connected))
	for _, subscription := range answer.Connected {
		found = append(found, kernel.EventSubscription{Publisher: subscription.Publisher, Event: subscription.Event,
			Stream: subscription.Stream, Consumer: subscription.Consumer, Subject: subscription.Subject,
			Object: subscription.Object})
	}
	return found
}
