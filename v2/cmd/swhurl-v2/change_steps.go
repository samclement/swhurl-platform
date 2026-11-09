package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// The commands a kind's change runs in its working copy, and the line a person could type instead. The line is what
// the pull request's body quotes, so it names the records and the profile as a checkout does, and the base commit as
// @base — the commit the console filled in, which nobody can type before it exists.

// changeTree is the working copy a change is applied in: the one folder its commands read and write.
type changeTree struct {
	dir     string // the checkout's root, which is every command's --out
	records string // <dir>/v2-records
	profile string // <dir>/v2/testdata/profile.json
	calls   string // <dir>/v2-records/calls.json
	egress  string // <dir>/platform/egress/allocation.json
}

// newChangeTree is the paths of one working copy.
func newChangeTree(dir string) changeTree {
	return changeTree{dir: dir,
		records: filepath.Join(dir, pages.ConsoleRecordsRoot),
		profile: filepath.Join(dir, "v2/testdata/profile.json"),
		calls:   filepath.Join(dir, pages.ConsoleCallsFile),
		egress:  filepath.Join(dir, pages.ConsoleEgressFile)}
}

// changeStep is one of v2's own commands, ready to run against the working copy, with the line it is quoted as.
type changeStep struct {
	command string
	request request
	display string
}

// changeSteps is what a kind's change runs, in order.
func (c cli) changeSteps(change pages.ConsoleChange, s *changeSpec, tree changeTree, revision string) ([]changeStep, error) {
	switch change.Name {
	case "v2-scale", "v2-expose", "v2-team":
		return settingSteps(tree, s, revision)
	case "v2-connect", "v2-disconnect":
		return c.connectionSteps(tree, s, revision)
	case "v2-allow":
		return c.allowSteps(tree, s, revision)
	case "v2-remove":
		return []changeStep{removeStep(tree, s.name(), s.env), renderStep(tree, revision)}, nil
	case "v2-promote":
		return c.promoteSteps(tree, s, revision)
	case "team-add", "team-edit", "team-remove":
		return teamSteps(tree, s), nil
	case "team-channel-set", "team-channel-remove":
		return channelSteps(tree, s), nil
	}
	return nil, fmt.Errorf("invalid: kind: %q has no commands yet", change.Name)
}

// settingSteps is a change v2's edit states whole: one edit per setting, then the render that writes what the records
// derive.
func settingSteps(tree changeTree, s *changeSpec, revision string) ([]changeStep, error) {
	if len(s.settings) == 0 {
		return nil, &orchestration.Refusal{Subject: "change", Reason: "nothing_requested: give at least one --set"}
	}
	env := s.env
	if s.kind == "v2-team" {
		env = "" // the team is the app's: the edit names no environment and rewrites both
	}
	steps := make([]changeStep, 0, len(s.settings)+1)
	for _, setting := range s.settings {
		steps = append(steps, editStep(tree, s.name(), env, setting))
	}
	return append(steps, renderStep(tree, revision)), nil
}

// connectionSteps is a connect or a disconnect, which asks for a delta on the record's own declarations rather than
// for a whole list: the editor shows what the cluster holds, and a change merged but not yet applied is not in it.
func (c cli) connectionSteps(tree changeTree, s *changeSpec, revision string) ([]changeStep, error) {
	current, err := c.instanceRecord(tree, s)
	if err != nil {
		return nil, err
	}
	settings, err := orchestration.ConnectionSettings(current, s.calls, s.subscriptions, s.dropCalls, s.dropSubscriptions)
	if err != nil {
		return nil, err
	}
	return append(edits(tree, s.name(), s.env, settings), renderStep(tree, revision)), nil
}

// allowSteps is a change to who may call the instance, asked for as grants and revocations.
func (c cli) allowSteps(tree changeTree, s *changeSpec, revision string) ([]changeStep, error) {
	current, err := c.instanceRecord(tree, s)
	if err != nil {
		return nil, err
	}
	settings, err := orchestration.AllowSettings(current, s.allows, s.disallows)
	if err != nil {
		return nil, err
	}
	return append(edits(tree, s.name(), s.env, settings), renderStep(tree, revision)), nil
}

// promoteSteps is a promotion of what staging selects now: it names the revision staging's record states, so what is
// merged is what the reviewer saw, and production's files come from the render beside staging's.
func (c cli) promoteSteps(tree changeTree, s *changeSpec, revision string) ([]changeStep, error) {
	current, err := c.instanceRecord(tree, s)
	if err != nil {
		return nil, err
	}
	expected, err := current.Revision()
	if err != nil {
		return nil, err
	}
	step := promoteStep(tree, s.name())
	step.request.expected = string(expected)
	return []changeStep{step, renderStep(tree, revision)}, nil
}

