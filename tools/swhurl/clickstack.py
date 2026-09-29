"""ClickStack first-run setup from Git: the admin account and the team ingestion key.

HyperDX has no setting for either. The first account is created through its
registration API, which works only while no team exists; the team's ingestion
key is a random UUID the collector checks every request against. This command
makes both come from ``observability/clickstack-runtime-inputs`` (SOPS):

1. wait for the API;
2. if no team exists, register ``CLICKSTACK_ADMIN_EMAIL`` with
   ``CLICKSTACK_ADMIN_PASSWORD`` (HyperDX then closes registration itself);
3. set the one team's ``apiKey`` to ``CLICKSTACK_INGESTION_KEY`` if it differs.

Idempotent; never prints a value. Scripts reach MongoDB and the API over
stdin (``kubectl exec -i``), so no secret appears in a command line.
"""
from __future__ import annotations

import base64
import json
import os
import time

from swhurl.report import Report
from swhurl.run import CommandError, Runner

NS = 'observability'
INPUTS_SECRET = 'clickstack-runtime-inputs'
MONGO_URI_SECRET = 'clickstack-mongodb-hyperdx-hyperdx'  # written by the MongoDB operator
MONGO_POD = 'clickstack-mongodb-0'
MONGO_DATA_CLAIM = 'data-volume-clickstack-mongodb-0'
CLICKHOUSE_POD = 'clickstack-clickhouse-clickhouse-0-0-0'  # operator naming: <cluster>-clickhouse-<shard>-<replica>-0
APP = 'deploy/clickstack-app'
API = 'http://127.0.0.1:8000'
MARKER = 'RESULT '


def _result(text: str) -> dict:
    """The JSON a script printed after ``RESULT ``; tools print warnings around it."""
    for line in text.splitlines():
        if line.startswith(MARKER):
            return json.loads(line[len(MARKER):])
    raise ValueError('no result line')


PUBLIC_KEYS = {'CLICKSTACK_ADMIN_EMAIL'}


def read_secret(runner: Runner, name: str) -> dict[str, str]:
    """Decoded ``data`` of a Secret in ``observability``; values other than the email are redacted."""
    secret = runner.json(['kubectl', '-n', NS, 'get', 'secret', name, '-o', 'json'], secret_output=True)
    values = {k: base64.b64decode(v).decode() for k, v in ((secret or {}).get('data') or {}).items()}
    for key, value in values.items():
        if key not in PUBLIC_KEYS:
            runner.add_secret(value)
    return values


def mongo(runner: Runner, uri: str, script: str, *, mutating: bool = False) -> dict:
    """Run a mongosh ``script`` against the hyperdx database; it must print one ``RESULT`` line."""
    program = f'db = connect({json.dumps(uri)});\n{script}\n'
    out = runner.output(['kubectl', '-n', NS, 'exec', '-i', MONGO_POD, '-c', 'mongod', '--',
                         'mongosh', '--quiet', '--nodb', '--norc', '--file', '/dev/stdin'],
                        input=program, secret_output=True, mutating=mutating)
    return _result(out)


def api(runner: Runner, method: str, path: str, body: dict | None = None, *, mutating: bool = False) -> dict:
    """Call the HyperDX API from inside the app pod; returns ``{status, body}``."""
    request = {'method': method, 'headers': {'content-type': 'application/json'}, 'redirect': 'manual'}
    if body is not None:
        request['body'] = json.dumps(body)
    program = (f'const r = await fetch({json.dumps(API + path)}, {json.dumps(request)});\n'
               'let body = null; try { body = await r.json(); } catch {}\n'
               f'console.log({json.dumps(MARKER)} + JSON.stringify({{status: r.status, body}}));\n')
    out = runner.output(['kubectl', '-n', NS, 'exec', '-i', APP, '-c', 'app', '--',
                         'node', '--input-type=module', '-'],
                        input=program, secret_output=True, mutating=mutating)
    return _result(out)


def team_key(runner: Runner) -> str:
    """The one team ingestion key ('' unless exactly one team key exists); registered as secret."""
    uri = read_secret(runner, MONGO_URI_SECRET).get('connectionString.standard', '')
    keys = mongo(runner, uri, TEAM_KEYS)['keys'] if uri else []
    for key in keys:
        runner.add_secret(key)
    return keys[0] if len(keys) == 1 else ''


