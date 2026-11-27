package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/connectivity"
	"github.com/samclement/swhurl-platform/v2/console"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The steps a kind's change runs in its working copy. A record kind's step is console's typed step, run as one call to
// the capability that owns it; create and the team registry's changes are still v2's own commands, run with the line
// they are quoted as.

// commandStep is one of v2's own commands, ready to run against the working copy, with the line it is quoted as.
type commandStep struct {
	command string
	request request
	display string
}

func (s commandStep) Operation() console.Operation { return console.Operation(s.command) }

func (s commandStep) Quote() string { return s.display }

// changeSteps is what a kind's change runs, in order.
func (c cli) changeSteps(change pages.ConsoleChange, s *changeSpec, tree files.Checkout) ([]console.Step, error) {
	switch change.Name {
	case "new":
		return newSteps(tree, s)
	case "v2-scale", "v2-expose", "v2-team":
		return settingSteps(tree, s)
	case "v2-connect", "v2-disconnect":
		return c.connectionSteps(tree, s)
	case "v2-allow":
		return c.allowSteps(tree, s)
	case "v2-remove":
		return []console.Step{console.RemoveStep{App: s.name(), Env: s.env}, console.RenderStep{}}, nil
	case "v2-promote":
		return c.promoteSteps(tree, s)
	case "team-add", "team-edit", "team-remove":
		return teamSteps(tree, s), nil
	case "team-channel-set", "team-channel-remove":
		return channelSteps(tree, s), nil
	}
	return nil, fmt.Errorf("invalid: kind: %q has no commands yet", change.Name)
}

// newSteps is a new app: create states the records from the image its repository published, the edits state the
// choices an image cannot make, and the render writes everything v2 derives from them. A create writes records only,
// so the render that allocates the proxy port has to be part of the same change.
func newSteps(tree files.Checkout, s *changeSpec) ([]console.Step, error) {
	team, exposure, rest := "", "", make([]string, 0, len(s.settings))
	for _, setting := range s.settings {
		switch {
		case strings.HasPrefix(setting, "team="):
			team = strings.TrimPrefix(setting, "team=")
		case strings.HasPrefix(setting, "exposure="):
			exposure = strings.TrimPrefix(setting, "exposure=")
		default:
			rest = append(rest, setting)
		}
	}
	if team == "" {
		return nil, &orchestration.Refusal{Subject: "change", Reason: "nothing_requested: a new app needs --set team=<team>"}
	}
	steps := append([]console.Step{createStep(tree, s.name(), team, s.image, exposure)},
		edits(s.name(), "staging", rest)...)
	return append(steps, console.RenderStep{}), nil
}

// createStep states a new app's staging records from the image its repository published.
func createStep(tree files.Checkout, app, team, image, exposure string) commandStep {
	r := request{records: tree.Records, app: app, team: team, image: image, exposure: exposure, profile: tree.Profile,
		out: tree.Root}
	display := "swhurl-v2 create --records " + pages.ConsoleRecordsRoot + " --app " + app + " --team " + team +
		" --image " + image + " --profile v2/testdata/profile.json --out ."
	if exposure != "" {
		display += " --exposure " + exposure
	}
	return commandStep{command: "create", request: r, display: display}
}

// settingSteps is a change v2's edit states whole: one edit per setting, then the render that writes what the records
// derive.
func settingSteps(tree files.Checkout, s *changeSpec) ([]console.Step, error) {
	if len(s.settings) == 0 {
		return nil, &orchestration.Refusal{Subject: "change", Reason: "nothing_requested: give at least one --set"}
	}
	env := s.env
	if s.kind == "v2-team" {
		env = "" // the team is the app's: the edit names no environment and rewrites both
	}
	return append(edits(s.name(), env, s.settings), console.RenderStep{}), nil
}

// connectionSteps is a connect or a disconnect, which asks for a delta on the record's own declarations rather than
// for a whole list: the editor shows what the cluster holds, and a change merged but not yet applied is not in it.
func (c cli) connectionSteps(tree files.Checkout, s *changeSpec) ([]console.Step, error) {
	current, err := c.instanceRecord(tree, s.name(), s.env)
	if err != nil {
		return nil, err
	}
	settings, err := connectivity.ConnectionSettings(current, s.calls, s.subscriptions, s.dropCalls, s.dropSubscriptions)
	if err != nil {
		return nil, err
	}
	return append(edits(s.name(), s.env, settings), console.RenderStep{}), nil
}

