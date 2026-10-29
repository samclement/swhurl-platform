package kernel

import (
	"bytes"
	"strings"
)

// One instance's side of the event service: the variables it reads, the Secret that holds its seed, the unit's
// dependency on the broker, and the stream and consumers it declares. What it publishes is the release's; what it
// receives is its own grant, already resolved.
//
// The seed itself is never here: it is an encrypted Secret in the instance's own folder — a file v2 lists and
// never writes or decrypts — so a record states the public key and the rendered files state where the private
// half is read from.
const (
	// eventsSecretSuffix names the Secret holding one instance's seed; the file that carries it is the
	// instance's declared input.
	eventsSecretSuffix = "-events"
	// EventsSecretFile is where that file sits in an instance's folder: beside the manifests, because a unit
	// can only list resources inside its own directory.
	EventsSecretFile = "events.sops.yaml"
	// natsUnit is the platform unit that serves the broker; an instance with a key waits for it.
	natsUnit = "platform-nats"
	// events.apiVersion is the stream controller's API (NACK), which turns these objects into streams and
	// consumers. v1's manifests name the same version, so both write what the controller reads.
	eventsAPIVersion = "jetstream.nats.io/v1beta2"
)

// The stream and consumer policy the platform applies to every instance's events. They are the compiler's own
// choices, like the Helm interval: changing one changes every compilation that renders an event and the objects
// the controller already created. A stream is not a system of record, so events expire rather than accumulate.
const (
	streamRetention  = "168h"
	streamMaxBytes   = 64 << 20 // 64 MiB, v1's stream limit: the oldest events go first
	consumerAckWait  = "30s"
	consumerMaxRetry = 5
)

// EventSubscription is one connected subscription with every name it needs: the publisher's stream, this
// subscriber's durable consumer on it, the subject the stream holds, and the Consumer object's name.
type EventSubscription struct {
	Publisher, Event string
	Stream, Consumer string
	Subject, Object  string
}

// eventsSecretName is the Secret one instance reads its seed from.
func eventsSecretName(application string) string { return application + eventsSecretSuffix }

// seedVariable is the name the image reads its seed under. The unit mounts the Secret with envFrom, so the
// variable's name is part of the Secret's contract, not the image's choice.
const seedVariable = "SWHURL_EVENTS_SEED"

// EventsSecret renders one instance's broker seed as the Secret its unit decrypts: the object the record folder
// holds and `compile` copies beside the manifests. The result is plaintext — a private key — so the caller
// encrypts it before it is written anywhere, and v2 never writes this document itself.
func EventsSecret(spec InstanceSpec, pair UserKey) ([]byte, error) {
	if !namePattern.MatchString(spec.Application) ||
		(spec.Environment != "staging" && spec.Environment != "prod") {
		return nil, refuse(Invalid, "events.secret", "expected an app name and an environment")
	}
	if pair.Seed == "" || pair.Public == "" {
		return nil, refuse(Invalid, "events.secret", "expected a key pair")
	}
	document := manifest{"apiVersion": "v1", "kind": "Secret",
		"metadata": manifest{"name": eventsSecretName(spec.Application),
			"namespace": NamespaceOf(spec.Application, spec.Environment)},
		"type": "Opaque", "stringData": manifest{seedVariable: pair.Seed}}
	return encodeManifest(document), nil
}

// CheckEncryptedSeed is what v2 can say about a ciphertext it cannot decrypt: it must hold at least one encrypted
// value, and it must not hold the plaintext seed. v2 never decrypts, so this is the whole of the check — whether
// the platform's identity can read it is proved when the workload starts.
func CheckEncryptedSeed(document []byte, seed string) error {
	if bytes.Contains(document, []byte(seed)) {
		return refuse(Invalid, "events.secret", "the ciphertext holds the plaintext seed")
	}
	if !bytes.Contains(document, []byte("ENC[")) {
		return refuse(Invalid, "events.secret", "the ciphertext holds no encrypted value")
	}
	return nil
}

// WithBrokerKey is the instance record with a pair's public key stated. Replacing a key that exists is a
// rotation — a live identity change that interrupts connections — so it must be asked for; and a rotation of
// nothing is a mistake, not a new identity.
func WithBrokerKey(spec InstanceSpec, pair UserKey, rotate bool) (InstanceSpec, error) {
	if !nkeyPattern().MatchString(pair.Public) {
		return InstanceSpec{}, refuse(Invalid, "events.key", "expected a user public nkey")
	}
	switch {
	case spec.Events.Key != "" && !rotate:
		return InstanceSpec{}, refuse(Unsupported, "events.key",
			"the instance already has a broker identity; replacing it is a rotation")
	case spec.Events.Key == "" && rotate:
		return InstanceSpec{}, refuse(Unsupported, "events.key", "the instance has no broker identity to rotate")
	}
	spec.Events.Key = pair.Public
	return spec, nil
}