// instanceRecord is the instance's record as the working copy states it: the tree the change is applied to, never the
// cluster.
func (c cli) instanceRecord(tree changeTree, s *changeSpec) (kernel.InstanceSpec, error) {
	path := filepath.Join(tree.records, s.name(), s.env, instanceFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return kernel.InstanceSpec{}, fmt.Errorf("io: instance: cannot read the record of %s/%s", s.name(), s.env)
	}
	spec, err := kernel.DecodeInstance(data)
	if err != nil {
		return kernel.InstanceSpec{}, err
	}
	return spec, nil
}

// edits is one edit per setting, each with the line a person could type.
func edits(tree changeTree, app, env string, settings []string) []changeStep {
	steps := make([]changeStep, 0, len(settings))
	for _, setting := range settings {
		steps = append(steps, editStep(tree, app, env, setting))
	}
	return steps
}

// editStep is one named change to one instance's record.
func editStep(tree changeTree, app, env, setting string) changeStep {
	r := request{records: tree.records, app: app, env: env, change: setting, profile: tree.profile,
		out: tree.dir, callFile: tree.calls, egressFile: tree.egress}
	display := "swhurl-v2 edit --records " + pages.ConsoleRecordsRoot + " --app " + app
	if env != "" {
		display += " --env=" + env
	}
	display += " --set " + setting + " --profile v2/testdata/profile.json --out . --calls " +
		pages.ConsoleCallsFile + " --egress " + pages.ConsoleEgressFile
	return changeStep{command: "edit", request: r, display: display}
}

// removeStep deletes one instance, and everything v2 rendered for it.
func removeStep(tree changeTree, app, env string) changeStep {
	r := request{records: tree.records, app: app, env: env, profile: tree.profile, callFile: tree.calls,
		out: tree.dir}
	return changeStep{command: "remove", request: r,
		display: "swhurl-v2 remove --records " + pages.ConsoleRecordsRoot + " --app " + app + " --env=" + env +
			" --profile v2/testdata/profile.json --calls " + pages.ConsoleCallsFile + " --out ."}
}

// promoteStep selects production's release from staging's record.
func promoteStep(tree changeTree, app string) changeStep {
	r := request{records: tree.records, app: app, env: "prod", profile: tree.profile, out: tree.dir,
		callFile: tree.calls, egressFile: tree.egress}
	return changeStep{command: "promote", request: r,
		display: "swhurl-v2 promote --records " + pages.ConsoleRecordsRoot + " --app " + app + " --env=prod" +
			" --profile v2/testdata/profile.json --out . --calls " + pages.ConsoleCallsFile + " --egress " +
			pages.ConsoleEgressFile}
}

// renderStep writes what the whole tree of records derives: the instance's files, the platform's documents and the
// units Flux applies.
func renderStep(tree changeTree, revision string) changeStep {
	r := request{records: tree.records, profile: tree.profile, out: tree.dir, revision: revision}
	return changeStep{command: "render", request: r,
		display: "swhurl-v2 render --records " + pages.ConsoleRecordsRoot + " --profile v2/testdata/profile.json" +
			" --out . --revision @base"}
}

// teamSteps is a change to the team registry: v2 judges the name, the description and the rename, and a team the
// platform still names is not renamed.
func teamSteps(tree changeTree, s *changeSpec) []changeStep {
	switch s.kind {
	case "team-remove":
		return []changeStep{teamStep(tree, s, "remove")}
	case "team-edit":
		steps := []changeStep{teamStep(tree, s, "describe")}
		if s.rename != "" && s.rename != s.name() {
			steps = append(steps, teamStep(tree, s, "rename"))
		}
		return steps
	}
	return []changeStep{teamStep(tree, s, "add")}
}

// channelSteps is a team's notification channel: the address reaches v2 on its standard input, never on a command line
// and never in a line this command prints.
func channelSteps(tree changeTree, s *changeSpec) []changeStep {
	if s.kind == "team-channel-remove" {
		return []changeStep{teamStep(tree, s, "drop-channel")}
	}
	return []changeStep{teamStep(tree, s, "channel")}
}

// teamStep is one change to the registry. Its line quotes the flags its action takes and nothing else: the console
// never holds a value it cannot name, and a channel's address is not a value it holds at all.
func teamStep(tree changeTree, s *changeSpec, action string) changeStep {
	r := request{records: tree.records, team: s.name(), action: action, profile: tree.profile, out: tree.dir}
	display := "swhurl-v2 team --records " + pages.ConsoleRecordsRoot + " --team " + s.name() + " --action " + action
	switch action {
	case "add", "describe":
		r.description = s.description
		display += " --description=" + s.description
	case "rename":
		r.rename = s.rename
		display += " --rename=" + s.rename
	case "channel":
		r.transport = s.transport
		display += " --transport=" + transportOf(s.transport)
	}
	return changeStep{command: "team", request: r,
		display: display + " --profile v2/testdata/profile.json --out ."}
}

// transportOf is a channel's transport as v2 states it: the one the change names, or the default v2 would use.
func transportOf(transport string) string {
	if transport == "" {
		return "ntfy"
	}
	return transport
}
