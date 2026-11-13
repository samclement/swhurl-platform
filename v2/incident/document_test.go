package incident

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestVersionOneStateRoundtrip(t *testing.T) {
	raw, err := os.ReadFile("testdata/state-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(string(raw), encoded) {
		t.Fatal("roundtrip changed history or unknown fields")
	}
	if !strings.Contains(encoded, `9007199254740993`) {
		t.Fatal("unknown integer rounded")
	}
	previous := state
	snapshot := testSnapshot(t)
	state, err = Evaluate(state, snapshot, 1791029200, "https://console.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := previous.Encode()
	if !sameJSON(string(raw), after) {
		t.Fatal("evaluation aliased original document")
	}
	_ = state
}
func sameJSON(a, b string) bool {
	decode := func(s string) any {
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		var v any
		_ = d.Decode(&v)
		return v
	}
	return reflect.DeepEqual(decode(a), decode(b))
}
func testSnapshot(t *testing.T) Snapshot {
	t.Helper()
	raw, err := os.ReadFile("testdata/snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func TestInvalidStateNeverResets(t *testing.T) {
	empty := fresh(1)
	raw, _ := empty.Encode()
	for _, bad := range []string{"null", `{}`, `{"version":999}`, raw + `{}`, strings.Replace(raw, `"seen":{}`, `"seen":null`, 1), strings.Replace(raw, `"started":1`, `"started":1e999`, 1), strings.Replace(raw, `"monitor":{}`, `"monitor":{"since":"bad"}`, 1), strings.Replace(raw, `"pending":[]`, `"pending":[{"about":[],"key":"k"}]`, 1)} {
		if state, err := DecodeState(bad); err == nil || state != nil {
			t.Fatalf("invalid state accepted: %s", bad)
		}
	}
	if absent, err := DecodeState(""); err != nil || absent != nil {
		t.Fatal("absence is not invalid state")
	}
}
func TestStateSizeBoundUsesPythonASCIIEncoding(t *testing.T) {
	state := fresh(1)
	state.document["opaque"] = strings.Repeat("😀", MaxStateBytes/12)
	if _, err := state.Encode(); err == nil {
		t.Fatal("surrogate pairs did not count toward size limit")
	}
	state.document["opaque"] = strings.Repeat("<", MaxStateBytes/2)
	encoded, err := state.Encode()
	if err != nil || strings.Contains(encoded, `\u003c`) {
		t.Fatal("HTML escaping changed the Python size contract")
	}
	if _, err := DecodeState(strings.Repeat(" ", MaxStateBytes) + `{}`); err == nil {
		t.Fatal("oversized read accepted")
	}
	for _, value := range []float64{math.NaN(), math.Inf(1)} {
		state.document["opaque"] = value
		if _, err := state.Encode(); err == nil {
			t.Fatal("nonfinite state encoded")
		}
	}
}
func TestHeldDeploymentDiagnostic(t *testing.T) {
	data := testSnapshot(t)
	previous, err := Evaluate(nil, data, 1791028800, "https://console.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	data.Workloads[0]["status"] = Object{"readyReplicas": float64(0)}
	data.Events = []Object{{"metadata": Object{"uid": "event", "creationTimestamp": "2026-10-03T12:00:01Z"}, "involvedObject": Object{"kind": "HelmRelease", "namespace": "example-staging", "name": "example", "uid": "hr1"}, "reason": "UpgradeSucceeded", "reportingComponent": "helm-controller"}}
	state, err := Evaluate(previous, data, 1791028860, "https://console.example.test", false)
	if err != nil || len(state.Held()) != 1 || !strings.Contains(state.Held()[0], "Deployment/example 0/1 Ready") {
		t.Fatalf("held diagnostic: %+v %v", state.Held(), err)
	}
	if len(state.Pending()) != 0 {
		t.Fatal("unready deployment announced")
	}
}
func TestInvalidSnapshotCannotEvaluateHealthy(t *testing.T) {
	data := testSnapshot(t)
	data.Events = []Object{{"lastTimestamp": "invalid"}}
	if _, err := Evaluate(nil, data, 1, "", false); err == nil {
		t.Fatal("malformed event time treated as health")
	}
}

func TestCurrentAndLegacyInstanceRoots(t *testing.T) {
	for _, path := range []string{"./apps/example/staging", "./v2-apps/instances/example/staging"} {
		data := testSnapshot(t)
		object(data.Units[0], "spec")["path"] = path
		state, err := Evaluate(nil, data, 1791028800, "https://console.example.test", true)
		if err != nil || state.Instances() != 1 {
			t.Fatalf("instance path %s not discovered: %v", path, err)
		}
	}
	for _, path := range []string{"./elsewhere/example/staging", "./tests/fixtures/apps/apps/example/staging", "./v2-apps/example/staging", "./v2-apps/instances/example/dev", "./v2-apps/instances/INVALID/prod", "./v2-apps/instances/example/staging/other", "v2-apps/instances/example/staging"} {
		data := testSnapshot(t)
		object(data.Units[0], "spec")["path"] = path
		state, err := Evaluate(nil, data, 1791028800, "https://console.example.test", true)
		if err != nil || state.Instances() != 0 {
			t.Fatalf("unmanaged path %s discovered: %v", path, err)
		}
	}
}

func TestTimestampSpellingAndPlatformFailureSurviveRoundtrip(t *testing.T) {
	state := fresh(1791028800)
	state.enqueue("one", "Title", "Message", false, "", 1791028800, nil)
	state.fault(object(state.document, "monitor"), "fault", "monitor", "detail", Grace, 1791028800, "", nil)
	encoded, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"started":1791028800.0`, `"queued":1791028800.0`, `"since":1791028800.0`} {
		if !strings.Contains(encoded, field) {
			t.Fatalf("Python float spelling lost: %s", encoded)
		}
	}
	if state.Settle(false, 1791028800+History+1) {
		t.Fatal("platform delivery treated as a copy")
	}
	if len(state.Pending()) != 1 || len(object(state.document, "seen")) != 0 {
		t.Fatal("failed platform notice discarded")
	}
}

func TestPersistedNoticeRequiredFields(t *testing.T) {
	valid := Object{"key": "copy@notify", "title": "Title", "message": "Message", "priority": float64(3), "queued": float64(1791028800), "about": []any{"example", "staging"}}
	for _, field := range []string{"key", "title", "message", "priority", "queued"} {
		t.Run(field, func(t *testing.T) {
			entry := Object{}
			for k, v := range valid {
				if k != field {
					entry[k] = v
				}
			}
			state := fresh(1791028801)
			state.document["pending"] = []any{entry}
			raw, err := state.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if decoded, err := DecodeState(raw); err == nil || decoded != nil {
				t.Fatal("incomplete persisted notice accepted")
			}
		})
	}
}

func TestKnownStateFieldTypesRefused(t *testing.T) {
	for _, tc := range []struct {
		name, record, key string
		value             any
	}{
		{"monitor notified", "monitor", "notified", "true"},
		{"monitor notified null", "monitor", "notified", nil},
		{"monitor run", "monitor", "run", false},
		{"pending empty key", "pending", "key", ""},
		{"pending title", "pending", "title", false},
		{"pending message", "pending", "message", nil},
		{"pending priority", "pending", "priority", "3"},
		{"pending queued", "pending", "queued", "1791028800"},
		{"pending history", "pending", "history", "7200"},
		{"pending tags", "pending", "tags", []any{false}},
		{"pending click", "pending", "click", 3},
		{"pending about", "pending", "about", []any{"example", false}},
		{"monitor fault", "monitor", "fault", true},
		{"monitor last notice", "monitor", "last_notice", "1791028800"},
		{"monitor since nonfinite", "monitor", "since", json.Number("1e999")},
		{"notify service notified", "notify_service", "notified", 1},
		{"instance notified", "incident", "notified", "true"},
		{"instance title", "instance", "title", false},
		{"instance unit", "instance", "unit", []any{}},
		{"instance namespace", "instance", "namespace", nil},
		{"instance name", "instance", "name", 3},
		{"instance console", "instance", "console", "false"},
		{"instance retained", "instance", "retained", "true"},
		{"instance uid", "instance", "uid", 3},
		{"instance uninstall", "instance", "uninstalled", true},
		{"instance generation", "instance", "generation", "2"},
		{"instance last active", "instance", "last_active", "1791028800"},
		{"instance failures", "instance", "failed_actions", []any{1}},
		{"instance failures null", "instance", "failed_actions", nil},
		{"instance about", "instance", "about", []any{"example", false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := fresh(1791028801)
			instance := Object{"incident": Object{}}
			object(state.document, "instances")["example-staging/example"] = instance
			pending := Object{"key": "key", "title": "Title", "message": "Message", "priority": float64(3), "queued": float64(1791028800)}
			state.document["pending"] = []any{pending}
			records := map[string]Object{"monitor": object(state.document, "monitor"), "notify_service": {}, "instance": instance, "incident": object(instance, "incident"), "pending": pending}
			state.document["notify_service"] = records["notify_service"]
			records[tc.record][tc.key] = tc.value
			raw, err := state.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if decoded, err := DecodeState(raw); err == nil || decoded != nil {
				t.Fatal("mistyped known field accepted")
			}
		})
	}
}
