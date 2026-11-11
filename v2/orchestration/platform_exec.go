package orchestration

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

const clickstackNamespace = "observability"

// The NATS server's pod, whose local monitoring port answers how much of JetStream's file store is reserved.
const natsNamespace, natsPod = "messaging", "nats-0"
const mongoURISecret = "clickstack-mongodb-hyperdx-hyperdx"
const mongoTeamKeys = `print("RESULT " + JSON.stringify({keys: db.teams.distinct("apiKey").filter(k => typeof k === "string" && k.length > 0)}));`
const installationScript = `const r = await fetch("http://127.0.0.1:8000/installation", {"method": "GET", "headers": {"content-type": "application/json"}, "redirect": "manual"});
let body = null; try { body = await r.json(); } catch {}
console.log("RESULT " + JSON.stringify({status: r.status, body}));
`
const telemetryQuery = "SELECT countIf(position(engine_full, 'toIntervalDay(30)') > 0), count() FROM system.tables WHERE database = 'default' AND engine LIKE '%MergeTree' FORMAT TSV"
const systemLogsQuery = "SELECT count() FROM system.tables WHERE database = 'system' AND name IN ('query_log', 'metric_log', 'asynchronous_metric_log', 'crash_log', 'processors_profile_log', 'part_log', 'trace_log', 'query_thread_log', 'query_views_log', 'opentelemetry_span_log') AND position(engine_full, 'TTL ') = 0 FORMAT TSV"
const mergesQuery = "SELECT count(), any(database || '.' || table) FROM system.part_log WHERE event_type = 'MergeParts' AND error != 0 AND event_time > now() - INTERVAL 1 HOUR FORMAT TSV"

func clickstackRef(kind, name string) observe.Ref {
	version := "v1"
	if kind == "Deployment" {
		version = "apps/v1"
	}
	return observe.Ref{APIVersion: version, Kind: kind, Namespace: clickstackNamespace, Name: name}
}

// readPodChecks shares ingestion reads across both groups; no raw key/URI/stdout/error survives
// the pure comparison. Independent retention queries run even when another query failed.
func readPodChecks(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	readIngestion(ctx, inputs, scope)
	if err := readRegistration(ctx, inputs, scope); err != nil {
		return err
	}
	return readRetention(ctx, inputs, scope)
}

func secretValue(ctx context.Context, data kube.Data, namespace, name, key string) ([]byte, bool) {
	found, err := data.Secrets(ctx, namespace, kube.Selector{Name: name})
	// No parser or transport diagnostic, including a nonstandard port error, may contain credentials.
	if err != nil {
		return nil, true
	}
	if len(found) != 1 {
		return nil, false
	}
	return found[0].Data[key], false
}

func readIngestion(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) {
	collector, collectorFailed := secretValue(ctx, inputs.Data, "logging", "clickstack-ingestion-key", "CLICKSTACK_INGESTION_KEY")
	uri, uriFailed := secretValue(ctx, inputs.Data, clickstackNamespace, mongoURISecret, "connectionString.standard")
	var output []byte
	failed := true
	if !uriFailed && len(uri) > 0 && utf8.Valid(uri) {
		encoded, _ := json.Marshal(string(uri))
		script := append([]byte("db = connect("), encoded...)
		script = append(script, []byte(");\n"+mongoTeamKeys+"\n")...)
		output, failed = podOutput(ctx, inputs.Executor, kube.ExecRequest{Namespace: clickstackNamespace, Pod: "clickstack-mongodb-0", Container: "mongod",
			Command: []string{"mongosh", "--quiet", "--nodb", "--norc", "--file", "/dev/stdin"}, Stdin: script})
	}
	scope.Ingestion = observe.CompareIngestion(collector, collectorFailed, output, failed)
}

func podOutput(ctx context.Context, executor kube.Executor, request kube.ExecRequest) ([]byte, bool) {
	if executor == nil {
		return nil, true
	}
	result, err := executor.Exec(ctx, request)
	if err != nil || result.ExitCode != 0 {
		return nil, true
	}
	return result.Stdout, false
}

func readRegistration(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	deployment, err := inputs.Reader.Read(ctx, clickstackRef("Deployment", "clickstack-app"))
	if err != nil {
		return err
	}
	selector := observe.ClickStackSelector(deployment)
	if selector == "" {
		return nil
	}
	pods, err := inputs.Reader.List(ctx, clickstackRef("Pod", ""), selector)
	if err != nil {
		return err
	}
	pod := observe.ClickStackPod(pods)
	if pod == "" {
		return nil
	}
	output, failed := podOutput(ctx, inputs.Executor, kube.ExecRequest{Namespace: clickstackNamespace, Pod: pod, Container: "app",
		Command: []string{"node", "--input-type=module", "-"}, Stdin: []byte(installationScript)})
	scope.Registration = observe.ParseRegistration(output, failed)
	return nil
}

func readRetention(ctx context.Context, inputs PlatformAllInputs, scope *observe.ScopeEvidence) error {
	for _, query := range []struct {
		text   string
		judge  func([]byte, bool) observe.PlatformEntry
		target *observe.PlatformEntry
	}{
		{telemetryQuery, observe.TelemetryTTL, &scope.Retention.Telemetry},
		{systemLogsQuery, observe.SystemLogTTL, &scope.Retention.SystemLogs},
		{mergesQuery, observe.MergeFailures, &scope.Retention.Merges},
	} {
		output, failed := podOutput(ctx, inputs.Executor, kube.ExecRequest{Namespace: clickstackNamespace, Pod: "clickstack-clickhouse-clickhouse-0-0-0", Container: "clickhouse-server",
			Command: []string{"clickhouse-client", "-q", query.text}})
		*query.target = query.judge(output, failed)
	}
	claim, err := inputs.Reader.Read(ctx, clickstackRef("PersistentVolumeClaim", "data-volume-clickstack-mongodb-0"))
	if err != nil {
		return err
	}
	if name := observe.MongoVolumeName(claim); name != "" {
		scope.Retention.Volume, err = inputs.Reader.Read(ctx, observe.Ref{APIVersion: "v1", Kind: "PersistentVolume", Name: name})
	}
	return err
}
