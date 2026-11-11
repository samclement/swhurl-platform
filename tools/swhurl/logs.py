"""``verify-logs``: live log format coverage.

It reads aggregate field coverage only and never prints bodies, original lines or
attribute values. The offline fixtures run in ``make check-otel`` (platform/otel/check.sh).
"""
from __future__ import annotations

import argparse
import json

from swhurl import clickstack
from swhurl.report import Report
from swhurl.run import CommandError, Runner

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
