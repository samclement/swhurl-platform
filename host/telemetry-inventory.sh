#!/usr/bin/env bash
# What telemetry ClickStack holds, by source, read from ClickHouse inside its pod (read-only queries, no Secret read):
#
#   host/telemetry-inventory.sh [hours]    the last <hours> hours (default 24)
#
# The baseline and the measure of every slice of docs/platform-telemetry-status.md: run it before and after a change
# and compare, rather than querying by hand. Metric rows per hour are the ClickHouse cost a new scrape adds.
set -Eeuo pipefail

hours=${1:-24}
[[ $hours =~ ^[1-9][0-9]{0,2}$ ]] || { echo "[ERROR] hours must be 1 to 999" >&2; exit 2; }
export KUBECONFIG=${KUBECONFIG:-$HOME/.kube/config}

pod=$(kubectl -n observability get pods -l clickhouse.com/role=clickhouse-server -o name | head -n 1)
[[ -n $pod ]] || { echo "[ERROR] no ClickHouse server pod in observability" >&2; exit 1; }

query() {  # <title> <sql>
  echo "== $1"
  kubectl -n observability exec "$pod" -c clickhouse-server -- clickhouse-client --format PrettyCompactMonoBlock -q "$2"
}

since="now() - INTERVAL $hours HOUR"
ns="ResourceAttributes['k8s.namespace.name']"
platform="$ns NOT LIKE '%-staging' AND $ns NOT LIKE '%-prod'"

query "Platform log lines by namespace and container (${hours}h)" "
  SELECT $ns AS namespace, ResourceAttributes['k8s.container.name'] AS container,
         LogAttributes['log.parser'] AS parser, count() AS lines
  FROM default.otel_logs WHERE Timestamp > $since AND $platform
  GROUP BY namespace, container, parser ORDER BY namespace, lines DESC"

query "Spans by namespace and service (${hours}h)" "
  SELECT $ns AS namespace, ServiceName AS service, count() AS spans
  FROM default.otel_traces WHERE Timestamp > $since
  GROUP BY namespace, service ORDER BY namespace, spans DESC"

query "Platform metric names beyond container and pod metrics (${hours}h)" "
  SELECT $ns AS namespace, ServiceName AS service, groupUniqArray(10)(MetricName) AS names, uniqExact(MetricName) AS n
  FROM merge('default', '^otel_metrics_') WHERE TimeUnix > $since AND $platform
    AND MetricName NOT LIKE 'k8s.%' AND MetricName NOT LIKE 'container.%' AND MetricName NOT LIKE 'system.%'
  GROUP BY namespace, service ORDER BY namespace"

query "Rows per hour by table (${hours}h)" "
  SELECT 'logs' AS signal, round(count() / $hours) AS rows_per_hour FROM default.otel_logs WHERE Timestamp > $since
  UNION ALL SELECT 'traces', round(count() / $hours) FROM default.otel_traces WHERE Timestamp > $since
  UNION ALL SELECT 'metrics', round(count() / $hours) FROM merge('default', '^otel_metrics_') WHERE TimeUnix > $since"
