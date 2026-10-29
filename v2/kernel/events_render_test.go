package kernel

import (
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// eventFacts is one instance that publishes and one that receives: the facts the renderer needs, with the
// resolution's names already derived.
func eventFacts() (InstanceFacts, WorkloadFacts) {
	subscriber, workload := previewFacts()
	subscriber.Application = "notifier"
	subscriber.Broker = BrokerIdentity{Key: "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI",
		Connected: []EventSubscription{{Publisher: "orders", Event: "created", Stream: "staging-orders",
			Consumer: "notifier_created", Subject: "staging.orders.created", Object: "orders-created"}}}
	return subscriber, workload
}

// A subscriber gets the broker's address and its own key, the Secret holding its seed, one consumer beside the
// manifests, and a unit that waits for the broker and decrypts the folder.
func TestEventsRenderTheInstanceSide(t *testing.T) {
	instance, workload := eventFacts()
	rendered, err := RenderInstance(instance, workload, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	values := documentOf(t, rendered, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	controller := values["controllers"].(map[string]any)["main"].(map[string]any)
	if annotation := controller["annotations"].(map[string]any)[reloadAnnotation]; annotation != "notifier-events" {
		t.Fatalf("a new seed must restart the app: %v", controller["annotations"])
	}
	container := controller["containers"].(map[string]any)["main"].(map[string]any)
	env := container["env"].(map[string]any)
	for name, want := range map[string]string{
		"SWHURL_EVENTS_NKEY":      instance.Broker.Key,
		"SWHURL_EVENTS_URL":       "nats://nats.messaging.svc.cluster.local:4222",
		"SWHURL_EVENTS_SUBSCRIBE": "orders.created",
	} {
		if env[name] != want {
			t.Errorf("env %s = %v, want %q", name, env[name], want)
		}
	}
	if _, found := env["SWHURL_EVENTS_PUBLISH"]; found {
		t.Errorf("a subscriber publishes nothing: %v", env["SWHURL_EVENTS_PUBLISH"])
	}
	sources := container["envFrom"].([]any)
	if sources[0].(map[string]any)["secretRef"].(map[string]any)["name"] != "notifier-events" {
		t.Fatalf("the seed is not read from the instance's Secret: %v", sources)
	}
	consumer := documentOf(t, rendered, "consumer-orders-created.json")
	if consumer["apiVersion"] != "jetstream.nats.io/v1beta2" || consumer["kind"] != "Consumer" {
		t.Fatalf("the consumer: %v", consumer)
	}
	spec := consumer["spec"].(map[string]any)
	for field, want := range map[string]any{"streamName": "staging-orders", "durableName": "notifier_created",
		"filterSubject": "staging.orders.created", "ackPolicy": "explicit", "deliverPolicy": "all",
		"ackWait": "30s", "maxDeliver": float64(5)} {
		if spec[field] != want {
			t.Errorf("consumer %s = %v, want %v", field, spec[field], want)
		}
	}
	// The folder lists the seed beside the manifests: the file itself is the operator's, and v2 never writes it.
	resources := documentOf(t, rendered, "kustomization.yaml")["resources"].([]any)
	if last := resources[len(resources)-1]; last != "events.sops.yaml" {
		t.Fatalf("the kustomization does not list the seed last: %v", resources)
	}
	unit := documentOf(t, rendered, "app-notifier-staging.json")["spec"].(map[string]any)
	if !containsDependency(unit["dependsOn"].([]any), "platform-nats") {
		t.Fatalf("the unit does not wait for the broker: %v", unit["dependsOn"])
	}
	if decryption := unit["decryption"].(map[string]any); decryption["provider"] != "sops" {
		t.Fatalf("the unit does not decrypt its folder: %v", decryption)
	}
	// What the files create is claimed by the instance: the consumer, and the Secret it reads.
	claims := []Object{}
	for _, claim := range rendered.Inventory {
		claims = append(claims, claim.Object)
	}
	for _, want := range []Object{{"jetstream.nats.io", "Consumer", "notifier-staging", "orders-created"},
		{"", "Secret", "notifier-staging", "notifier-events"}} {
		if !slices.Contains(claims, want) {
			t.Errorf("the inventory lacks %v: %v", want, claims)
		}
	}
}

// A publisher gets its stream, the variable naming what it publishes, and its own subject in the user list; the
// stream keeps everything under the instance's name.
func TestEventsRenderTheStream(t *testing.T) {
	instance, workload := eventFacts()
	instance.Application = "orders"
	instance, workload = publisher(instance, workload)
	rendered, err := RenderInstance(instance, workload, profile(t))
	if err != nil {
		t.Fatal(err)
	}
	values := documentOf(t, rendered, "helmrelease.json")["spec"].(map[string]any)["values"].(map[string]any)
	env := values["controllers"].(map[string]any)["main"].(map[string]any)["containers"].(map[string]any)["main"].(map[string]any)["env"].(map[string]any)
	if env["SWHURL_EVENTS_PUBLISH"] != "cancelled,created" {
		t.Fatalf("a publisher names its events in order: %v", env["SWHURL_EVENTS_PUBLISH"])
	}
	stream := documentOf(t, rendered, "stream.json")
	if stream["kind"] != "Stream" || stream["metadata"].(map[string]any)["name"] != "staging-orders" {
		t.Fatalf("the stream: %v", stream)
	}
	spec := stream["spec"].(map[string]any)
	for field, want := range map[string]any{"name": "staging-orders", "storage": "file", "preventDelete": true,
		"replicas": float64(1), "retention": "limits", "discard": "old", "maxAge": "168h", "maxBytes": float64(67108864)} {
		if spec[field] != want {
			t.Errorf("stream %s = %v, want %v", field, spec[field], want)
		}
	}
	if subjects := spec["subjects"].([]any); len(subjects) != 1 || subjects[0] != "staging.orders.>" {
		t.Fatalf("the stream's subjects: %v", subjects)
	}
}

// publisher turns the facts into the publishing instance, which states two events so their order is visible.
func publisher(instance InstanceFacts, workload WorkloadFacts) (InstanceFacts, WorkloadFacts) {
	instance.Broker.Connected = nil
	workload.Publishes = []string{"created", "cancelled"}
	return instance, workload
}

// What the instance side refuses: a key without a broker to sign in to, events without a key, and a subscription
// whose consumer could not be named.
func TestEventsRefusals(t *testing.T) {
	profile := profile(t)
	for _, tt := range []struct {
		name   string
		change func(*InstanceFacts, *WorkloadFacts, *PlatformProfile)
		code   Code
		field  string
	}{
		{"no broker", func(_ *InstanceFacts, _ *WorkloadFacts, p *PlatformProfile) { p.NATS = NATS{} },
			Unsupported, "nats"},
		{"events without a key", func(s *InstanceFacts, _ *WorkloadFacts, _ *PlatformProfile) { s.Broker.Key = "" },
			Invalid, "events.key"},
		{"publications without a key", func(s *InstanceFacts, w *WorkloadFacts, _ *PlatformProfile) {
			s.Broker.Key, w.Publishes = "", []string{"created"}
		}, Invalid, "events.key"},
		{"a subscription without its names", func(s *InstanceFacts, _ *WorkloadFacts, _ *PlatformProfile) {
			s.Broker.Connected = []EventSubscription{{Publisher: "orders", Event: "created", Stream: "staging-orders"}}
		}, Invalid, "events.subscriptions"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instance, workload := eventFacts()
			p := profile
			tt.change(&instance, &workload, &p)
			rendered, err := RenderInstance(instance, workload, p)
			refusal(t, err, tt.code, tt.field)
			if !reflect.DeepEqual(rendered, ResourceSet{}) {
				t.Fatal("partial resources on refusal")
			}
		})
	}
}

// The instance and release records state the identity, never the seed: encoding one cannot leak a private half.
func TestEventsRecordsCarryOnlyThePublicHalf(t *testing.T) {
	instance, _ := eventFacts()
	encoded, err := EncodeInstance(instanceRecord(instance))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "\"key\": \""+instance.Broker.Key+"\"") ||
		!strings.Contains(string(encoded), "\"subscriptions\": [") {
		t.Fatalf("the record does not state the identity:\n%s", encoded)
	}
	if strings.Contains(string(encoded), "SEED") || strings.Contains(string(encoded), "seed") {
		t.Fatalf("the record names a seed:\n%s", encoded)
	}
	release, err := EncodeRelease(ReleaseBundle{SchemaVersion: 1, Application: "orders",
		Source: Source{Repository: "owner/orders", Commit: strings.Repeat("a", 40)},
		Artifact: Artifact{Image: "ghcr.io/owner/orders", Digest: "sha256:" + strings.Repeat("b", 64),
			MediaType: ManifestMediaType, Platform: "linux/amd64"},
		Runtime: Runtime{Kind: "web", Port: 8080, HealthPath: "/", UID: 101}, Publishes: []string{"created"}})
	if err != nil || !strings.Contains(string(release), "\"publishes\": [") {
		t.Fatalf("the release does not state what it publishes: %v\n%s", err, release)
	}
}

// instanceRecord is the record the renderer's facts describe, for the encoding test above.
func instanceRecord(facts InstanceFacts) InstanceSpec {
	return InstanceSpec{SchemaVersion: 1, Application: facts.Application, Environment: facts.Environment,
		Release:       ReleaseID("rel-sha256:" + strings.Repeat("a", 64)),
		Configuration: facts.Configuration, Grants: Grants{Exposure: "private"},
		Events: Events{Key: facts.Broker.Key, Subscriptions: []string{"orders.created"}}}
}

func containsDependency(dependsOn []any, name string) bool {
	for _, dependency := range dependsOn {
		if dependency.(map[string]any)["name"] == name {
			return true
		}
	}
	return false
}

// The stream and consumer policy are the ones the platform's own manifests state, so an adopted instance's
// stream is not redefined when v2 takes it over.
func TestEventPolicyMatchesTheCommittedManifest(t *testing.T) {
	// apps/demo-orders/staging/events.yaml is v1's stream and consumers for one instance.
	// A publisher's manifest holds the stream; a subscriber's holds its consumer.
	for path, wants := range map[string][]string{
		"../../apps/demo-orders/staging/events.yaml": {"maxAge: " + streamRetention,
			"maxBytes: " + strconv.Itoa(streamMaxBytes), "preventDelete: true", "retention: limits", "discard: old",
			"storage: file", "replicas: 1"},
		"../../apps/swhurl-try-13/staging/events.yaml": {"ackWait: " + consumerAckWait,
			"maxDeliver: " + strconv.Itoa(consumerMaxRetry), "ackPolicy: explicit", "deliverPolicy: all"},
	} {
		committed, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(committed), want) {
				t.Errorf("%s does not state %q", path, want)
			}
		}
	}
}
