"""Collect step: fixed telemetry queries, the pre-filter and the redacted bundle.

Reads state and never writes it. Writes ``report.json`` always and ``bundle.json``
only when one finding fires and the budget allows a model call (docs/plan.md
section 14, "Handoff files"). No model, Secret or Kubernetes write is reachable from here.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import logging
import os
import time
from collections.abc import Iterable
from pathlib import Path
from typing import Any, Protocol

import httpx

from swhurl.run import CommandError, Runner

from . import (
    MAX_QUERY_ROWS,
    MAX_TEXT,
    allowlist,
    evidence_bundle,
    fingerprint,
    prefilter,
    query_result_settings,
    read_bounded_response,
    state,
)
from .allowlist import Allowlist, App
from .errors import PolicyError, ReviewFailure

log = logging.getLogger(__name__)

CLICKHOUSE_URL = 'http://clickstack-clickhouse-clickhouse-headless.observability.svc.cluster.local:8123'
RELEASES = ['kubectl', 'get', 'helmreleases.helm.toolkit.fluxcd.io', '--all-namespaces', '-o', 'json']
SEVERITY_RANK = {'high': 0, 'default': 1, 'low': 2}
STACK_FRAMES = 10
FRAME_TEXT = 300

SCOPE = ("ResourceAttributes['k8s.namespace.name'] = {namespace:String}"
         " AND {column} >= toDateTime({start:UInt32}) AND {column} < toDateTime({end:UInt32})")
LOG_SCOPE = SCOPE.replace('{column}', 'Timestamp')
METRIC_SCOPE = SCOPE.replace('{column}', 'TimeUnix')
ERROR_LOG = "(lower(SeverityText) IN ('error', 'fatal') OR SeverityNumber >= 17)"
EXCEPTION_TYPE = "if(LogAttributes['exception.type'] != '', LogAttributes['exception.type'], LogAttributes['err.type'])"
EXCEPTION_STACK = ("if(LogAttributes['exception.stacktrace'] != '', LogAttributes['exception.stacktrace'],"
                   " LogAttributes['err.stack'])")
LOG_PATH = "if(LogAttributes['url.path'] != '', LogAttributes['url.path'], LogAttributes['path'])"
UNEXPECTED = "ServiceName != '' AND ServiceName != {expected:String}"

# Every caller value is a query parameter; nothing below is ever formatted with one.
COUNTS = {
    'error-logs': [
        f"SELECT ServiceName AS service, {EXCEPTION_TYPE} AS exception_type,"
        " if(exception_type = '', replaceRegexpAll(substring(Body, 1, 80), '[0-9a-fA-F]{4,}|[0-9]+', '#'), '')"
        " AS body_prefix, toUInt32(count()) AS value"
        f" FROM default.otel_logs WHERE {LOG_SCOPE} AND {ERROR_LOG}"
        f" GROUP BY service, exception_type, body_prefix ORDER BY value DESC, service, exception_type, body_prefix"
        f" LIMIT {MAX_QUERY_ROWS}"],
    'error-spans': [
        "SELECT ServiceName AS service, SpanName AS span, toUInt32(count()) AS value"
        f" FROM default.otel_traces WHERE {LOG_SCOPE} AND StatusCode = 'Error'"
        f" GROUP BY service, span ORDER BY value DESC, service, span LIMIT {MAX_QUERY_ROWS}"],
    'unexpected-service': [
        f"SELECT ServiceName AS service, toUInt32(count()) AS value FROM default.{table}"
        f" WHERE {LOG_SCOPE} AND {UNEXPECTED} GROUP BY service ORDER BY value DESC, service LIMIT {MAX_QUERY_ROWS}"
        for table in ('otel_logs', 'otel_traces')],
    'missing-telemetry': [
        "SELECT toUInt32(count()) AS value FROM default.otel_metrics_sum"
        f" WHERE {METRIC_SCOPE} AND ServiceName = {{expected:String}}"],
}
LOG_FIELDS = ('timestamp', 'level', 'service', 'message', 'exception_type', 'stack', 'path')
TRACE_FIELDS = ('timestamp', 'trace_id', 'span_id', 'service', 'span', 'status', 'http_status', 'route')
LOG_SAMPLE = (
    "SELECT toString(Timestamp) AS timestamp, SeverityText AS level, ServiceName AS service,"
    f" substring(Body, 1, {MAX_TEXT}) AS message, {EXCEPTION_TYPE} AS exception_type,"
    f" substring({EXCEPTION_STACK}, 1, {STACK_FRAMES * FRAME_TEXT}) AS stack, {LOG_PATH} AS path"
    f" FROM default.otel_logs WHERE {LOG_SCOPE} AND {{predicate}} ORDER BY Timestamp DESC LIMIT {MAX_QUERY_ROWS}")
LOG_PREDICATE = {'error-logs': ERROR_LOG, 'error-spans': ERROR_LOG, 'unexpected-service': UNEXPECTED,
                 'missing-telemetry': "ServiceName != ''"}
TRACE_SAMPLE = (
    "SELECT toString(Timestamp) AS timestamp, TraceId AS trace_id, SpanId AS span_id, ServiceName AS service,"
    " SpanName AS span, StatusCode AS status, SpanAttributes['http.response.status_code'] AS http_status,"
    " SpanAttributes['http.route'] AS route"
    f" FROM default.otel_traces WHERE {LOG_SCOPE} AND {{predicate}} ORDER BY Timestamp DESC LIMIT {MAX_QUERY_ROWS}")
TRACE_PREDICATE = {'error-logs': "StatusCode = 'Error'", 'error-spans': "StatusCode = 'Error'",
                   'unexpected-service': UNEXPECTED, 'missing-telemetry': "StatusCode = 'Error'"}


class Telemetry(Protocol):
    """Run one fixed query with parameters and return at most ``MAX_QUERY_ROWS`` rows."""

    def query(self, sql: str, params: dict[str, str | int]) -> list[dict[str, Any]]: ...


class ClickHouseHTTP:
    """ClickHouse's HTTP interface with server-side limits and a hard client byte cap."""

    def __init__(self, url: str, user: str, password: str, *, timeout: float = 30,
                 transport: httpx.BaseTransport | None = None):
        self.url, self.user, self.password, self.timeout, self.transport = url, user, password, timeout, transport

    def query(self, sql: str, params: dict[str, str | int]) -> list[dict[str, Any]]:
        settings = {'database': 'default', 'default_format': 'JSONEachRow', 'max_execution_time': 20,
                    'result_overflow_mode': 'throw', **query_result_settings(),
                    **{f'param_{name}': value for name, value in params.items()}}
        try:
            with (httpx.Client(transport=self.transport, timeout=self.timeout) as client,
                  client.stream('POST', self.url, params=settings, content=sql.encode(),
                                headers={'X-ClickHouse-User': self.user,
                                         'X-ClickHouse-Key': self.password}) as response):
                if response.status_code != 200:
                    raise ReviewFailure('query')
                body = read_bounded_response(response.iter_bytes())
            rows = [json.loads(line) for line in body.decode().splitlines() if line.strip()]
        except (httpx.HTTPError, PolicyError, UnicodeDecodeError, json.JSONDecodeError):
            # HTTP errors and ClickHouse exception text can quote the query or its values.
            raise ReviewFailure('query') from None
        if len(rows) > MAX_QUERY_ROWS or not all(isinstance(row, dict) for row in rows):
            raise ReviewFailure('query')
        return rows


