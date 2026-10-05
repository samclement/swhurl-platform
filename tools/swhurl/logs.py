"""Exercise the real collector's log parsers and inspect live format coverage.

Fixture bodies are synthetic. Live checks read aggregate field coverage only;
they never print bodies, original lines, or attribute values.
"""
from __future__ import annotations

import argparse
import copy
import json
import socket
import tempfile
import time
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

import yaml

from swhurl import ROOT, clickstack
from swhurl.report import Report
from swhurl.run import CommandError, Runner

FIXTURES = ROOT / 'tests/fixtures/logs.json'


def any_value(value: object) -> dict:
    """OTLP/JSON AnyValue; used for synthetic records only."""
    if isinstance(value, str):
        return {'stringValue': value}
    if isinstance(value, bool):
        return {'boolValue': value}
    if isinstance(value, int):
        return {'intValue': str(value)}
    if isinstance(value, float):
        return {'doubleValue': value}
    if isinstance(value, dict):
        return {'kvlistValue': {'values': attributes(value)}}
    if isinstance(value, list):
        return {'arrayValue': {'values': [any_value(v) for v in value]}}
    return {}


def attributes(values: dict) -> list[dict]:
    return [{'key': k, 'value': any_value(v)} for k, v in values.items()]


def unpack(value: dict) -> object:
    if 'kvlistValue' in value:
        return {v['key']: unpack(v['value']) for v in value['kvlistValue']['values']}
    if 'arrayValue' in value:
        return [unpack(v) for v in value['arrayValue']['values']]
    if 'intValue' in value:
        return int(value['intValue'])
    return next(iter(value.values()), None)


def records(payloads: list[dict]) -> list[dict]:
    return [record for payload in payloads for resource in payload.get('resourceLogs', [])
            for scope in resource.get('scopeLogs', []) for record in scope.get('logRecords', [])]


def fixture_payload(cases: list[dict]) -> dict:
    resources = []
    for case in cases:
        record = {'body': any_value(case['input']), 'attributes': attributes({'test.case': case['name'],
                  **case.get('attributes', {})}), **case.get('record', {})}
        resources.append({'resource': {'attributes': attributes(case.get('resource', {}))},
                          'scopeLogs': [{'logRecords': [record]}]})
    return {'resourceLogs': resources}


def check_records(cases: list[dict], found: list[dict]) -> list[str]:
    """Compare expected fields without including log contents in a failure message."""
    indexed: dict[str, list[tuple[dict, dict]]] = {}
    for record in found:
        attrs = {a['key']: unpack(a['value']) for a in record.get('attributes', [])}
        indexed.setdefault(attrs.get('test.case', ''), []).append((record, attrs))
    errors = []
    for case in cases:
        matches = indexed.get(case['name'], [])
        if case.get('dropped'):
            if matches:
                errors.append(f'{case["name"]}: expected the record to be dropped, got {len(matches)}')
            continue
        if len(matches) != 1:
            errors.append(f'{case["name"]}: expected one record, got {len(matches)}')
            continue
        record, attrs = matches[0]
        expected = case['expected']
        for key, value in expected.items():
            actual = attrs if key == 'attributes' else unpack(record.get('body', {})) if key == 'body' else record.get(key, 0)
            if key == 'attributes':
                for field, expected_value in value.items():
                    if attrs.get(field) != expected_value:
                        errors.append(f'{case["name"]}: incorrect attribute {field}')
            elif actual != value:
                errors.append(f'{case["name"]}: incorrect {key}')
        for key, value in case.get('record', {}).items():
            if key not in expected and record.get(key) != value:
                errors.append(f'{case["name"]}: existing {key} changed')
        for key, value in case.get('attributes', {}).items():
            if attrs.get(key) != value:
                errors.append(f'{case["name"]}: existing attribute {key} changed')
        original = case.get('original', case['input'])
        if isinstance(original, str) and expected.get('body', original) != original:
            if attrs.get('log.record.original') != original:
                errors.append(f'{case["name"]}: original line missing or changed')
    return errors


