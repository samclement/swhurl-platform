package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/adapters/sops"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// An app's form: scale, access, connections, access lists, team, promotion and removal are one kind of change each, and
// every one is a pull request v2 opens. The console decides which change the form asks for and refuses a form that
// would change nothing; what the pull request carries, and every word in it, stays v2's.

// changeApp is one app form's change. The checks are the console's own, in its own words and its own status codes
// (404 for no such change or instance, 409 when it cannot write or has no records, 400 for a refused form); what is
// left is v2's command, run as a job whose lines and link the operator reads.
func (s *consoleServer) changeApp(w http.ResponseWriter, r *http.Request) {
	app, env, change := r.PathValue("app"), r.PathValue("env"), r.PathValue("change")
	kind := "v2-" + change
	one, _ := pages.ConsoleChangeNamed(kind)
	instance, instanceKnown := pages.Named(app, env)
	if !instanceKnown || !oneOf(change, appChanges) {
		s.failure(w, r, http.StatusNotFound, "No such app instance or change.")
		return
	}
	fields, err := formFields(r)
	if err != nil {
		s.failure(w, r, http.StatusBadRequest, "The form could not be read: "+err.Error())
		return
	}
	if !s.hasGitHub {
		s.failure(w, r, http.StatusConflict, "No GitHub token is configured (console-github Secret).")
		return
	}
	if s.jobs == nil {
		s.failure(w, r, http.StatusConflict, noJobsProblem)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()
	spec, problem, status := s.changeSpecOf(ctx, instance, one, change, fields, identityOf(r))
	if status != 0 {
		s.failure(w, r, status, problem)
		return
	}
	job, err := s.jobs.Start(ctx, one.Name, one.Target(app, env), identityOf(r), nil, s.changeWork(spec))
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

// changeSpecOf is what one form asks for: the instance's records as the pinned commit states them, the form's own
// refusals and the flags v2's command takes. A problem comes with the status the console answers it with.
func (s *consoleServer) changeSpecOf(ctx context.Context, instance pages.Instance, one pages.ConsoleChange, change string,
	fields map[string][]string, identity string) (*changeSpec, string, int) {
	tree, rows, row, err := s.appRow(ctx, instance)
	if err != nil {
		return nil, err.Error(), http.StatusBadGateway
	}
	if row == nil || row.Inventory == nil {
		return nil, "its records are not in Git, so what it declares is unknown", http.StatusConflict
	}
	if problem, refused := appFormRefusal(change, one, instance.Env, row, rows); refused {
		return nil, problem, http.StatusBadRequest
	}
	teams, _ := orchestration.ConsoleTeams(tree)
	flags, err := pages.ConsoleFormChange(change, instance.App, fields, row.Inventory, teams)
	if err != nil {
		return nil, err.Error(), http.StatusBadRequest
	}
	if problem, refused := offered(change, instance.Env, instance.App, rows, tree, fields); refused {
		return nil, problem, http.StatusBadRequest
	}
	return &changeSpec{kind: one.Name, app: instance.App, env: instance.Env, requestedBy: identity, repository: s.repository,
		settings: flags.Settings, calls: flags.Calls, subscriptions: flags.Subscriptions, dropCalls: flags.DropCalls,
		dropSubscriptions: flags.DropSubscriptions, allows: flags.Allows, disallows: flags.Disallows}, "", 0
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

// appFormRefusal is what a change refuses before its fields are read: a change a page does not offer, an environment
// the kind does not name, and the two things a form cannot ask for at all. A promotion also needs production to exist
// and staging to be promotable, which is the cluster's answer rather than the form's.
func appFormRefusal(change string, one pages.ConsoleChange, env string, row *pages.AppRow,
	rows []pages.AppRow) (string, bool) {
	switch change {
	case "egress":
		return "the console offers no egress form: grant a host with `swhurl-v2 edit --app " + row.Instance.App +
			" --env=" + env + " --set egress=<host>` and run the render it asks for", true
	case "schedule":
		return "v2 does not model a schedule: the console offers no schedule form", true
	}
	if !one.Allows(env) {
		return change + " from the " + strings.Join(one.Envs, " or ") + " instance", true
	}
	if change != "promote" {
		return "", false
	}
	if !declaredIn(rows, row.Instance.App, "prod") {
		return "v2 has no first promotion: production has no records yet", true
	}
	if row.Report != nil && !row.Report.Promotion.Ready {
		return strings.ReplaceAll(row.Report.Promotion.Reason, "_", " "), true
	}
	return "", false
}

// declaredIn is whether one environment of an app has records the console could see.
func declaredIn(rows []pages.AppRow, app, env string) bool {
	for _, row := range rows {
		if row.Instance.App == app && row.Instance.Env == env && row.Inventory != nil {
			return true
		}
	}
	return false
}

// offered refuses a connection the form did not offer: a call to an app that is not a web app in that environment, or
// an event nothing there publishes. The lists are the page's own, read at the same pinned commit.
func offered(change, env, app string, rows []pages.AppRow, tree map[string][]byte,
	fields map[string][]string) (string, bool) {
	if change != "connect" {
		return "", false
	}
	connectable := pages.ConnectableTo(rows, orchestration.ConsoleOffers(tree, rows, env), env, app)
	callable := make(map[string]bool, len(connectable.Apps))
	for _, one := range connectable.Apps {
		callable[one.App] = true
	}
	published := make(map[string]bool, len(connectable.Events))
	for _, one := range connectable.Events {
		published[one.Name] = true
	}
	var missing []string
	for _, name := range splitNames(fields["calls"]) {
		if !callable[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range splitNames(fields["subscribe"]) {
		if !published[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return strings.Join(missing, ", ") + ": not a web app, or an event, that staging has now; reload the form", true
	}
	return "", false
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

// splitNames is a form's list of names: every value, split on commas, without the empty ones.
func splitNames(values []string) []string {
	var found []string
	for _, value := range values {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				found = append(found, name)
			}
		}
	}
	return found
}

// changeWork is one app change as a job's work: v2's own command in this process, whose lines are the job's and whose
// pull request is the link the job keeps.
func (s *consoleServer) changeWork(spec *changeSpec) orchestration.JobWork {
	return func(ctx context.Context, line func(string)) (string, error) {
		return s.openChangeInput(spec, "", line)
	}
}

// openChange runs one change: it downloads the pinned commit, applies the change in a working copy of its own, publishes
// what its kind owns and opens the pull request. A refusal is the whole of what the command wrote, in v2's words.
func (s *consoleServer) openChangeInput(spec *changeSpec, input string, line func(string)) (string, error) {
	opened := ""
	var refusal strings.Builder
	c := s.command(&refusal, func(text string) {
		if url, isPull := strings.CutPrefix(text, "Opened "); isPull {
			opened = strings.TrimSpace(url)
		}
		line(text)
	})
	c.stdin = strings.NewReader(input)
	if status := c.change(request{command: "change", spec: spec}); status != 0 {
		return "", errors.New(strings.TrimSpace(refusal.String()))
	}
	return opened, nil
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
// written to NATS at once, which is why it also writes one audit line, and the page it returns to shows what is stored
// rather than what was asked.
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
	store := s.flagStore(ctx)
	declaration, problem := orchestration.FlagDeclaration(tree[flagDocument(instance)])
	set := first(fields, "action") == "set"
	if set {
		err = orchestration.SetFlag(ctx, store, instance.Env, instance.App, name, first(fields, "value"), declaration)
	} else {
		err = orchestration.ClearFlag(ctx, store, instance.Env, instance.App, name)
	}
	if err != nil {
		s.failure(w, r, http.StatusBadRequest, name+": "+err.Error())
		return
	}
	card := orchestration.Flags(ctx, store, instance.Env, instance.App, declaration, problem, s.now())
	done := "cleared"
	if set {
		done = "= " + storedValue(card, name)
	}
	s.audit("%s flag %s/%s %s %s", identityOf(r), instance.App, instance.Env, name, done)
	http.Redirect(w, r, "/apps/"+instance.App+"/"+instance.Env+"?flag="+name+"#flags", http.StatusSeeOther)
}

// storedValue is the value the write left one flag at, as the card reads it back.
func storedValue(card orchestration.FlagCard, name string) string {
	for _, row := range card.Rows {
		if row.Name == name && row.Overridden {
			encoded, err := json.Marshal(row.Override.Value)
			if err != nil {
				return "unknown"
			}
			return string(encoded)
		}
	}
	return "unknown"
}

// useOffer is the catalogue's Use: the app chosen there connects to the app whose offer was being read, as its own
// connection form would. It is the same handler, with the chosen app in the path.
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
	r.SetPathValue("app", chosen)
	r.SetPathValue("change", "connect")
	s.changeApp(w, r)
}

// first is one form field's first value.
func first(fields map[string][]string, name string) string {
	if values := fields[name]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// audit writes one line to the console's log: a change that is not a pull request, and a job's own start and end, so
// the pod's log says what the console did.
func (s *consoleServer) audit(format string, args ...any) {
	if s.log == nil {
		return
	}
	fmt.Fprintf(s.log, "[AUDIT] "+format+"\n", args...)
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
	one, known := pages.ConsoleChangeNamed(kind)
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
		s.failure(w, r, http.StatusConflict, noJobsProblem)
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
	spec := &changeSpec{kind: kind, team: name, description: flags.Description, rename: flags.Rename,
		transport: flags.Transport, requestedBy: identityOf(r), repository: s.repository}
	job, err := s.jobs.Start(ctx, teamJobActions[kind], one.Target(name, ""), identityOf(r), nil,
		s.teamWork(spec, flags.Address))
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

// teamWork is one team change as a job's work, with the channel's address on the command's standard input.
func (s *consoleServer) teamWork(spec *changeSpec, address string) orchestration.JobWork {
	return func(ctx context.Context, line func(string)) (string, error) {
		input := ""
		if address != "" {
			input = address + "\n"
		}
		return s.openChangeInput(spec, input, line)
	}
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