def query_names() -> dict[str, str]:
    """Every query the collector can send, by a stable name (fixtures and tests key on these)."""
    names = {}
    for signal, queries in COUNTS.items():
        for index, sql in enumerate(queries):
            names[sql] = signal if len(queries) == 1 else f'{signal}:{index}'
    for signal in COUNTS:
        names.setdefault(LOG_SAMPLE.replace('{predicate}', LOG_PREDICATE[signal]), f'log-sample:{signal}')
        names.setdefault(TRACE_SAMPLE.replace('{predicate}', TRACE_PREDICATE[signal]), f'trace-sample:{signal}')
    return names


class FixtureTelemetry:
    """Answers the fixed queries from checked-in rows; any other query is refused."""

    def __init__(self, rows: dict[str, list[dict[str, Any]]]):
        self.rows, self.calls, self.names = rows, [], query_names()

    def query(self, sql: str, params: dict[str, str | int]) -> list[dict[str, Any]]:
        if sql not in self.names:
            raise ReviewFailure('query')
        self.calls.append((self.names[sql], dict(params)))
        return [dict(row) for row in self.rows.get(self.names[sql], [])]


def window(now: float) -> tuple[int, int]:
    """The previous full UTC hour as epoch seconds."""
    end = int(now) - int(now) % 3600
    return end - 3600, end


def iso(epoch: float) -> str:
    return dt.datetime.fromtimestamp(epoch, dt.UTC).isoformat()


def _count(row: dict[str, Any]) -> int:
    value = row.get('value')
    if isinstance(value, str) and value.isdigit():
        value = int(value)
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ReviewFailure('query')
    return value


def _text(row: dict[str, Any], name: str) -> str:
    value = row.get(name)
    if not isinstance(value, str):
        raise ReviewFailure('query')
    return value


