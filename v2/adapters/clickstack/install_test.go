package clickstack_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
	"github.com/samclement/swhurl-platform/v2/adapters/clickstack/clickstacktest"
)

var (
	connectLine = regexp.MustCompile(`^db = connect(\("[^"]*"\));$`)
	valueLine   = regexp.MustCompile(`^const (EMAIL|KEY) = ("[^"]*"); (.*)$`)
	statusCode  = regexp.MustCompile(`HTTP (\d+)$`)
)

// installation answers the installer's programs as ClickStack's containers would, from a fake installation: each
// fetch as HyperDX's internal API answers it, each mongosh script as MongoDB would after connecting.
type installation struct {
	t    *testing.T
	fake *clickstacktest.Installation
}

func (s installation) app(ctx context.Context, program []byte) ([]byte, error) {
	if s.fake.Down {
		return nil, errors.New("no ready ClickStack app pod")
	}
	found := fetchLine.FindStringSubmatch(strings.SplitN(string(program), "\n", 2)[0])
	var address string
	var request struct {
		Method, Redirect string
		Headers          map[string]string
		Body             *string
	}
	if found == nil || json.Unmarshal([]byte(found[1]), &address) != nil || json.Unmarshal([]byte(found[2]), &request) != nil {
		s.t.Fatalf("not a fetch: %s", program)
	}
	path, ok := strings.CutPrefix(address, "http://127.0.0.1:8000")
	if !ok || strings.HasPrefix(path, "/api/v2") || request.Headers["authorization"] != "" {
		s.t.Fatalf("not the internal API without a key: %s %+v", address, request)
	}
	return s.answer(ctx, request.Method+" "+path, request.Body), nil
}

// answer serves one request; a refusal's body repeats the credentials, which the adapter must never repeat.
func (s installation) answer(ctx context.Context, operation string, body *string) []byte {
	var answer any = map[string]bool{}
	var err error
	switch operation {
	case "GET /ready":
		err = s.fake.Ready(ctx)
	case "GET /installation":
		var exists bool
		exists, err = s.fake.TeamExists(ctx)
		answer = map[string]bool{"isTeamExisting": exists}
	case "POST /register/password":
		var account struct{ Email, Password, ConfirmPassword string }
		if body == nil || json.Unmarshal([]byte(*body), &account) != nil || account.Password != account.ConfirmPassword {
			s.t.Fatalf("a registration body: %v", body)
		}
		err = s.fake.Register(ctx, account.Email, account.Password)
		if err != nil {
			answer = map[string]string{"error": "refused " + account.Email + " " + *body}
		}
	default:
		s.t.Fatalf("an unexpected request %s", operation)
	}
	status := 200
	if found := statusCode.FindStringSubmatch(errorText(err)); found != nil {
		status, _ = strconv.Atoi(found[1])
	}
	encoded, _ := json.Marshal(map[string]any{"status": status, "body": answer})
	return append([]byte("RESULT "), encoded...)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s installation) mongo(ctx context.Context, program []byte) ([]byte, error) {
	lines := strings.Split(strings.TrimSuffix(string(program), "\n"), "\n")
	connect := connectLine.FindStringSubmatch(lines[0])
	var uri string
	if len(lines) != 2 || connect == nil || json.Unmarshal([]byte(strings.Trim(connect[1], "()")), &uri) != nil {
		s.t.Fatalf("not a connected script: %s", program)
	}
	if uri != s.fake.URI {
		return []byte("MongoServerError: Authentication failed.\n"), nil
	}
	found := valueLine.FindStringSubmatch(lines[1])
	var value string
	if found == nil || json.Unmarshal([]byte(found[2]), &value) != nil {
		s.t.Fatalf("not a stated script: %s", lines[1])
	}
	answer, err := s.script(ctx, uri, value, found[3])
	if err != nil {
		answer = map[string]int{"matched": 0}
	}
	encoded, _ := json.Marshal(answer)
	return append([]byte("Warning: some notice\nRESULT "), encoded...), nil
}

