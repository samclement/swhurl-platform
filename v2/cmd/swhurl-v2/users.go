package main

import (
	"encoding/json"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/events"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// renderUsers is the server's user list for the records, and the instances it lists. The two service identities are
// the platform's own published public keys, given as the files they are.
func renderUsers(pinned snapshot, profile kernel.PlatformProfile) ([]kernel.NATSUser, kernel.ResourceSet, error) {
	instances, err := usersFactOf(pinned)
	if err != nil {
		return nil, kernel.ResourceSet{}, err
	}
	// The two service identities are the platform's own, so they come from the profile with the broker's
	// address and the Secret that decrypts an instance's seed.
	rendered, err := kernel.RenderUsers(kernel.NATSUsers{ControllerKey: profile.NATS.ControllerKey,
		ConsoleKey: profile.NATS.ConsoleKey, Instances: instances})
	return instances, rendered, err
}

// usersFactOf is every instance that signs in, with what it may do. An instance that declares events and has no
// key is refused rather than left out: the list would otherwise say it needs nothing.
func usersFactOf(pinned snapshot) ([]kernel.NATSUser, error) {
	facts := eventDeclarationsOf(pinned)
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

// eventsKey is the public key an instance signs in with, or empty when it has none. Every instance the platform
// renders has one, whether or not it declares events: the same identity reads its feature-flag overrides.
func eventsKey(instance snapshotInstance) string {
	var settings struct {
		Events struct{ Key string }
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil {
		return ""
	}
	return settings.Events.Key
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
		Events struct{ Subscriptions []string }
	}
	if err := json.Unmarshal(instance.Records["instance"], &settings); err != nil {
		return []string{}
	}
	return settings.Events.Subscriptions
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
