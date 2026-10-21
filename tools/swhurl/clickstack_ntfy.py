"""The two reserved HyperDX webhook definitions, reconciled by the dashboard job.

Destination credentials come from live Secrets; templates stay in HyperDX's MongoDB.
"""
from __future__ import annotations

import base64
import json

from swhurl import app_alerts
from swhurl.apps import calling, channels
from swhurl.notifications.notice import ntfy_target, platform_topic
from swhurl.run import Runner

NAME = 'ntfy: platform'
DESCRIPTION = 'ClickStack alerts to the platform ntfy topic'
ALERT_SECRET = 'notify-alerts'  # the notify service compares against the same Secret (notifications/service.py)


def destination(runner: Runner) -> dict:
    server, topic = ntfy_target(platform_topic(runner))
    # HyperDX JSON-escapes each string variable before applying this template.
    # The condition sets high priority on firing and normal priority on recovery.
    # Put the conditional before another field: `{{/if}}}` is parsed by
    # Handlebars as a triple-brace close, even though the last `}` is JSON.
    body = ('{"priority":{{#if (eq state "ALERT")}}4{{else}}3{{/if}},'
            + json.dumps({'topic': topic, 'title': 'ClickStack: {{title}}',
                          'message': '{{body}}', 'click': '{{link}}'}, separators=(',', ':'))[1:])
    runner.add_secret(body)
    return {'name': NAME, 'service': 'generic', 'url': f'{server}/',
            'description': DESCRIPTION, 'body': body}


def notify_destination(runner: Runner) -> dict:
    """The webhook every app alert uses: the notify service, with the shared secret it asks for in the
    body (a HyperDX webhook cannot carry a header or a token that rotates)."""
    secrets = runner.json(['kubectl', '-n', channels.NAMESPACE, 'get', 'secret', ALERT_SECRET, '-o', 'json'], secret_output=True)
    secret = base64.b64decode(secrets['data']['secret'], validate=True).decode()
    runner.add_secret(secret)
    if not secret:
        raise ValueError(f'{channels.NAMESPACE}/{ALERT_SECRET} holds no secret')
    # HyperDX JSON-escapes each string variable before applying this template.
    body = ('{' + f'"secret":{json.dumps(secret)},' + '"title":"{{title}}","body":"{{body}}","link":"{{link}}","state":"{{state}}"}')
    runner.add_secret(body)
    return {'name': app_alerts.WEBHOOK, 'service': 'generic', 'url': f'{calling.NOTIFY_URL}/v1/alert',
            'description': 'Default app alerts, to the notify service and on to the app\'s team', 'body': body}
