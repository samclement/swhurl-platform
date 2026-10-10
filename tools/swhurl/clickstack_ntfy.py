"""Keep ClickStack's reusable ntfy destination on the existing failures topic.

The topic comes from the live notification Secret. No topic or access key is
printed or written to Git; the webhook itself is stored in ClickStack's MongoDB.
"""
from __future__ import annotations

import base64
import json
from urllib.parse import urlsplit

from swhurl import clickstack, dashboards
from swhurl.report import Report
from swhurl.run import CommandError, Runner

NAME = 'ntfy: failures'
DESCRIPTION = 'ClickStack alerts to the platform failures ntfy topic'
NTFY_SECRET = ['kubectl', '-n', 'console', 'get', 'secret', 'notification-ntfy', '-o', 'json']


def destination(runner: Runner) -> dict:
    data = runner.json(NTFY_SECRET, secret_output=True)['data']
    url = base64.b64decode(data['NTFY_FAILURES_URL']).decode()
    runner.add_secret(url)
    parts = urlsplit(url)
    topic = parts.path.removeprefix('/')
    if (parts.scheme, parts.netloc) != ('https', 'ntfy.sh') or not topic or '/' in topic or parts.query or parts.fragment:
        raise ValueError('the failures ntfy destination must be a single https://ntfy.sh/<topic> URL')
    runner.add_secret(topic)
    # HyperDX JSON-escapes each string variable before applying this template.
    # The condition sets high priority on firing and normal priority on recovery.
    body = (json.dumps({'topic': topic, 'title': 'ClickStack: {{title}}',
                        'message': '{{body}}', 'click': '{{link}}'}, separators=(',', ':'))
            .removesuffix('}') + ',"priority":{{#if (eq state "ALERT")}}4{{else}}3{{/if}}}')
    runner.add_secret(body)
    return {'name': NAME, 'service': 'generic', 'url': 'https://ntfy.sh/',
            'description': DESCRIPTION, 'body': body}


def sync(runner: Runner, report: Report) -> int:
    report.section('ClickStack ntfy webhook')
    try:
        wanted = destination(runner)
        inputs = clickstack.read_secret(runner, clickstack.INPUTS_SECRET)
        email = inputs.get('CLICKSTACK_ADMIN_EMAIL', '')
        uri = clickstack.read_secret(runner, clickstack.MONGO_URI_SECRET).get('connectionString.standard', '')
        if not (email and uri):
            report.bad('no ClickStack admin email or MongoDB connection string; run make clickstack-bootstrap')
            return report.exit_code()
        key = clickstack.mongo(runner, uri, f'const EMAIL = {json.dumps(email)}; {dashboards.ADMIN_KEY}')['key']
        if not key:
            report.bad('the ClickStack admin has no access key; sign in once or run make clickstack-bootstrap')
            return report.exit_code()
        api = dashboards.HyperDX(runner, key)
        result = api.call('GET', '/webhooks')
        found = [w for w in result.get('data', []) if w.get('name') == NAME and w.get('service') == 'generic']
        if result.get('meta', {}).get('total', len(result.get('data', []))) > len(result.get('data', [])):
            report.bad('ClickStack returned a truncated webhook list; no change made')
            return report.exit_code()
        current = found[0] if found else None
        if current and all(current.get(k) == v for k, v in wanted.items()):
            report.ok(f'{NAME} is up to date')
            return report.exit_code()
        action = 'update' if current else 'create'
        if runner.dry_run:
            report.info(f'would {action} {NAME}')
            return report.exit_code()
        if current:
            api.call('PUT', f"/webhooks/{current['id']}", wanted)
        else:
            api.call('POST', '/webhooks', wanted)
        report.ok(f'{action}d {NAME}')
        return report.exit_code()
    except (CommandError, KeyError, ValueError) as exc:
        report.bad(runner.redact(str(exc)))
        return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    if argv:
        raise ValueError('clickstack-ntfy-webhook takes no arguments')
    runner = runner or Runner.from_environment()
    return sync(runner, report or Report(redact=runner.redact))