def _fixtures_complete(cases: list[dict], found: list[dict]) -> bool:
    """True once the exporter flushed a record for every case that is not meant to be dropped.

    Fixtures travel in one OTLP request, so a record a processor drops is already gone when the others
    flush. Duplicates and records that should have been dropped are reported by ``check_records``."""
    seen = set()
    for record in found:
        attrs = {a['key']: unpack(a['value']) for a in record.get('attributes', [])}
        seen.add(attrs.get('test.case', ''))
    return all(case['name'] in seen for case in cases if not case.get('dropped'))


def _read_records(path: Path) -> list[dict] | None:
    """Read currently flushed lines; a final partial line is still in flight."""
    try:
        return records([json.loads(line) for line in path.read_text().splitlines() if line.strip()])
    except (OSError, json.JSONDecodeError):
        return None


def _wait_for_otlp_receiver(port: int, process, deadline: float) -> bool:
    """Probe readiness with safe GETs so fixture POSTs are never replayed."""
    url = f'http://127.0.0.1:{port}/'
    while process.poll() is None and time.monotonic() < deadline:
        try:
            with urlopen(Request(url), timeout=0.2):
                return True
        except HTTPError:
            # OTLP receivers commonly return 404 at `/`; that still proves the listener is ready.
            return True
        except (URLError, TimeoutError):
            time.sleep(0.05)
    return False


def exercise(runner: Runner, binary: Path, config: dict, extra_args: list[str], cases: list[dict]) -> list[str]:
    """Run rendered processors against OTLP fixtures and real container framing, isolated from the cluster."""
    names = [n for n in config['service']['pipelines']['logs']['processors'] if n.startswith(('transform/', 'filter/'))]
    if not names:
        return []
    with tempfile.TemporaryDirectory(prefix='swhurl-logs-') as directory:
        tmp = Path(directory)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        output = tmp / 'output.json'
        runtime = {'receivers': {'otlp': {'protocols': {'http': {'endpoint': f'127.0.0.1:{port}'}}}},
                   'processors': {n: copy.deepcopy(config['processors'][n]) for n in names},
                   'exporters': {'file': {'path': str(output), 'flush_interval': '100ms'}},
                   'service': {'telemetry': {'logs': {'level': 'error'}},
                               'pipelines': {'logs': {'receivers': ['otlp'], 'processors': names, 'exporters': ['file']}}}}
        framed = [c for c in cases if c.get('framing')]
        for case in framed:
            path = tmp / 'pods' / f'fixture_{case["resource"]["k8s.pod.name"]}_00000000-0000-0000-0000-000000000000' / 'main' / '0.log'
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('\n'.join(case['framing']) + '\n')
            receiver = copy.deepcopy(config['receivers']['file_log'])
            receiver.update(include=[str(path)], exclude=[], start_at='beginning', poll_interval='100ms')
            # Inject a synthetic identifier after the original container parser.
            receiver['operators'].append({'type': 'add', 'field': 'attributes["test.case"]', 'value': case['name']})
            name = f'file_log/{case["name"]}'
            runtime['receivers'][name] = receiver
            runtime['service']['pipelines']['logs']['receivers'].append(name)
        path = tmp / 'config.yaml'
        path.write_text(yaml.safe_dump(runtime))
        gates = [arg for arg in extra_args if arg.startswith('--feature-gates=')]
        command = [str(binary), *gates, f'--config=file:{path}']
        process = runner.start(command)
        deadline = time.monotonic() + 8  # failure guard; successful fixtures stop as soon as they flush
        try:
            payload = json.dumps(fixture_payload([c for c in cases if not c.get('framing')])).encode()
            if not _wait_for_otlp_receiver(port, process, deadline):
                return ['fixture collector did not become ready for OTLP input']
            request = Request(f'http://127.0.0.1:{port}/v1/logs', data=payload,
                              headers={'Content-Type': 'application/json'})
            try:
                with urlopen(request, timeout=min(5, max(0.2, deadline - time.monotonic()))) as response:
                    result = json.load(response)
            except (URLError, TimeoutError, json.JSONDecodeError):
                # The collector may have accepted the batch before the response failed. Retrying this
                # non-idempotent POST would duplicate every record and hide the actual fixture result.
                return ['fixture collector response failed after the single OTLP submission']
            if result.get('partialSuccess', {}).get('rejectedLogRecords', 0):
                return ['collector rejected fixture records']
            while True:
                found = _read_records(output)
                if found is not None and _fixtures_complete(cases, found):
                    process.interrupt()  # SIGINT lets the collector flush and shut down cleanly
                    break
                if process.poll() is not None:
                    return ['fixture collector stopped before all records were flushed']
                if time.monotonic() >= deadline:
                    return ['fixture collector timed out before all records were flushed']
                time.sleep(0.05)
            if process.wait(timeout=3) != 0:
                return ['fixture collector exited unsuccessfully']
            found = _read_records(output)
            if found is None:
                return ['fixture collector output was incomplete']
            return check_records(cases, found)
        finally:
            if process.poll() is None:
                process.interrupt()
                process.wait(timeout=3)


