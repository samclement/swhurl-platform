package observe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// IngestionEvidence contains only presence/equality/error facts, never key or URI bytes.
type IngestionEvidence struct {
	CollectorPresent, CollectorFailed, TeamAvailable, Equal bool
}

// CompareIngestion consumes decoded Secret bytes and mongosh's result without retaining credentials.
// Failed reads stay distinct from absence; invalid/unavailable/nonunique team output never matches.
func CompareIngestion(collector []byte, collectorFailed bool, teamOutput []byte, teamFailed bool) IngestionEvidence {
	e := IngestionEvidence{CollectorPresent: len(collector) > 0, CollectorFailed: collectorFailed}
	var team struct {
		Keys []string `json:"keys"`
	}
	if teamFailed || json.Unmarshal(resultLine(teamOutput), &team) != nil || len(team.Keys) != 1 || team.Keys[0] == "" {
		return e
	}
	e.TeamAvailable = true
	e.Equal = bytes.Equal(collector, []byte(team.Keys[0]))
	return e
}

func resultLine(output []byte) []byte {
	var result []byte
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("RESULT ")) {
			if result != nil {
				return nil
			}
			result = line[len("RESULT "):]
		}
	}
	return result
}

const collectorKey = "logging/clickstack-ingestion-key.CLICKSTACK_INGESTION_KEY"

func platformRuntimeSecrets(e IngestionEvidence) []PlatformEntry {
	if e.CollectorFailed {
		return []PlatformEntry{bad("cannot read " + collectorKey)}
	}
	if !e.CollectorPresent {
		return []PlatformEntry{bad(collectorKey + " is empty (run: make reconcile UNIT=platform-otel)")}
	}
	return []PlatformEntry{ok(collectorKey + " present")}
}

func platformIngestion(e IngestionEvidence) []PlatformEntry {
	// Each group judges its own requirement, as in the original verifier.
	if !e.TeamAvailable {
		return []PlatformEntry{bad("cannot read a unique ClickStack team ingestion key; is MongoDB Running and make clickstack-bootstrap done?")}
	}
	if e.CollectorFailed || !e.CollectorPresent {
		return []PlatformEntry{bad("cannot compare ingestion Secret bytes with ClickStack: collector key unavailable (see Runtime Secrets)")}
	}
	if e.Equal {
		return []PlatformEntry{ok("ingestion Secret bytes match ClickStack; verify collector logs and fresh telemetry after a restart")}
	}
	return []PlatformEntry{bad("CLICKSTACK_INGESTION_KEY does not match the ClickStack team ingestion key. Fix: run make clickstack-bootstrap (writes the Git key into the team); if the two SOPS copies differ, make check-secrets says so")}
}

// RegistrationEvidence reports a strictly boolean installation response, not arbitrary JSON truthiness.
type RegistrationEvidence struct{ Readable, TeamExists bool }

// ParseRegistration discards the raw body (including any API error text).
func ParseRegistration(output []byte, failed bool) RegistrationEvidence {
	var doc struct {
		Status int `json:"status"`
		Body   *struct {
			Team *bool `json:"isTeamExisting"`
		} `json:"body"`
	}
	if failed || json.Unmarshal(resultLine(output), &doc) != nil || doc.Status != 200 || doc.Body == nil || doc.Body.Team == nil {
		return RegistrationEvidence{}
	}
	return RegistrationEvidence{true, *doc.Body.Team}
}

func platformRegistration(e RegistrationEvidence) []PlatformEntry {
	if !e.Readable {
		return []PlatformEntry{bad("cannot ask the HyperDX API whether a team exists (kubectl -n observability logs deploy/clickstack-app)")}
	}
	if e.TeamExists {
		return []PlatformEntry{ok("a team exists, so HyperDX registration is closed")}
	}
	return []PlatformEntry{bad("no team yet: HyperDX registration is open (run: make clickstack-bootstrap)")}
}

// ClickStackSelector reads the installed Deployment's selector, never guesses pod names. Invalid or
// absent selectors fail closed. Both matchLabels and Kubernetes set/existence expressions are supported.
func ClickStackSelector(deployment Read) string {
	var doc struct {
		Spec struct {
			Selector struct {
				Labels      map[string]string `json:"matchLabels"`
				Expressions []struct {
					Key, Operator string
					Values        []string
				} `json:"matchExpressions"`
			} `json:"selector"`
		} `json:"spec"`
	}
	if deployment.Status != Present || json.Unmarshal(deployment.Object, &doc) != nil {
		return ""
	}
	terms := []string{}
	for _, key := range sortedLabelKeys(doc.Spec.Selector.Labels) {
		terms = append(terms, key+"="+doc.Spec.Selector.Labels[key])
	}
	for _, e := range doc.Spec.Selector.Expressions {
		switch e.Operator {
		case "In", "NotIn":
			if len(e.Values) == 0 {
				return ""
			}
			op := "in"
			if e.Operator == "NotIn" {
				op = "notin"
			}
			terms = append(terms, e.Key+" "+op+" ("+strings.Join(e.Values, ",")+")")
		case "Exists":
			terms = append(terms, e.Key)
		case "DoesNotExist":
			terms = append(terms, "!"+e.Key)
		default:
			return ""
		}
	}
	return strings.Join(terms, ",")
}

