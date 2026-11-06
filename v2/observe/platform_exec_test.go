package observe

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const ingestionFixture = "fixture-team-ingestion-key-0001"
const otherIngestion = "fixture-other-ingestion-key-9999"
const uriFixture = "mongodb://synthetic-user:synthetic-password@db/hyperdx"

func teamOutput(keys any) []byte {
	data, _ := json.Marshal(map[string]any{"keys": keys})
	return append([]byte("Warning: EACCES\nRESULT "), data...)
}

func assertExecReportSafe(t *testing.T, checks []PlatformCheck) {
	t.Helper()
	report := PlatformReport{Checks: checks}
	encoded, err := EncodePlatformReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(encoded), PlatformText(report)} {
		for _, secret := range []string{ingestionFixture, otherIngestion, uriFixture} {
			for i := 0; i < 3; i++ {
				if strings.Contains(text, secret) {
					t.Fatal("credential in serialized report")
				}
				secret = base64.StdEncoding.EncodeToString([]byte(secret))
			}
		}
	}
}

func TestIngestionCasesRetainedFromPython(t *testing.T) {
	for _, test := range []struct {
		name               string
		collector          []byte
		collectorFailed    bool
		output             []byte
		execFailed         bool
		runtime, ingestion CheckLevel
		want               string
	}{
		{"healthy", []byte(ingestionFixture), false, teamOutput([]string{ingestionFixture}), false, CheckOK, CheckOK, "bytes match"},
		{"mismatch", []byte(otherIngestion), false, teamOutput([]string{ingestionFixture}), false, CheckOK, CheckBad, "make clickstack-bootstrap"},
		{"double encoded", []byte(base64.StdEncoding.EncodeToString([]byte(ingestionFixture))), false, teamOutput([]string{ingestionFixture}), false, CheckOK, CheckBad, "does not match"},
		{"newline", []byte(ingestionFixture + "\n"), false, teamOutput([]string{ingestionFixture}), false, CheckOK, CheckBad, "does not match"},
		{"absent", nil, false, teamOutput([]string{ingestionFixture}), false, CheckBad, CheckBad, "collector key unavailable"},
		{"unreadable or invalid base64", nil, true, teamOutput([]string{ingestionFixture}), false, CheckBad, CheckBad, "collector key unavailable"},
		{"team exec failed", []byte(ingestionFixture), false, []byte(uriFixture + ingestionFixture), true, CheckOK, CheckBad, "unique ClickStack"},
		{"no team", []byte(ingestionFixture), false, teamOutput([]string{}), false, CheckOK, CheckBad, "unique ClickStack"},
		{"empty key", []byte(ingestionFixture), false, teamOutput([]string{""}), false, CheckOK, CheckBad, "unique ClickStack"},
		{"two keys", []byte(ingestionFixture), false, teamOutput([]string{ingestionFixture, otherIngestion}), false, CheckOK, CheckBad, "unique ClickStack"},
		{"invalid key type", []byte(ingestionFixture), false, teamOutput([]int{123}), false, CheckOK, CheckBad, "unique ClickStack"},
		{"malformed team", []byte(ingestionFixture), false, []byte("RESULT " + uriFixture), false, CheckOK, CheckBad, "unique ClickStack"},
		{"missing marker", []byte(ingestionFixture), false, []byte(ingestionFixture), false, CheckOK, CheckBad, "unique ClickStack"},
		{"duplicate marker", []byte(ingestionFixture), false, append(teamOutput([]string{ingestionFixture}), append([]byte("\n"), teamOutput([]string{otherIngestion})...)...), false, CheckOK, CheckBad, "unique ClickStack"},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := CompareIngestion(test.collector, test.collectorFailed, test.output, test.execFailed)
			runtime, ingestion := platformRuntimeSecrets(e), platformIngestion(e)
			if len(runtime) != 1 || len(ingestion) != 1 || runtime[0].Level != test.runtime || ingestion[0].Level != test.ingestion || !strings.Contains(ingestion[0].Message, test.want) {
				t.Fatalf("%+v %+v", runtime, ingestion)
			}
			assertExecReportSafe(t, []PlatformCheck{{"runtime-secrets", "Runtime Secrets", runtime}, {"ingestion-key", "Ingestion Key Sync", ingestion}})
		})
	}
}

func TestRegistrationCases(t *testing.T) {
	for _, test := range []struct {
		output string
		failed bool
		level  CheckLevel
		want   string
	}{
		{`RESULT {"status":200,"body":{"isTeamExisting":true}}`, false, CheckOK, "registration is closed"},
		{`RESULT {"status":200,"body":{"isTeamExisting":false}}`, false, CheckBad, "registration is open"},
		{`RESULT {"status":200,"body":{"isTeamExisting":"false"}}`, false, CheckBad, "cannot ask"},
		{`RESULT {"status":200,"body":null}`, false, CheckBad, "cannot ask"},
		{`RESULT {"status":500,"body":{"isTeamExisting":true}}`, false, CheckBad, "cannot ask"},
		{`RESULT {"status":200,"body":{}}`, false, CheckBad, "cannot ask"},
		{"RESULT " + uriFixture + ingestionFixture, false, CheckBad, "cannot ask"},
		{"RESULT " + uriFixture + ingestionFixture, true, CheckBad, "cannot ask"},
		{"", false, CheckBad, "cannot ask"},
	} {
		t.Run(test.want+test.output[:min(len(test.output), 30)], func(t *testing.T) {
			entries := platformRegistration(ParseRegistration([]byte(test.output), test.failed))
			if len(entries) != 1 || entries[0].Level != test.level || !strings.Contains(entries[0].Message, test.want) {
				t.Fatalf("%+v", entries)
			}
			assertExecReportSafe(t, []PlatformCheck{{"registration", "ClickStack Sign-up", entries}})
		})
	}
}

