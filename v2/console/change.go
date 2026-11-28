package console

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// A change is one kind of pull request the console opens. One table owns its branch facts,
// flags and typed steps, so Activity reads exactly the kinds the command publishes.

// Operation is one of v2's own operations a change runs on its working copy, named as the command that runs it.
type Operation string

// The operations a change may run.
const (
	// Create states a new app's records from the image its repository published.
	Create Operation = "create"
	// Edit changes one setting of an instance's records.
	Edit Operation = "edit"
	// Remove deletes one instance and everything v2 rendered for it.
	Remove Operation = "remove"
	// Promote selects production's release from staging's record.
	Promote Operation = "promote"
	// Render writes what the whole tree of records derives.
	Render Operation = "render"
	// Team changes the team registry.
	Team Operation = "team"
)

// Change is one kind of console change.
type Change struct {
	// Name is the kind, as its branch names it: console/<name>-<app or team>[-<env>]-<commit>.
	Name string
	// Title is the pull request's title; {name} and {env} are filled in and it never names a credential.
	Title string
	// Envs are the environments the branch may name, and none for a team's change.
	Envs []string
	// TargetEnv is the instance a change is to when that is not the branch's: a promotion changes production.
	TargetEnv string
	// BothEnvs is whether the change rewrites both of an app's environments: the app-level team change, and a
	// promotion, which writes production's record pair beside staging's.
	BothEnvs bool
	// Takes are the flags the kind may state, Needs the ones it must, and Any a group it needs one of.
	Takes, Needs, Any []string
	// Operations are what the change runs, in this order; an operation may run more than once or not at all, as
	// an edit runs once per setting.
	Operations []Operation
	// steps turns the change's request into the steps it runs.
	steps stepsOf
}

// changes are every kind the console opens.
var changes = []Change{
	{Name: "new", Title: "v2-records: add {name}/staging", Envs: []string{"staging"}, Takes: []string{"set", "image", "preface"}, Needs: []string{"set", "image"},
		Operations: []Operation{Create, Edit, Render}, steps: newSteps},
	{Name: "team-add", Title: "teams: add {name}", Takes: []string{"description"}, Operations: []Operation{Team},
		steps: teamSteps(kernel.AddTeam)},
	{Name: "team-edit", Title: "teams: change {name}", Takes: []string{"description", "rename"}, Operations: []Operation{Team},
		steps: editTeamSteps},
	{Name: "team-remove", Title: "teams: remove {name}", Operations: []Operation{Team}, steps: teamSteps(kernel.RemoveTeam)},
	{Name: "team-channel-set", Title: "teams: set the notification channel of {name}", Takes: []string{"transport"}, Operations: []Operation{Team},
		steps: teamSteps(kernel.GiveChannel)},
	{Name: "team-channel-remove", Title: "teams: remove the notification channel of {name}", Operations: []Operation{Team},
		steps: teamSteps(kernel.DropChannel)},
	{Name: "v2-scale", Title: "apps: scale {name}/{env}", Envs: []string{"staging", "prod"}, Takes: []string{"set"}, Needs: []string{"set"},
		Operations: []Operation{Edit, Render}, steps: instanceSettingSteps},
	{Name: "v2-expose", Title: "apps: change access to {name}/{env}", Envs: []string{"staging", "prod"}, Takes: []string{"set"}, Needs: []string{"set"},
		Operations: []Operation{Edit, Render}, steps: instanceSettingSteps},
	{Name: "v2-connect", Title: "apps: change the connections of {name}/{env}", Envs: []string{"staging", "prod"}, Takes: []string{"call", "subscribe"}, Any: []string{"call", "subscribe"},
		Operations: []Operation{Edit, Render}, steps: connectionSteps},
	{Name: "v2-disconnect", Title: "apps: change the connections of {name}/{env}", Envs: []string{"staging", "prod"}, Takes: []string{"drop-call", "drop-subscription"},
		Any: []string{"drop-call", "drop-subscription"}, Operations: []Operation{Edit, Render}, steps: connectionSteps},
	{Name: "v2-allow", Title: "apps: change who may call {name}/{env}", Envs: []string{"staging", "prod"}, Takes: []string{"allow", "disallow"}, Any: []string{"allow", "disallow"},
		Operations: []Operation{Edit, Render}, steps: allowSteps},
	{Name: "v2-team", Title: "apps: change the team of {name}", Envs: []string{"staging", "prod"}, BothEnvs: true, Takes: []string{"set"}, Needs: []string{"set"},
		Operations: []Operation{Edit, Render}, steps: appSettingSteps},
	{Name: "v2-remove", Title: "apps: remove {name}/{env}", Envs: []string{"staging", "prod"}, Operations: []Operation{Remove, Render}, steps: removeSteps},
	{Name: "v2-promote", Title: "apps: promote {name}/staging to {name}/prod", Envs: []string{"staging"}, TargetEnv: "prod", BothEnvs: true, Takes: []string{"image"},
		Operations: []Operation{Promote, Render}, steps: promoteSteps},
}