// allowSteps is a change to who may call the instance, asked for as grants and revocations.
func (c cli) allowSteps(tree files.Checkout, s *changeSpec) ([]console.Step, error) {
	current, err := c.instanceRecord(tree, s.name(), s.env)
	if err != nil {
		return nil, err
	}
	settings, err := connectivity.AllowSettings(current, s.allows, s.disallows)
	if err != nil {
		return nil, err
	}
	return append(edits(s.name(), s.env, settings), console.RenderStep{}), nil
}

// promoteSteps is a promotion of what staging selects now: it expects the revision production's record states, the
// instance a promotion changes, so a production changed since the working copy was read refuses rather than being
// overwritten, and production's files come from the render beside staging's.
func (c cli) promoteSteps(tree files.Checkout, s *changeSpec) ([]console.Step, error) {
	current, err := c.instanceRecord(tree, s.name(), "prod")
	if err != nil {
		return nil, err
	}
	expected, err := current.Revision()
	if err != nil {
		return nil, err
	}
	return []console.Step{console.PromoteStep{App: s.name(), Env: "prod", Expected: string(expected)},
		console.RenderStep{}}, nil
}

// instanceRecord is one instance's record as the working copy states it: the tree the change is applied to, never the
// cluster.
func (c cli) instanceRecord(tree files.Checkout, app, env string) (kernel.InstanceSpec, error) {
	path := filepath.Join(tree.Records, app, env, instanceFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return kernel.InstanceSpec{}, fmt.Errorf("io: instance: cannot read the record of %s/%s", app, env)
	}
	spec, err := kernel.DecodeInstance(data)
	if err != nil {
		return kernel.InstanceSpec{}, err
	}
	return spec, nil
}

// edits is one edit per setting, in order.
func edits(app, env string, settings []string) []console.Step {
	steps := make([]console.Step, 0, len(settings))
	for _, setting := range settings {
		steps = append(steps, console.EditStep{App: app, Env: env, Setting: setting})
	}
	return steps
}

// teamSteps is a change to the team registry: v2 judges the name, the description and the rename, and a team the
// platform still names is not renamed.
func teamSteps(tree files.Checkout, s *changeSpec) []console.Step {
	switch s.kind {
	case "team-remove":
		return []console.Step{teamStep(tree, s, "remove")}
	case "team-edit":
		steps := []console.Step{teamStep(tree, s, "describe")}
		if s.rename != "" && s.rename != s.name() {
			steps = append(steps, teamStep(tree, s, "rename"))
		}
		return steps
	}
	return []console.Step{teamStep(tree, s, "add")}
}

// channelSteps is a team's notification channel: the address reaches v2 on its standard input, never on a command line
// and never in a line this command prints.
func channelSteps(tree files.Checkout, s *changeSpec) []console.Step {
	if s.kind == "team-channel-remove" {
		return []console.Step{teamStep(tree, s, "drop-channel")}
	}
	return []console.Step{teamStep(tree, s, "channel")}
}

// teamStep is one change to the registry. Its line quotes the flags its action takes and nothing else: the console
// never holds a value it cannot name, and a channel's address is not a value it holds at all.
func teamStep(tree files.Checkout, s *changeSpec, action string) commandStep {
	r := request{records: tree.Records, team: s.name(), action: action, profile: tree.Profile, out: tree.Root}
	display := "swhurl-v2 team --records " + pages.ConsoleRecordsRoot + " --team " + s.name() + " --action " + action
	switch action {
	case "add", "describe":
		r.description = s.description
		display += " --description=" + s.description
	case "rename":
		r.rename = s.rename
		display += " --rename=" + s.rename
	case "channel":
		r.transport = transportOf(s.transport)
		display += " --transport=" + transportOf(s.transport)
	}
	return commandStep{command: "team", request: r,
		display: display + " --profile v2/testdata/profile.json --out ."}
}

// transportOf is a channel's transport as v2 states it: the one the change names, or the default v2 would use.
func transportOf(transport string) string {
	if transport == "" {
		return "ntfy"
	}
	return transport
}
