package orchestration

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

const execKey = "fixture-team-ingestion-key-0001"
const execURI = "mongodb://user:synthetic-password@db/hyperdx"

func podInputs(t *testing.T) (PlatformAllInputs, *kubetest.Fake, *kubetest.Executor) {
	t.Helper()
	fake, inputs := scopeCluster(t)
	fake.SecretData["logging"] = []kube.Secret{{Name: "clickstack-ingestion-key", Data: map[string][]byte{"CLICKSTACK_INGESTION_KEY": []byte(execKey)}}}
	fake.SecretData[clickstackNamespace] = []kube.Secret{{Name: mongoURISecret, Data: map[string][]byte{"connectionString.standard": []byte(execURI)}}}
	fake.Objects[clickstackRef("Deployment", "clickstack-app")] = []byte(`{"spec":{"selector":{"matchLabels":{"app.kubernetes.io/name":"clickstack-app"}}}}`)
	fake.Collections[clickstackRef("Pod", "")] = [][]byte{[]byte(`{"metadata":{"name":"clickstack-app-ready","namespace":"observability"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"app","ready":true}]}}`)}
	fake.Objects[clickstackRef("PersistentVolumeClaim", "data-volume-clickstack-mongodb-0")] = []byte(`{"spec":{"volumeName":"mongo-data"}}`)
	fake.Objects[observe.Ref{APIVersion: "v1", Kind: "PersistentVolume", Name: "mongo-data"}] = []byte(`{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}`)
	answer := func(output string) kubetest.ExecAnswer {
		return kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: []byte(output)}}
	}
	executor := kubetest.NewExecutor(answer(`RESULT {"keys":["`+execKey+`"]}`), answer(`RESULT {"status":200,"body":{"isTeamExisting":true}}`), answer("9\t9"), answer("0"), answer("0\t"))
	inputs.Executor = executor
	return inputs, fake, executor
}

func TestPodChecksShareReadsAndUseOnlyFixedReadQueries(t *testing.T) {
	inputs, fake, executor := podInputs(t)
	scope := observe.ScopeEvidence{}
	if err := readPodChecks(context.Background(), inputs, &scope); err != nil {
		t.Fatal(err)
	}
	if !scope.Ingestion.Equal || !scope.Registration.TeamExists {
		t.Fatal("healthy facts lost")
	}
	expected := []kube.ExecRequest{
		{Namespace: "observability", Pod: "clickstack-mongodb-0", Container: "mongod", Command: []string{"mongosh", "--quiet", "--nodb", "--norc", "--file", "/dev/stdin"}, Stdin: []byte("db = connect(\"" + execURI + "\");\n" + mongoTeamKeys + "\n")},
		{Namespace: "observability", Pod: "clickstack-app-ready", Container: "app", Command: []string{"node", "--input-type=module", "-"}, Stdin: []byte(installationScript)},
	}
	for _, query := range []string{telemetryQuery, systemLogsQuery, mergesQuery} {
		expected = append(expected, kube.ExecRequest{Namespace: "observability", Pod: "clickstack-clickhouse-clickhouse-0-0-0", Container: "clickhouse-server", Command: []string{"clickhouse-client", "-q", query}})
	}
	if len(executor.Requests) != 5 {
		t.Fatal("exec reads duplicated or skipped")
	}
	for i, want := range expected {
		got := executor.Requests[i]
		if got.Namespace != want.Namespace || got.Pod != want.Pod || got.Container != want.Container || !slices.Equal(got.Command, want.Command) || !bytes.Equal(got.Stdin, want.Stdin) {
			t.Fatalf("exec %d not the fixed read query/destination", i)
		}
		if strings.Contains(strings.Join(got.Command, " "), execURI) {
			t.Fatal("URI in argv")
		}
	}
	if !slices.Equal(fake.Calls, []string{"secrets clickstack-ingestion-key", "secrets " + mongoURISecret}) {
		t.Fatalf("%v", fake.Calls)
	}
	if !slices.Equal(fake.Selectors, []string{"app.kubernetes.io/name=clickstack-app"}) {
		t.Fatalf("%v", fake.Selectors)
	}
	for _, entry := range []observe.PlatformEntry{scope.Retention.Telemetry, scope.Retention.SystemLogs, scope.Retention.Merges} {
		if entry.Level != observe.CheckOK {
			t.Fatalf("%+v", entry)
		}
	}
}

