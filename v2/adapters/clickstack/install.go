package clickstack

import (
	"context"
	"encoding/json"
)

// Installer is HyperDX's first run as the platform performs it. HyperDX has no setting for its first account or its
// team's ingestion key: its internal API, which needs no key, answers whether it is ready and whether a team exists
// and registers the first account; MongoDB holds the accounts and the team's key. Each MongoDB answer is computed
// beside MongoDB, so the team's current key never leaves its pod.
type Installer interface {
	// Ready is nil when the API answers its readiness probe with 200.
	Ready(ctx context.Context) error
	// TeamExists is whether HyperDX holds a team, which closes its registration.
	TeamExists(ctx context.Context) (bool, error)
	// Register creates the first account and its team; HyperDX refuses it while a team exists.
	Register(ctx context.Context, email, password string) error
	// AccountExists is whether an account with this email exists.
	AccountExists(ctx context.Context, uri, email string) (bool, error)
	// TeamKey is how the teams stand against the wanted ingestion key.
	TeamKey(ctx context.Context, uri, key string) (TeamKey, error)
	// SetTeamKey sets the one team's ingestion key; no team to set is an error.
	SetTeamKey(ctx context.Context, uri, key string) error
}

// TeamKey is how many teams exist, and whether there is exactly one and its ingestion key is the wanted one.
type TeamKey struct {
	Teams   int
	Matches bool
}

// internalBase is HyperDX's internal API, the one its own frontend calls.
const internalBase = "http://127.0.0.1:8000"

// The MongoDB programs; each prints one RESULT line.
const (
	accountCount = `print("RESULT " + JSON.stringify({accounts: db.users.countDocuments({email: EMAIL})}));`
	teamKeyState = `const t = db.teams.find({}, {apiKey: 1}).toArray(); ` +
		`print("RESULT " + JSON.stringify({teams: t.length, matches: t.length === 1 && t[0].apiKey === KEY}));`
	setTeamKey = `const r = db.teams.updateOne({}, {$set: {apiKey: KEY}}); ` +
		`print("RESULT " + JSON.stringify({matched: r.matchedCount}));`
)

var _ Installer = installer{}

type installer struct{ mongo, app Runner }

// NewInstaller is the first-run adapter over the same runners as New.
func NewInstaller(mongo, app Runner) Installer { return installer{mongo: mongo, app: app} }

func (i installer) Ready(ctx context.Context) error {
	_, err := fetch(ctx, i.app, "GET", internalBase, "/ready", nil, nil)
	return err
}

func (i installer) TeamExists(ctx context.Context) (bool, error) {
	body, err := fetch(ctx, i.app, "GET", internalBase, "/installation", nil, nil)
	if err != nil {
		return false, err
	}
	var answer struct {
		Existing *bool `json:"isTeamExisting"`
	}
	if json.Unmarshal(body, &answer) != nil || answer.Existing == nil {
		return false, &Error{"GET /installation", "malformed"}
	}
	return *answer.Existing, nil
}

func (i installer) Register(ctx context.Context, email, password string) error {
	_, err := fetch(ctx, i.app, "POST", internalBase, "/register/password", nil,
		map[string]string{"email": email, "password": password, "confirmPassword": password})
	return err
}

func (i installer) AccountExists(ctx context.Context, uri, email string) (bool, error) {
	var answer struct {
		Accounts *int `json:"accounts"`
	}
	if err := i.query(ctx, "account", uri, "const EMAIL = "+literal(email)+"; "+accountCount, &answer); err != nil {
		return false, err
	}
	if answer.Accounts == nil {
		return false, &Error{"account", "malformed"}
	}
	return *answer.Accounts > 0, nil
}

func (i installer) TeamKey(ctx context.Context, uri, key string) (TeamKey, error) {
	var answer struct {
		Teams   *int `json:"teams"`
		Matches bool `json:"matches"`
	}
	if err := i.query(ctx, "team key", uri, "const KEY = "+literal(key)+"; "+teamKeyState, &answer); err != nil {
		return TeamKey{}, err
	}
	if answer.Teams == nil {
		return TeamKey{}, &Error{"team key", "malformed"}
	}
	return TeamKey{Teams: *answer.Teams, Matches: answer.Matches}, nil
}

func (i installer) SetTeamKey(ctx context.Context, uri, key string) error {
	var answer struct {
		Matched *int `json:"matched"`
	}
	if err := i.query(ctx, "set team key", uri, "const KEY = "+literal(key)+"; "+setTeamKey, &answer); err != nil {
		return err
	}
	if answer.Matched == nil {
		return &Error{"set team key", "malformed"}
	}
	if *answer.Matched != 1 {
		return &Error{"set team key", "no_team"}
	}
	return nil
}

// query runs one MongoDB program after connecting to uri and decodes its answer.
func (i installer) query(ctx context.Context, operation, uri, script string, into any) error {
	if uri == "" {
		return &Error{operation, "credentials_required"}
	}
	out, err := i.mongo(ctx, []byte("db = connect("+literal(uri)+");\n"+script+"\n"))
	if err != nil {
		return &Error{operation, "exec failed: " + err.Error()}
	}
	if !result(out, into) {
		return &Error{operation, "malformed"}
	}
	return nil
}
