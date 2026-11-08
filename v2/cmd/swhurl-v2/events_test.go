package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// eventInstance is one instance's record pair as render holds it.
func eventInstance(t *testing.T, application string, events, release map[string]any) snapshotInstance {
	t.Helper()
	records := map[string]json.RawMessage{}
	for role, value := range map[string]any{
		"instance": map[string]any{"schemaVersion": 1, "application": application, "environment": "staging",
			"events": events},
		"release": release,
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		records[role] = data
	}
	return snapshotInstance{Application: application, Environment: "staging", Records: records}
}

// eventSnapshot is the records with a publisher, a subscriber, a publisher that publishes nothing, and a name no
// instance answers to: the resolver connects the first two, leaves the third's event refused for its own
// publisher to answer and the fourth waiting.
func eventSnapshot(t *testing.T) snapshot {
	t.Helper()
	return snapshot{Revision: strings.Repeat("a", 40), Instances: []snapshotInstance{
		eventInstance(t, "orders", map[string]any{"key": "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G"},
			map[string]any{"schemaVersion": 1, "publishes": []any{"created"}}),
		eventInstance(t, "notifier", map[string]any{
			"key":           "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI",
			"subscriptions": []any{"orders.created", "absent.created"}}, map[string]any{"schemaVersion": 1}),
		eventInstance(t, "quiet", map[string]any{}, map[string]any{"schemaVersion": 1}),
	}}
}

// The resolution records the connected subscription with every name a consumer, a stream and a permission need, and
// reports the one whose publisher no instance answers to as waiting rather than refused.
func TestResolutionRecordsSubscriptions(t *testing.T) {
	document, err := resolutionOf(eventSnapshot(t))
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}
	if len(document.Subscriptions) != 1 {
		t.Fatalf("subscriptions: %+v", document.Subscriptions)
	}
	want := subscriptionDocument{Subscriber: "notifier", Publisher: "orders", Event: "created",
		Environment: "staging", Stream: "staging-orders", Consumer: "notifier_created",
		Subject: "staging.orders.created", Object: "orders-created"}
	if document.Subscriptions[0] != want {
		t.Fatalf("the subscription: %+v", document.Subscriptions[0])
	}
	if len(document.EventWaiting) != 1 || document.EventWaiting[0].Callee != "absent" ||
		document.EventWaiting[0].Reason != "publisher_not_ready" {
		t.Fatalf("waiting: %+v", document.EventWaiting)
	}
}

// A release states what it publishes — an absent list is none — so a subscription to an event it does not publish
// is refused rather than left waiting for something that can never arrive.
func TestResolutionRefusesAnEventItsPublisherDoesNotPublish(t *testing.T) {
	pinned := snapshot{Revision: strings.Repeat("a", 40), Instances: []snapshotInstance{
		eventInstance(t, "quiet", map[string]any{"key": "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G"},
			map[string]any{"schemaVersion": 1}),
		eventInstance(t, "notifier", map[string]any{
			"key":           "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI",
			"subscriptions": []any{"quiet.created"}}, map[string]any{"schemaVersion": 1}),
	}}
	if _, err := resolutionOf(pinned); err == nil || !strings.Contains(err.Error(), "not_published") {
		t.Fatalf("resolution: %v", err)
	}
}