func TestPodCheckFailuresNeverSerializeCredentialMaterial(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(PlatformAllInputs, *kubetest.Fake, *kubetest.Executor)
	}{
		{"collector absent", func(_ PlatformAllInputs, f *kubetest.Fake, _ *kubetest.Executor) { delete(f.SecretData, "logging") }},
		{"invalid base64 or denied", func(_ PlatformAllInputs, f *kubetest.Fake, _ *kubetest.Executor) {
			f.Failing = map[string]error{"secrets clickstack-ingestion-key": &kube.Error{Code: kube.Failed, Subject: "secrets", Reason: observe.ReasonMalformed}}
		}},
		{"mongo secret denied", func(_ PlatformAllInputs, f *kubetest.Fake, _ *kubetest.Executor) {
			f.Failing = map[string]error{"secrets " + mongoURISecret: &kube.Error{Code: kube.Failed, Subject: "secrets", Reason: observe.ReasonForbidden}}
		}},
		{"mismatch", func(_ PlatformAllInputs, f *kubetest.Fake, _ *kubetest.Executor) {
			f.SecretData["logging"][0].Data["CLICKSTACK_INGESTION_KEY"] = []byte("other-key")
		}},
		{"exec failed", func(_ PlatformAllInputs, _ *kubetest.Fake, e *kubetest.Executor) {
			e.Answers[0].Err = errors.New(execURI + execKey)
		}},
		{"exec nonzero", func(_ PlatformAllInputs, _ *kubetest.Fake, e *kubetest.Executor) { e.Answers[0].Result.ExitCode = 17 }},
		{"malformed mongo", func(_ PlatformAllInputs, _ *kubetest.Fake, e *kubetest.Executor) {
			e.Answers[0].Result.Stdout = []byte("RESULT " + execURI + execKey)
		}},
		{"malformed node", func(_ PlatformAllInputs, _ *kubetest.Fake, e *kubetest.Executor) {
			e.Answers[1].Result.Stdout = []byte("RESULT " + execURI + execKey)
		}},
		{"independent telemetry query failure", func(_ PlatformAllInputs, _ *kubetest.Fake, e *kubetest.Executor) {
			e.Answers[2].Err = errors.New(execURI + execKey)
		}},
		{"independent system query failure", func(_ PlatformAllInputs, _ *kubetest.Fake, e *kubetest.Executor) { e.Answers[3].Result.ExitCode = 7 }},
		{"independent merge query failure", func(_ PlatformAllInputs, _ *kubetest.Fake, e *kubetest.Executor) {
			e.Answers[4].Err = errors.New(execURI)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inputs, fake, executor := podInputs(t)
			test.change(inputs, fake, executor)
			report, err := PlatformAll(context.Background(), inputs)
			if err != nil {
				t.Fatal("failed external read stopped report")
			}
			encoded, err := observe.EncodePlatformReport(report)
			if err != nil {
				t.Fatal(err)
			}
			text := observe.PlatformText(report) + string(encoded)
			for _, key := range []string{execKey, execURI, base64.StdEncoding.EncodeToString([]byte(execKey)), "other-key"} {
				if strings.Contains(text, key) {
					t.Fatal("credential in report")
				}
			}
			if test.name == "collector absent" || test.name == "invalid base64 or denied" {
				for _, check := range groupsNamed(report, "runtime-secrets", "ingestion-key") {
					if len(check.Entries) != 1 || check.Entries[0].Level != observe.CheckBad {
						t.Fatal("both ingestion groups must fail")
					}
				}
			}
			if strings.HasPrefix(test.name, "independent") && len(executor.Requests) != 6 {
				t.Fatal("one query failure stopped later reads")
			}
		})
	}
}

