"""Operator views of the incident reviewer: its state, and one bundle for the privacy review.

Both read only. ``incident-review-bundle`` runs the collector's own queries from this
host (through ``clickhouse-client`` in the ClickHouse pod, as ``verify-platform`` does)
against an empty state, so it shows exactly what the pod would send for that hour
without changing the reviewer's state or calling a model.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import sys
import time
from typing import Any

from swhurl import clickstack
from swhurl.run import CommandError, Runner

from . import MAX_QUERY_RESPONSE_BYTES, MAX_QUERY_ROWS, allowlist, collect, state
from .errors import PolicyError, ReviewFailure


class ClickHouseExec:
    """The collector's queries through ``kubectl exec`` for an operator on the host."""

    def __init__(self, runner: Runner):
        self.runner = runner

    def query(self, sql: str, params: dict[str, str | int]) -> list[dict[str, Any]]:
        command = ['kubectl', '-n', clickstack.NS, 'exec', clickstack.CLICKHOUSE_POD, '--', 'clickhouse-client',
                   '--format', 'JSONEachRow', f'--max_result_rows={MAX_QUERY_ROWS}', '--result_overflow_mode=throw',
                   *[f'--param_{name}={value}' for name, value in params.items()], '-q', sql]
        try:
            out = self.runner.output(command, secret_output=True)
            if len(out.encode()) > MAX_QUERY_RESPONSE_BYTES:
                raise ReviewFailure('query')
            return [json.loads(line) for line in out.splitlines() if line.strip()]
        except (CommandError, json.JSONDecodeError):
            raise ReviewFailure('query') from None


def when(epoch: float) -> str:
    return dt.datetime.fromtimestamp(epoch, dt.UTC).strftime('%Y-%m-%d %H:%M') if epoch else 'never'


def clock(epoch: float) -> str:
    return dt.datetime.fromtimestamp(epoch, dt.UTC).strftime('%H:%M')


def status_main(argv: list[str] | None = None, runner: Runner | None = None, *, now: float | None = None) -> int:
    parser = argparse.ArgumentParser(prog='swhurl incident-review-status',
                                     description='Summarise the incident reviewer state (read-only; no log text)')
    parser.parse_args(argv)
    runner = runner or Runner.from_environment()
    now = now if now is not None else time.time()
    try:
        current = state.read_state(runner)
    except ReviewFailure as failure:
        print(f'[ERROR] cannot read the reviewer state: {failure.reason}', file=sys.stderr)
        return 1
    defaults = allowlist.current().defaults
    spend = current['spend']
    print(f"Last sweep: {when(current['last_sweep'])} UTC, result: {current['last_result'] or 'none yet'}")
    print(f"Spend {spend['month'] or '-'}: {spend['cents']} of {defaults['monthly_refuse_cents']} cents, "
          f"{spend['analyses']} analyses ({spend['analyses_today']} of {defaults['max_analyses_per_day']} today)")
    print(f"Pending messages: {len(current['pending'])}")
    print('Counts per hour, newest last:')
    for key, counts in sorted(current['baselines'].items()):
        print(f"  {key}: {' '.join(str(c) for c in counts[-8:])} (mean {state.baseline_mean(current, key):.1f})")
    print(f"Incidents: {len(current['incidents'])}")
    for fingerprint, item in sorted(current['incidents'].items(), key=lambda pair: -pair[1]['last_seen']):
        cooling = 'cooling down' if item['cooldown_until'] > now else 'may be analysed again'
        print(f"  {fingerprint} {item['app']}/{item['env']} {item['signal']}: {item['status']}, {item['coverage']}, "
              f"last seen {when(item['last_seen'])}, {cooling}")
    return 0


def bundle_main(argv: list[str] | None = None, runner: Runner | None = None, *, now: float | None = None) -> int:
    parser = argparse.ArgumentParser(prog='swhurl incident-review-bundle', description=__doc__.split('\n\n')[0])
    parser.add_argument('--hours-ago', type=int, default=0,
                        help='review the full hour this many hours before the latest one (default 0)')
    parser.add_argument('--summary', action='store_true', help='print field names and sizes, not contents')
    args = parser.parse_args(argv)
    if not 0 <= args.hours_ago <= 23:
        parser.error('--hours-ago must be between 0 and 23')
    runner = runner or Runner.from_environment()
    real_now = now if now is not None else time.time()
    now = real_now - args.hours_ago * 3600
    try:
        report, bundle = collect.collect(allow=allowlist.current(), current=state.empty_state(),
                                         telemetry=ClickHouseExec(runner), images=collect.release_images(runner),
                                         now=now)
    except (ReviewFailure, PolicyError) as error:
        print(f'[ERROR] collection failed: {error}', file=sys.stderr)
        return 1
    print(f"[INFO] window {report['window']['start']} to {report['window']['end']}: {report['status']}", file=sys.stderr)
    for finding in report['findings']:
        print(f"[INFO] {finding['app']}/{finding['env']} {finding['signal']} {finding['key']!r}: {finding['count']} "
              f"({finding['decision']}, {finding['reason']})", file=sys.stderr)
    if bundle is None:
        latest = collect.window(real_now)
        print(f'[INFO] nothing fired between {clock(collect.window(now)[0])} and {clock(collect.window(now)[1])} UTC, '
              'so no bundle would be sent for that hour', file=sys.stderr)
        print(f'[INFO] only complete hours can be previewed: the current hour ({clock(latest[1])} to '
              f'{clock(latest[1] + 3600)} UTC) becomes available at {clock(latest[1] + 3600)} UTC', file=sys.stderr)
        print(f'[INFO] --hours-ago N looks N hours before the latest complete hour ({clock(latest[0])} to '
              f'{clock(latest[1])} UTC); N can be 0 to 23', file=sys.stderr)
        return 0
    if args.summary:
        size = len(json.dumps(bundle, separators=(',', ':'), ensure_ascii=False).encode())
        print(json.dumps({'bytes': size, 'fields': sorted(bundle), 'fingerprint': bundle['fingerprint'],
                          **{kind: {'records': len(bundle[kind]),
                                    'fields': sorted({key for record in bundle[kind] for key in record})}
                             for kind in ('logs', 'metrics', 'traces')}}, indent=2))
    else:
        print(json.dumps(bundle, indent=2, ensure_ascii=False))
    return 0