// compile --out delivers an instance's seed beside its manifests, byte for byte, and refuses a keyed instance
// without one: a folder that cannot be applied is never written.
func TestCompileWritesTheEventsSecret(t *testing.T) {
	directory := t.TempDir()
	release := kernel.ReleaseBundle{SchemaVersion: 1, Application: "orders",
		Source: kernel.Source{Repository: "samclement/orders", Commit: strings.Repeat("b", 40)},
		Artifact: kernel.Artifact{Image: "ghcr.io/samclement/orders", Digest: "sha256:" + strings.Repeat("c", 64),
			MediaType: kernel.ManifestMediaType, Platform: "linux/amd64"},
		Runtime:   kernel.Runtime{Kind: "web", Port: 8080, HealthPath: "/", UID: 65532},
		Publishes: []string{"created"}, Documents: []string{"events"}}
	releaseID, err := release.ID()
	if err != nil {
		t.Fatal(err)
	}
	release.ReleaseID = releaseID
	releaseDocument, err := kernel.EncodeRelease(release)
	if err != nil {
		t.Fatal(err)
	}
	instance := kernel.InstanceSpec{SchemaVersion: 1, Application: "orders", Environment: "staging",
		Release: release.ReleaseID, Configuration: kernel.Configuration{Replicas: 1, MemoryMiB: 64},
		Grants: kernel.Grants{Exposure: "private"},
		Events: kernel.Events{Key: "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G"}}
	instanceDocument, err := kernel.EncodeInstance(instance)
	if err != nil {
		t.Fatal(err)
	}
	appDocument, err := kernel.EncodeApp(kernel.AppSpec{SchemaVersion: 1, Application: "orders", Team: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string][]byte{"release.json": releaseDocument,
		"instance.json": instanceDocument, "app.json": appDocument} {
		if err := os.WriteFile(filepath.Join(directory, name), document, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	calls := `{"kind":"v2-calls","version":1,"revision":"` + strings.Repeat("a", 40) + `","pairs":[],"subscriptions":[]}`
	callsPath := filepath.Join(directory, "calls.json")
	if err := os.WriteFile(callsPath, []byte(calls), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := []byte("a ciphertext v2 never reads\n")
	secret := filepath.Join(directory, "events.sops.yaml")
	if err := os.WriteFile(secret, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	args := []string{"compile", "--instance", filepath.Join(directory, "instance.json"),
		"--release", filepath.Join(directory, "release.json"), "--app", filepath.Join(directory, "app.json"),
		"--profile", profilePath(t), "--calls", callsPath, "--events-secret", secret, "--out", destination}
	var out, diagnostic bytes.Buffer
	for range 2 {
		out.Reset()
		diagnostic.Reset()
		if status := run(args, &out, &diagnostic, os.ReadFile, files.New()); status != 0 {
			t.Fatalf("compile exit %d: %s", status, diagnostic.String())
		}
		if !strings.Contains(out.String(), "events.sops.yaml") {
			t.Fatalf("the seed was not written: %s", out.String())
		}
		written := filepath.Join(destination, "v2-apps/instances/orders/staging")
		if got, err := os.ReadFile(filepath.Join(written, "events.sops.yaml")); err != nil || !bytes.Equal(got, seed) {
			t.Fatalf("the seed's bytes: %v %q", err, got)
		}
		// The kustomization lists the file it was delivered with: kustomize refuses a missing resource rather
		// than applying a folder without it.
		if !strings.Contains(readFileOrFail(t, filepath.Join(written, "kustomization.yaml")), `"events.sops.yaml"`) {
			t.Fatal("the kustomization does not list the seed")
		}
	}
	// Without the seed the command refuses before it writes anything.
	without := []string{}
	for index, arg := range args {
		if arg != "--events-secret" && (index == 0 || args[index-1] != "--events-secret") {
			without = append(without, arg)
		}
	}
	refused := t.TempDir()
	without[len(without)-1] = refused
	out.Reset()
	diagnostic.Reset()
	if status := run(without, &out, &diagnostic, os.ReadFile, files.New()); status != 2 ||
		!strings.Contains(diagnostic.String(), "an_instance_with_a_key_needs_its_seed") {
		t.Fatalf("exit %d, stderr %q", status, diagnostic.String())
	}
	if entries, _ := os.ReadDir(refused); len(entries) != 0 {
		t.Fatalf("a refusal wrote %v", entries)
	}
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The profile's broker facts are the platform's own files: the two public keys are what platform/nats publishes,
// and the address is the Service the chart deploys. A rotation on either side fails here rather than at runtime.
func TestProfileStatesThePlatformsBroker(t *testing.T) {
	profile, err := kernel.DecodeProfile(fixture(t, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []struct{ profile, file string }{
		{profile.NATS.ControllerKey, "../../../platform/nats/controller-nkey.pub"},
		{profile.NATS.ConsoleKey, "../../../platform/nats/console-nkey.pub"},
	} {
		published, err := os.ReadFile(key.file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(published)) != key.profile {
			t.Fatalf("the profile's key is not what %s publishes", key.file)
		}
	}
	if profile.NATS.URL != "nats://nats.messaging.svc.cluster.local:4222" ||
		profile.NATS.DecryptionSecret != "sops-age" {
		t.Fatalf("the broker's facts: %+v", profile.NATS)
	}
}

// A v2-owned instance states its broker identity in its own record, and the readers see it: the resolver connects
// a subscription it grants itself and the user list gives it the consumer's permission.
func TestOwnRecordsStateTheBrokerIdentity(t *testing.T) {
	key := "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI"
	pinned := snapshot{Revision: strings.Repeat("a", 40), Instances: []snapshotInstance{
		eventInstance(t, "orders", map[string]any{"key": "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G"},
			map[string]any{"schemaVersion": 1, "publishes": []any{"created"}}),
		eventInstance(t, "notifier", map[string]any{"key": key, "subscriptions": []any{"orders.created"}},
			map[string]any{"schemaVersion": 1}),
	}}
	document, err := resolutionOf(pinned)
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}
	connected := false
	for _, subscription := range document.Subscriptions {
		connected = connected || subscription.Consumer == "notifier_created"
	}
	if !connected {
		t.Fatalf("the v2 instance's subscription did not connect: %+v", document.Subscriptions)
	}
	profile, err := kernel.DecodeProfile(fixture(t, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, rendered, err := renderUsers(pinned, profile)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if len(rendered.Files) != 1 {
		t.Fatalf("files: %+v", rendered.Files)
	}
	list := string(rendered.Files[0].Document)
	if !strings.Contains(list, key) ||
		!strings.Contains(list, "$JS.API.CONSUMER.MSG.NEXT.staging-orders.notifier_created") {
		t.Fatalf("the v2 instance is not a user with its consumer's permission:\n%s", list)
	}
}
