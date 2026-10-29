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

// eventSnapshot is a snapshot with a publisher, a subscriber, and a v2-owned instance whose records cannot state
// publications yet: the resolver connects the first two and leaves the third waiting.
func eventSnapshot(t *testing.T) map[string]any {
	t.Helper()
	instance := func(application, format string, settings, release map[string]any) map[string]any {
		return map[string]any{"application": application, "environment": "staging", "format": format,
			"missing": []any{}, "objects": []any{},
			"records": map[string]any{"instance": settings, "release": release}}
	}
	return map[string]any{"version": 1, "revision": strings.Repeat("a", 40), "residual": []any{},
		"instances": []any{
			instance("orders", "v1", map[string]any{"allocations": map[string]any{
				"eventsKey": "UBEEVDBYIJ4OB75M7RMLJDXC6IG4344VTY63IKJS3UC3XJTHMOXZ6M2G"}},
				map[string]any{"publishes": []any{"created"}}),
			instance("notifier", "v1", map[string]any{"subscriptions": []any{"orders.created", "quiet.created"},
				"allocations": map[string]any{"eventsKey": "UCWLMGY4L75WNKJ2FRJFZ7QDGKWDDHDLJCJVU3OPXVUJ42THGXEDBJPI"}},
				map[string]any{}),
			instance("quiet", "v2", map[string]any{}, map[string]any{}),
		}}
}

// resolve records the connected subscription with every name a consumer, a stream and a permission need, and
// reports the one that waits as waiting rather than refused.
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
	if len(document.EventWaiting) != 1 || document.EventWaiting[0].Callee != "quiet" ||
		document.EventWaiting[0].Reason != "publisher_not_ready" {
		t.Fatalf("waiting: %+v", document.EventWaiting)
	}
	if !strings.Contains(diagnostic.String(), "1 event subscription(s), 1 waiting") {
		t.Errorf("the summary: %s", diagnostic.String())
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
