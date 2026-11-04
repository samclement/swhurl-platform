package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/sops/sopstest"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

const teamAddress = "https://ntfy.sh/a-private-topic"

// teamRun runs one team change on the render fixture's checkout, with the address on standard input.
func teamRun(t *testing.T, out, records, stdin string, args ...string) (int, teamDocument, string) {
	t.Helper()
	var stdout, diagnostic bytes.Buffer
	c := cli{readFile: os.ReadFile, writer: files.New(), encrypter: sopstest.New(), stdin: strings.NewReader(stdin)}
	status := runWith(append([]string{"team", "--records", records, "--profile", "../../testdata/profile.json",
		"--out", out}, args...), &stdout, &diagnostic, c)
	var document teamDocument
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
			t.Fatalf("the report is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return status, document, diagnostic.String()
}

// The commands a team's life takes, each leaving a tree the render check accepts: a team is added, given its channel,
// renamed while nothing names it, loses its channel and goes.
func TestATeamsLifeKeepsTheRenderedTreeCurrent(t *testing.T) {
	out, records := renderedTree(t)
	steps := [][]string{
		{"--team", "payments", "--action", "add", "--description", "Orders and payments"},
		{"--team", "payments", "--action", "channel"},
		{"--team", "payments", "--action", "rename", "--rename", "billing"},
		{"--team", "billing", "--action", "describe"},
		{"--team", "billing", "--action", "drop-channel"},
		{"--team", "billing", "--action", "remove"},
	}
	for _, step := range steps {
		status, document, diagnostic := teamRun(t, out, records, teamAddress+"\n", step...)
		if status != 0 || document.Kind != "v2-team" {
			t.Fatalf("%v exited %d: %s", step, status, diagnostic)
		}
		if check, _, diagnostic := renderRun(t, files.New(), renderArgs(out, records, "--check")...); check != 0 {
			t.Fatalf("after %v the check exited %d: %s", step, check, diagnostic)
		}
		if step[3] == "channel" {
			secret, err := os.ReadFile(filepath.Join(out, "platform/notify/channels/channel-payments.sops.yaml"))
			if err != nil || strings.Contains(string(secret), teamAddress) || document.Channel != "payments" {
				t.Fatalf("the Secret: %v, %s", err, secret)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(out, "platform/notify/channels/channel-payments.sops.yaml")); !os.IsNotExist(err) {
		t.Fatalf("the dropped channel's Secret: %v", err)
	}
}

// A refused change writes nothing, says why without the address, and exits 2; a flag the action does not take is a
// usage error.
func TestATeamChangeTheRecordsRefuseChangesNothing(t *testing.T) {
	out, records := renderedTree(t)
	before := treeContents(t, out)
	for _, tc := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{"--team", "demo", "--action", "remove"}, "team_in_use"},
		{"", []string{"--team", "demo", "--action", "rename", "--rename", "examples"}, "team_in_use"},
		{"", []string{"--team", "nobody", "--action", "describe"}, "no_team"},
		{"", []string{"--team", "demo", "--action", "channel"}, "standard input"},
		{"http://ntfy.sh/a-private-topic\n", []string{"--team", "demo", "--action", "channel"}, "invalid: address"},
		{"", []string{"--team", "demo", "--action", "remove", "--description", "x"}, "usage"},
		{"", []string{"--team", "demo", "--action", "rename"}, "usage"},
		{"", []string{"--team", "demo", "--action", "merge"}, "usage"},
	} {
		status, _, diagnostic := teamRun(t, out, records, tc.stdin, tc.args...)
		if status != 2 || !strings.Contains(diagnostic, tc.want) || strings.Contains(diagnostic, "a-private-topic") {
			t.Fatalf("%v exited %d: %s", tc.args, status, diagnostic)
		}
	}
	if after := treeContents(t, out); len(after) != len(before) {
		t.Fatal("a refused change wrote something")
	}
}

// The render refuses an app record naming a team nobody registered, a grant to one, a channel without its Secret and
// a Secret no channel names; each refusal says which.
func TestRenderRefusesTeamsAndChannelsThatDisagree(t *testing.T) {
	refused := func(t *testing.T, out, records, want string) {
		t.Helper()
		status, _, diagnostic := renderRun(t, files.New(), renderArgs(out, records, "--check")...)
		if status != 2 || !strings.Contains(diagnostic, want) {
			t.Fatalf("the check exited %d: %s", status, diagnostic)
		}
	}
	registry := func(t *testing.T, records string, teams map[string]kernel.Team) {
		writeRecord(t, filepath.Join(records, kernel.TeamsRecordName), func() ([]byte, error) {
			return kernel.EncodeTeams(kernel.TeamRegistry{SchemaVersion: 1, Teams: teams})
		})
	}
	t.Run("app team", func(t *testing.T) {
		out, records := renderedTree(t)
		registry(t, records, map[string]kernel.Team{"other": {}})
		refused(t, out, records, "demo-orders/prod: no_team: app.team")
	})
	t.Run("missing secret", func(t *testing.T) {
		out, records := renderedTree(t)
		registry(t, records, map[string]kernel.Team{"demo": {Channel: &kernel.Channel{Name: "platform", Transport: "ntfy"}}})
		refused(t, out, records, "platform/notify/channels/channel-platform.sops.yaml is missing")
	})
	t.Run("stray secret", func(t *testing.T) {
		out, records := renderedTree(t)
		writeRecord(t, filepath.Join(out, "platform/notify/channels/channel-old.sops.yaml"),
			func() ([]byte, error) { return []byte("sops"), nil })
		refused(t, out, records, "channel-old.sops.yaml is no team's channel")
	})
	t.Run("no registry", func(t *testing.T) {
		out, records := renderedTree(t)
		if err := os.Remove(filepath.Join(records, kernel.TeamsRecordName)); err != nil {
			t.Fatal(err)
		}
		status, _, _ := renderRun(t, files.New(), renderArgs(out, records, "--check")...)
		if status == 0 {
			t.Fatal("a tree without a registry passed")
		}
	})
}