def signal_counts(telemetry: Telemetry, signal: str, params: dict[str, str | int]) -> dict[str, int]:
    """Failure counts by fingerprint key (for ``missing-telemetry``: healthy points under ``no-metrics``)."""
    counts: dict[str, int] = {}
    for sql in COUNTS[signal]:
        for row in telemetry.query(sql, params):
            if signal == 'missing-telemetry':
                key = 'no-metrics'
            elif signal == 'error-logs':
                key = f"{_text(row, 'service')} {_text(row, 'exception_type') or _text(row, 'body_prefix')}".strip()
            elif signal == 'error-spans':
                key = f"{_text(row, 'service')} {_text(row, 'span')}".strip()
            else:
                key = _text(row, 'service')
            if not key or len(key) > 300:
                raise ReviewFailure('query')
            counts[key] = counts.get(key, 0) + _count(row)
    if signal == 'missing-telemetry':
        counts.setdefault('no-metrics', 0)
    return counts


def approved_log(row: dict[str, Any]) -> dict[str, Any]:
    """Reduce one sample row to the fields approved in Stage 0, or refuse it."""
    if not isinstance(row, dict) or set(row) != set(LOG_FIELDS) or not all(isinstance(v, str) for v in row.values()):
        raise ReviewFailure('redaction')
    path = row['path'].split('?', 1)[0].split('#', 1)[0]
    return {'timestamp': row['timestamp'][:40], 'level': row['level'][:20], 'service': row['service'][:200],
            'message': row['message'][:MAX_TEXT], 'exception_type': row['exception_type'][:200],
            'stack': [line.strip()[:FRAME_TEXT] for line in row['stack'].splitlines() if line.strip()][:STACK_FRAMES],
            'path': path[:300] if path.startswith('/') else ''}


def approved_trace(row: dict[str, Any]) -> dict[str, Any]:
    if not isinstance(row, dict) or set(row) != set(TRACE_FIELDS) or not all(isinstance(v, str) for v in row.values()):
        raise ReviewFailure('redaction')
    return {name: row[name][:200] for name in TRACE_FIELDS}


def release_images(runner: Runner) -> dict[tuple[str, str], dict[str, str]]:
    """Image tag and digest of every HelmRelease, by (namespace, name)."""
    try:
        doc = runner.json(RELEASES)
        found = {}
        for item in doc['items']:
            image = (((((item.get('spec') or {}).get('values') or {}).get('controllers') or {}).get('main') or {})
                     .get('containers') or {}).get('main', {}).get('image') or {}
            found[(item['metadata']['namespace'], item['metadata']['name'])] = {
                'tag': str(image.get('tag') or '')[:200], 'digest': str(image.get('digest') or '')[:200]}
        return found
    except (CommandError, AttributeError, KeyError, TypeError, ValueError):
        raise ReviewFailure('query') from None


def findings_for(app: App, env: str, telemetry: Telemetry, current: dict, allow: Allowlist,
                 params: dict[str, str | int], now: float) -> tuple[list[dict], dict[str, int]]:
    findings, totals = [], {}
    for signal in app.signals:
        counts = signal_counts(telemetry, signal, params)
        baseline_key = f'{app.app}/{env}/{signal}'
        totals[baseline_key] = sum(counts.values())
        mean = state.baseline_mean(current, baseline_key)
        for key, count in counts.items():
            finding = {'app': app.app, 'env': env, 'signal': signal, 'key': key, 'count': count,
                       'baseline_mean': round(mean, 2),
                       'coverage': 'covered' if allow.covered(app.app, signal) else 'alert-gap'}
            finding['fingerprint'] = fingerprint({'repository': app.repository, **finding})
            decision = prefilter(finding, current, allow.defaults, now)
            finding.update(decision='fired' if decision['fire'] else 'quiet', reason=decision['reason'])
            findings.append(finding)
    return findings, totals


def build_bundle(app: App, finding: dict, telemetry: Telemetry, params: dict[str, str | int],
                 image: dict[str, str], peers: Iterable[dict], span: tuple[int, int]) -> dict:
    signal = finding['signal']
    logs = [approved_log(row) for row in
            telemetry.query(LOG_SAMPLE.replace('{predicate}', LOG_PREDICATE[signal]), params)]
    traces = [approved_trace(row) for row in
              telemetry.query(TRACE_SAMPLE.replace('{predicate}', TRACE_PREDICATE[signal]), params)]
    metrics = [{'signal': peer['signal'], 'key': peer['key'], 'count': peer['count'],
                'baseline_mean': peer['baseline_mean']} for peer in peers][:MAX_QUERY_ROWS]
    raw = {'version': 1, 'repository': app.repository, 'app': app.app, 'env': finding['env'], 'signal': signal,
           'key': finding['key'], 'window': {'start': iso(span[0]), 'end': iso(span[1])}, 'image': image,
           'logs': logs, 'metrics': metrics, 'traces': traces, 'links': []}
    try:
        bundle = evidence_bundle(raw)
    except PolicyError:
        raise ReviewFailure('redaction') from None
    if bundle['fingerprint'] != finding['fingerprint']:
        raise ReviewFailure('contract')
    return bundle