func TestRetentionQueryCases(t *testing.T) {
	for _, test := range []struct {
		name, output string
		failed       bool
		judge        func([]byte, bool) PlatformEntry
		level        CheckLevel
		want         string
	}{
		{"telemetry healthy", "9\t9\n", false, TelemetryTTL, CheckOK, "all 9"},
		{"partial TTL", "8\t9", false, TelemetryTTL, CheckBad, "1 of 9"},
		{"zero tables", "0\t0", false, TelemetryTTL, CheckBad, "0 of 0"},
		{"telemetry failed", ingestionFixture, true, TelemetryTTL, CheckBad, "cannot read"},
		{"invalid count", uriFixture, false, TelemetryTTL, CheckBad, "cannot read"},
		{"partial count", "9", false, TelemetryTTL, CheckBad, "cannot read"},
		{"extra count", "9 9 9", false, TelemetryTTL, CheckBad, "cannot read"},
		{"negative count", "-1 9", false, TelemetryTTL, CheckBad, "cannot read"},
		{"system healthy", "0\n", false, SystemLogTTL, CheckOK, "expire after 7 days"},
		{"missing TTL", "3", false, SystemLogTTL, CheckBad, "3 ClickHouse system"},
		{"system failed", uriFixture, true, SystemLogTTL, CheckBad, "cannot read"},
		{"system invalid", ingestionFixture, false, SystemLogTTL, CheckBad, "cannot read"},
		{"merge healthy", "0\t\n", false, MergeFailures, CheckOK, "no ClickHouse merge"},
		{"merge failed", "3765\tsystem.metric_log\n", false, MergeFailures, CheckBad, "3765 ClickHouse merge(s) failed in the last hour (for example system.metric_log)"},
		{"merge telemetry", "28\tdefault.otel_metrics_histogram", false, MergeFailures, CheckBad, "for example default.otel_metrics_histogram"},
		{"merge no example", "2", false, MergeFailures, CheckBad, "2 ClickHouse merge(s)"},
		{"merge unsafe example", "2\t" + ingestionFixture, false, MergeFailures, CheckBad, "2 ClickHouse merge(s) failed in the last hour;"},
		{"merge exec failed", uriFixture, true, MergeFailures, CheckBad, "cannot read"},
		{"merge invalid", ingestionFixture, false, MergeFailures, CheckBad, "cannot read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := test.judge([]byte(test.output), test.failed)
			if entry.Level != test.level || !strings.Contains(entry.Message, test.want) {
				t.Fatalf("%+v", entry)
			}
			assertExecReportSafe(t, []PlatformCheck{{"retention", "Retention", []PlatformEntry{entry}}})
		})
	}
}

func TestMongoVolumeRetentionCases(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		doc    string
		level  CheckLevel
	}{
		{"retain", Present, `{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}`, CheckOK},
		{"delete", Present, `{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`, CheckBad},
		{"missing", Absent, "", CheckBad}, {"failed", Unavailable, uriFixture, CheckBad},
		{"missing spec", Present, `{}`, CheckBad}, {"malformed", Present, ingestionFixture, CheckBad},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := retainedMongoVolume(Read{Status: test.status, Object: []byte(test.doc)})
			if entry.Level != test.level {
				t.Fatalf("%+v", entry)
			}
		})
	}
	claim := Read{Status: Present, Object: []byte(`{"spec":{"volumeName":"pv-1"}}`)}
	if MongoVolumeName(claim) != "pv-1" || MongoVolumeName(Read{Status: Unavailable, Object: claim.Object}) != "" {
		t.Fatal("claim absence/failure lost")
	}
}

func TestReadyClickStackPodSelection(t *testing.T) {
	pod := func(name, phase, conditions, statuses, extra string) []byte {
		return []byte(fmt.Sprintf(`{"metadata":{"name":%q%s},"status":{"phase":%q,"conditions":%s,"containerStatuses":%s}}`, name, extra, phase, conditions, statuses))
	}
	ready := `[{"type":"Ready","status":"True"}]`
	app := `[{"name":"app","ready":true}]`
	pods := List{Status: Present, Items: [][]byte{
		pod("z-ready", "Running", ready, app, ""), pod("b-ready", "Running", ready, app, ""),
		pod("a-terminating", "Running", ready, app, `,"deletionTimestamp":"2026-11-06T00:00:00Z"`),
		pod("a-unready", "Running", `[]`, app, ""), pod("a-no-app", "Running", ready, `[]`, ""), pod("a-pending", "Pending", ready, app, ""),
	}}
	if ClickStackPod(pods) != "b-ready" {
		t.Fatal("wrong ready pod")
	}
	pods.Status = Unavailable
	if ClickStackPod(pods) != "" {
		t.Fatal("failed list selected pod")
	}
	dep := Read{Status: Present, Object: []byte(`{"spec":{"selector":{"matchLabels":{"z":"2","a":"1"}}}}`)}
	if ClickStackSelector(dep) != "a=1,z=2" {
		t.Fatal("selector not installed/deterministic")
	}
}
