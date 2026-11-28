package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/console"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// An app's form: scale, access, connections, access lists, team, promotion and removal are one kind of change each, and
// every one is a pull request v2 opens. The console decides which change the form asks for and refuses a form that
// would change nothing; what the pull request carries, and every word in it, stays v2's.

// changeApp is one app form's change. The checks are the console's own, in its own words and its own status codes
// (404 for no such change or instance, 409 when it cannot write or has no records, 400 for a refused form); what is
// left is v2's command, run as a job whose lines and link the operator reads.
func (s *consoleServer) changeApp(w http.ResponseWriter, r *http.Request) {
	fields, err := formFields(r)
	if err != nil {
		s.failure(w, r, http.StatusBadRequest, "The form could not be read: "+err.Error())
		return
	}
	s.startAppChange(w, r, r.PathValue("app"), r.PathValue("env"), r.PathValue("change"), fields)
}

// startAppChange is one app change from a form already read: its request's body is spent, so the fields come with it.
func (s *consoleServer) startAppChange(w http.ResponseWriter, r *http.Request, app, env, change string,
	fields map[string][]string) {
	kind := "v2-" + change
	one, _ := console.ChangeNamed(kind)
	instance, instanceKnown := pages.Named(app, env)
	if !instanceKnown || !oneOf(change, appChanges) {
		s.failure(w, r, http.StatusNotFound, "No such app instance or change.")
		return
	}
	if !s.hasGitHub {
		s.failure(w, r, http.StatusConflict, "No GitHub token is configured (console-github Secret).")
		return
	}
	if s.jobs == nil {
		s.failure(w, r, http.StatusConflict, console.NoJobsProblem)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()
	spec, problem, status := s.reads().AppChange(ctx, instance, one, change, fields, identityOf(r))
	if status != 0 {
		s.failure(w, r, status, problem)
		return
	}
	job, err := s.jobs.Start(ctx, one.Name, one.Target(app, env), identityOf(r), nil, console.ChangeWork(*spec, s.openProposal))
	if err != nil {
		if inPlace(r) {
			s.refusalAnswer(w, r, err.Error())
			return
		}
		s.failure(w, r, http.StatusConflict, err.Error())
		return
	}
	s.started(w, r, job)
}

// appChanges are the changes an app's forms may start. The egress grant and a schedule are not among them: neither is a
// connection the console's forms own.
var appChanges = []string{"scale", "expose", "connect", "disconnect", "allow", "team", "promote", "remove",
	"egress", "schedule"}

// oneOf is whether the list holds the value.
func oneOf(value string, list []string) bool {
	for _, found := range list {
		if found == value {
			return true
		}
	}
	return false
}

// writeTimeout bounds a form's own checks: the cluster reads a change's decision needs, never the change itself, which
// runs as a job.
const writeTimeout = 60 * time.Second

// maxFormBytes bounds a posted form: the console's fields are short, and a body that big is not one of its forms.
const maxFormBytes = 64 << 10

// readBody is one request's body, refusing one larger than a form can be.
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFormBytes))
	if err != nil {
		return nil, err
	}
	if len(body) >= maxFormBytes {
		return nil, errors.New("the form is too large")
	}
	return body, nil
}

// formFields is a posted form's fields, as the browser sent them: one value per field unless a list of checkboxes.
func formFields(r *http.Request) (map[string][]string, error) {
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	fields, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	return fields, nil
}

// openProposal runs v2's command in this process. The capability supplies the request;
// the command owns its environment, filesystem and diagnostic stream.
func (s *consoleServer) openProposal(ctx context.Context, proposal console.ChangeProposal, line func(string)) (string, error) {
	var refusal strings.Builder
	c := s.command(&refusal, line)
	c.stdin = strings.NewReader(proposal.Address)
	spec := proposalSpec(proposal)
	opened, status := c.openChange(spec)
	if status != 0 {
		return "", errors.New(strings.TrimSpace(refusal.String()))
	}
	return opened, nil
}

