package kernel

import (
	"bytes"
	"strings"
	"testing"
)

// An app record is one fact with an identity: the app it names and its team. The same content gives the
// same ID, and a different team is a different app identity.
func TestAppRecordIdentity(t *testing.T) {
	app, err := DecodeApp(fixture(t, "app.json"))
	if err != nil {
		t.Fatal(err)
	}
	if app.Application != "demo-orders" || app.Team != "demo" {
		t.Fatalf("app %+v", app)
	}
	id, err := app.ID()
	if err != nil || id != app.AppID {
		t.Fatalf("identity %s: %v", id, err)
	}
	encoded, err := EncodeApp(app)
	if err != nil {
		t.Fatal(err)
	}
	again, err := DecodeApp(encoded)
	againID, againErr := again.ID()
	if err != nil || againErr != nil || againID != id {
		t.Fatalf("re-encoded app: %+v %v %v", again, err, againErr)
	}
	other := app
	other.Team, other.AppID = "insights", ""
	if changed, err := other.ID(); err != nil || changed == id {
		t.Fatalf("a changed team must change the app ID: %s %v", changed, err)
	}
	other.AppID = id
	if _, err := other.ID(); err == nil {
		t.Fatal("an asserted ID for other content was accepted")
	}
}

// A record this platform did not write is refused before anything renders: an unknown field, a missing
// team, an invalid name or an asserted identity that does not match.
func TestAppRecordRefusals(t *testing.T) {
	text := string(fixture(t, "app.json"))
	for _, tt := range []struct {
		name, text, reason string
	}{
		{"unknown field", strings.Replace(text, `"team": "demo"`, `"team": "demo", "region": "x"`, 1), "unsupported field"},
		{"missing team", strings.Replace(text, `"team": "demo",`, ``, 1), "required field is missing"},
		{"null team", strings.Replace(text, `"team": "demo"`, `"team": null`, 1), "null is not supported"},
		{"bad application", strings.Replace(text, `"application": "demo-orders"`, `"application": "Demo"`, 1), "expected an app name"},
		{"bad team", strings.Replace(text, `"team": "demo"`, `"team": "Demo"`, 1), "expected a team name"},
		{"bad asserted id", strings.Replace(text, `"team": "demo"`, `"team": "insights"`, 1), "asserted identity does not match content"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeApp([]byte(tt.text))
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error = %v, want %q", err, tt.reason)
			}
		})
	}
}

// CompileWith carries the app's team into the namespace and the app's identity into the compilation. The
// no-app form used by previews and live reads renders no team label.
func TestCompileCarriesTheAppRecord(t *testing.T) {
	spec, release, p := instance(t, "prod.json"), releaseFile(t, "r41.json"), profile(t)
	withApp, err := CompileWith(spec, release, app(t), p, Connections{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withApp.Inputs.App == "" {
		t.Fatal("the compilation does not name its app record")
	}
	namespace := ""
	for _, file := range withApp.Files {
		if strings.HasSuffix(file.Path, "/namespace.json") {
			namespace = string(file.Document)
		}
	}
	if !strings.Contains(namespace, `"platform.swhurl.com/team":"demo"`) {
		t.Fatalf("the namespace has no team label: %s", namespace)
	}
	withoutApp, err := Compile(spec, release, p)
	if err != nil {
		t.Fatal(err)
	}
	if withoutApp.Inputs.App != "" {
		t.Fatal("a compilation without an app record names one")
	}
	for _, file := range withoutApp.Files {
		if strings.HasSuffix(file.Path, "/namespace.json") && strings.Contains(string(file.Document), "platform.swhurl.com/team") {
			t.Fatal("a compilation without an app record rendered a team label")
		}
	}
}

// The release says whether the image reports telemetry; the profile says where. A release that does and a
// profile that does not is a refusal, never an invented endpoint.
func TestTelemetryAndNotifyFacts(t *testing.T) {
	spec, release, p := instance(t, "prod.json"), releaseFile(t, "r41.json"), profile(t)
	c, err := CompileWith(spec, release, app(t), p, Connections{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	helm := ""
	for _, file := range c.Files {
		if strings.HasSuffix(file.Path, "/helmrelease.json") {
			helm = string(file.Document)
		}
	}
	for _, want := range []string{`"OTEL_EXPORTER_OTLP_ENDPOINT":"http://$(HOST_IP):4318"`,
		`"OTEL_EXPORTER_OTLP_PROTOCOL":"http/protobuf"`, `"OTEL_SERVICE_NAME":"demo-orders"`,
		`"SWHURL_NOTIFY_URL":"http://notify.notify.svc.cluster.local:8080"`,
		`"audience":"notify"`, `"path":"notify/token"`} {
		if !strings.Contains(helm, want) {
			t.Errorf("the HelmRelease lacks %s", want)
		}
	}
	quiet := release
	quiet.Telemetry, quiet.ReleaseID = false, ""
	quietID, err := quiet.ID()
	if err != nil {
		t.Fatal(err)
	}
	quietSpec := spec
	quietSpec.Release = quietID
	c, err = CompileWith(quietSpec, quiet, app(t), p, Connections{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range c.Files {
		if strings.HasSuffix(file.Path, "/helmrelease.json") && strings.Contains(string(file.Document), "OTEL_") {
			t.Fatal("a release without telemetry rendered an OTEL variable")
		}
	}
	noCollector := p
	noCollector.Telemetry = Telemetry{}
	if _, err := CompileWith(spec, release, app(t), noCollector, Connections{}, nil); err == nil ||
		!strings.Contains(err.Error(), "unsupported: telemetry") {
		t.Fatalf("a release with telemetry and no collector: %v", err)
	}
	noNotify := p
	noNotify.Notify = Notify{}
	if _, err := CompileWith(spec, release, app(t), noNotify, Connections{}, nil); err == nil ||
		!strings.Contains(err.Error(), "unsupported: notify") {
		t.Fatalf("a profile with no notify service: %v", err)
	}
}

// The app record's ID is not the release's or the instance's: another app name is another app identity.
func TestAppIdentityIsItsOwn(t *testing.T) {
	app := app(t)
	other := app
	other.Application, other.AppID = "other", ""
	id, err := other.ID()
	if err != nil || id == app.AppID {
		t.Fatalf("another app's identity: %s %v", id, err)
	}
	if bytes.Equal(appCanonical(app), appCanonical(other)) {
		t.Fatal("two apps have the same canonical bytes")
	}
}
