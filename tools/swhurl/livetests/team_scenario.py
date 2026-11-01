"""
Drive the team platform's whole chain on three deployed apps and check what ClickStack recorded:
  - CALLER calls CALLEE with its platform-issued token, and CALLEE names it as the caller
  - CALLEE publishes its first declared event, and SUBSCRIBER receives it
  - one trace holds all three apps' spans, each with its team, and the callee's log line names the caller

    make live-test-team-scenario CALLER=reports CALLEE=orders SUBSCRIBER=notifier [ENV=staging]

The three apps must be linked already (CALLER declares the call, CALLEE publishes an event that
SUBSCRIBER subscribes to) and still have the stack templates' example routes: CALLER's
/call/<app>, and CALLEE publishing on every request.
Creates nothing: it sends one request, from the Traefik pod as a routed request arrives, and reads.
"""
from __future__ import annotations

import json
import os
import secrets

from swhurl import clickstack, interface
from swhurl.apps import calling, events, helmrelease
from swhurl.apps.instance import Instance
from swhurl.apps.model import records
from swhurl.livetests import LiveTest, Preflight, run_live_test


def release(t: LiveTest, app: str, env: str) -> dict:
    found = t.get('-n', Instance(app, env).namespace, 'get', 'helmrelease', app)
    if not found:
        raise Preflight(f'{app}/{env} is not deployed')
    return found


def body(t: LiveTest, caller: str, callee: str, subscriber: str, env: str) -> None:
    for name in (caller, callee, subscriber):
        if not records.NAME_RE.match(name or ''):
            raise Preflight('give CALLER, CALLEE and SUBSCRIBER (app names) and optionally ENV')
    releases = {app: release(t, app, env) for app in (caller, callee, subscriber)}
    if callee not in calling.declared_calls(helmrelease.variables(releases[caller])):
        raise Preflight(f'{caller}/{env} does not declare a call to {callee}: add it with swhurl-v2 edit --set calls=…')
    published = events.published(helmrelease.variables(releases[callee]))
    if not published:
        raise Preflight(f'{callee}/{env} publishes no event: add a send operation to its asyncapi.yaml '
                        'named by provides.eventsFile, then deploy it')
    event = published[0]
    if (callee, event) not in events.subscribed(helmrelease.variables(releases[subscriber])):
        raise Preflight(f'{subscriber}/{env} does not subscribe to {callee}.{event}: '
                        f'add it with swhurl-v2 edit --set subscriptions=…')
    port = helmrelease.port(releases[caller])
    if not port:
        raise Preflight(f'{caller}/{env} is a worker: the caller must be a web app')

    t.step(f'One request: {caller} calls {callee}, which publishes {callee}.{event}')
    pods = (t.get('-n', f'{caller}-{env}', 'get', 'pods', '-l', f'app.kubernetes.io/instance={caller}') or {}).get('items') or []
    address = next((p['status'].get('podIP') for p in pods if p.get('status', {}).get('podIP')), None)
    if not address:
        raise Preflight(f'{caller}/{env} has no running pod')
    trace = secrets.token_hex(16)  # ours to choose, so the request's trace can be found afterwards
    reply = t.runner.run(['kubectl', '-n', 'kube-system', 'exec', 'deploy/traefik', '--', 'wget', '-q', '-T', '30', '-O-',
                          '--header', f'traceparent: 00-{trace}-{secrets.token_hex(8)}-01',
                          f'http://{address}:{port}/call/{callee}'], check=False)
    try:
        answer = json.loads(reply.stdout)
    except ValueError:
        answer = {}
    if not t.check(answer.get('called') == callee and answer.get('status') == 200,
                   f'{caller} called {callee} and got 200', f'the call failed: {(reply.stdout or reply.stderr).strip()[:300]}'):
        return

    t.step(f'What ClickStack recorded for trace {trace}')
    spans: list[list[str]] = []

    def recorded() -> bool:
        nonlocal spans
        rows = clickstack.clickhouse(t.runner, (
            "SELECT ServiceName, ResourceAttributes['swhurl.team'], SpanKind, SpanName, SpanAttributes['caller.type'], "
            f"SpanAttributes['caller.id'] FROM otel_traces WHERE TraceId = '{trace}' ORDER BY Timestamp FORMAT TSV"))
        spans = [line.split('\t') for line in rows.splitlines() if line]
        return {caller, callee, subscriber} <= {span[0] for span in spans}
    complete = t.poll(recorded, attempts=18, interval=5)
    services = sorted({span[0] for span in spans})
    t.check(complete, f'one trace holds spans from all three apps ({len(spans)} spans)',
            f'after 90 seconds the trace holds spans from {", ".join(services) or "no app"} only')
    t.check(any(span[0] == callee and span[2] == 'Server' and span[4:6] == ['app', caller] for span in spans),
            f"{callee}'s span names its caller: app {caller}", f'{callee} recorded no span with caller.id = {caller}')
    t.check(any(span[0] == callee and span[2] == 'Producer' for span in spans)
            and any(span[0] == subscriber and span[2] == 'Consumer' and span[3] == f'process {callee}.{event}' for span in spans),
            f'{callee} published {callee}.{event} and {subscriber} processed it, in the same trace',
            f'no publish span from {callee} or no "process {callee}.{event}" span from {subscriber}')
    for app in (caller, callee, subscriber):
        wanted = t.label(t.get('get', 'namespace', Instance(app, env).namespace), interface.TEAM) or ''
        found = sorted({span[1] for span in spans if span[0] == app})
        t.check(bool(wanted) and found == [wanted], f"{app}'s spans carry its team: {wanted}",
                f"{app}'s spans carry team {found or 'none'}, its namespace says {wanted or 'none'}")
    logged = clickstack.clickhouse(t.runner, (
        "SELECT count() FROM otel_logs WHERE TraceId = '" + trace + f"' AND ServiceName = '{callee}' "
        f"AND LogAttributes['caller.type'] = 'app' AND LogAttributes['caller.id'] = '{caller}' FORMAT TSV"))
    t.check(logged.strip() not in ('', '0'), f"{callee}'s log line for the request names the caller",
            f'{callee} logged no line in this trace with caller.id = {caller}')
    t.report.info(f'In HyperDX, search for trace {trace}')


def main(argv: list[str] | None = None, runner=None, report=None, sleep=None) -> int:
    names = [os.environ.get(key, '') for key in ('CALLER', 'CALLEE', 'SUBSCRIBER')]
    env = os.environ.get('ENV') or 'staging'
    kwargs = {'sleep': sleep} if sleep else {}
    return run_live_test(__doc__, lambda t: body(t, *names, env), passed='Team scenario passed.',
                         runner=runner, report=report, **kwargs)
