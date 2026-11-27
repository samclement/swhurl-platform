package console

import (
	"path/filepath"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
)

// Each typed step runs its own operation and quotes the line a checkout runs it with.
func TestStepsQuoteTheirLines(t *testing.T) {
	for _, test := range []struct {
		step      Step
		operation Operation
		line      string
	}{
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
