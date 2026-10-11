"""Validated ntfy publication and durable outbox delivery."""
from __future__ import annotations

import logging
from collections.abc import Callable
from urllib.parse import urlsplit

import httpx

from swhurl.apps.channels import address_problem
from swhurl.run import Runner

from .errors import NotificationError
from .evaluate import HISTORY
from .state import save_state

CHANNEL_RETRY = 7200  # seconds a copy for an unreachable or missing team channel stays in the outbox


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


def deliver(runner: Runner, state: dict, sender: Callable[[dict], None], now: float, *,
            save: Callable[[Runner, dict], None] = save_state, history: float = HISTORY) -> None:
    """Post every pending notice in order. ``save`` checkpoints the caller's own state store."""
    log = logging.getLogger(__name__)
    save(runner, state)  # durable outbox before any network side effect
    waiting = []  # copies for a team channel that could not be sent: they never hold up the platform topic
    try:
        while state['pending']:
            notice = state['pending'][0]
            try:
                sender(notice)
            except NotificationError as error:
                if 'channel' not in notice:
                    raise
                state['pending'].pop(0)
                retry = now - notice['queued'] < CHANNEL_RETRY
                if retry:
                    waiting.append(notice)
                else:  # given up: remembered like a delivered one, so it is not queued again
                    state['seen'][notice['key']] = now + notice.get('history', history)
                    save(runner, state)
                log.error('Channel %s: %s (%s)', notice['channel'], error, 'will retry' if retry else 'gave up')
                continue
            state['seen'][notice['key']] = now + notice.get('history', history)
            state['pending'].pop(0)
            save(runner, state)
            log.info('Notification delivered: %s%s', notice['title'],
                     f" (channel {notice['channel']})" if 'channel' in notice else '')
    finally:
        if waiting:
            state['pending'].extend(waiting)
            save(runner, state)