def collect(*, allow: Allowlist, current: dict, telemetry: Telemetry,
            images: dict[tuple[str, str], dict[str, str]], now: float, trigger: str = 'sweep') -> tuple[dict, dict | None]:
    """One sweep: the report and, when exactly one finding is chosen for analysis, its bundle."""
    span = window(now)
    report: dict[str, Any] = {'version': 1, 'status': 'quiet', 'reason': None, 'budget': None, 'trigger': trigger,
                              'window': {'start': iso(span[0]), 'end': iso(span[1])}, 'findings': [], 'counts': {}}
    scoped = []
    for app in allow.apps:
        for env in app.environments:
            params = {'namespace': app.namespace(env), 'start': span[0], 'end': span[1],
                      'expected': app.expected_service}
            findings, totals = findings_for(app, env, telemetry, current, allow, params, now)
            report['findings'] += findings
            report['counts'].update(totals)
            scoped += [(app, params, finding) for finding in findings if finding['decision'] == 'fired']
    if not scoped:
        return report, None
    block = state.budget_block(current, allow.defaults, now)
    if block:
        report.update(status='budget', budget=block)
        return report, None
    scoped.sort(key=lambda item: (SEVERITY_RANK[item[0].severity[item[2]['signal']]], -item[2]['count'],
                                  item[0].signals.index(item[2]['signal']), item[2]['fingerprint']))
    app, params, chosen = scoped[0]
    for _, _, other in scoped[1:]:
        other['decision'] = 'fired-deferred'
    peers = [f for f in report['findings'] if (f['app'], f['env']) == (chosen['app'], chosen['env'])]
    image = images.get((app.namespace(chosen['env']), app.app), {'tag': '', 'digest': ''})
    bundle = build_bundle(app, chosen, telemetry, params, image, peers, span)
    report['status'] = 'fired'
    return report, bundle


def failed_report(reason: str, now: float, trigger: str = 'sweep') -> dict:
    span = window(now)
    return {'version': 1, 'status': 'failed', 'reason': reason, 'budget': None, 'trigger': trigger,
            'window': {'start': iso(span[0]), 'end': iso(span[1])}, 'findings': [], 'counts': {}}


def write_handoff(work: Path, report: dict, bundle: dict | None) -> None:
    """Write the report, and the bundle only when there is one; a stale bundle is removed first."""
    target = work / 'bundle' / 'bundle.json'
    target.unlink(missing_ok=True)
    if bundle is not None:
        target.write_text(json.dumps(bundle, separators=(',', ':'), ensure_ascii=False))
    (work / 'collect' / 'report.json').write_text(json.dumps(report, separators=(',', ':')))


def main(argv: list[str] | None = None, runner: Runner | None = None, *,
         telemetry: Telemetry | None = None, now: float | None = None) -> int:
    parser = argparse.ArgumentParser(description='Incident review collect step (runs in the reviewer pod)')
    parser.add_argument('--work', type=Path, default=Path('/work'))
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format='%(levelname)s %(name)s %(message)s')
    runner = runner or Runner.from_environment()
    now = now if now is not None else time.time()
    try:
        allow = allowlist.current()
        if telemetry is None:
            password = os.environ.get('CLICKHOUSE_PASSWORD', '')
            runner.add_secret(password)
            if not password or not os.environ.get('CLICKHOUSE_USER'):
                raise ReviewFailure('query')
            telemetry = ClickHouseHTTP(os.environ.get('CLICKHOUSE_URL', CLICKHOUSE_URL),
                                       os.environ['CLICKHOUSE_USER'], password)
        current = state.read_state(runner)
        report, bundle = collect(allow=allow, current=current, telemetry=telemetry,
                                 images=release_images(runner), now=now)
    except ReviewFailure as failure:
        report, bundle = failed_report(failure.reason, now), None
    except PolicyError:
        report, bundle = failed_report('contract', now), None
    write_handoff(args.work, report, bundle)
    log.info('incident review collect: status=%s reason=%s findings=%d bundle=%s', report['status'],
             report['reason'], len(report['findings']), bundle is not None)
    return 0
