package console

import (
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/connectivity/rules"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// A change's steps, typed: each one is an operation with what it is given, run as one call to the capability that
// owns it, and quoted in the pull request as the line a person could type in a checkout instead. The line is derived
// from the step, so the two cannot drift: it names the records and the profile as a checkout does, and the base commit
// as @base — the commit the console filled in, which nobody can type before it exists.

// Step is one operation of a change, with what it is given.
type Step interface {
	// Operation is the planned operation the step runs.
	Operation() Operation
	// Quote is the line a person could type in a checkout to run the same step.
	Quote() string
}

// profilePath is where a checkout keeps the platform profile.
const profilePath = "v2/testdata/profile.json"

// WorkingCopy is the checkout a change's steps run in: dir is its root, and the records and documents are where every
// checkout keeps them, the paths each step's line names relative to it.
func WorkingCopy(dir string) files.Checkout {
	return files.Checkout{Root: dir, Records: filepath.Join(dir, pages.ConsoleRecordsRoot),
		Profile: filepath.Join(dir, profilePath), Calls: filepath.Join(dir, pages.ConsoleCallsFile),
		Egress: filepath.Join(dir, pages.ConsoleEgressFile)}
}

// CreateStep states a new app's staging records from the image its repository published.
type CreateStep struct{ App, Team, Image, Exposure string }

func (CreateStep) Operation() Operation { return Create }

func (s CreateStep) Quote() string {
	line := "swhurl-v2 create --records " + pages.ConsoleRecordsRoot + " --app " + s.App + " --team " + s.Team +
		" --image " + s.Image + " --profile " + profilePath + " --out ."
	if s.Exposure != "" {
		line += " --exposure " + s.Exposure
	}
	return line
}

// TeamStep is one change to the team registry. Its line quotes the values its action takes and nothing else: a
// channel's address is not a value the console holds at all, and reaches v2 on its standard input.
type TeamStep struct{ Change kernel.TeamChange }

func (TeamStep) Operation() Operation { return Team }

func (s TeamStep) Quote() string {
	line := "swhurl-v2 team --records " + pages.ConsoleRecordsRoot + " --team " + s.Change.Team + " --action " +
		string(s.Change.Action)
	switch s.Change.Action {
	case kernel.AddTeam, kernel.DescribeTeam:
		line += " --description=" + s.Change.Description
	case kernel.RenameTeam:
		line += " --rename=" + s.Change.Rename
	case kernel.GiveChannel:
		line += " --transport=" + s.Change.Transport
	}
	return line + " --profile " + profilePath + " --out ."
}

// EditStep changes one setting, name=value, of one instance's records; an app-level setting names no environment.
type EditStep struct{ App, Env, Setting string }

func (EditStep) Operation() Operation { return Edit }

func (s EditStep) Quote() string {
	line := "swhurl-v2 edit --records " + pages.ConsoleRecordsRoot + " --app " + s.App
	if s.Env != "" {
		line += " --env=" + s.Env
	}
	return line + " --set " + s.Setting + " --profile " + profilePath + " --out . --calls " + pages.ConsoleCallsFile +
		" --egress " + pages.ConsoleEgressFile
}

// RemoveStep deletes one instance and everything v2 rendered for it.
type RemoveStep struct{ App, Env string }

func (RemoveStep) Operation() Operation { return Remove }

func (s RemoveStep) Quote() string {
	return "swhurl-v2 remove --records " + pages.ConsoleRecordsRoot + " --app " + s.App + " --env=" + s.Env +
		" --profile " + profilePath + " --calls " + pages.ConsoleCallsFile + " --out ."
}

// PromoteStep selects an instance's release from staging's record, if the instance's record is still Expected.
type PromoteStep struct{ App, Env, Expected string }

func (PromoteStep) Operation() Operation { return Promote }

func (s PromoteStep) Quote() string {
	return "swhurl-v2 promote --records " + pages.ConsoleRecordsRoot + " --app " + s.App + " --env=" + s.Env +
		" --profile " + profilePath + " --expect=" + s.Expected + " --out . --calls " + pages.ConsoleCallsFile +
		" --egress " + pages.ConsoleEgressFile
}

// RenderStep writes what the whole tree of records derives, recording the change's base commit.
type RenderStep struct{}

func (RenderStep) Operation() Operation { return Render }

func (RenderStep) Quote() string {
	return "swhurl-v2 render --records " + pages.ConsoleRecordsRoot + " --profile " + profilePath +
		" --out . --revision @base"
}

// Request is what a change asks for beyond its kind, as the command's flags state it: the app and environment or the
// team it is to, and the values its kind takes. The kind's CheckFlags has refused any value the kind does not take.
type Request struct {
	App, Env, Team                                     string
	Settings                                           []string
	Calls, Subscriptions, DropCalls, DropSubscriptions []string
	Allows, Disallows                                  []string
	Description, Rename, Transport, Image              string
}

// Name is the app or the team the change is to.
func (r Request) Name() string {
	if r.Team != "" {
		return r.Team
	}
	return r.App
}

// Refusal is a change asked for with nothing its kind can run: no file was written.
type Refusal struct{ Subject, Reason string }

func (r *Refusal) Error() string { return "refused: " + r.Subject + ": " + r.Reason }

// stepsOf is how one kind turns a request into its steps, reading what it needs from the working copy the steps run
// in: the tree the change is applied to, never the cluster.
type stepsOf func(request Request, tree files.Checkout) ([]Step, error)

// newSteps is a new app: create states the records from the image its repository published, the edits state the
// choices an image cannot make, and the render writes everything v2 derives from them. A create writes records only,
// so the render that allocates the proxy port has to be part of the same change.
func newSteps(request Request, _ files.Checkout) ([]Step, error) {
	create, rest := CreateStep{App: request.App, Image: request.Image}, make([]string, 0, len(request.Settings))
	for _, setting := range request.Settings {
		switch {
		case strings.HasPrefix(setting, "team="):
			create.Team = strings.TrimPrefix(setting, "team=")
		case strings.HasPrefix(setting, "exposure="):
			create.Exposure = strings.TrimPrefix(setting, "exposure=")
		default:
			rest = append(rest, setting)
		}
	}
	if create.Team == "" {
		return nil, &Refusal{Subject: "change", Reason: "nothing_requested: a new app needs --set team=<team>"}
	}
	steps := append([]Step{create}, edits(request.App, "staging", rest)...)
	return append(steps, RenderStep{}), nil
}

// instanceSettingSteps is a change v2's edit states whole: one edit of the instance per setting, then the render that
// writes what the records derive.
func instanceSettingSteps(request Request, _ files.Checkout) ([]Step, error) {
	return settingSteps(request.App, request.Env, request.Settings)
}

// appSettingSteps is a change to the app's own setting, its team: the edit names no environment and rewrites both,
// whichever page asked for it.
func appSettingSteps(request Request, _ files.Checkout) ([]Step, error) {
	return settingSteps(request.App, "", request.Settings)
}

// settingSteps is one edit per setting, then the render.
func settingSteps(app, env string, settings []string) ([]Step, error) {
	if len(settings) == 0 {
		return nil, &Refusal{Subject: "change", Reason: "nothing_requested: give at least one --set"}
	}
	return append(edits(app, env, settings), RenderStep{}), nil
}

// connectionSteps is a connect or a disconnect, which asks for a delta on the record's own declarations rather than
// for a whole list: the editor shows what the cluster holds, and a change merged but not yet applied is not in it.
func connectionSteps(request Request, tree files.Checkout) ([]Step, error) {
	current, err := instanceRecord(tree, request.App, request.Env)
	if err != nil {
		return nil, err
	}
	settings, err := rules.ConnectionSettings(current, request.Calls, request.Subscriptions, request.DropCalls,
		request.DropSubscriptions)
	if err != nil {
		return nil, err
	}
	return append(edits(request.App, request.Env, settings), RenderStep{}), nil
}

// allowSteps is a change to who may call the instance, asked for as grants and revocations.
func allowSteps(request Request, tree files.Checkout) ([]Step, error) {
	current, err := instanceRecord(tree, request.App, request.Env)
	if err != nil {
		return nil, err
	}
	settings, err := rules.AllowSettings(current, request.Allows, request.Disallows)
	if err != nil {
		return nil, err
	}
	return append(edits(request.App, request.Env, settings), RenderStep{}), nil
}

// removeSteps deletes the instance, then renders what the records left derive.
func removeSteps(request Request, _ files.Checkout) ([]Step, error) {
	return []Step{RemoveStep{App: request.App, Env: request.Env}, RenderStep{}}, nil
}

// promoteSteps is a promotion of what staging selects now: it expects the revision production's record states, the
// instance a promotion changes, so a production changed since the working copy was read refuses rather than being
// overwritten, and production's files come from the render beside staging's.
func promoteSteps(request Request, tree files.Checkout) ([]Step, error) {
	current, err := instanceRecord(tree, request.App, "prod")
	if err != nil {
		return nil, err
	}
	expected, err := current.Revision()
	if err != nil {
		return nil, err
	}
	return []Step{PromoteStep{App: request.App, Env: "prod", Expected: string(expected)}, RenderStep{}}, nil
}

// instanceRecord is one instance's record as the working copy states it.
func instanceRecord(tree files.Checkout, app, env string) (kernel.InstanceSpec, error) {
	data, err := tree.Instance(app, env)
	if err != nil {
		return kernel.InstanceSpec{}, err
	}
	return kernel.DecodeInstance(data)
}

// edits is one edit per setting, in order.
func edits(app, env string, settings []string) []Step {
	steps := make([]Step, 0, len(settings))
	for _, setting := range settings {
		steps = append(steps, EditStep{App: app, Env: env, Setting: setting})
	}
	return steps
}

// teamSteps is one change to the team registry: v2 judges the name, the description and the channel.
func teamSteps(action kernel.TeamAction) stepsOf {
	return func(request Request, _ files.Checkout) ([]Step, error) {
		return []Step{teamStep(request, action)}, nil
	}
}

// editTeamSteps is a team's new description and, when it names another, its new name; a team the platform still
// names is not renamed, which v2 judges.
func editTeamSteps(request Request, _ files.Checkout) ([]Step, error) {
	steps := []Step{teamStep(request, kernel.DescribeTeam)}
	if request.Rename != "" && request.Rename != request.Team {
		steps = append(steps, teamStep(request, kernel.RenameTeam))
	}
	return steps, nil
}

// teamStep is one action on the registry, carrying only the value that action takes; a channel states its transport,
// the one the change names or the one v2 would use.
func teamStep(request Request, action kernel.TeamAction) TeamStep {
	change := kernel.TeamChange{Action: action, Team: request.Team}
	switch action {
	case kernel.AddTeam, kernel.DescribeTeam:
		change.Description = request.Description
	case kernel.RenameTeam:
		change.Rename = request.Rename
	case kernel.GiveChannel:
		change.Transport = request.Transport
		if change.Transport == "" {
			change.Transport = orchestration.DefaultTransport
		}
	}
	return TeamStep{Change: change}
}
