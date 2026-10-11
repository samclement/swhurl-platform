"""Validated ntfy publication and durable outbox delivery."""
from __future__ import annotations

import logging
import os
from collections.abc import Callable
from pathlib import Path
from urllib.parse import urlsplit

import httpx

from swhurl.apps.channels import address_problem
from swhurl.run import Runner

from .errors import NotificationError
from .evaluate import HISTORY
from .state import save_state

SERVICE_URL = 'SWHURL_SERVICE_NOTIFY_URL'
TOKEN_FILE = '/var/run/swhurl/tokens/notify/token'
CHANNEL_RETRY = 7200  # seconds a copy that cannot be sent stays in the outbox


def copy_of(notice: dict) -> str:
    """What a copy is for: a team's channel (the notify service's outbox) or the notify service (the
    checker's); empty for a message to the platform topic, which is never skipped."""
    if 'channel' in notice:
        return f"channel {notice['channel']}"
    return 'notify service' if 'about' in notice else ''


def publish(url: str, notification: dict) -> None:
    if address_problem('ntfy', url):
        raise NotificationError('ntfy destination must be an HTTPS topic URL without a query or fragment')
    parts = urlsplit(url)
    topic = parts.path.strip('/')
    payload = {k: v for k, v in notification.items() if k in {'title', 'message', 'priority', 'tags', 'click'}}
    payload['topic'] = topic
    try:
        response = httpx.post(f'{parts.scheme}://{parts.netloc}', json=payload, timeout=10)
        response.raise_for_status()
    except httpx.HTTPError:
        # HTTP exceptions can include the topic URL or response body. Never surface either.
        raise NotificationError('ntfy delivery failed; notification remains pending') from None


def forward(notice: dict) -> None:
    """Hand a notice about an app to the notify service, which sends it to the channels of the app's team.
    The checker proves who it is with its service-account token for the audience ``notify``."""
    url = os.environ.get(SERVICE_URL, '').rstrip('/')
    if not url:
        raise NotificationError(f'{SERVICE_URL} is not set')
    try:
        token = Path(os.environ.get('SWHURL_NOTIFY_TOKEN_FILE', TOKEN_FILE)).read_text().strip()
    except OSError:
        raise NotificationError('no token file for the notify service') from None
    app, env = notice['about']
    body = {'app': app, 'env': env, 'key': notice['key'], 'title': notice['title'], 'message': notice['message'],
            'priority': 'high' if notice['priority'] >= 4 else 'normal', 'click': notice.get('click', '')}
    try:
        response = httpx.post(f'{url}/v1/notify', json=body, headers={'Authorization': f'Bearer {token}'}, timeout=5)
    except httpx.HTTPError:
        raise NotificationError('notify service unreachable') from None  # the exception can carry the token
    if response.status_code != 202:
        raise NotificationError(f'notify service answered {response.status_code}')


def deliver(runner: Runner, state: dict, sender: Callable[[dict], None], now: float, *,
            save: Callable[[Runner, dict], None] = save_state, history: float = HISTORY) -> None:
    """Post every pending notice in order. ``save`` checkpoints the caller's own state store."""
    log = logging.getLogger(__name__)
    save(runner, state)  # durable outbox before any network side effect
    waiting = []  # copies that could not be sent: they never hold up the platform topic or each other
    try:
        while state['pending']:
            notice = state['pending'][0]
            try:
                sender(notice)
            except NotificationError as error:
                if not copy_of(notice):
                    raise
                state['pending'].pop(0)
                retry = now - notice['queued'] < CHANNEL_RETRY
                if retry:
                    waiting.append(notice)
                else:  # given up: remembered like a delivered one, so it is not queued again
                    state['seen'][notice['key']] = now + notice.get('history', history)
                    save(runner, state)
                log.error('%s: %s (%s)', copy_of(notice), error, 'will retry' if retry else 'gave up')
                continue
            state['seen'][notice['key']] = now + notice.get('history', history)
            state['pending'].pop(0)
            save(runner, state)
            log.info('Notification delivered: %s%s', notice['title'], f' ({copy_of(notice)})' if copy_of(notice) else '')
    finally:
        if waiting:
            state['pending'].extend(waiting)
            save(runner, state)