// proposalSpec translates the console's typed request to the command's flag representation.
func proposalSpec(proposal console.ChangeProposal) *changeSpec {
	one := proposal.Request
	return &changeSpec{kind: proposal.Kind, app: one.App, env: one.Env, team: one.Team,
		settings: one.Settings, calls: one.Calls, subscriptions: one.Subscriptions,
		dropCalls: one.DropCalls, dropSubscriptions: one.DropSubscriptions, allows: one.Allows,
		disallows: one.Disallows, description: one.Description, rename: one.Rename, transport: one.Transport,
		image: one.Image, requestedBy: proposal.Identity, repository: proposal.Repository}
}

// command is one of v2's commands run in the console's own process: its lines are the job's, its refusal is the text
// the job ends with. Its files are read from disk like every command's, because a change's steps run on a working copy
// the command itself downloads and stages; a command that must read the pinned commit is given a file of it instead.
func (s *consoleServer) command(diagnostic io.Writer, line func(string)) cli {
	// The effects are the CLI's own, because these are the CLI's commands: the console supplies the identity and the
	// tokens, and nothing else a command would find in a terminal.
	return cli{out: lineWriter{line: line}, diagnostic: diagnostic, readFile: readInput, writer: files.New(),
		archives: archiveReader, registries: registryReader, seed: randomSeed, encrypter: sops.New(),
		inCluster: podServiceAccount(), stdin: strings.NewReader(""), consoleSource: s.github,
		getenv: s.commandEnvironment()}
}

// commandEnvironment is what a command reads from its environment: the console's own tokens and repository, which its
// pod holds, and nothing else changed.
func (s *consoleServer) commandEnvironment() func(string) string {
	return func(name string) string {
		switch name {
		case consoleTokenEnv:
			return s.token
		case consoleRepositoryEnv:
			return s.repository
		case appReposTokenEnv:
			return s.reposToken
		case appWebhookTokenEnv:
			return s.hookToken
		}
		return os.Getenv(name)
	}
}

// lineWriter turns what a command prints into the job's lines, one line each.
type lineWriter struct{ line func(string) }

func (l lineWriter) Write(body []byte) (int, error) {
	for _, text := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if text != "" {
			l.line(text)
		}
	}
	return len(body), nil
}

