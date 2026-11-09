package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/github"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// A change is the console's one way to open a pull request. It downloads main at one commit, applies its kind's
// commands to that copy, and publishes only the files its kind derives, so a pull request can carry nothing the
// console did not ask for and nothing it cannot explain. What a kind may carry, and what the operator is told, are
// pages' and orchestration's decisions; this file is the command's own: its flags, its working copy, and the order.
//
// The console reads its output as the job's log and takes the last line of standard output as the pull request it
// opened, so every line of progress goes there and only a refusal goes to standard error.

const (
	// consoleTokenEnv and consoleRepositoryEnv are where a change reads its credential and its repository. Never an
	// argument: a process's arguments are visible to every process on the host.
	consoleTokenEnv      = "CONSOLE_TOKEN"
	consoleRepositoryEnv = "CONSOLE_REPO"
	// consoleBaseBranch is what a change is applied to, and what its pull request asks to be merged into.
	consoleBaseBranch = "main"
	// consoleTokenMaxLength is the longest credential GitHub issues: anything longer is a mistake, not a token.
	consoleTokenMaxLength = 255
)

// changeSpec is what a change's flags state: which kind of pull request, whom it is for, and the values its kind
// takes. It is a pointer in the request because only a change reads it, and the shape check keeps a kind from reaching
// a command with a value that would mean something else there.
type changeSpec struct {
	kind, app, env, team                               string
	settings                                           []string
	calls, subscriptions, dropCalls, dropSubscriptions []string
	allows, disallows                                  []string
	description, rename, transport                     string
	image, preface                                     string
	requestedBy, repository, revision                  string
	dryRun                                             bool
}

// name is the app or the team the change is to.
func (s *changeSpec) name() string {
	if s.team != "" {
		return s.team
	}
	return s.app
}

// given is the optional flags this change states, by the names it was given them under.
func (s *changeSpec) given() map[string]bool {
	stated := map[string]bool{
		"set": len(s.settings) > 0, "call": len(s.calls) > 0, "subscribe": len(s.subscriptions) > 0,
		"drop-call": len(s.dropCalls) > 0, "drop-subscription": len(s.dropSubscriptions) > 0,
		"allow": len(s.allows) > 0, "disallow": len(s.disallows) > 0,
		"description": s.description != "", "rename": s.rename != "", "transport": s.transport != "",
		"image": s.image != "", "preface": s.preface != "",
	}
	return stated
}

// changeShape is what one kind's change states: the flags it may carry, the ones it must, and whether it needs one of
// a group. It is the kind-to-flags table, so a caller's mistake is refused in the change's own words instead of
// reaching a command that would answer with its whole usage text.
type changeShape struct {
	takes, needs, any []string
}

// changeShapes is every kind the console opens, and what each one's flags mean.
var changeShapes = map[string]changeShape{
	"new":                 {takes: []string{"set", "image", "preface"}, needs: []string{"set", "image"}},
	"v2-scale":            {takes: []string{"set"}, needs: []string{"set"}},
	"v2-expose":           {takes: []string{"set"}, needs: []string{"set"}},
	"v2-team":             {takes: []string{"set"}, needs: []string{"set"}},
	"v2-connect":          {takes: []string{"call", "subscribe"}, any: []string{"call", "subscribe"}},
	"v2-disconnect":       {takes: []string{"drop-call", "drop-subscription"}, any: []string{"drop-call", "drop-subscription"}},
	"v2-allow":            {takes: []string{"allow", "disallow"}, any: []string{"allow", "disallow"}},
	"v2-remove":           {},
	"v2-promote":          {},
	"team-add":            {takes: []string{"description"}},
	"team-edit":           {takes: []string{"description", "rename"}},
	"team-remove":         {},
	"team-channel-set":    {takes: []string{"transport"}},
	"team-channel-remove": {},
}

// repeated is a flag the operator may give more than once, in order.
type repeated struct{ values *[]string }

func (r repeated) String() string { return "" }

func (r repeated) Set(value string) error {
	*r.values = append(*r.values, value)
	return nil
}