// Changes is every kind of console change.
func Changes() []Change { return slices.Clone(changes) }

// ChangeNamed reads one kind by name; false for a kind the console does not open.
func ChangeNamed(kind string) (Change, bool) {
	for _, one := range changes {
		if one.Name == kind {
			return one, true
		}
	}
	return Change{}, false
}

// Steps is what the change runs for one request, in order, as typed steps the command runs and quotes.
func (c Change) Steps(request Request, tree files.Checkout) ([]Step, error) {
	return c.steps(request, tree)
}

// CheckFlags refuses a change that states a flag its kind does not take, or omits one it needs, in the change's own
// words rather than in a command's whole usage text. stated is every optional flag by name, true where it was given.
func (c Change) CheckFlags(stated map[string]bool) error {
	for _, flag := range slices.Sorted(maps.Keys(stated)) {
		if stated[flag] && !slices.Contains(c.Takes, flag) {
			return fmt.Errorf("invalid: --%s: %s does not take it", flag, c.Name)
		}
	}
	for _, flag := range c.Needs {
		if !stated[flag] {
			return fmt.Errorf("invalid: --%s: %s needs it", flag, c.Name)
		}
	}
	if len(c.Any) > 0 && !slices.ContainsFunc(c.Any, func(flag string) bool { return stated[flag] }) {
		return fmt.Errorf("invalid: %s needs one of --%s", c.Name, strings.Join(c.Any, ", --"))
	}
	return nil
}

// Team is whether the change is to the team registry rather than to an app.
func (c Change) Team() bool { return len(c.Envs) == 0 }

// Allows is whether the kind's branch may name one environment.
func (c Change) Allows(env string) bool {
	for _, one := range c.Envs {
		if one == env {
			return true
		}
	}
	return false
}

// Target is what a change is to: <app>/<env>, a promotion's production, or team:<name>.
func (c Change) Target(name, env string) string {
	if c.Team() {
		return "team:" + name
	}
	if c.TargetEnv != "" {
		return name + "/" + c.TargetEnv
	}
	return name + "/" + env
}

// Heading is the pull request's title for one target.
func (c Change) Heading(name, env string) string {
	return strings.NewReplacer("{name}", name, "{env}", env).Replace(c.Title)
}

var consoleBranchPatterns = func() map[string]*regexp.Regexp {
	found := make(map[string]*regexp.Regexp, len(changes))
	for _, one := range changes {
		envs := ""
		if len(one.Envs) > 0 {
			envs = "-(" + strings.Join(one.Envs, "|") + ")"
		}
		found[one.Name] = regexp.MustCompile(`^console/` + regexp.QuoteMeta(one.Name) +
			`-([a-z][a-z0-9-]*?)` + envs + `-[0-9a-f]{7,40}$`)
	}
	return found
}()

// consoleName is a name a branch may carry: a DNS label, as an app's or a team's name is.
var consoleName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// ParseConsoleBranch reads console/<kind>-<app or team>[-<env>]-<commit> as its kind, its name and the environment
// it names (empty for a team's change). It reports false for any other branch, and for a name that is not a DNS
// label, so a branch the console did not open is never read as one.
func ParseConsoleBranch(branch string) (change Change, name, env string, ok bool) {
	for _, one := range changes {
		found := consoleBranchPatterns[one.Name].FindStringSubmatch(branch)
		if found == nil || !consoleName.MatchString(found[1]) {
			continue
		}
		if len(found) > 2 {
			env = found[2]
		}
		return one, found[1], env, true
	}
	return Change{}, "", "", false
}