// script runs one stated program on the fake, recognised by the collection call it makes.
func (s installation) script(ctx context.Context, uri, value, script string) (any, error) {
	switch {
	case strings.Contains(script, "db.users.countDocuments({email: EMAIL})"):
		exists, _ := s.fake.AccountExists(ctx, uri, value)
		return map[string]int{"accounts": map[bool]int{false: 0, true: 1}[exists]}, nil
	case strings.Contains(script, "db.teams.find({}, {apiKey: 1})"):
		state, _ := s.fake.TeamKey(ctx, uri, value)
		return map[string]any{"teams": state.Teams, "matches": state.Matches}, nil
	case strings.Contains(script, "db.teams.updateOne({}, {$set: {apiKey: KEY}})"):
		return map[string]int{"matched": 1}, s.fake.SetTeamKey(ctx, uri, value)
	}
	s.t.Fatalf("an unexpected script: %s", script)
	return nil, nil
}

func TestInstallerContract(t *testing.T) {
	clickstacktest.InstallerContract(t, func(t *testing.T, fake *clickstacktest.Installation) clickstack.Installer {
		backend := installation{t: t, fake: fake}
		return clickstack.NewInstaller(backend.mongo, backend.app)
	})
}

// The installer's programs are exactly these: the credentials only inside them, as JavaScript literals.
func TestInstallerProgramsAreStated(t *testing.T) {
	var programs []string
	record := func(answer string) clickstack.Runner {
		return func(_ context.Context, program []byte) ([]byte, error) {
			programs = append(programs, string(program))
			return []byte(answer), nil
		}
	}
	ctx := context.Background()
	installer := clickstack.NewInstaller(record(`RESULT {"matched":1}`), record(`RESULT {"status":200,"body":{}}`))
	if err := installer.Register(ctx, email, `pass"word`); err != nil {
		t.Fatal(err)
	}
	if err := installer.SetTeamKey(ctx, uri, `k"\y`); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`const r = await fetch("http://127.0.0.1:8000/register/password", {"method":"POST","headers":{"content-type":"application/json"},` +
			`"redirect":"manual","body":"{\"confirmPassword\":\"pass\\\"word\",\"email\":\"` + email + `\",\"password\":\"pass\\\"word\"}"});` + "\n" +
			"let body = null; try { body = await r.json(); } catch {}\n" +
			`console.log("RESULT " + JSON.stringify({status: r.status, body}));` + "\n",
		`db = connect("` + uri + `");` + "\n" + `const KEY = "k\"\\y"; const r = db.teams.updateOne({}, {$set: {apiKey: KEY}}); ` +
			`print("RESULT " + JSON.stringify({matched: r.matchedCount}));` + "\n",
	}
	if len(programs) != 2 || programs[0] != want[0] || programs[1] != want[1] {
		t.Fatalf("programs:\n%s\nwant:\n%s", strings.Join(programs, "\n---\n"), strings.Join(want, "\n---\n"))
	}
}

// Every installer failure names the operation and a fixed reason, never a credential or an answer's bytes.
func TestInstallerFailuresNeverRepeatCredentialsOrAnswers(t *testing.T) {
	ctx := context.Background()
	private := []string{key, uri, email, "private-answer", "pass-word"}
	for name, runner := range map[string]clickstack.Runner{
		"exec":      func(context.Context, []byte) ([]byte, error) { return nil, errors.New("exec in pod-0 failed") },
		"no result": func(context.Context, []byte) ([]byte, error) { return []byte("private-answer"), nil },
		"empty":     func(context.Context, []byte) ([]byte, error) { return []byte(`RESULT {}`), nil },
		"status": func(context.Context, []byte) ([]byte, error) {
			return []byte(`RESULT {"status":503,"body":{"error":"private-answer"}}`), nil
		},
	} {
		installer := clickstack.NewInstaller(runner, runner)
		_, teamErr := installer.TeamExists(ctx)
		_, accountErr := installer.AccountExists(ctx, uri, email)
		_, keyErr := installer.TeamKey(ctx, uri, key)
		for _, err := range []error{installer.Register(ctx, email, "pass-word"), teamErr, accountErr, keyErr, installer.SetTeamKey(ctx, uri, key)} {
			if err == nil || containsAny(err.Error(), private) {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	if _, err := clickstack.NewInstaller(nil, nil).TeamKey(ctx, "", key); err == nil || !strings.Contains(err.Error(), "credentials_required") {
		t.Errorf("a team key without a URI: %v", err)
	}
}