// registerChangeCommand adds what a change alone takes.
func (r *request) registerChangeCommand(flags *flag.FlagSet) {
	spec := &changeSpec{}
	r.spec = spec
	flags.Var(&once{value: &spec.kind}, "kind", "the kind of change, as the console names it")
	flags.Var(&once{value: &spec.app}, "app", "the application it changes")
	flags.Var(&once{value: &spec.env}, "env", "its environment")
	flags.Var(&once{value: &spec.team}, "team", "its team, for a change to the team registry")
	flags.Var(repeated{&spec.settings}, "set", "one setting, name=value")
	flags.Var(repeated{&spec.calls}, "call", "an app to call, beside the ones the record already states")
	flags.Var(repeated{&spec.subscriptions}, "subscribe", "an event to receive, <app>.<event>")
	flags.Var(repeated{&spec.dropCalls}, "drop-call", "an app to stop calling")
	flags.Var(repeated{&spec.dropSubscriptions}, "drop-subscription", "an event to stop receiving")
	flags.Var(repeated{&spec.allows}, "allow", "an app or team:<team> whose calls to accept")
	flags.Var(repeated{&spec.disallows}, "disallow", "an app or team:<team> whose calls to refuse")
	flags.Var(&once{value: &spec.description}, "description", "a team's description")
	flags.Var(&once{value: &spec.rename}, "rename", "a team's new name")
	flags.Var(&once{value: &spec.transport}, "transport", "a channel's transport (default: ntfy)")
	flags.Var(&once{value: &spec.image}, "image", "a new app's first image, REPO:<run>-<commit>")
	flags.Var(&once{value: &spec.preface}, "preface", "what a new app's pull request says before its commands")
	flags.Var(&once{value: &spec.requestedBy}, "requested-by", "who asked for it, as the commit states them")
	flags.Var(&once{value: &spec.repository}, "repository", "owner/repository (default: $CONSOLE_REPO)")
	flags.Var(&once{value: &spec.revision}, "revision", "the commit to build on (default: main's head)")
	flags.Var(&onceBool{value: &spec.dryRun}, "dry-run", "apply the change and report it; write nothing to GitHub")
}

// changeNamed is what a change must state: its kind, who asked, and either an app and an environment or a team.
func (r *request) changeNamed() []string {
	spec := r.spec
	if spec == nil || spec.kind == "" || spec.requestedBy == "" {
		return []string{""}
	}
	switch {
	case spec.team != "" && (spec.app != "" || spec.env != ""):
		return []string{""}
	case spec.team == "" && (spec.app == "" || spec.env == ""):
		return []string{""}
	}
	return []string{spec.kind, spec.name(), spec.requestedBy}
}

// change opens the pull request one kind of change asks for.
func (c cli) change(r request) int {
	spec := r.spec
	change, found := pages.ConsoleChangeNamed(spec.kind)
	if !found {
		return fail(c.diagnostic, fmt.Errorf("invalid: kind: %q is not a change the console opens", spec.kind))
	}
	if change.Team() != (spec.team != "") || (!change.Team() && !change.Allows(spec.env)) {
		return fail(c.diagnostic, fmt.Errorf("invalid: env: %s does not name %q", spec.kind, spec.env))
	}
	if err := shaped(spec.kind, spec); err != nil {
		return fail(c.diagnostic, err)
	}
	repository, status := c.changeRepository(spec)
	if status != 0 {
		return status
	}
	token := c.environment(consoleTokenEnv)
	if token == "" || len(token) > consoleTokenMaxLength {
		return fail(c.diagnostic, errors.New("invalid: token: "+consoleTokenEnv+" is not set"))
	}
	revision, dir, status := c.changeCopy(spec, repository, token)
	if status != 0 {
		return status
	}
	defer os.RemoveAll(dir)
	return c.changeApplied(change, spec, dir, revision, repository, token)
}

// shaped refuses a change that states a flag its kind does not take, or omits one it needs.
func shaped(kind string, spec *changeSpec) error {
	shape, known := changeShapes[kind]
	if !known {
		return fmt.Errorf("invalid: kind: %q has no flags yet", kind)
	}
	stated := spec.given()
	for _, flag := range slices.Sorted(maps.Keys(stated)) {
		if stated[flag] && !slices.Contains(shape.takes, flag) {
			return fmt.Errorf("invalid: --%s: %s does not take it", flag, kind)
		}
	}
	for _, flag := range shape.needs {
		if !stated[flag] {
			return fmt.Errorf("invalid: --%s: %s needs it", flag, kind)
		}
	}
	if len(shape.any) > 0 && !slices.ContainsFunc(shape.any, func(flag string) bool { return stated[flag] }) {
		return fmt.Errorf("invalid: %s needs one of --%s", kind, strings.Join(shape.any, ", --"))
	}
	return nil
}