func TestRegistrationUnavailableNeverExecutesAnUnreadyPod(t *testing.T) {
	for _, which := range []string{"deployment missing", "deployment denied", "pods missing", "pods denied", "pod terminating", "pod unready"} {
		t.Run(which, func(t *testing.T) {
			inputs, fake, executor := podInputs(t)
			switch which {
			case "deployment missing":
				delete(fake.Objects, clickstackRef("Deployment", "clickstack-app"))
			case "deployment denied":
				delete(fake.Objects, clickstackRef("Deployment", "clickstack-app"))
				fake.Answers.Failed = clickstackRef("Deployment", "clickstack-app")
			case "pods missing":
				fake.Collections[clickstackRef("Pod", "")] = nil
			case "pods denied":
				delete(fake.Collections, clickstackRef("Pod", ""))
				fake.Answers.Pods = clickstackRef("Pod", "")
				fake.Answers.ListReason = observe.ReasonForbidden
			case "pod terminating":
				fake.Collections[clickstackRef("Pod", "")][0] = []byte(`{"metadata":{"name":"terminating","deletionTimestamp":"now"}}`)
			case "pod unready":
				fake.Collections[clickstackRef("Pod", "")][0] = []byte(`{"metadata":{"name":"unready"},"status":{"phase":"Running"}}`)
			}
			scope := observe.ScopeEvidence{}
			if err := readRegistration(context.Background(), inputs, &scope); err != nil {
				t.Fatal(err)
			}
			if len(executor.Requests) != 0 || scope.Registration.Readable {
				t.Fatal("unavailable registration passed or exec guessed pod")
			}
		})
	}
}

func TestClusterScopeNeverUsesExecutorOrData(t *testing.T) {
	_, fake, executor := podInputs(t)
	fake.Failing = map[string]error{"secrets clickstack-ingestion-key": errors.New("must not read")}
	inputs := PlatformInputs{Reader: fake, Now: func() time.Time { return time.Time{} }}
	report, err := Platform(context.Background(), inputs)
	if err != nil || len(report.Checks) != 6 || len(fake.Calls) != 0 || len(executor.Requests) != 0 {
		t.Fatal("cluster scope crossed platform boundary")
	}
}

func TestMongoVolumeMissingAndFailedReadsRemainBad(t *testing.T) {
	for _, which := range []string{"claim absent", "claim denied", "claim unbound", "volume absent", "volume denied", "volume delete"} {
		t.Run(which, func(t *testing.T) {
			inputs, fake, executor := podInputs(t)
			claim := clickstackRef("PersistentVolumeClaim", "data-volume-clickstack-mongodb-0")
			volume := observe.Ref{APIVersion: "v1", Kind: "PersistentVolume", Name: "mongo-data"}
			switch which {
			case "claim absent":
				delete(fake.Objects, claim)
			case "claim denied":
				delete(fake.Objects, claim)
				fake.Answers.Failed = claim
			case "claim unbound":
				fake.Objects[claim] = []byte(`{"spec":{}}`)
			case "volume absent":
				delete(fake.Objects, volume)
			case "volume denied":
				delete(fake.Objects, volume)
				fake.Answers.Failed = volume
			case "volume delete":
				fake.Objects[volume] = []byte(`{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`)
			}
			// Retention on its own: the first three answers are the independent SQL responses.
			executor.Answers = executor.Answers[2:]
			scope := observe.ScopeEvidence{}
			if err := readRetention(context.Background(), inputs, &scope); err != nil {
				t.Fatal(err)
			}
			report := observe.PlatformReport{Checks: observe.PlatformScopeChecks(observe.PlatformEvidence{}, scope)}
			entries := groupsNamed(report, "retention")[0].Entries
			if len(entries) != 4 || entries[3].Level != observe.CheckBad || len(executor.Requests) != 3 {
				t.Fatal("retention failed to report volume independently")
			}
			if strings.HasPrefix(which, "claim") && slices.Contains(fake.Reads, volume) {
				t.Fatal("unbound/unreadable claim guessed volume")
			}
		})
	}
}

// groupsNamed are the report's groups with these names, in the order asked for: a group is found by its name, never
// by its position, so a new group elsewhere in the catalogue cannot silently change what a test reads.
func groupsNamed(report observe.PlatformReport, names ...string) []observe.PlatformCheck {
	found := []observe.PlatformCheck{}
	for _, name := range names {
		for _, check := range report.Checks {
			if check.Name == name {
				found = append(found, check)
			}
		}
	}
	return found
}