def check_fixtures(runner: Runner, binary: Path, config: dict, extra_args: list[str], report: Report) -> None:
    fixtures = json.loads(FIXTURES.read_text())
    kind = 'events' if 'transform/events' in config.get('processors', {}) else 'node'
    cases = [case for case in fixtures if case.get('pipeline', 'node') == kind]
    errors = exercise(runner, binary, config, extra_args, cases)
    for error in errors:
        report.bad(error)
    if not errors:
        report.ok(f'{len(cases)} log fixtures passed through the actual collector ({kind})')


COVERAGE_QUERY = """
SELECT ResourceAttributes['k8s.namespace.name'] AS namespace,
       ResourceAttributes['k8s.pod.name'] AS pod,
       ResourceAttributes['k8s.container.name'] AS container,
       ResourceAttributes['service.name'] AS service,
       if(LogAttributes['log.parser'] != '', LogAttributes['log.parser'],
          if(ScopeName = 'node-logger', 'native-otlp', '')) AS parser,
       count() AS records,
       countIf(SeverityNumber > 0) AS with_severity,
       countIf(TraceId != '') AS with_trace,
       countIf(mapContains(LogAttributes, 'log.record.original')) AS with_original,
       max(Timestamp) AS latest
FROM default.otel_logs
WHERE Timestamp > now() - INTERVAL {minutes:UInt32} MINUTE
GROUP BY namespace, pod, container, service, parser
ORDER BY namespace, pod, container, parser
FORMAT JSONEachRow
"""


def verify(runner: Runner, report: Report, minutes: int = 15) -> int:
    report.section(f'Log format coverage (last {minutes} minutes)')
    try:
        out = runner.output(['kubectl', '-n', 'observability', 'exec', clickstack.CLICKHOUSE_POD, '--',
                             'clickhouse-client', f'--param_minutes={minutes}', '-q', COVERAGE_QUERY], secret_output=True)
        rows = [json.loads(line) for line in out.splitlines() if line.strip()]
        pods = runner.json(['kubectl', 'get', 'pods', '-A', '-o', 'json'])
    except (CommandError, ValueError) as error:
        report.bad(f'could not inspect log coverage: {error}')
        return report.exit_code()
    active = {(p['metadata']['namespace'], p['metadata']['name'], c['name'])
              for p in (pods or {}).get('items', []) if p.get('status', {}).get('phase') == 'Running'
              for c in p['spec']['containers']}
    seen = set()
    for row in rows:
        identity = (row['namespace'], row['pod'], row['container'])
        seen.add(identity)
        label = '/'.join(identity) if row['pod'] else row['service'] or 'cluster events'
        parser = row['parser'] or 'unclassified'
        detail = (f'{label}: {parser}; {row["records"]} records, {row["with_severity"]} with severity, '
                  f'{row["with_trace"]} with trace, {row["with_original"]} with original')
        if parser == 'unclassified':
            report.warn(detail)
        else:
            report.ok(detail)
    for ns, pod, container in sorted(active - seen):
        report.warn(f'{ns}/{pod}/{container}: no fresh records; quiet or not yet observed (use offline fixtures)')
    if not rows:
        report.bad('no fresh log records')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--minutes', type=int, default=15)
    args = parser.parse_args(argv)
    if not 1 <= args.minutes <= 1440:
        parser.error('--minutes must be between 1 and 1440')
    return verify(runner or Runner(), report or Report(), args.minutes)
