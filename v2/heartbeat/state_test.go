package heartbeat_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samclement/swhurl-platform/v2/heartbeat"
)

const first = "console/console-notifications"

// python is a state file Python's save_state wrote (testdata/README.md says how).
func python(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Each file Python wrote reads as the state it was given and encodes back to the same bytes.
func TestPythonFilesRoundTripByteForByte(t *testing.T) {
	for _, tt := range []struct {
		file string
		want heartbeat.State
	}{
		{"python-alerting.json", heartbeat.State{first: {Alerting: true, Since: now, LastAlert: now + 3600.25}}},
		{"python-cleared.json", heartbeat.State{first: {}}},
		{"python-others.json", heartbeat.State{first: {}, "a/b": {}, "reports/sync": alerting}},
	} {
		document := python(t, tt.file)
		state, err := heartbeat.DecodeState(document)
		if err != nil || !equal(state, tt.want) {
			t.Fatalf("%s: %+v %v", tt.file, state, err)
		}
		if encoded := state.Encode(); !bytes.Equal(encoded, document) {
			t.Fatalf("%s encoded as %s", tt.file, encoded)
		}
	}
}

func TestStateWrittenBeforeThereWereOtherJobsStillReads(t *testing.T) {
	state, err := heartbeat.DecodeState([]byte(`{"alerting_since":12,"last_alert":13}` + "\n"))
	if err != nil || !equal(state, heartbeat.State{first: {Alerting: true, Since: 12, LastAlert: 13}}) {
		t.Fatalf("%+v %v", state, err)
	}
	for _, bad := range []string{
		`{"alerting_since":12,"last_alert":13,"others":[]}`,
		`{"alerting_since":null,"last_alert":null,"others":{"a/b":{"alerting_since":1}}}`,
		`{"alerting_since":null,"last_alert":null,"others":{"console/console-notifications":{"alerting_since":null,"last_alert":null}}}`,
	} {
		if _, err := heartbeat.DecodeState([]byte(bad)); !errors.Is(err, heartbeat.ErrInvalid) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

// Python's rules: exactly the two fields, both null or both finite numbers, integers allowed and booleans not; others
// an object. A document that is not JSON cannot be read.
func TestInvalidStateIsRefusedNeverReset(t *testing.T) {
	for _, bad := range []string{
		`null`, `[]`, `"x"`, `{}`, `{"alerting_since":null}`, `{"alerting_since":null,"last_alert":null,"extra":1}`,
		`{"alerting_since":1,"last_alert":null}`, `{"alerting_since":null,"last_alert":1}`,
		`{"alerting_since":true,"last_alert":1}`, `{"alerting_since":"1","last_alert":1}`,
		`{"alerting_since":1e400,"last_alert":1}`, `{"alerting_since":1,"last_alert":-1e999}`,
		`{"alerting_since":null,"last_alert":null,"others":null}`,
		`{"alerting_since":null,"last_alert":null,"others":{"a/b":null}}`,
		`{"alerting_since":null,"last_alert":null,"others":{"a/b":[]}}`,
	} {
		if _, err := heartbeat.DecodeState([]byte(bad)); !errors.Is(err, heartbeat.ErrInvalid) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	for _, unreadable := range []string{``, `{`, `{"alerting_since":NaN,"last_alert":1}`, "\xff"} {
		if _, err := heartbeat.DecodeState([]byte(unreadable)); !errors.Is(err, heartbeat.ErrUnreadable) {
			t.Fatalf("%q: %v", unreadable, err)
		}
	}
	state, err := heartbeat.DecodeState([]byte(` { "last_alert" : -2.5e3 , "alerting_since" : 0 } `))
	if err != nil || !equal(state, heartbeat.State{first: {Alerting: true, Since: 0, LastAlert: -2500}}) {
		t.Fatalf("spacing and order: %+v %v", state, err)
	}
}

// What Go writes is what Python's json.dump(separators=(',', ':')) writes for the same values: an empty state is the
// first job's empty flag, other jobs follow in sorted order, a time is a Python float and a key is ASCII-escaped.
func TestEncodingIsPythons(t *testing.T) {
	for _, tt := range []struct {
		state heartbeat.State
		want  string
	}{
		{heartbeat.State{}, `{"alerting_since":null,"last_alert":null}`},
		{heartbeat.State{first: alerting}, `{"alerting_since":1791028800.0,"last_alert":1791028800.0}`},
		{heartbeat.State{first: {Alerting: true, Since: 12, LastAlert: 1791028800.123456}},
			`{"alerting_since":12.0,"last_alert":1791028800.123456}`},
		{heartbeat.State{first: {Alerting: true, Since: 1e16, LastAlert: 0.00001}},
			`{"alerting_since":1e+16,"last_alert":1e-05}`},
		{heartbeat.State{first: {Alerting: true, Since: 1e15, LastAlert: 0.0001}},
			`{"alerting_since":1000000000000000.0,"last_alert":0.0001}`},
		{heartbeat.State{"z/z": {}, "reports/sync": alerting},
			`{"alerting_since":null,"last_alert":null,"others":{"reports/sync":{"alerting_since":1791028800.0,"last_alert":1791028800.0},"z/z":{"alerting_since":null,"last_alert":null}}}`},
		{heartbeat.State{"q\"\\\n<é😀\x7f": {}},
			`{"alerting_since":null,"last_alert":null,"others":{"q\"\\\n<\u00e9\ud83d\ude00\u007f":{"alerting_since":null,"last_alert":null}}}`},
	} {
		if got := string(tt.state.Encode()); got != tt.want+"\n" {
			t.Fatalf("got  %s\nwant %s", got, tt.want)
		}
	}
}

// A state survives its own encoding.
func TestEncodedStateDecodes(t *testing.T) {
	state := heartbeat.State{first: alerting, "reports/sync": {Alerting: true, Since: 1.5, LastAlert: 2}, "a/b": {}}
	decoded, err := heartbeat.DecodeState(state.Encode())
	if err != nil || !equal(decoded, state) {
		t.Fatalf("%+v %v", decoded, err)
	}
}

func equal(a, b heartbeat.State) bool {
	if len(a) != len(b) {
		return false
	}
	for key, flag := range a {
		if other, has := b[key]; !has || other != flag {
			return false
		}
	}
	return true
}
