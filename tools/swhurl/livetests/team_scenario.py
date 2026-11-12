"""
Check in ClickStack that the team platform's whole chain held for the traffic three running apps already carry:
  - CALLER calls CALLEE with its platform-issued token, and CALLEE names it as the caller
  - CALLEE publishes one of its events, and SUBSCRIBER receives it
  - one trace holds all three apps' spans, each with the team its records name, and the callee's log line names the caller

    make live-test-team-scenario [ENV=staging]

The three apps are chosen from swhurl-v2 status --all over the records at HEAD (needs Go 1.26.0): in ENV, the first
app (by name) that another app calls and that publishes an event a third app subscribes to, with its first caller
and subscriber. Something must drive that call (the demo's demo-shopper does, every 30 seconds): the check reads the
newest trace of the last 10 minutes in which CALLEE served CALLER.
Sends nothing and creates nothing: it only reads.
"""
from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path

from swhurl import ROOT
from swhurl.livetests import LiveTest, Preflight, run_live_test

V2_GO_ENV = {'GOTOOLCHAIN': 'go1.26.0', 'GOPROXY': 'off', 'GOSUMDB': 'off', 'CGO_ENABLED': '0'}
MINUTES = 10  # how far back a trace is looked for; demo-shopper calls every 30 seconds
CLICKHOUSE_POD = 'clickstack-clickhouse-clickhouse-0-0-0'  # operator naming: <cluster>-clickhouse-<shard>-<replica>-0


def inventories(t: LiveTest, env: str) -> dict[str, dict]:
    """Each instance's inventory in ``env``, by app, as swhurl-v2 status --all states it from the records at HEAD."""
    with tempfile.TemporaryDirectory() as work:  # readable by the caller only; holds the kubeconfig
        binary, kubeconfig = Path(work) / 'swhurl-v2', Path(work) / 'kubeconfig.json'
        t.runner.run(['go', 'build', '-o', binary, './cmd/swhurl-v2'], cwd=ROOT / 'v2', env=V2_GO_ENV)
        kubeconfig.touch(mode=0o600)
        kubeconfig.write_text(t.runner.output(['kubectl', 'config', 'view', '--raw', '-o', 'json'], secret_output=True))
        revision = t.runner.output(['git', 'rev-parse', 'HEAD'], cwd=ROOT).strip()
        # Exit 1 means an instance is bad, after the report: the inventory is still the records'.
        result = t.runner.run([binary, 'status', '--all', '--json', '--records', 'v2-records',
                               '--profile', 'v2/testdata/profile.json', '--calls', 'v2-records/calls.json',
                               '--egress', 'platform/egress/allocation.json', '--revision', revision,
                               '--kubeconfig', kubeconfig], cwd=ROOT, check=False)
    try:
        status = json.loads(result.stdout)
    except ValueError:
        raise Preflight(f'swhurl-v2 status --all exited {result.returncode}: {result.stderr.strip()[:300]}') from None
    return {item['application']: item['inventory'] for item in status['instances'] if item['environment'] == env}


def chain(found: dict[str, dict], env: str) -> tuple[str, str, str, str]:
    """(caller, callee, event, subscriber): the first callee by name with a caller and a subscribed event."""
    for callee, inventory in sorted(found.items()):
        callers = sorted(app for app, other in found.items() if callee in other['calls'])
        for event in inventory['publishes']:
            subscribers = sorted(app for app, other in found.items() if f'{callee}.{event}' in other['subscriptions'])
            if callers and subscribers:
                return callers[0], callee, event, subscribers[0]
    raise Preflight(f'no {env} instances form a chain: an app calling an app that publishes an event another '
                    'app subscribes to (swhurl-v2 edit --set calls=… and --set subscriptions=…)')


def clickhouse(t: LiveTest, query: str) -> str:
    return t.runner.output(['kubectl', '-n', 'observability', 'exec', CLICKHOUSE_POD, '--',
                            'clickhouse-client', '-q', query]).strip()


def body(t: LiveTest, env: str) -> None:
    found = inventories(t, env)
    caller, callee, event, subscriber = chain(found, env)

    t.step(f'The newest trace in which {callee} served {caller} ({env}, last {MINUTES} minutes)')
    served = (f"ServiceName = '{callee}' AND SpanKind = 'Server' "
              f"AND SpanAttributes['caller.type'] = 'app' AND SpanAttributes['caller.id'] = '{caller}'")
    chained = (f"(ServiceName = '{callee}' AND SpanKind = 'Producer') + "
               f"(ServiceName = '{subscriber}' AND SpanKind = 'Consumer' AND SpanName = 'process {callee}.{event}') * 2")
    # Each trace's verdicts as bits (1: the callee published, 2: the subscriber processed); a complete one first.
    row = clickhouse(t, (
        f"SELECT TraceId, groupBitOr({chained}) AS seen FROM otel_traces "
        f"WHERE Timestamp > now() - INTERVAL {MINUTES} MINUTE AND ServiceName IN ('{callee}', '{subscriber}') "
        f"GROUP BY TraceId HAVING countIf({served}) > 0 ORDER BY seen = 3 DESC, max(Timestamp) DESC LIMIT 1 FORMAT TSV"))
    trace, _, seen = row.partition('\t')
    if not t.check(bool(trace), f'{callee} served {caller} in trace {trace}',
                   f'no trace in the last {MINUTES} minutes in which {callee} served {caller}: does anything drive {caller}?'):
        return
    t.check(seen.strip() == '3', f'{callee} published {callee}.{event} and {subscriber} processed it, in the same trace',
            f'no publish span from {callee} or no "process {callee}.{event}" span from {subscriber} in the trace')
    spans = [line.split('\t') for line in clickhouse(t, (
        "SELECT ServiceName, ResourceAttributes['swhurl.team'] FROM otel_traces "
        f"WHERE TraceId = '{trace}' AND ServiceName IN ('{caller}', '{callee}', '{subscriber}') FORMAT TSV")).splitlines() if line]
    for app in (caller, callee, subscriber):
        wanted = found[app]['team']
        recorded_teams = sorted({span[1] for span in spans if span[0] == app})
        t.check(recorded_teams == [wanted], f"{app}'s spans carry its team: {wanted}",
                f"{app}'s spans carry team {recorded_teams or 'none'}, its records say {wanted}")
    logged = clickhouse(t, (
        "SELECT count() FROM otel_logs WHERE TraceId = '" + trace + f"' AND ServiceName = '{callee}' "
        f"AND LogAttributes['caller.type'] = 'app' AND LogAttributes['caller.id'] = '{caller}' FORMAT TSV"))
    t.check(logged not in ('', '0'), f"{callee}'s log line for the request names the caller",
            f'{callee} logged no line in this trace with caller.id = {caller}')
    t.report.info(f'In HyperDX, search for trace {trace}')


def main(argv: list[str] | None = None, runner=None, report=None, sleep=None) -> int:
    env = os.environ.get('ENV') or 'staging'
    kwargs = {'sleep': sleep} if sleep else {}
    return run_live_test(__doc__, lambda t: body(t, env), passed='Team scenario passed.',
                         runner=runner, report=report, **kwargs)
