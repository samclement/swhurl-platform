package pages

import (
	"fmt"
	"regexp"
	"strings"
)

// A console change is one kind of pull request the console opens: the branch it is pushed to, the instance or team
// it changes, and the paths it may carry. The kinds are one table because three readers must agree about them: the
// command that opens the pull request, Activity's reading of a branch, and the rule that a change carries only what
// its own kind derives. It lives here, in the console's data layer, because this is the lowest layer the command and
// the pages share; a branch name is permanent — Activity reads the branches the console opened before this table
// existed — so a kind keeps the name it was first pushed under.

// The repository's own layout, as a console change carries it.
const (
	// ConsoleRecordsRoot is the folder the platform's records live in.
	ConsoleRecordsRoot = "v2-records"
	// ConsoleAppsRoot is the folder the compiler writes one instance's files into.
	ConsoleAppsRoot = "v2-apps/instances"
	// ConsoleUnitsRoot is the folder Flux applies an instance from.
	ConsoleUnitsRoot = "clusters/home"
	// ConsoleAuthFolder is the folder v2 renders the platform's signing keys into, one ConfigMap per namespace.
	ConsoleAuthFolder = "platform/auth"
	// ConsoleCallsFile is the resolution every instance's declarations are decided in.
	ConsoleCallsFile = ConsoleRecordsRoot + "/calls.json"
	// ConsoleEgressFile is the allocation the proxy's port and the instance's grants are decided in.
	ConsoleEgressFile = "platform/egress/allocation.json"
	// ConsoleTeamsFile is the team registry: one record for the whole platform.
	ConsoleTeamsFile = ConsoleRecordsRoot + "/teams.json"
	// ConsoleRoutesFile is what v2 renders from the registry for the notification service.
	ConsoleRoutesFile = "platform/notify/routes.json"
	// ConsoleChannelsFolder is where v2 renders one encrypted Secret per team channel.
	ConsoleChannelsFolder = "platform/notify/channels"
)

// ConsoleSharedFiles are the platform files any change may carry beside the instance's own: what the render writes
// for the platform as a whole rather than for one instance. A render that writes a new one adds it here, and the
// check that a change carries only what its kind derives reads the same list; the render's own platform outputs are
// tested against it, so the two cannot drift apart.
var ConsoleSharedFiles = []string{
	ConsoleCallsFile,
	ConsoleEgressFile,
	"platform/egress/configmap.json",
	"platform/egress/networkpolicy.json",
	"platform/nats/users.json",
	"platform/reloader/values.json",
	ConsoleUnitsRoot + "/kustomization.yaml",
}

// ConsoleChange is one kind of console pull request.
type ConsoleChange struct {
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
}

// consoleChanges are every kind. Each one's prefix is distinct, so the order decides nothing but which is read first.
var consoleChanges = []ConsoleChange{
	{Name: "new", Title: "v2-records: add {name}/staging", Envs: []string{"staging"}},
	{Name: "team-add", Title: "teams: add {name}"},
	{Name: "team-edit", Title: "teams: change {name}"},
	{Name: "team-remove", Title: "teams: remove {name}"},
	{Name: "team-channel-set", Title: "teams: set the notification channel of {name}"},
	{Name: "team-channel-remove", Title: "teams: remove the notification channel of {name}"},
	{Name: "v2-scale", Title: "apps: scale {name}/{env}", Envs: []string{"staging", "prod"}},
	{Name: "v2-expose", Title: "apps: change access to {name}/{env}", Envs: []string{"staging", "prod"}},
	{Name: "v2-connect", Title: "apps: change the connections of {name}/{env}", Envs: []string{"staging", "prod"}},
	{Name: "v2-disconnect", Title: "apps: change the connections of {name}/{env}", Envs: []string{"staging", "prod"}},
	{Name: "v2-allow", Title: "apps: change who may call {name}/{env}", Envs: []string{"staging", "prod"}},
	// The team is the app's: the edit names no environment and rewrites both, whichever page asked for it.
	{Name: "v2-team", Title: "apps: change the team of {name}", Envs: []string{"staging", "prod"}, BothEnvs: true},
	{Name: "v2-remove", Title: "apps: remove {name}/{env}", Envs: []string{"staging", "prod"}},
	{Name: "v2-promote", Title: "apps: promote {name}/staging to {name}/prod", Envs: []string{"staging"},
		TargetEnv: "prod", BothEnvs: true},
}