def wait_ready(runner: Runner, timeout: float, interval: float = 5) -> bool:
    deadline = time.monotonic() + timeout
    while True:
        try:
            if api(runner, 'GET', '/ready')['status'] == 200:
                return True
        except (CommandError, ValueError):
            pass
        if time.monotonic() >= deadline:
            return False
        time.sleep(interval)


TEAM_KEY_MATCHES = ('const t = db.teams.find({}, {apiKey: 1}).toArray();'
                    ' print("RESULT " + JSON.stringify({teams: t.length, same: t.length === 1 && t[0].apiKey === KEY}));')
SET_TEAM_KEY = ('const r = db.teams.updateOne({}, {$set: {apiKey: KEY}});'
                ' print("RESULT " + JSON.stringify({matched: r.matchedCount}));')
TEAM_KEYS = ('print("RESULT " + JSON.stringify({keys: db.teams.distinct("apiKey")'
             '.filter(k => typeof k === "string" && k.length > 0)}));')
ADMIN_EXISTS = 'print("RESULT " + JSON.stringify({admin: db.users.countDocuments({email: EMAIL})}));'


def bootstrap(runner: Runner, report: Report, timeout: float = 300) -> int:
    report.section('ClickStack bootstrap')
    inputs = read_secret(runner, INPUTS_SECRET)
    email, password = inputs.get('CLICKSTACK_ADMIN_EMAIL', ''), inputs.get('CLICKSTACK_ADMIN_PASSWORD', '')
    key = inputs.get('CLICKSTACK_INGESTION_KEY', '')
    if not (email and password and key):
        report.bad(f'{NS}/{INPUTS_SECRET} lacks CLICKSTACK_ADMIN_EMAIL, CLICKSTACK_ADMIN_PASSWORD or '
                   'CLICKSTACK_INGESTION_KEY (see platform/clickstack/secret.sops.yaml)')
        return report.exit_code()
    uri = read_secret(runner, MONGO_URI_SECRET).get('connectionString.standard', '')
    if not uri:
        report.bad(f'{NS}/{MONGO_URI_SECRET} has no connection string; is MongoDB Running?')
        return report.exit_code()

    if not wait_ready(runner, timeout):
        report.bad(f'HyperDX API not ready after {timeout:.0f}s (kubectl -n {NS} logs {APP})')
        return report.exit_code()
    report.ok('HyperDX API is ready')

    if api(runner, 'GET', '/installation')['body'].get('isTeamExisting'):
        report.ok('a team exists; registration is closed')
    elif runner.dry_run:
        report.info(f'would register the admin account {email}')
    else:
        response = api(runner, 'POST', '/register/password',
                       {'email': email, 'password': password, 'confirmPassword': password}, mutating=True)
        if response['status'] != 200:
            report.bad(f'registration returned HTTP {response["status"]} '
                       f'({(response["body"] or {}).get("error", "no detail")})')
            return report.exit_code()
        report.ok(f'registered the admin account {email}')
    admin = mongo(runner, uri, f'const EMAIL = {json.dumps(email)}; {ADMIN_EXISTS}')['admin']
    if admin:
        report.ok(f'{email} has an account')
    elif not runner.dry_run:
        report.warn(f'no account for {email}: the team was registered with another address')

    team = mongo(runner, uri, f'const KEY = {json.dumps(key)}; {TEAM_KEY_MATCHES}')
    if team['teams'] > 1:
        report.bad(f'{team["teams"]} teams exist; expected one')
    elif team['teams'] == 0:
        if not runner.dry_run:
            report.bad('no team exists after registration')
    elif team['same']:
        report.ok('team ingestion key matches CLICKSTACK_INGESTION_KEY')
    elif runner.dry_run:
        report.info('would set the team ingestion key to CLICKSTACK_INGESTION_KEY')
    else:
        mongo(runner, uri, f'const KEY = {json.dumps(key)}; {SET_TEAM_KEY}', mutating=True)
        report.ok('set the team ingestion key to CLICKSTACK_INGESTION_KEY')
        report.info('the ClickStack collector picks up the new key over OpAMP within a minute')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    runner = runner or Runner.from_environment()
    report = report or Report(redact=runner.redact)
    return bootstrap(runner, report, float(os.environ.get('TIMEOUT_SECS') or 300))
