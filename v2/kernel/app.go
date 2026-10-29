package kernel

import (
	"encoding/json"
	"regexp"
)

// AppID is an app record's content identity: app-sha256 over its canonical encoding.
type AppID string

// AppSpec is the app-level fact v2 owns: the team that labels every namespace the app has. It is not a
// release fact (the image does not depend on it) and not an instance fact (staging and production must
// agree), so an app has one record whatever environments it runs in.
type AppSpec struct {
	SchemaVersion int32  `json:"schemaVersion"`
	Application   string `json:"application"`
	Team          string `json:"team"`
	AppID         AppID  `json:"appId,omitempty"`
}

var appIDPattern = regexp.MustCompile(`^app-sha256:[0-9a-f]{64}$`)

// validate checks the fields in the order of the app's field table.
func (a AppSpec) validate() error {
	if a.SchemaVersion != 1 {
		return refuse(Unsupported, "schemaVersion", "only version 1 is supported")
	}
	if !namePattern.MatchString(a.Application) {
		return refuse(Invalid, "application", "expected an app name")
	}
	if !namePattern.MatchString(a.Team) {
		return refuse(Invalid, "team", "expected a team name")
	}
	if a.AppID != "" && !appIDPattern.MatchString(string(a.AppID)) {
		return refuse(Invalid, "appId", "expected an app ID")
	}
	return nil
}

// ID is the app record's identity, and checks an asserted one against its content.
func (a AppSpec) ID() (AppID, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	id := AppID(hash("app-sha256:", appCanonical(a)))
	if a.AppID != "" && a.AppID != id {
		return "", refuse(IdentityMismatch, "appId", "asserted identity does not match content")
	}
	return id, nil
}

func appCanonical(a AppSpec) []byte {
	return canonical([]any{"swhurl.app.v1", a.SchemaVersion, a.Application, a.Team})
}

// DecodeApp reads one app record and validates what every reader of it depends on: the app it names, its
// team and its identity.
func DecodeApp(data []byte) (AppSpec, error) {
	if err := document(data); err != nil {
		return AppSpec{}, err
	}
	if _, err := fields(data, "app", []string{"schemaVersion", "application", "team"}, "appId"); err != nil {
		return AppSpec{}, err
	}
	var app AppSpec
	if err := json.Unmarshal(data, &app); err != nil {
		return AppSpec{}, refuse(Invalid, "app", "incorrect field type or integer range")
	}
	id, err := app.ID()
	if err != nil {
		return AppSpec{}, err
	}
	app.AppID = id
	return app, nil
}

// EncodeApp is the record's bytes: indented, with its computed identity stated.
func EncodeApp(app AppSpec) ([]byte, error) {
	id, err := app.ID()
	if err != nil {
		return nil, err
	}
	app.AppID = id
	return json.MarshalIndent(app, "", "  ")
}