// Branch is the branch a change is pushed to. It is refused unless ParseConsoleBranch reads it back as the same
// change, because the console writes no branch but a new one of its own.
func (c Change) Branch(name, env, revision string) (string, error) {
	made := "console/" + c.Name + "-" + name
	if !c.Team() {
		made += "-" + env
	}
	made += "-" + revision
	read, readName, readEnv, ok := ParseConsoleBranch(made)
	if !ok || read.Name != c.Name || readName != name || readEnv != env {
		return "", fmt.Errorf("refusing to push %q: the console pushes only its own branches", made)
	}
	return made, nil
}

// ConsoleTeamInput is whether a path is one a team change may carry: the registry, what v2 renders from it for the
// notification service, and one channel's encrypted Secret.
func ConsoleTeamInput(file string) bool {
	if file == pages.ConsoleTeamsFile || file == pages.ConsoleRoutesFile {
		return true
	}
	channel, inFolder := strings.CutPrefix(file, pages.ConsoleChannelsFolder+"/")
	return inFolder && channel != "" && !strings.Contains(channel, "/")
}

// ownedEnvs are the environments whose files the change may carry.
func (c Change) ownedEnvs(env string) []string {
	if c.BothEnvs {
		return []string{"staging", "prod"}
	}
	return []string{env}
}

// Owns is whether a path is one this kind's pull request may carry: the instance's own records and everything v2
// derives from them, never another app's and never a file outside the render.
func (c Change) Owns(name, env, file string) bool {
	if c.Team() {
		return ConsoleTeamInput(file)
	}
	for _, shared := range pages.ConsoleSharedFiles {
		if file == shared {
			return true
		}
	}
	if strings.HasPrefix(file, pages.ConsoleAuthFolder+"/") {
		return true
	}
	if file == kernel.AppRecordPath(pages.ConsoleRecordsRoot, name) {
		return true
	}
	for _, one := range c.ownedEnvs(env) {
		namespace := name + "-" + one
		if strings.HasPrefix(file, kernel.RecordFolder(pages.ConsoleRecordsRoot, name, one)+"/") ||
			strings.HasPrefix(file, pages.ConsoleAppsRoot+"/"+name+"/"+one+"/") ||
			file == pages.ConsoleUnitsRoot+"/app-"+namespace+".json" {
			return true
		}
	}
	return false
}

// MergePlanFor is what a person needs before merging a change, from the paths it added or changed: empty when
// merging is all. A change to an encrypted input needs its values set on the branch, which no merge can supply; a
// new app's seed is written and encrypted by v2 itself, so a creation is exempt, and a deletion needs no setup.
func MergePlanFor(kind string, changed []string) string {
	if kind == "new" {
		return ""
	}
	encrypted := []string{}
	for _, file := range changed {
		if strings.HasSuffix(file, ".sops.yaml") {
			encrypted = append(encrypted, file)
		}
	}
	if len(encrypted) == 0 {
		return ""
	}
	for _, file := range encrypted {
		if !strings.HasSuffix(file, "/"+consoleEventsKey) && file != consoleEventsKey {
			return "needs Secret values to be set on the pull request branch"
		}
	}
	return "adds or changes the app's generated encrypted events key"
}

// consoleEventsKey is the encrypted events key v2 writes for an app, named as the image's declaration states it.
const consoleEventsKey = "events.sops.yaml"

// BranchTarget is a console pull request's action and what it changes (<app>/<env> or team:<name>), from its
// branch console/<action>-<name>[-<env>]-<commit>; both empty for any other branch. The kinds are the one table in
// console_change.go, which the command that opens a pull request reads too, so a branch and a change cannot disagree.
func BranchTarget(branch string) (action, target string) {
	change, name, env, ok := ParseConsoleBranch(branch)
	if !ok {
		return "", ""
	}
	return change.Name, change.Target(name, env)
}

// PullsFor are the open pull requests that add or promote one instance.
func PullsFor(pulls []pages.OpenPull, instance pages.Instance) []pages.PendingPull {
	found := []pages.PendingPull{}
	for _, pull := range pulls {
		action, target := BranchTarget(pull.Branch)
		if (action == "new" || action == "v2-promote") && target == instance.Name() {
			found = append(found, pages.PendingPullOf(pull))
		}
	}
	return found
}
