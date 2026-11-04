package orchestration

import (
	"errors"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files/filestest"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const (
	registryPath    = "v2-records/teams.json"
	registryAddress = "https://ntfy.sh/a-private-topic"
)

func registryInputs(t *testing.T, change kernel.TeamChange) RegistryInputs {
	t.Helper()
	record := `{"schemaVersion":1,"teams":{"demo":{"description":"Demo apps","channel":{"name":"platform","transport":"ntfy"}},` +
		`"quiet":{}}}`
	app, err := kernel.EncodeApp(kernel.AppSpec{SchemaVersion: 1, Application: "example", Team: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	return RegistryInputs{Registry: []byte(record), RegistryPath: registryPath, Apps: [][]byte{app},
		Instances: [][]byte{mintInstance(t)}, Profile: mintProfile(t, true), Change: change, Address: registryAddress}
}

// Giving a team its channel encrypts the Secret and writes it before the registry and the files that list it, and the
// address reaches the encrypter only.
func TestAChannelIsEncryptedAndWrittenFirst(t *testing.T) {
	writer, encrypter := filestest.New(), sopstest.New()
	outcome, err := ChangeRegistry(registryInputs(t, kernel.TeamChange{Action: kernel.GiveChannel, Team: "quiet",
		Transport: "ntfy"}), t.TempDir(), writer, encrypter)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, written := range outcome.Written {
		paths = append(paths, written.Path)
	}
	want := "platform/notify/channels/channel-quiet.sops.yaml v2-records/teams.json platform/notify/routes.json " +
		"platform/notify/channels/kustomization.yaml"
	if strings.Join(paths, " ") != want || outcome.Channel != "quiet" {
		t.Fatalf("written %v, channel %q", paths, outcome.Channel)
	}
	if len(encrypter.Calls) != 1 || !strings.Contains(string(encrypter.Calls[0].Document), registryAddress) ||
		encrypter.Calls[0].Recipient == "" {
		t.Fatalf("encryptions: %+v", encrypter.Calls)
	}
	for path, content := range writer.Contents {
		if strings.Contains(string(content), registryAddress) {
			t.Fatalf("%s holds the address", path)
		}
	}
	if routes := string(writer.Contents["platform/notify/routes.json"]); !strings.Contains(routes, `\"quiet\":[\"quiet\"]`) {
		t.Fatalf("routes: %s", routes)
	}
}

// A refused address or a failed encryption writes nothing at all.
func TestAChannelThatCannotBeEncryptedWritesNothing(t *testing.T) {
	inputs := registryInputs(t, kernel.TeamChange{Action: kernel.GiveChannel, Team: "quiet", Transport: "ntfy"})
	inputs.Address = "http://ntfy.sh/topic"
	writer := filestest.New()
	if _, err := ChangeRegistry(inputs, t.TempDir(), writer, sopstest.New()); err == nil || writer.Writes != 0 {
		t.Fatalf("a refused address: %v, %d writes", err, writer.Writes)
	}
	inputs.Address = registryAddress
	encrypter := sopstest.New()
	encrypter.Err = errors.New("refused_encryption: sops_not_available")
	if _, err := ChangeRegistry(inputs, t.TempDir(), writer, encrypter); err == nil || writer.Writes != 0 {
		t.Fatalf("a failed encryption: %v, %d writes", err, writer.Writes)
	}
	encrypter = sopstest.New()
	encrypter.Output = []byte("stringData:\n  address: " + registryAddress + "\n")
	if _, err := ChangeRegistry(inputs, t.TempDir(), writer, encrypter); err == nil || writer.Writes != 0 ||
		strings.Contains(err.Error(), registryAddress) {
		t.Fatalf("a plaintext answer: %v, %d writes", err, writer.Writes)
	}
}

// Dropping a channel removes its Secret after the files that listed it stop listing it.
func TestADroppedChannelIsRemovedLast(t *testing.T) {
	writer := filestest.New()
	dir := t.TempDir()
	secret := "platform/notify/channels/channel-platform.sops.yaml"
	if _, err := writer.Write(dir, []kernel.File{{Path: secret, Document: []byte("sops")}}); err != nil {
		t.Fatal(err)
	}
	outcome, err := ChangeRegistry(registryInputs(t, kernel.TeamChange{Action: kernel.DropChannel, Team: "demo"}), dir,
		writer, sopstest.New())
	if err != nil {
		t.Fatal(err)
	}
	last := outcome.Written[len(outcome.Written)-1]
	if last.Path != secret || last.State != "removed" || writer.Removals != 1 {
		t.Fatalf("written %+v", outcome.Written)
	}
	if _, present := writer.Contents[secret]; present {
		t.Fatal("the Secret stayed")
	}
	if listed := string(writer.Contents["platform/notify/channels/kustomization.yaml"]); strings.Contains(listed, "platform") {
		t.Fatalf("still listed: %s", listed)
	}
}

// A team an app record names is not removed, and nothing is written.
func TestARegistryChangeTheRecordsRefuseWritesNothing(t *testing.T) {
	writer := filestest.New()
	_, err := ChangeRegistry(registryInputs(t, kernel.TeamChange{Action: kernel.RenameTeam, Team: "demo", Rename: "x"}),
		t.TempDir(), writer, sopstest.New())
	if !strings.Contains(errorString(err), "team_in_use") || writer.Writes != 0 {
		t.Fatalf("rename of a named team: %v, %d writes", err, writer.Writes)
	}
	outcome, err := ChangeRegistry(registryInputs(t, kernel.TeamChange{Action: kernel.AddTeam, Team: "payments",
		Description: "Orders"}), t.TempDir(), writer, sopstest.New())
	if err != nil || len(outcome.Written) != 3 || writer.Removals != 0 {
		t.Fatalf("add: %v, %+v", err, outcome.Written)
	}
}