// eventsEnv is what an instance reads to sign in and to know what it publishes and receives.
func eventsEnv(spec InstanceFacts, http WorkloadFacts, nats NATS) manifest {
	env := manifest{"SWHURL_EVENTS_NKEY": spec.Broker.Key, "SWHURL_EVENTS_URL": nats.URL}
	if len(http.Publishes) > 0 {
		env["SWHURL_EVENTS_PUBLISH"] = joinNames(http.Publishes)
	}
	if len(spec.Broker.Connected) > 0 {
		names := make([]string, 0, len(spec.Broker.Connected))
		for _, subscription := range spec.Broker.Connected {
			names = append(names, subscription.Publisher+"."+subscription.Event)
		}
		env["SWHURL_EVENTS_SUBSCRIBE"] = joinNames(names)
	}
	return env
}

// joinNames is a comma-separated list in its canonical order, so the same facts render the same variable.
func joinNames(names []string) string { return strings.Join(sortedNames(names), ",") }

// eventsObjects are the cluster objects an instance's events declare: its stream when it publishes, and one
// durable consumer per connected subscription. The stream keeps everything under the instance's own subject;
// a consumer remembers what one subscriber has seen, so a subscriber that was down receives what it missed.
func eventsObjects(spec InstanceFacts, http WorkloadFacts) []struct {
	name string
	doc  manifest
} {
	namespace := namespaceName(spec)
	if len(http.Publishes) == 0 && len(spec.Broker.Connected) == 0 {
		return nil
	}
	var objects []struct {
		name string
		doc  manifest
	}
	if len(http.Publishes) > 0 {
		stream := spec.Environment + "-" + spec.Application
		objects = append(objects, struct {
			name string
			doc  manifest
		}{"stream.json", manifest{"apiVersion": eventsAPIVersion, "kind": "Stream",
			"metadata": metadata(stream, namespace),
			"spec": manifest{"name": stream, "subjects": []any{spec.Environment + "." + spec.Application + ".>"},
				"storage": "file", "preventDelete": true, "replicas": 1, "retention": "limits", "discard": "old",
				"maxAge": streamRetention, "maxBytes": streamMaxBytes}}})
	}
	for _, subscription := range spec.Broker.Connected {
		objects = append(objects, struct {
			name string
			doc  manifest
		}{"consumer-" + subscription.Object + ".json", manifest{"apiVersion": eventsAPIVersion, "kind": "Consumer",
			"metadata": metadata(subscription.Object, namespace),
			"spec": manifest{"streamName": subscription.Stream, "durableName": subscription.Consumer,
				"filterSubject": subscription.Subject, "ackPolicy": "explicit", "deliverPolicy": "all",
				"ackWait": consumerAckWait, "maxDeliver": consumerMaxRetry}}})
	}
	return objects
}

// brokerRule is the egress one instance needs to sign in: the broker's pods on its own port, by selector. Every
// other way out is the platform's (DNS, the proxy); without this rule an instance's identity could not reach the
// service it signs in to, because an instance's policy denies egress by default.
func brokerRule(nats NATS) manifest {
	return manifest{
		"to": []any{manifest{
			"namespaceSelector": manifest{"matchLabels": manifest{"kubernetes.io/metadata.name": nats.Namespace}},
			"podSelector":       manifest{"matchLabels": labelManifest(nats.Labels)}}},
		"ports": []any{manifest{"port": nats.Port, "protocol": "TCP"}}}
}

// validateEvents checks what an instance's side of the event service needs: a broker for a key, a key for
// anything it publishes or receives, and names a consumer could be built from.
func (spec InstanceFacts) validateEvents(http WorkloadFacts, profile PlatformProfile) error {
	if spec.Broker.Key != "" && (profile.NATS.URL == "" || profile.NATS.Namespace == "") {
		return refuse(Unsupported, "nats", "the instance has a broker identity and the profile states no broker")
	}
	if spec.Broker.Key == "" && (len(http.Publishes) > 0 || len(spec.Broker.Connected) > 0) {
		return refuse(Invalid, "events.key", "an instance that publishes or receives events needs a key")
	}
	if err := spec.Broker.validate(); err != nil {
		return err
	}
	return validateEventNames("publishes", http.Publishes)
}

// validate checks the three names every connected subscription carries.
func (b BrokerIdentity) validate() error {
	for _, subscription := range b.Connected {
		if subscription.Stream == "" || subscription.Consumer == "" || subscription.Subject == "" || subscription.Object == "" {
			return refuse(Invalid, "events.subscriptions", "expected a stream, a consumer, a subject and an object")
		}
	}
	return nil
}