// changeFlag sets or clears one feature flag's override for an instance. It is not a pull request: the override is
// written to NATS at once, which is why the flag store also writes one audit line, and the page it returns to shows
// what is stored rather than what was asked.
func (s *consoleServer) changeFlag(w http.ResponseWriter, r *http.Request) {
	instance, known := pages.Named(r.PathValue("app"), r.PathValue("env"))
	if !known {
		s.failure(w, r, http.StatusNotFound, "No such app instance.")
		return
	}
	name := r.PathValue("flag")
	fields, err := formFields(r)
	if err != nil {
		s.failure(w, r, http.StatusBadRequest, "The form could not be read: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()
	tree, err := s.pinnedTree(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	problem, status := s.flags.Change(ctx, instance, tree, console.FlagChange{Name: name,
		Set: first(fields, "action") == "set", Value: first(fields, "value"), Identity: identityOf(r)})
	if problem != "" {
		s.failure(w, r, status, problem)
		return
	}
	http.Redirect(w, r, "/apps/"+instance.App+"/"+instance.Env+"?flag="+name+"#flags", http.StatusSeeOther)
}

// useOffer is the catalogue's Use: the app chosen there connects to the app whose offer was being read, as its own
// connection form would: the same change, from the fields it read once.
func (s *consoleServer) useOffer(w http.ResponseWriter, r *http.Request) {
	fields, err := formFields(r)
	if err != nil {
		s.failure(w, r, http.StatusBadRequest, "The form could not be read: "+err.Error())
		return
	}
	chosen := first(fields, "app")
	if chosen == "" {
		s.failure(w, r, http.StatusBadRequest, "Choose the app that will use it.")
		return
	}
	s.startAppChange(w, r, chosen, r.PathValue("env"), "connect", fields)
}

// first is one form field's first value.
func first(fields map[string][]string, name string) string {
	if values := fields[name]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// teamJobActions are the job names a team's changes run as, as the console has always named them: the Activity page
// reads them back.
var teamJobActions = map[string]string{
	"team-add": "team add", "team-edit": "team edit", "team-remove": "team remove",
	"team-channel-set": "channel set", "team-channel-remove": "channel remove",
}

// changeTeam adds, edits or removes a team: a pull request v2's own team command opens, refused before it starts when
// the name, the description or the new name would make one nothing could apply.
func (s *consoleServer) changeTeam(w http.ResponseWriter, r *http.Request) {
	action, kind := r.PathValue("action"), "team-"+r.PathValue("action")
	if !oneOf(action, []string{"add", "edit", "remove"}) {
		s.teamsRefusal(w, r, "no such team change")
		return
	}
	s.proposeTeam(w, r, kind)
}

// changeChannel gives a team its notification channel, or removes it. The address reaches v2 on its standard input and
// the job's redactor from the same moment, so it is never in an argument, a page or a log line.
func (s *consoleServer) changeChannel(w http.ResponseWriter, r *http.Request) {
	action, kind := r.PathValue("action"), "team-channel-"+r.PathValue("action")
	if !oneOf(action, []string{"set", "remove"}) {
		s.teamsRefusal(w, r, "no such channel change")
		return
	}
	s.proposeTeam(w, r, kind)
}

// proposeTeam starts one team change as a job, or says why the form was refused: the Teams page again, with the
// sentence, as the console always answered a team form.
func (s *consoleServer) proposeTeam(w http.ResponseWriter, r *http.Request, kind string) {
	one, known := console.ChangeNamed(kind)
	fields, err := formFields(r)
	if !known {
		s.teamsRefusal(w, r, "no such team change")
		return
	}
	if err != nil {
		s.teamsRefusal(w, r, "The form could not be read: "+err.Error())
		return
	}
	if !s.hasGitHub {
		s.failure(w, r, http.StatusConflict, "No GitHub token is configured (console-github Secret).")
		return
	}
	if s.jobs == nil {
		s.failure(w, r, http.StatusConflict, console.NoJobsProblem)
		return
	}
	name := strings.TrimSpace(first(fields, "name"))
	flags, err := pages.ConsoleTeamChange(kind, name, fields)
	if err != nil {
		s.teamsRefusal(w, r, err.Error())
		return
	}
	if flags.Address != "" {
		s.redact.Add(flags.Address)
	}
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()
	proposal := console.ChangeProposal{Kind: kind, Identity: identityOf(r), Repository: s.repository,
		Request: console.Request{Team: name, Description: flags.Description, Rename: flags.Rename, Transport: flags.Transport}}
	if flags.Address != "" {
		proposal.Address = flags.Address + "\n"
	}
	job, err := s.jobs.Start(ctx, teamJobActions[kind], one.Target(name, ""), identityOf(r), nil,
		console.ChangeWork(proposal, s.openProposal))
	if err != nil {
		if inPlace(r) {
			s.refusalAnswer(w, r, err.Error())
			return
		}
		s.failure(w, r, http.StatusConflict, err.Error())
		return
	}
	s.started(w, r, job)
}

// teamsRefusal answers a refused team form as the Teams page with its sentence, which is where its button was.
func (s *consoleServer) teamsRefusal(w http.ResponseWriter, r *http.Request, problem string) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	data, err := s.readTeams(ctx)
	if err != nil {
		s.failure(w, r, http.StatusBadGateway, err.Error())
		return
	}
	data.Error = problem
	data.shell = s.page(r, false)
	s.render(w, r, http.StatusBadRequest, "teams.html", data)
}