// changeRepository is the repository to open the pull request on: the flag, or the console's own environment.
func (c cli) changeRepository(spec *changeSpec) (string, int) {
	repository := spec.repository
	if repository == "" {
		repository = c.environment(consoleRepositoryEnv)
	}
	if repository == "" {
		return "", fail(c.diagnostic, errors.New("invalid: repository: give --repository or set "+consoleRepositoryEnv))
	}
	return repository, 0
}

// changeCopy is the repository at one commit, unpacked into a folder of its own, and that folder.
func (c cli) changeCopy(spec *changeSpec, repository, token string) (string, string, int) {
	ctx := context.Background()
	source, err := c.changeSource()
	if err != nil {
		return "", "", fail(c.diagnostic, err)
	}
	revision := spec.revision
	if revision == "" {
		if revision, err = source.Revision(ctx, repository, token); err != nil {
			return "", "", fail(c.diagnostic, err)
		}
	}
	fmt.Fprintf(c.out, "Downloading %s %s (%s)\n", repository, consoleBaseBranch, firstSeven(revision))
	archive, err := source.Tarball(ctx, repository, token, revision)
	if err != nil {
		return "", "", fail(c.diagnostic, err)
	}
	tree, err := orchestration.ReadTree(archive, true)
	if err != nil {
		return "", "", fail(c.diagnostic, err)
	}
	dir, err := os.MkdirTemp("", "swhurl-change-")
	if err != nil {
		return "", "", fail(c.diagnostic, errors.New("io: working copy: cannot create a folder"))
	}
	if err := files.Unpack(dir, tree); err != nil {
		os.RemoveAll(dir)
		return "", "", fail(c.diagnostic, err)
	}
	return revision, dir, 0
}

// changeApplied runs the kind's commands in the working copy and publishes what they wrote, in the order the operator
// reads them: what each command said, what the change carries, and the pull request it opened.
func (c cli) changeApplied(change pages.ConsoleChange, spec *changeSpec, dir, revision, repository, token string) int {
	before, err := files.Snapshot(dir)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	steps, err := c.changeSteps(change, spec, newChangeTree(dir), revision)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	for _, step := range steps {
		if status := c.runChangeStep(step); status != 0 {
			return status
		}
	}
	after, err := files.Snapshot(dir)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	publisher := c.publisher
	if publisher == nil {
		publisher = github.NewPublisher(&http.Client{Timeout: 60 * time.Second})
	}
	commands := make([]string, 0, len(steps))
	for _, step := range steps {
		commands = append(commands, step.display)
	}
	_, err = orchestration.Publish(context.Background(), orchestration.PublishInputs{
		Change: change, Name: spec.name(), Env: spec.env, Identity: spec.requestedBy, Revision: revision,
		Before: before, After: after, Commands: commands, Preface: spec.preface, Repository: repository,
		Token: token, DryRun: spec.dryRun,
	}, publisher, func(line string) { fmt.Fprintln(c.out, line) })
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return 0
}

// runChangeStep runs one of v2's own commands on the working copy, and shows what it said where it said it. A refusal
// is the whole of what the step wrote, on the diagnostic, so the console shows v2's own words and not a paraphrase.
func (c cli) runChangeStep(step changeStep) int {
	var shown bytes.Buffer
	quiet := c
	quiet.out, quiet.diagnostic = &shown, &shown
	r := step.request
	r.command = step.command
	if status := commands()[step.command](quiet, r); status != 0 {
		c.diagnostic.Write(shown.Bytes())
		return status
	}
	c.out.Write(shown.Bytes())
	return 0
}

// changeSource is the GitHub reader a change downloads through: the console's own, unless a test supplied one.
func (c cli) changeSource() (github.Console, error) {
	if c.consoleSource != nil {
		return c.consoleSource, nil
	}
	return github.NewConsole(&http.Client{Timeout: 120 * time.Second}), nil
}

// firstSeven is the seven characters a branch and a line name a commit by.
func firstSeven(revision string) string {
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}
