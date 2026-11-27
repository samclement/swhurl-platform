package console

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Each typed step runs its own operation and quotes the line a checkout runs it with.
func TestStepsQuoteTheirLines(t *testing.T) {
	for _, test := range []struct {
		step      Step
		operation Operation
		line      string
	}{
		{CreateStep{App: "web", Team: "demo", Image: "ghcr.io/o/web:1-abc"}, Create,
			"swhurl-v2 create --records v2-records --app web --team demo --image ghcr.io/o/web:1-abc" +
				" --profile v2/testdata/profile.json --out ."},
		{CreateStep{App: "web", Team: "demo", Image: "ghcr.io/o/web:1-abc", Exposure: "public-web"}, Create,
			"swhurl-v2 create --records v2-records --app web --team demo --image ghcr.io/o/web:1-abc" +
				" --profile v2/testdata/profile.json --out . --exposure public-web"},
		{TeamStep{kernel.TeamChange{Action: kernel.AddTeam, Team: "payments", Description: "Orders"}}, Team,
			"swhurl-v2 team --records v2-records --team payments --action add --description=Orders" +
				" --profile v2/testdata/profile.json --out ."},
		{TeamStep{kernel.TeamChange{Action: kernel.RenameTeam, Team: "payments", Rename: "billing"}}, Team,
			"swhurl-v2 team --records v2-records --team payments --action rename --rename=billing" +
				" --profile v2/testdata/profile.json --out ."},
		{TeamStep{kernel.TeamChange{Action: kernel.GiveChannel, Team: "payments", Transport: "ntfy"}}, Team,
			"swhurl-v2 team --records v2-records --team payments --action channel --transport=ntfy" +
				" --profile v2/testdata/profile.json --out ."},
		{TeamStep{kernel.TeamChange{Action: kernel.RemoveTeam, Team: "payments"}}, Team,
			"swhurl-v2 team --records v2-records --team payments --action remove --profile v2/testdata/profile.json" +
				" --out ."},
		{EditStep{App: "web", Env: "staging", Setting: "replicas=3"}, Edit,
			"swhurl-v2 edit --records v2-records --app web --env=staging --set replicas=3 --profile v2/testdata/profile.json" +
				" --out . --calls v2-records/calls.json --egress platform/egress/allocation.json"},
		{EditStep{App: "web", Setting: "team=payments"}, Edit,
			"swhurl-v2 edit --records v2-records --app web --set team=payments --profile v2/testdata/profile.json" +
				" --out . --calls v2-records/calls.json --egress platform/egress/allocation.json"},
		{RemoveStep{App: "web", Env: "staging"}, Remove,
			"swhurl-v2 remove --records v2-records --app web --env=staging --profile v2/testdata/profile.json" +
				" --calls v2-records/calls.json --out ."},
		{PromoteStep{App: "web", Env: "prod", Expected: "abc"}, Promote,
			"swhurl-v2 promote --records v2-records --app web --env=prod --profile v2/testdata/profile.json" +
				" --expect=abc --out . --calls v2-records/calls.json --egress platform/egress/allocation.json"},
		{RenderStep{}, Render,
			"swhurl-v2 render --records v2-records --profile v2/testdata/profile.json --out . --revision @base"},
	} {
		if got := test.step.Operation(); got != test.operation {
			t.Errorf("%+v runs %s, want %s", test.step, got, test.operation)
		}
		if got := test.step.Quote(); got != test.line {
			t.Errorf("%+v quotes\n %s\nwant\n %s", test.step, got, test.line)
		}
	}
}

// A working copy keeps its records and documents where the quoted lines name them.
func TestWorkingCopy(t *testing.T) {
	want := files.Checkout{Root: "/w", Records: filepath.FromSlash("/w/v2-records"),
		Profile: filepath.FromSlash("/w/v2/testdata/profile.json"), Calls: filepath.FromSlash("/w/v2-records/calls.json"),
		Egress: filepath.FromSlash("/w/platform/egress/allocation.json")}
	if got := WorkingCopy("/w"); got != want {
		t.Errorf("the working copy is %+v", got)
	}
}