// ClickStackPod selects the lexically first ready, running, nonterminating app pod. Listing with
// the Deployment selector is the caller's responsibility. No evidence of readiness means no exec.
type clickstackPod struct {
	Metadata struct {
		Name              string
		DeletionTimestamp *string
	}
	Status struct {
		Phase             string
		Conditions        []struct{ Type, Status string }
		ContainerStatuses []struct {
			Name  string
			Ready bool
		}
	}
}

func (p clickstackPod) ready() bool {
	if p.Metadata.DeletionTimestamp != nil || p.Status.Phase != "Running" {
		return false
	}
	ready, app := false, false
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			ready = true
		}
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Name == "app" && c.Ready {
			app = true
		}
	}
	return ready && app
}

func ClickStackPod(pods List) string {
	if pods.Status != Present {
		return ""
	}
	selected := ""
	for _, item := range pods.Items {
		var pod clickstackPod
		if json.Unmarshal(item, &pod) != nil || !pod.ready() {
			continue
		}
		if pod.Metadata.Name != "" && (selected == "" || pod.Metadata.Name < selected) {
			selected = pod.Metadata.Name
		}
	}
	return selected
}

// MongoVolumeName is the bound volume a successfully read PVC states, or empty when it cannot be read.
func MongoVolumeName(claim Read) string {
	var doc struct{ Spec struct{ VolumeName string } }
	if claim.Status != Present || json.Unmarshal(claim.Object, &doc) != nil {
		return ""
	}
	return doc.Spec.VolumeName
}

func retainedMongoVolume(volume Read) PlatformEntry {
	var doc struct {
		Spec struct{ PersistentVolumeReclaimPolicy string }
	}
	if volume.Status == Present && json.Unmarshal(volume.Object, &doc) == nil && doc.Spec.PersistentVolumeReclaimPolicy == "Retain" {
		return ok("ClickStack MongoDB data volume is Retain (outlives its claim)")
	}
	return bad("ClickStack MongoDB data volume is not Retain; its storage class must be local-path-retain")
}

// RetentionEvidence holds already judged, credential-free query entries and volume evidence.
type RetentionEvidence struct {
	Telemetry, SystemLogs, Merges PlatformEntry
	Volume                        Read
}

func platformRetention(e RetentionEvidence) []PlatformEntry {
	return []PlatformEntry{e.Telemetry, e.SystemLogs, e.Merges, retainedMongoVolume(e.Volume)}
}

// TelemetryTTL preserves the thirty-day count comparison, including zero-table and partial TTL failures.
func TelemetryTTL(output []byte, failed bool) PlatformEntry {
	counts, valid := queryCounts(output, 2)
	if failed || !valid {
		return bad("cannot read ClickHouse telemetry table TTLs (kubectl -n observability get pods)")
	}
	with, total := counts[0], counts[1]
	if total > 0 && with == total {
		return ok(fmt.Sprintf("all %d telemetry tables expire after 30 days", total))
	}
	return bad(fmt.Sprintf("%d of %d telemetry tables have no 30-day TTL, so they grow without limit; the collector image default may have changed", total-with, total))
}

// SystemLogTTL deliberately checks TTL presence, as the existing query does, not its duration.
func SystemLogTTL(output []byte, failed bool) PlatformEntry {
	counts, valid := queryCounts(output, 1)
	if failed || !valid {
		return bad("cannot read ClickHouse system log TTLs (kubectl -n observability get pods)")
	}
	if counts[0] == 0 {
		return ok("ClickHouse system logs expire after 7 days")
	}
	return bad(fmt.Sprintf("%d ClickHouse system log table(s) have no TTL; the chart sets them in clickhouse.cluster.spec.settings.extraConfig", counts[0]))
}

// MergeFailures permits only known platform table names in the example; untrusted table text is
// omitted rather than echoed. The count and verdict are unchanged.
func MergeFailures(output []byte, failed bool) PlatformEntry {
	fields := strings.Split(strings.TrimSpace(string(output)), "\t")
	counts, valid := queryCounts([]byte(fields[0]), 1)
	if failed || !valid {
		return bad("cannot read ClickHouse merge results (system.part_log; kubectl -n observability get pods)")
	}
	if counts[0] == 0 {
		return ok("no ClickHouse merge failed in the last hour")
	}
	example := ""
	if len(fields) > 1 && knownMergeTable(fields[1]) {
		example = " (for example " + fields[1] + ")"
	}
	return bad(fmt.Sprintf("%d ClickHouse merge(s) failed in the last hour%s; see docs/operations.md#troubleshooting", counts[0], example))
}

func sortedLabelKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func queryCounts(output []byte, size int) ([]int64, bool) {
	fields := strings.Fields(string(output))
	if len(fields) != size {
		return nil, false
	}
	counts := make([]int64, size)
	for i, field := range fields {
		n, err := strconv.ParseInt(field, 10, 64)
		if err != nil || n < 0 {
			return nil, false
		}
		counts[i] = n
	}
	return counts, true
}

func knownMergeTable(table string) bool {
	switch table {
	case "default.otel_logs", "default.otel_traces", "default.otel_metrics_exponential_histogram",
		"default.otel_metrics_gauge", "default.otel_metrics_histogram", "default.otel_metrics_sum", "default.otel_metrics_summary":
		return true
	}
	database, name, found := strings.Cut(table, ".")
	if !found || database != "system" {
		return false
	}
	switch name {
	case "query_log", "metric_log", "asynchronous_metric_log", "crash_log", "processors_profile_log",
		"part_log", "trace_log", "query_thread_log", "query_views_log", "opentelemetry_span_log":
		return true
	}
	return false
}
