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

// eventSnapshot is a v2 snapshot with a publisher, a subscriber, a v2 publisher that publishes nothing, and a
// name no instance answers to: the resolver connects the first two, refuses the third's event and leaves the
// fourth waiting.
func eventSnapshot(t *testing.T) map[string]any {
	t.Helper()
	instance := func(application string, events map[string]any, release map[string]any) map[string]any {
		return map[string]any{"application": application, "environment": "staging", "format": "v2",
			"missing": []any{}, "objects": []any{},
			"records": map[string]any{"instance": map[string]any{"schemaVersion": 1,
				"application": application, "environment": "staging", "events": events}, "release": release}}
	}
	return map[string]any{"version": 1, "revision": strings.Repeat("a", 40), "residual": []any{},
		"instances": []any{
			instance("orders", map[string]any{"key": "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G"},
				map[string]any{"schemaVersion": 1, "publishes": []any{"created"}}),
			instance("notifier", map[string]any{
				"key":           "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI",
				"subscriptions": []any{"orders.created", "absent.created"}}, map[string]any{"schemaVersion": 1}),
			instance("quiet", map[string]any{}, map[string]any{"schemaVersion": 1}),
		}}
}

// resolve records the connected subscription with every name a consumer, a stream and a permission need, and
// reports the one whose publisher no instance answers to as waiting rather than refused.
func TestResolveRecordsSubscriptions(t *testing.T) {
	snapshot := writeSnapshot(t, eventSnapshot(t))
	var out, diagnostic bytes.Buffer
	if status := run([]string{"resolve", "--snapshot", snapshot}, &out, &diagnostic, os.ReadFile, files.New()); status != 0 {
		t.Fatalf("exit %d: %s", status, diagnostic.String())
	}
	var document struct {
		Subscriptions []struct {
			Subscriber, Publisher, Event, Environment string
			Stream, Consumer, Subject, Object         string
		}
		EventWaiting []struct{ Caller, Callee, Environment, Reason string }
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("the resolution is not JSON: %v", err)
	}
	if len(document.Subscriptions) != 1 {
		t.Fatalf("subscriptions: %+v", document.Subscriptions)
	}
	want := struct {
		Subscriber, Publisher, Event, Environment string
		Stream, Consumer, Subject, Object         string
	}{"notifier", "orders", "created", "staging", "staging-orders", "notifier_created",
		"staging.orders.created", "orders-created"}
	if document.Subscriptions[0] != want {
		t.Fatalf("the subscription: %+v", document.Subscriptions[0])
	}
	if len(document.EventWaiting) != 1 || document.EventWaiting[0].Callee != "absent" ||
		document.EventWaiting[0].Reason != "publisher_not_ready" {
		t.Fatalf("waiting: %+v", document.EventWaiting)
	}
	if !strings.Contains(diagnostic.String(), "1 event subscription(s), 1 waiting") {
		t.Errorf("the summary: %s", diagnostic.String())
	}
}

// A v2 instance's release states what it publishes — an absent list is none — so a subscription to an event it does
// not publish is refused rather than left waiting for something that can never arrive.
func TestResolveRefusesAnEventItsV2PublisherDoesNotPublish(t *testing.T) {
	snapshot := eventSnapshot(t)
	instances := snapshot["instances"].([]any)
	notifier := instances[1].(map[string]any)["records"].(map[string]any)["instance"].(map[string]any)
	notifier["events"].(map[string]any)["subscriptions"] = []any{"quiet.created"}
	var out, diagnostic bytes.Buffer
	status := run([]string{"resolve", "--snapshot", writeSnapshot(t, snapshot)}, &out, &diagnostic, os.ReadFile, files.New())
	if status != 2 || !strings.Contains(diagnostic.String(), "not_published") {
		t.Fatalf("exit %d: %s", status, diagnostic.String())
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
		Publishes: []string{"created"}}
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
	snapshot := map[string]any{"version": 1, "revision": strings.Repeat("a", 40), "residual": []any{},
		"instances": []any{
			map[string]any{"application": "orders", "environment": "staging", "format": "v2", "missing": []any{},
				"objects": []any{}, "records": map[string]any{"instance": map[string]any{"events": map[string]any{
					"key": "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G"}},
					"release": map[string]any{"publishes": []any{"created"}}}},
			map[string]any{"application": "notifier", "environment": "staging", "format": "v2", "missing": []any{},
				"objects": []any{}, "records": map[string]any{"instance": map[string]any{
					"events": map[string]any{"key": key, "subscriptions": []any{"orders.created"}}},
					"release": map[string]any{}}},
		}}
	path := writeSnapshot(t, snapshot)
	var out, diagnostic bytes.Buffer
	if status := run([]string{"resolve", "--snapshot", path}, &out, &diagnostic, os.ReadFile, files.New()); status != 0 {
		t.Fatalf("resolve exit %d: %s", status, diagnostic.String())
	}
	if !strings.Contains(out.String(), `"durableName"`) && !strings.Contains(out.String(), `"notifier_created"`) {
		t.Fatalf("the v2 instance's subscription did not connect:\n%s", out.String())
	}
	out.Reset()
	diagnostic.Reset()
	if status := run([]string{"users", "--snapshot", path, "--profile", profilePath(t)}, &out, &diagnostic,
		os.ReadFile, files.New()); status != 0 {
		t.Fatalf("users exit %d: %s", status, diagnostic.String())
	}
	if !strings.Contains(out.String(), key) ||
		!strings.Contains(out.String(), "$JS.API.CONSUMER.MSG.NEXT.staging-orders.notifier_created") {
		t.Fatalf("the v2 instance is not a user with its consumer's permission:\n%s", out.String())
	}
}