// Each kind's request becomes the steps its operations are: a new app's team and exposure are the create's and the
// rest its edits, a team's edit renames only to another name, and a channel states its transport but never an address.
func TestStepsOfARequest(t *testing.T) {
	for _, test := range []struct {
		kind    string
		request Request
		want    []Step
	}{
		{"new", Request{App: "web", Env: "staging", Image: "i:1", Settings: []string{"replicas=2", "team=demo",
			"exposure=public-web"}},
			[]Step{CreateStep{App: "web", Team: "demo", Image: "i:1", Exposure: "public-web"},
				EditStep{App: "web", Env: "staging", Setting: "replicas=2"}, RenderStep{}}},
		{"v2-team", Request{App: "web", Env: "prod", Settings: []string{"team=payments"}},
			[]Step{EditStep{App: "web", Setting: "team=payments"}, RenderStep{}}},
		{"v2-scale", Request{App: "web", Env: "prod", Settings: []string{"replicas=0"}},
			[]Step{EditStep{App: "web", Env: "prod", Setting: "replicas=0"}, RenderStep{}}},
		{"v2-remove", Request{App: "web", Env: "prod"}, []Step{RemoveStep{App: "web", Env: "prod"}, RenderStep{}}},
		{"team-add", Request{Team: "payments", Description: "Orders"},
			[]Step{TeamStep{kernel.TeamChange{Action: kernel.AddTeam, Team: "payments", Description: "Orders"}}}},
		{"team-edit", Request{Team: "payments", Description: "Orders", Rename: "payments"},
			[]Step{TeamStep{kernel.TeamChange{Action: kernel.DescribeTeam, Team: "payments", Description: "Orders"}}}},
		{"team-edit", Request{Team: "payments", Description: "Orders", Rename: "billing"},
			[]Step{TeamStep{kernel.TeamChange{Action: kernel.DescribeTeam, Team: "payments", Description: "Orders"}},
				TeamStep{kernel.TeamChange{Action: kernel.RenameTeam, Team: "payments", Rename: "billing"}}}},
		{"team-remove", Request{Team: "payments", Description: "ignored"},
			[]Step{TeamStep{kernel.TeamChange{Action: kernel.RemoveTeam, Team: "payments"}}}},
		{"team-channel-set", Request{Team: "payments"},
			[]Step{TeamStep{kernel.TeamChange{Action: kernel.GiveChannel, Team: "payments", Transport: "ntfy"}}}},
		{"team-channel-remove", Request{Team: "payments", Transport: "ntfy"},
			[]Step{TeamStep{kernel.TeamChange{Action: kernel.DropChannel, Team: "payments"}}}},
	} {
		change, _ := ChangeNamed(test.kind)
		steps, err := change.Steps(test.request, WorkingCopy(t.TempDir()))
		if err != nil || !reflect.DeepEqual(steps, test.want) {
			t.Errorf("%s %+v runs %+v, %v; want %+v", test.kind, test.request, steps, err, test.want)
		}
		for _, step := range steps {
			if strings.Contains(step.Quote(), "address") {
				t.Errorf("%s quotes %q", test.kind, step.Quote())
			}
		}
	}
}

// A change with nothing its kind can run is refused before a file is written, and a change to an instance the
// working copy does not hold names the instance.
func TestStepsRefuse(t *testing.T) {
	tree := WorkingCopy(t.TempDir())
	for _, test := range []struct {
		kind    string
		request Request
		want    string
	}{
		{"new", Request{App: "web", Env: "staging", Image: "i:1", Settings: []string{"replicas=2"}},
			"refused: change: nothing_requested: a new app needs --set team=<team>"},
		{"v2-scale", Request{App: "web", Env: "staging"}, "refused: change: nothing_requested: give at least one --set"},
		{"v2-connect", Request{App: "web", Env: "staging", Calls: []string{"orders"}},
			"io: instance: cannot read the record of web/staging"},
		{"v2-allow", Request{App: "web", Env: "prod", Allows: []string{"orders"}},
			"io: instance: cannot read the record of web/prod"},
		{"v2-promote", Request{App: "web", Env: "staging"}, "io: instance: cannot read the record of web/prod"},
	} {
		change, _ := ChangeNamed(test.kind)
		if _, err := change.Steps(test.request, tree); err == nil || err.Error() != test.want {
			t.Errorf("%s gave %v, want %s", test.kind, err, test.want)
		}
	}
}
