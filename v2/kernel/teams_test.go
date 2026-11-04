package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

const teamsRecord = `{
  "schemaVersion": 1,
  "teams": {
    "demo": {"description": "Apps that show the platform", "channel": {"name": "platform", "transport": "ntfy"}},
    "insights": {"description": "Reporting", "channel": {"name": "insights", "transport": "ntfy"}},
    "quiet": {}
  }
}`

func teams(t *testing.T) TeamRegistry {
	t.Helper()
	registry, err := DecodeTeams([]byte(teamsRecord))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestTeamsRoundTrip(t *testing.T) {
	registry := teams(t)
	encoded, err := EncodeTeams(registry)
	if err != nil {
		t.Fatal(err)
	}
	again, err := DecodeTeams(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if second, _ := EncodeTeams(again); string(second) != string(encoded) {
		t.Fatalf("encoding is not stable:\n%s\n%s", encoded, second)
	}
	if got := registry.Channels(); len(got) != 2 || got[0].Name != "insights" || got[1].Name != "platform" {
		t.Fatalf("channels = %v", got)
	}
}

func TestTeamsRefuseWhatTheRegistryCannotMean(t *testing.T) {
	for _, tc := range []struct {
		name, record string
		code         Code
		field        string
	}{
		{"unknown field", `{"schemaVersion":1,"teams":{},"owners":{}}`, Unsupported, "teams"},
		{"unknown team field", `{"schemaVersion":1,"teams":{"a":{"notify":["a"]}}}`, Unsupported, "teams.team"},
		{"version", `{"schemaVersion":2,"teams":{}}`, Unsupported, "schemaVersion"},
		{"team name", `{"schemaVersion":1,"teams":{"Payments":{}}}`, Invalid, "teams"},
		{"two lines", `{"schemaVersion":1,"teams":{"a":{"description":"one\ntwo"}}}`, Invalid, "teams.team.description"},
		{"long", `{"schemaVersion":1,"teams":{"a":{"description":"` + strings.Repeat("x", 201) + `"}}}`, Invalid,
			"teams.team.description"},
		{"transport", `{"schemaVersion":1,"teams":{"a":{"channel":{"name":"a","transport":"email"}}}}`, Unsupported,
			"teams.team.channel.transport"},
		{"shared channel", `{"schemaVersion":1,"teams":{"a":{"channel":{"name":"c","transport":"ntfy"}},` +
			`"b":{"channel":{"name":"c","transport":"ntfy"}}}}`, ChannelTaken, "teams.team.channel.name"},
		{"channel without transport", `{"schemaVersion":1,"teams":{"a":{"channel":{"name":"a"}}}}`, Invalid,
			"teams.team.channel.transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeTeams([]byte(tc.record))
			refusal(t, err, tc.code, tc.field)
		})
	}
}

func TestCheckTeamsRefusesAnUnregisteredTeam(t *testing.T) {
	registry := teams(t)
	if err := CheckTeams(registry, []AppSpec{{Team: "demo"}}, []InstanceSpec{{Allows: []string{"team:insights", "shop"}}}); err != nil {
		t.Fatal(err)
	}
	refusal(t, CheckTeams(registry, []AppSpec{{Team: "payments"}}, nil), NoTeam, "app.team")
	refusal(t, CheckTeams(registry, nil, []InstanceSpec{{Allows: []string{"team:payments"}}}), NoTeam, "allows")
}

func TestTeamChanges(t *testing.T) {
	registry := teams(t)
	change := func(c TeamChange, apps []AppSpec, instances []InstanceSpec) (TeamRegistry, error) {
		t.Helper()
		return ChangeTeams(registry, c, apps, instances)
	}
	added, err := change(TeamChange{Action: AddTeam, Team: "payments", Description: "Orders and payments"}, nil, nil)
	if err != nil || added.Teams["payments"].Description != "Orders and payments" {
		t.Fatalf("add = %v, %v", added.Teams, err)
	}
	if _, present := registry.Teams["payments"]; present {
		t.Fatal("a change modified the registry it was given")
	}
	_, err = change(TeamChange{Action: AddTeam, Team: "demo"}, nil, nil)
	refusal(t, err, TeamExists, "team")
	_, err = change(TeamChange{Action: DescribeTeam, Team: "payments"}, nil, nil)
	refusal(t, err, NoTeam, "team")
	_, err = change(TeamChange{Action: DescribeTeam, Team: "demo", Description: "two  spaces"}, nil, nil)
	refusal(t, err, Invalid, "teams.team.description")

	renamed, err := change(TeamChange{Action: RenameTeam, Team: "demo", Rename: "examples"}, nil, nil)
	if err != nil || renamed.Teams["examples"].Channel.Name != "platform" {
		t.Fatalf("rename = %v, %v", renamed.Teams, err)
	}
	if _, present := renamed.Teams["demo"]; present {
		t.Fatal("the old name stayed")
	}
	_, err = change(TeamChange{Action: RenameTeam, Team: "demo", Rename: "insights"}, nil, nil)
	refusal(t, err, TeamExists, "rename")
	_, err = change(TeamChange{Action: RenameTeam, Team: "demo", Rename: "examples"}, []AppSpec{{Team: "demo"}}, nil)
	refusal(t, err, TeamInUse, "team")
	_, err = change(TeamChange{Action: RenameTeam, Team: "demo", Rename: "examples"}, nil,
		[]InstanceSpec{{Allows: []string{"team:demo"}}})
	refusal(t, err, TeamInUse, "team")

	removed, err := change(TeamChange{Action: RemoveTeam, Team: "quiet"}, nil, nil)
	if _, present := removed.Teams["quiet"]; err != nil || present {
		t.Fatalf("remove = %v, %v", removed.Teams, err)
	}
	_, err = change(TeamChange{Action: RemoveTeam, Team: "quiet"}, []AppSpec{{Team: "quiet"}}, nil)
	refusal(t, err, TeamInUse, "team")
	_, err = change(TeamChange{Action: RemoveTeam, Team: "insights"}, nil, nil)
	refusal(t, err, TeamInUse, "channel")
}

func TestATeamGetsAndLosesItsChannel(t *testing.T) {
	registry := teams(t)
	given, err := ChangeTeams(registry, TeamChange{Action: GiveChannel, Team: "quiet", Transport: "ntfy"}, nil, nil)
	if err != nil || *given.Teams["quiet"].Channel != (Channel{"quiet", "ntfy"}) {
		t.Fatalf("give = %v, %v", given.Teams["quiet"], err)
	}
	// A team with a channel keeps its name for a new address: demo's is called platform.
	kept, err := ChangeTeams(registry, TeamChange{Action: GiveChannel, Team: "demo", Transport: "ntfy"}, nil, nil)
	if err != nil || kept.Teams["demo"].Channel.Name != "platform" {
		t.Fatalf("give again = %v, %v", kept.Teams["demo"], err)
	}
	_, err = ChangeTeams(registry, TeamChange{Action: GiveChannel, Team: "quiet", Transport: "email"}, nil, nil)
	refusal(t, err, Unsupported, "teams.team.channel.transport")
	// A team named like another team's channel cannot take it over.
	clash := TeamRegistry{SchemaVersion: 1, Teams: map[string]Team{"platform": {}, "demo": registry.Teams["demo"]}}
	_, err = ChangeTeams(clash, TeamChange{Action: GiveChannel, Team: "platform", Transport: "ntfy"}, nil, nil)
	refusal(t, err, ChannelTaken, "teams.team.channel.name")

	dropped, err := ChangeTeams(registry, TeamChange{Action: DropChannel, Team: "demo"}, nil, nil)
	if err != nil || dropped.Teams["demo"].Channel != nil || dropped.Teams["demo"].Description == "" {
		t.Fatalf("drop = %v, %v", dropped.Teams["demo"], err)
	}
	_, err = ChangeTeams(registry, TeamChange{Action: DropChannel, Team: "quiet"}, nil, nil)
	refusal(t, err, Invalid, "channel")
}

func TestRenderNotify(t *testing.T) {
	rendered, err := RenderNotify(teams(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Files) != 2 || rendered.Files[0].Path != "notify/routes.json" ||
		rendered.Files[1].Path != "notify/channels/kustomization.yaml" {
		t.Fatalf("files = %v", rendered.Files)
	}
	var configMap struct {
		Metadata struct{ Name, Namespace string }
		Data     map[string]string
	}
	if err := json.Unmarshal(rendered.Files[0].Document, &configMap); err != nil {
		t.Fatal(err)
	}
	if configMap.Metadata.Name != "notification-routes" || configMap.Metadata.Namespace != "notify" {
		t.Fatalf("metadata = %v", configMap.Metadata)
	}
	// The shape the notification service reads: each channel's transport, each team's channels as a list.
	want := `{"channels":{"insights":"ntfy","platform":"ntfy"},"teams":{"demo":["platform"],"insights":["insights"]}}`
	if got := configMap.Data["routes.json"]; got != want {
		t.Fatalf("routes = %s", got)
	}
	want = `{"apiVersion":"kustomize.config.k8s.io/v1beta1","kind":"Kustomization",` +
		`"resources":["channel-insights.sops.yaml","channel-platform.sops.yaml"]}`
	if got := string(rendered.Files[1].Document); got != want {
		t.Fatalf("kustomization = %s", got)
	}
	if path := ChannelSecretPath("platform"); path != "notify/channels/channel-platform.sops.yaml" {
		t.Fatalf("path = %s", path)
	}
}

func TestChannelSecret(t *testing.T) {
	const address = "https://ntfy.sh/a-topic_1"
	plain, err := ChannelSecret(Channel{"platform", "ntfy"}, address)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"apiVersion":"v1","kind":"Secret","metadata":{"labels":{"platform.swhurl.com/notify-transport":"ntfy"},` +
		`"name":"channel-platform","namespace":"notify"},"stringData":{"address":"https://ntfy.sh/a-topic_1"},"type":"Opaque"}`
	if string(plain) != want {
		t.Fatalf("secret = %s", plain)
	}
	for _, bad := range []string{"http://ntfy.sh/topic", "https://ntfy.sh/", "https://ntfy.sh/a/b",
		"https://ntfy.sh/topic?x=1", "https://user:pass@ntfy.sh/topic", "https://ntfy.sh/topic#f", ""} {
		_, err := ChannelSecret(Channel{"platform", "ntfy"}, bad)
		refusal(t, err, Invalid, "address")
		// The message's own example is https://ntfy.sh/<topic>; a rejected address with a topic must not appear.
		if strings.Contains(bad, "topic") && strings.Contains(err.Error(), bad) {
			t.Fatalf("the refusal repeats the address: %v", err)
		}
	}
	refusal(t, CheckEncryptedAddress([]byte("stringData:\n  address: "+address), address), Invalid, "channel.secret")
	refusal(t, CheckEncryptedAddress([]byte("sops: {}"), address), Invalid, "channel.secret")
	if err := CheckEncryptedAddress([]byte("address: ENC[AES256_GCM,data:x]"), address); err != nil {
		t.Fatal(err)
	}
}
