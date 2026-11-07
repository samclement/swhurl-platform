package clickstacktest

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
)

// NewTeamKey is the key HyperDX gives the team a registration creates: random, never the wanted one.
const NewTeamKey = "random-team-key"

// Installation is an in-memory HyperDX before and after its first run: its accounts and its teams' ingestion keys,
// in the MongoDB at URI. Down makes its API unreachable; Refused answers a registration with that HTTP status, and
// a registration while a team exists is answered 409. Calls records every operation in order.
type Installation struct {
	URI      string
	Down     bool
	Refused  int
	Accounts []string
	TeamKeys []string
	Calls    []string
}

var _ clickstack.Installer = (*Installation)(nil)

func (f *Installation) api(operation string) error {
	f.Calls = append(f.Calls, operation)
	if f.Down {
		return &clickstack.Error{Operation: operation, Reason: "exec failed: no ready ClickStack app pod"}
	}
	return nil
}

func (f *Installation) Ready(context.Context) error { return f.api("GET /ready") }

func (f *Installation) TeamExists(context.Context) (bool, error) {
	if err := f.api("GET /installation"); err != nil {
		return false, err
	}
	return len(f.TeamKeys) > 0, nil
}

// Register creates the account and a team with NewTeamKey.
func (f *Installation) Register(_ context.Context, email, _ string) error {
	operation := "POST /register/password"
	if err := f.api(operation); err != nil {
		return err
	}
	status := f.Refused
	if status == 0 && len(f.TeamKeys) > 0 {
		status = 409
	}
	if status != 0 {
		return &clickstack.Error{Operation: operation, Reason: "HTTP " + strconv.Itoa(status)}
	}
	f.Accounts = append(f.Accounts, email)
	f.TeamKeys = append(f.TeamKeys, NewTeamKey)
	return nil
}

// database is MongoDB at uri: another URI does not connect, so its program answers nothing readable.
func (f *Installation) database(operation, uri string) error {
	f.Calls = append(f.Calls, operation)
	if uri == "" {
		return &clickstack.Error{Operation: operation, Reason: "credentials_required"}
	}
	if uri != f.URI {
		return &clickstack.Error{Operation: operation, Reason: "malformed"}
	}
	return nil
}

func (f *Installation) AccountExists(_ context.Context, uri, email string) (bool, error) {
	if err := f.database("account", uri); err != nil {
		return false, err
	}
	return slices.Contains(f.Accounts, email), nil
}

func (f *Installation) TeamKey(_ context.Context, uri, key string) (clickstack.TeamKey, error) {
	if err := f.database("team key", uri); err != nil {
		return clickstack.TeamKey{}, err
	}
	return clickstack.TeamKey{Teams: len(f.TeamKeys), Matches: len(f.TeamKeys) == 1 && f.TeamKeys[0] == key}, nil
}

// SetTeamKey sets the first team's key, as MongoDB's updateOne with an empty filter does.
func (f *Installation) SetTeamKey(_ context.Context, uri, key string) error {
	if err := f.database("set team key", uri); err != nil {
		return err
	}
	if len(f.TeamKeys) == 0 {
		return &clickstack.Error{Operation: "set team key", Reason: "no_team"}
	}
	f.TeamKeys[0] = key
	return nil
}

// The credentials an InstallerContract run uses; no error may repeat one.
const (
	contractURI      = "mongodb://hyperdx:mongo-password@clickstack-mongodb-svc:27017/hyperdx"
	contractEmail    = "admin@example.test"
	contractPassword = "Sekret-Passw0rd!"
	contractKey      = "wanted-ingestion-key"
)

// InstallerContract is the behaviour every clickstack.Installer must have. A fresh installation is ready and holds
// no team or account; registration creates both, with a key that is not the wanted one, and is then refused; the
// key set is the key that matches; setting a key with no team is an error; a down API is neither ready nor answers
// whether a team exists; another MongoDB answers nothing; and no error repeats a credential. open is given the
// Installation to build an Installer over, which the Installer's operations change.
func InstallerContract(t *testing.T, open func(*testing.T, *Installation) clickstack.Installer) {
	t.Helper()
	ctx := context.Background()
	installer := open(t, &Installation{URI: contractURI})
	fresh(t, ctx, installer)
	if err := installer.Register(ctx, contractEmail, contractPassword); err != nil {
		t.Fatalf("register: %v", err)
	}
	registered(t, ctx, installer)
	down := open(t, &Installation{URI: contractURI, Down: true})
	if err := down.Ready(ctx); err == nil {
		t.Fatal("a down API is ready")
	}
	if _, err := down.TeamExists(ctx); err == nil {
		t.Fatal("a down API answers whether a team exists")
	}
	refused := open(t, &Installation{URI: contractURI, Refused: 400})
	secret(t, "a refused registration", refused.Register(ctx, contractEmail, contractPassword), "HTTP 400")
	_, err := installer.AccountExists(ctx, "mongodb://other:other-password@elsewhere/hyperdx", contractEmail)
	secret(t, "another MongoDB", err, "malformed")
}

func fresh(t *testing.T, ctx context.Context, installer clickstack.Installer) {
	t.Helper()
	if err := installer.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if exists, err := installer.TeamExists(ctx); err != nil || exists {
		t.Fatalf("a fresh installation has a team: %v, %v", exists, err)
	}
	if exists, err := installer.AccountExists(ctx, contractURI, contractEmail); err != nil || exists {
		t.Fatalf("a fresh installation has the account: %v, %v", exists, err)
	}
	if state, err := installer.TeamKey(ctx, contractURI, contractKey); err != nil || state != (clickstack.TeamKey{}) {
		t.Fatalf("a fresh installation's teams: %+v, %v", state, err)
	}
	secret(t, "a key with no team", installer.SetTeamKey(ctx, contractURI, contractKey), "no_team")
}

func registered(t *testing.T, ctx context.Context, installer clickstack.Installer) {
	t.Helper()
	if exists, err := installer.TeamExists(ctx); err != nil || !exists {
		t.Fatalf("a registration made no team: %v, %v", exists, err)
	}
	if exists, err := installer.AccountExists(ctx, contractURI, contractEmail); err != nil || !exists {
		t.Fatalf("a registration made no account: %v, %v", exists, err)
	}
	if state, err := installer.TeamKey(ctx, contractURI, contractKey); err != nil || state != (clickstack.TeamKey{Teams: 1}) {
		t.Fatalf("a new team's key: %+v, %v", state, err)
	}
	secret(t, "a second registration", installer.Register(ctx, contractEmail, contractPassword), "HTTP 409")
	if err := installer.SetTeamKey(ctx, contractURI, contractKey); err != nil {
		t.Fatalf("set team key: %v", err)
	}
	if state, err := installer.TeamKey(ctx, contractURI, contractKey); err != nil || state != (clickstack.TeamKey{Teams: 1, Matches: true}) {
		t.Fatalf("the key set: %+v, %v", state, err)
	}
}

// secret checks a failure names its reason and repeats no credential.
func secret(t *testing.T, what string, err error, reason string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("%s: %v, want %s", what, err, reason)
	}
	for _, value := range []string{contractURI, "mongo-password", "other-password", contractPassword, contractKey, NewTeamKey} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("%s repeats a credential: %v", what, err)
		}
	}
}