// ConsoleChanges is every kind of console pull request.
func ConsoleChanges() []ConsoleChange { return append([]ConsoleChange(nil), consoleChanges...) }

// ConsoleChangeNamed reads one kind by name; false for a kind the console does not open.
func ConsoleChangeNamed(kind string) (ConsoleChange, bool) {
	for _, one := range consoleChanges {
		if one.Name == kind {
			return one, true
		}
	}
	return ConsoleChange{}, false
}

// Team is whether the change is to the team registry rather than to an app.
func (c ConsoleChange) Team() bool { return len(c.Envs) == 0 }

// Allows is whether the kind's branch may name one environment.
func (c ConsoleChange) Allows(env string) bool {
	for _, one := range c.Envs {
		if one == env {
			return true
		}
	}
	return false
}

// Target is what a change is to: <app>/<env>, a promotion's production, or team:<name>.
func (c ConsoleChange) Target(name, env string) string {
	if c.Team() {
		return "team:" + name
	}
	if c.TargetEnv != "" {
		return name + "/" + c.TargetEnv
	}
	return name + "/" + env
}

// Heading is the pull request's title for one target.
func (c ConsoleChange) Heading(name, env string) string {
	return strings.NewReplacer("{name}", name, "{env}", env).Replace(c.Title)
}

var consoleBranchPatterns = func() map[string]*regexp.Regexp {
	found := make(map[string]*regexp.Regexp, len(consoleChanges))
	for _, one := range consoleChanges {
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
func ParseConsoleBranch(branch string) (change ConsoleChange, name, env string, ok bool) {
	for _, one := range consoleChanges {
		found := consoleBranchPatterns[one.Name].FindStringSubmatch(branch)
		if found == nil || !consoleName.MatchString(found[1]) {
			continue
		}
		if len(found) > 2 {
			env = found[2]
		}
		return one, found[1], env, true
	}
	return ConsoleChange{}, "", "", false
}

// Branch is the branch a change is pushed to. It is refused unless ParseConsoleBranch reads it back as the same
// change, because the console writes no branch but a new one of its own.
func (c ConsoleChange) Branch(name, env, revision string) (string, error) {
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
	if file == ConsoleTeamsFile || file == ConsoleRoutesFile {
		return true
	}
	channel, inFolder := strings.CutPrefix(file, ConsoleChannelsFolder+"/")
	return inFolder && channel != "" && !strings.Contains(channel, "/")
}

// ownedEnvs are the environments whose files the change may carry.
func (c ConsoleChange) ownedEnvs(env string) []string {
	if c.BothEnvs {
		return []string{"staging", "prod"}
	}
	return []string{env}
}

// Owns is whether a path is one this kind's pull request may carry: the instance's own records and everything v2
// derives from them, never another app's and never a file outside the render.
func (c ConsoleChange) Owns(name, env, file string) bool {
	if c.Team() {
		return ConsoleTeamInput(file)
	}
	for _, shared := range ConsoleSharedFiles {
		if file == shared {
			return true
		}
	}
	if strings.HasPrefix(file, ConsoleAuthFolder+"/") {
		return true
	}
	if file == ConsoleRecordsRoot+"/"+name+"/app.json" {
		return true
	}
	for _, one := range c.ownedEnvs(env) {
		namespace := name + "-" + one
		if strings.HasPrefix(file, ConsoleRecordsRoot+"/"+name+"/"+one+"/") ||
			strings.HasPrefix(file, ConsoleAppsRoot+"/"+name+"/"+one+"/") ||
			file == ConsoleUnitsRoot+"/app-"+namespace+".json" {
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
