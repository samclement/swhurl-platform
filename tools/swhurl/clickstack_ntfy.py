"""Keep ClickStack's two webhooks: the operator's ntfy destination on the platform topic, and the one
that hands every app's default alerts to the notify service.

The topic and the notify service's alert secret come from live Secrets. Neither is printed or
written to Git by this command; each webhook is stored in ClickStack's MongoDB.
"""
from __future__ import annotations

import base64
import json
from urllib.parse import urlsplit

from swhurl import app_alerts, clickstack, dashboards
from swhurl.apps import calling, channels
from swhurl.report import Report
from swhurl.run import CommandError, Runner

NAME = 'ntfy: platform'
DESCRIPTION = 'ClickStack alerts to the platform ntfy topic'
NTFY_SECRET = ['kubectl', '-n', 'console', 'get', 'secret', 'notification-ntfy', '-o', 'json']
ALERT_SECRET = 'notify-alerts'  # the notify service compares against the same Secret (notifications/service.py)


def destination(runner: Runner) -> dict:
    data = runner.json(NTFY_SECRET, secret_output=True)['data']
    url = base64.b64decode(data['NTFY_PLATFORM_URL']).decode()
    runner.add_secret(url)
    parts = urlsplit(url)
    topic = parts.path.removeprefix('/')
    if (parts.scheme, parts.netloc) != ('https', 'ntfy.sh') or not topic or '/' in topic or parts.query or parts.fragment:
        raise ValueError('the platform ntfy destination must be a single https://ntfy.sh/<topic> URL')
    runner.add_secret(topic)
    # HyperDX JSON-escapes each string variable before applying this template.
    # The condition sets high priority on firing and normal priority on recovery.
    # Put the conditional before another field: `{{/if}}}` is parsed by
    # Handlebars as a triple-brace close, even though the last `}` is JSON.
    body = ('{"priority":{{#if (eq state "ALERT")}}4{{else}}3{{/if}},'
            + json.dumps({'topic': topic, 'title': 'ClickStack: {{title}}',
                          'message': '{{body}}', 'click': '{{link}}'}, separators=(',', ':'))[1:])
    runner.add_secret(body)
    return {'name': NAME, 'service': 'generic', 'url': 'https://ntfy.sh/',
            'description': DESCRIPTION, 'body': body}


def notify_destination(runner: Runner) -> dict:
    """The webhook every app alert uses: the notify service, with the shared secret it asks for in the
    body (a HyperDX webhook cannot carry a header or a token that rotates)."""
    secrets = runner.json(['kubectl', '-n', channels.NAMESPACE, 'get', 'secret', ALERT_SECRET, '-o', 'json'], secret_output=True)
    secret = base64.b64decode(secrets['data']['secret']).decode()
    runner.add_secret(secret)
    if not secret:
        raise ValueError(f'{channels.NAMESPACE}/{ALERT_SECRET} holds no secret')
    # HyperDX JSON-escapes each string variable before applying this template.
    body = ('{' + f'"secret":{json.dumps(secret)},' + '"title":"{{title}}","body":"{{body}}","link":"{{link}}","state":"{{state}}"}')
    runner.add_secret(body)
    return {'name': app_alerts.WEBHOOK, 'service': 'generic', 'url': f'{calling.NOTIFY_URL}/v1/alert',
            'description': 'Default app alerts, to the notify service and on to the app\'s team', 'body': body}


def sync(runner: Runner, report: Report, destination=destination, section: str = 'ClickStack ntfy webhook') -> int:
    report.section(section)
    try:
        wanted = destination(runner)
        name = wanted['name']
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
        found = [w for w in result.get('data', []) if w.get('name') == name and w.get('service') == 'generic']
        if result.get('meta', {}).get('total', len(result.get('data', []))) > len(result.get('data', [])):
            report.bad('ClickStack returned a truncated webhook list; no change made')
            return report.exit_code()
        current = found[0] if found else None
        if current and all(current.get(k) == v for k, v in wanted.items()):
            report.ok(f'{name} is up to date')
            return report.exit_code()
        action = 'update' if current else 'create'
        if runner.dry_run:
            report.info(f'would {action} {name}')
            return report.exit_code()
        if current:
            api.call('PUT', f"/webhooks/{current['id']}", wanted)
        else:
            api.call('POST', '/webhooks', wanted)
        report.ok(f'{action}d {name}')
        return report.exit_code()
    except (CommandError, KeyError, ValueError) as exc:
        report.bad(runner.redact(str(exc)))
        return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    if argv:
        raise ValueError('clickstack-ntfy-webhook takes no arguments')
    runner = runner or Runner.from_environment()
    return sync(runner, report or Report(redact=runner.redact))


def main_notify(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    if argv:
        raise ValueError('clickstack-notify-webhook takes no arguments')
    runner = runner or Runner.from_environment()
    return sync(runner, report or Report(redact=runner.redact), notify_destination, 'ClickStack notify webhook')
